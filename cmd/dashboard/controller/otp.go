package controller

import (
"net/http"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/pkg/utils"
	"github.com/nezhahq/nezha/service/singleton"
)

type OTPSetupResponse struct {
	Secret string `json:"secret"`
	QRURL  string `json:"qr_url"`
}

// setupOTP 生成 OTP 密钥和二维码
func setupOTP(c *gin.Context) (*OTPSetupResponse, error) {
	user := getAuthUser(c)
	if user == nil {
		return nil, singleton.Localizer.ErrorT("unauthorized")
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      singleton.Conf.SiteName,
		AccountName: user.Username,
		SecretSize:  20,
	})
	if err != nil {
		return nil, err
	}

	var setting model.OTPSetting
	// 已存在 OTP 设置行（例如用户再次打开设置页）时，FirstOrCreate 不会更新
	// 库内 secret，却会返回新生成的密钥，导致后续 verify 用库内旧密钥校验失败、
	// 永远无法启用。这里统一用本次新生成的密钥覆盖存储并返回，保证“返回的
	// secret / 二维码 / 库内 secret”三者一致。
	if err := singleton.DB.Where("user_id = ?", user.ID).First(&setting).Error; err != nil {
		setting = model.OTPSetting{
			UserID: user.ID,
			Secret:  key.Secret(),
			Enabled: false,
		}
		singleton.DB.Create(&setting)
	} else {
		setting.Secret = key.Secret()
		setting.Enabled = false
		singleton.DB.Save(&setting)
	}

	return &OTPSetupResponse{
		Secret: setting.Secret,
		QRURL:  key.URL(),
	}, nil
}

type OTPVerifyRequest struct {
	Code       string `json:"code"`
	BackupCode string `json:"backup_code,omitempty"`
}

// verifyOTP 验证 OTP 码并启用 2FA，返回生成的备份码列表
func verifyOTP(c *gin.Context) ([]string, error) {
	user := getAuthUser(c)
	if user == nil {
		return nil, singleton.Localizer.ErrorT("unauthorized")
	}

	var req OTPVerifyRequest
	if err := c.ShouldBind(&req); err != nil {
		return nil, err
	}

	var setting model.OTPSetting
	if err := singleton.DB.Where("user_id = ?", user.ID).First(&setting).Error; err != nil {
		return nil, singleton.Localizer.ErrorT("otp not setup")
	}

	if !totp.Validate(req.Code, setting.Secret) {
		return nil, singleton.Localizer.ErrorT("invalid otp code")
	}

	setting.Enabled = true
	singleton.DB.Save(&setting)

	codes := generateBackupCodes(user.ID)

	singleton.WriteAuditLog(c, model.AuditActionUserUpdate, "user", user.ID, "2FA enabled", true)
	return codes, nil
}

// disableOTP 禁用 2FA
func disableOTP(c *gin.Context) (bool, error) {
	user := getAuthUser(c)
	if user == nil {
		return false, singleton.Localizer.ErrorT("unauthorized")
	}

	var req OTPVerifyRequest
	if err := c.ShouldBind(&req); err != nil {
		return false, err
	}

	var setting model.OTPSetting
	singleton.DB.Where("user_id = ? AND enabled = ?", user.ID, true).First(&setting)

	if req.Code != "" {
		if !totp.Validate(req.Code, setting.Secret) {
			return false, singleton.Localizer.ErrorT("invalid otp code")
		}
	} else if req.BackupCode != "" {
		var bc model.BackupCode
		if err := singleton.DB.Where("user_id = ? AND code = ? AND used = ?", user.ID, req.BackupCode, false).First(&bc).Error; err != nil {
			return false, singleton.Localizer.ErrorT("invalid backup code")
		}
		singleton.DB.Model(&bc).Update("used", true)
	} else {
		return false, singleton.Localizer.ErrorT("otp code or backup code required")
	}

	singleton.DB.Model(&model.OTPSetting{}).Where("user_id = ?", user.ID).Update("enabled", false)
	singleton.DB.Where("user_id = ?", user.ID).Delete(&model.BackupCode{})

	singleton.WriteAuditLog(c, model.AuditActionUserUpdate, "user", user.ID, "2FA disabled", true)
	return true, nil
}

// getBackupCodes 获取备用码
func getBackupCodes(c *gin.Context) ([]string, error) {
	user := getAuthUser(c)
	if user == nil {
		return nil, singleton.Localizer.ErrorT("unauthorized")
	}

	var codes []model.BackupCode
	singleton.DB.Where("user_id = ? AND used = ?", user.ID, false).Find(&codes)

	result := make([]string, len(codes))
	for i, bc := range codes {
		result[i] = bc.Code
	}
	return result, nil
}

// regenerateBackupCodes 重新生成备用码
func regenerateBackupCodes(c *gin.Context) ([]string, error) {
	user := getAuthUser(c)
	if user == nil {
		return nil, singleton.Localizer.ErrorT("unauthorized")
	}

	singleton.DB.Where("user_id = ?", user.ID).Delete(&model.BackupCode{})

	return generateBackupCodes(user.ID), nil
}

// generateBackupCodes 生成备用码（内部函数）
func generateBackupCodes(userID uint64) []string {
	codes := make([]string, 10)
	for i := range codes {
		code, _ := utils.GenerateRandomString(12)
		codes[i] = code
		singleton.DB.Create(&model.BackupCode{
			UserID: userID,
			Code:    code,
			Used:    false,
		})
	}
	return codes
}

// getAuthUser 从上下文获取当前用户
func getAuthUser(c *gin.Context) *model.User {
	auth, ok := c.Get(model.CtxKeyAuthorizedUser)
	if !ok {
		return nil
	}
	user, _ := auth.(*model.User)
	return user
}

// Get OTP Status
// @Summary get OTP status
// @Security BearerAuth
// @Tags otp
// @Produce json
// @Success 200 {object} model.CommonResponse[map[string]bool]
// @Router /api/v1/otp/status [get]
func getOTPStatus(c *gin.Context) {
	user := getAuthUser(c)
	if user == nil {
		c.JSON(http.StatusOK, model.CommonResponse[any]{Success: false, Error: "unauthorized"})
		return
	}
	var setting model.OTPSetting
	if singleton.DB.First(&setting, "user_id = ?", user.ID).Error != nil {
		c.JSON(http.StatusOK, model.CommonResponse[map[string]bool]{
			Success: true,
			Data:    map[string]bool{"enabled": false},
		})
		return
	}
	c.JSON(http.StatusOK, model.CommonResponse[map[string]bool]{
		Success: true,
		Data:    map[string]bool{"enabled": setting.Enabled},
	})
}
