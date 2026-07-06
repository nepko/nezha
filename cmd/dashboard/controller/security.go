package controller

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

// loginGuardMiddleware 在登录处理器前执行：
//  1. 若配置了登录 CIDR 白名单且客户端 IP 不在其中，直接拒绝；
//  2. 若客户端 IP 已被暴力破解防护封禁，直接拒绝并返回剩余时长。
func loginGuardMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !singleton.Conf.LoginProtectEnabled {
			c.Next()
			return
		}
		ip := c.GetString(model.CtxKeyRealIPStr)

		if !singleton.IPInCIDRs(ip, singleton.Conf.AllowedLoginCIDRs) {
		c.Set(model.CtxKeyLoginBlocked, model.LoginBlockedInfo{
			Type:      "cidr_denied",
			Remaining: 0,
			Message:   singleton.Localizer.T("login cidr denied"),
		})
			c.JSON(http.StatusOK, model.CommonResponse[model.LoginBlockedInfo]{
				Success: false,
				Error:   "LOGIN_BLOCKED",
				Data: model.LoginBlockedInfo{
					Type:      "cidr_denied",
					Remaining: 0,
					Message:   singleton.Localizer.T("login cidr denied"),
				},
			})
			c.Abort()
			return
		}

		if banned, remaining := singleton.IsIPBanned(ip); banned {
			c.Set(model.CtxKeyLoginBlocked, model.LoginBlockedInfo{
				Type:      "ip_banned",
				Remaining: remaining,
				Message:   singleton.Localizer.T("login ip banned"),
			})
			c.JSON(http.StatusOK, model.CommonResponse[model.LoginBlockedInfo]{
				Success: false,
				Error:   "LOGIN_BLOCKED",
			Data: model.LoginBlockedInfo{
				Type:      "ip_banned",
				Remaining: remaining,
				Message:   singleton.Localizer.T("login ip banned"),
			},
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// Get login protection config
// @Summary Get login protection config
// @Security BearerAuth
// @Tags admin required
// @Produce json
// @Success 200 {object} model.CommonResponse[model.LoginProtectionConfig]
// @Router /security/login-protection [get]
func getLoginProtection(c *gin.Context) (*model.LoginProtectionConfig, error) {
	return &model.LoginProtectionConfig{
		Enabled:       singleton.Conf.LoginProtectEnabled,
		MaxAttempts:   singleton.Conf.LoginMaxAttempts,
		LockMinutes:   singleton.Conf.LoginLockMinutes,
		BanIPThreshold: singleton.Conf.LoginBanIPThreshold,
		BanMinutes:    singleton.Conf.LoginBanMinutes,
		AllowedCIDRs:  singleton.Conf.AllowedLoginCIDRs,
	}, nil
}

// Update login protection config
// @Summary Update login protection config
// @Security BearerAuth
// @Tags admin required
// @Accept json
// @Param body body model.LoginProtectionConfig true "config"
// @Produce json
// @Success 200 {object} model.CommonResponse[any]
// @Router /security/login-protection [post]
func updateLoginProtection(c *gin.Context) (any, error) {
	var cfg model.LoginProtectionConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		return nil, err
	}
	singleton.Conf.LoginProtectEnabled = cfg.Enabled
	singleton.Conf.LoginMaxAttempts = cfg.MaxAttempts
	singleton.Conf.LoginLockMinutes = cfg.LockMinutes
	singleton.Conf.LoginBanIPThreshold = cfg.BanIPThreshold
	singleton.Conf.LoginBanMinutes = cfg.BanMinutes
	singleton.Conf.AllowedLoginCIDRs = cfg.AllowedCIDRs
	if err := singleton.Conf.Save(); err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionConfigUpdate, "security", 0, "update login protection", true)
	return nil, nil
}

// List current locked accounts and banned IPs
// @Summary List login locks
// @Security BearerAuth
// @Tags admin required
// @Produce json
// @Success 200 {object} model.CommonResponse[[]model.LoginLockEntry]
// @Router /security/locks [get]
func listLoginLocks(c *gin.Context) ([]model.LoginLockEntry, error) {
	now := time.Now().Unix()
	var entries []model.LoginLockEntry

	var lockedUsers []model.User
	if err := singleton.DB.Select("username", "locked_until").Where("locked_until > ?", now).Find(&lockedUsers).Error; err != nil {
		return nil, err
	}
	for _, u := range lockedUsers {
		entries = append(entries, model.LoginLockEntry{
			Kind:      "account",
			Target:    u.Username,
			Remaining: u.LockedUntil - now,
			Reason:    "brute_force_lock",
		})
	}

	entries = append(entries, singleton.ListBannedIPs()...)
	return entries, nil
}

// Unlock a locked account
// @Summary Unlock account
// @Security BearerAuth
// @Tags admin required
// @Accept json
// @Param body body map[string]string true "username"
// @Produce json
// @Success 200 {object} model.CommonResponse[any]
// @Router /security/locks/unlock [post]
func unlockAccount(c *gin.Context) (any, error) {
	var body struct {
		Username string `json:"username"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return nil, err
	}
	if body.Username == "" {
		return nil, newGormError("username required")
	}
	if err := singleton.DB.Model(&model.User{}).Where("username = ?", body.Username).
		Updates(map[string]interface{}{"login_fails": 0, "locked_until": 0}).Error; err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionUserUpdate, "user", 0, "unlock account: "+body.Username, true)
	return nil, nil
}

// Unban an IP blocked by the brute-force protector
// @Summary Unban IP
// @Security BearerAuth
// @Tags admin required
// @Accept json
// @Param body body map[string]string true "ip"
// @Produce json
// @Success 200 {object} model.CommonResponse[any]
// @Router /security/locks/unban [post]
func unbanIP(c *gin.Context) (any, error) {
	var body struct {
		IP string `json:"ip"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return nil, err
	}
	if body.IP == "" {
		return nil, newGormError("ip required")
	}
	singleton.ClearIPBan(body.IP)
	if err := model.BatchUnblockIP(singleton.DB, []string{body.IP}); err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionConfigUpdate, "security", 0, "unban ip: "+body.IP, true)
	return nil, nil
}
