package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/pquerna/otp/totp"

	"github.com/nezhahq/nezha/cmd/dashboard/controller/waf"
	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/pkg/idcodec"
	"github.com/nezhahq/nezha/pkg/utils"
	"github.com/nezhahq/nezha/service/singleton"
)

const (
	jwtClaimUserID = "uid"
	jwtClaimKeyID  = "keyId"
	jwtKeyIDBytes  = 32
)

// ErrRequire2FA is returned when the user has 2FA enabled but didn't provide an OTP token.
var ErrRequire2FA = errors.New("require_2fa")

// ErrAccountLocked is returned when the account is temporarily locked by the
// brute-force protector. The remaining lock time is carried via the gin context
// (model.CtxKeyLoginBlocked) since gin-jwt only forwards the error string.
var ErrAccountLocked = errors.New("account_locked")

// recordFailedLogin 累计账号失败次数并在达到阈值时锁定账号，同时累计 IP 失败次数。
func recordFailedLogin(uid uint64, ip string) {
	if !singleton.Conf.LoginProtectEnabled {
		return
	}
	if uid != 0 {
		_ = singleton.DB.Model(&model.User{}).Where("id = ?", uid).
			Update("login_fails", gorm.Expr("login_fails + 1")).Error
		var u model.User
		if singleton.DB.Select("login_fails").First(&u, uid).Error == nil &&
			singleton.Conf.LoginMaxAttempts > 0 &&
			u.LoginFails >= singleton.Conf.LoginMaxAttempts {
			_ = singleton.DB.Model(&model.User{}).Where("id = ?", uid).
				Updates(map[string]interface{}{
					"login_fails":  0,
					"locked_until": time.Now().Unix() + int64(singleton.Conf.LoginLockMinutes)*60,
				}).Error
		}
	}
	singleton.RecordIPLoginFailure(ip)
}

// resetLoginFails 登录成功后清空账号失败计数并解除 IP 封禁计数。
func resetLoginFails(uid uint64, ip string) {
	if !singleton.Conf.LoginProtectEnabled {
		return
	}
	if uid != 0 {
		_ = singleton.DB.Model(&model.User{}).Where("id = ?", uid).
			Updates(map[string]interface{}{"login_fails": 0, "locked_until": 0}).Error
	}
	singleton.ClearIPBan(ip)
}

func uaHash(c *gin.Context) string {
	sum := sha256.Sum256([]byte(c.Request.UserAgent()))
	return hex.EncodeToString(sum[:])
}

func issueJWTSession(c *gin.Context, user *model.User, jwtTimeoutHours int) (map[string]interface{}, error) {
	keyID, err := utils.GenerateRandomString(jwtKeyIDBytes)
	if err != nil {
		return nil, err
	}
	// encodedUID is reversible Sqids obfuscation keyed by JWTSecretKey, NOT
	// a one-way hash. It exists to defeat enumeration on the wire, not to
	// keep the uid confidential — see L2 note in idcodec docs.
	encodedUID, err := idcodec.Encode(user.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := model.JWTSession{
		KeyID:        keyID,
		UserID:       user.ID,
		IP:           c.GetString(model.CtxKeyRealIPStr),
		UAHash:       uaHash(c),
		TokenVersion: user.TokenVersion,
		ExpiresAt:    now.Add(time.Hour * time.Duration(jwtTimeoutHours)),
		CreatedAt:    now,
		LastUsedAt:   now,
	}
	if err := singleton.DB.Create(&sess).Error; err != nil {
		return nil, err
	}
	return map[string]interface{}{
		jwtClaimUserID: encodedUID,
		jwtClaimKeyID:  keyID,
	}, nil
}

func initParams() *jwt.GinJWTMiddleware {
	return &jwt.GinJWTMiddleware{
		Realm:       singleton.Conf.SiteName,
		Key:         []byte(singleton.Conf.JWTSecretKey),
		CookieName:  "nz-jwt",
		SendCookie:  true,
		// Pin the signing algorithm so a future library default change (or an
		// `alg: none` confusion attempt) cannot weaken token validation.
		SigningAlgorithm: "HS256",
		// Lax keeps OAuth callback redirects (top-level GET navigations from
		// the provider domain) working while blocking cross-site POST CSRF.
		// HttpOnly/Secure are intentionally left default: the frontend reads
		// `!!document.cookie` for login-state display and many deployments
		// terminate TLS at a proxy upstream — both warrant a separate change.
		CookieSameSite: http.SameSiteLaxMode,
		Timeout:        time.Hour * time.Duration(singleton.Conf.JWTTimeout),
		MaxRefresh:     time.Hour * time.Duration(singleton.Conf.JWTTimeout),
		IdentityKey:    model.CtxKeyAuthorizedUser,
		PayloadFunc:    payloadFunc(),

		IdentityHandler: identityHandler(),
		Authenticator:   authenticator(),
		Authorizator:    authorizator(),
		Unauthorized:    unauthorized(),
		// query: token still accepted because the WebSocket browser API
		// cannot set Authorization headers; removing it would break the
		// /ws/* routes until the frontend migrates to cookie auth.
		TokenLookup:   "header: Authorization, query: token, cookie: nz-jwt",
		TokenHeadName: "Bearer",
		TimeFunc:      time.Now,

		LoginResponse: func(c *gin.Context, code int, token string, expire time.Time) {
			setCSRFCookie(c)
			c.JSON(http.StatusOK, model.CommonResponse[model.LoginResponse]{
				Success: true,
				Data: model.LoginResponse{
					Token:  token,
					Expire: expire.Format(time.RFC3339),
				},
			})
		},
		RefreshResponse: refreshResponse,
	}
}

func payloadFunc() func(data any) jwt.MapClaims {
	return func(data any) jwt.MapClaims {
		if v, ok := data.(map[string]interface{}); ok {
			return v
		}
		return jwt.MapClaims{}
	}
}

func identityHandler() func(c *gin.Context) any {
	return func(c *gin.Context) any {
		claims := jwt.ExtractClaims(c)

		keyID, ok := claims[jwtClaimKeyID].(string)
		if !ok || keyID == "" {
			return nil
		}
		encodedUID, ok := claims[jwtClaimUserID].(string)
		if !ok || encodedUID == "" {
			return nil
		}
		claimUID, err := idcodec.Decode(encodedUID)
		if err != nil {
			realIP := c.GetString(model.CtxKeyRealIPStr)
			model.BlockIP(singleton.DB, realIP, model.WAFBlockReasonTypeBruteForceToken, model.BlockIDToken)
			return nil
		}

		var sess model.JWTSession
		if err := singleton.DB.First(&sess, "key_id = ?", keyID).Error; err != nil {
			return nil
		}
		if sess.RevokedAt != nil {
			return nil
		}
		now := time.Now()
		if now.After(sess.ExpiresAt) {
			return nil
		}
		if claimUID != sess.UserID {
			realIP := c.GetString(model.CtxKeyRealIPStr)
			model.BlockIP(singleton.DB, realIP, model.WAFBlockReasonTypeBruteForceToken, model.BlockIDToken)
			return nil
		}
		currentIP := c.GetString(model.CtxKeyRealIPStr)
		if sess.IP != currentIP {
			c.Set(model.CtxKeyIsIPMismatch, true)
			return nil
		}

		var user model.User
		if err := singleton.DB.First(&user, sess.UserID).Error; err != nil {
			return nil
		}
		if user.TokenVersion != sess.TokenVersion {
			return nil
		}

		_ = singleton.DB.Model(&model.JWTSession{}).
			Where("key_id = ?", keyID).
			Update("last_used_at", now).Error

		c.Set(jwtClaimKeyID, keyID)
		return &user
	}
}

// User Login
// @Summary user login
// @Schemes
// @Description user login
// @Accept json
// @param loginRequest body model.LoginRequest true "Login Request"
// @Produce json
// @Success 200 {object} model.CommonResponse[model.LoginResponse]
// @Router /login [post]
func authenticator() func(c *gin.Context) (any, error) {
	return func(c *gin.Context) (any, error) {
		var loginVals model.LoginRequest
		if err := c.ShouldBind(&loginVals); err != nil {
			return "", jwt.ErrMissingLoginValues
		}

		var user model.User
		realip := c.GetString(model.CtxKeyRealIPStr)

		if err := singleton.DB.Select("id", "password", "reject_password", "token_version", "locked_until").Where("username = ?", loginVals.Username).First(&user).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				model.BlockIP(singleton.DB, realip, model.WAFBlockReasonTypeLoginFail, model.BlockIDUnknownUser)
				singleton.WriteLoginAuditLog(c, loginVals.Username, 0, model.AuditActionLoginFailed, false)
				recordFailedLogin(0, realip)
			}
			return nil, jwt.ErrFailedAuthentication
		}

		// 二开：账号被暴力破解防护锁定
		if singleton.Conf.LoginProtectEnabled && user.LockedUntil > time.Now().Unix() {
			remaining := user.LockedUntil - time.Now().Unix()
			c.Set(model.CtxKeyLoginBlocked, model.LoginBlockedInfo{
				Type:      "account_locked",
				Remaining: remaining,
				Message:   singleton.Localizer.T("login locked"),
			})
			singleton.WriteLoginAuditLog(c, loginVals.Username, user.ID, model.AuditActionLoginBlocked, false)
			return nil, ErrAccountLocked
		}

		if user.RejectPassword {
			model.BlockIP(singleton.DB, realip, model.WAFBlockReasonTypeLoginFail, int64(user.ID))
			singleton.WriteLoginAuditLog(c, loginVals.Username, user.ID, model.AuditActionLoginFailed, false)
			recordFailedLogin(user.ID, realip)
			return nil, jwt.ErrFailedAuthentication
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(loginVals.Password)); err != nil {
			model.BlockIP(singleton.DB, realip, model.WAFBlockReasonTypeLoginFail, int64(user.ID))
			singleton.WriteLoginAuditLog(c, loginVals.Username, user.ID, model.AuditActionLoginFailed, false)
			recordFailedLogin(user.ID, realip)
			return nil, jwt.ErrFailedAuthentication
		}

		model.UnblockIP(singleton.DB, realip, model.BlockIDUnknownUser)
		model.UnblockIP(singleton.DB, realip, int64(user.ID))

		// Check if 2FA is enabled for this user
		var otpSetting model.OTPSetting
		if singleton.DB.First(&otpSetting, "user_id = ?", user.ID).Error == nil && otpSetting.Enabled {
			if loginVals.OtpToken == "" {
				return nil, ErrRequire2FA
			}
			if !totp.Validate(loginVals.OtpToken, otpSetting.Secret) {
				// 动态码校验失败：尝试用备份码登录（设备丢失时的应急恢复手段）。
				// 原先登录入口只认 TOTP 动态码，备份码形同虚设——丢了设备即永久锁死。
				var bc model.BackupCode
				if err := singleton.DB.Where("user_id = ? AND code = ? AND used = ?", user.ID, loginVals.OtpToken, false).First(&bc).Error; err != nil {
					singleton.WriteLoginAuditLog(c, loginVals.Username, user.ID, model.AuditActionLoginFailed, false)
					recordFailedLogin(user.ID, realip)
					return nil, jwt.ErrFailedAuthentication
				}
				// 备份码命中：标记已用并放行（落到下方统一签发 JWT）
				singleton.DB.Model(&bc).Update("used", true)
			}
		}

		resetLoginFails(user.ID, realip)
		singleton.WriteLoginAuditLog(c, loginVals.Username, user.ID, model.AuditActionLogin, true)
		return issueJWTSession(c, &user, singleton.Conf.JWTTimeout)
	}
}

func authorizator() func(data any, c *gin.Context) bool {
	return func(data any, c *gin.Context) bool {
		_, ok := data.(*model.User)
		return ok
	}
}

func unauthorized() func(c *gin.Context, code int, message string) {
	return func(c *gin.Context, code int, message string) {
		if message == "require_2fa" {
			c.JSON(http.StatusOK, model.CommonResponse[map[string]bool]{
				Success: false,
				Error:   "2FA_REQUIRED",
			})
			return
		}
		// 二开：账号锁定 / IP 封禁 / CIDR 拒绝，携带剩余时间供前端倒计时
		if message == "account_locked" {
			if v, ok := c.Get(model.CtxKeyLoginBlocked); ok {
				if info, ok := v.(model.LoginBlockedInfo); ok {
					c.JSON(http.StatusOK, model.CommonResponse[model.LoginBlockedInfo]{
						Success: false,
						Error:   "LOGIN_BLOCKED",
						Data:    info,
					})
					return
				}
			}
			c.JSON(http.StatusOK, model.CommonResponse[model.LoginBlockedInfo]{
				Success: false,
				Error:   "LOGIN_BLOCKED",
				Data:    model.LoginBlockedInfo{Type: "account_locked", Remaining: 0, Message: ""},
			})
			return
		}
		c.JSON(http.StatusOK, model.CommonResponse[any]{
			Success: false,
			Error:   "ApiErrorUnauthorized",
		})
	}
}

// Refresh token
// @Summary Refresh token
// @Security BearerAuth
// @Schemes
// @Description Refresh token
// @Tags auth required
// @Produce json
// @Success 200 {object} model.CommonResponse[model.LoginResponse]
// @Router /refresh-token [post]
func refreshResponse(c *gin.Context, code int, token string, expire time.Time) {
	if keyID := c.GetString(jwtClaimKeyID); keyID != "" {
		_ = singleton.DB.Model(&model.JWTSession{}).
			Where("key_id = ?", keyID).
			Updates(map[string]interface{}{
				"expires_at":   expire,
				"last_used_at": time.Now(),
			}).Error
	}
	setCSRFCookie(c)
	c.JSON(http.StatusOK, model.CommonResponse[model.LoginResponse]{
		Success: true,
		Data: model.LoginResponse{
			Token:  token,
			Expire: expire.Format(time.RFC3339),
		},
	})
}

func fallbackAuthMiddleware(mw *jwt.GinJWTMiddleware) func(c *gin.Context) {
	return func(c *gin.Context) {
		claims, err := mw.GetClaimsFromJWT(c)
		if err != nil {
			return
		}

		switch v := claims["exp"].(type) {
		case nil:
			return
		case float64:
			if int64(v) < mw.TimeFunc().Unix() {
				return
			}
		case json.Number:
			n, err := v.Int64()
			if err != nil {
				return
			}
			if n < mw.TimeFunc().Unix() {
				return
			}
		default:
			return
		}

		realIP := c.GetString(model.CtxKeyRealIPStr)

		c.Set("JWT_PAYLOAD", claims)
		identity := mw.IdentityHandler(c)

		if identity != nil {
			model.UnblockIP(singleton.DB, realIP, model.BlockIDToken)
			c.Set(mw.IdentityKey, identity)
		} else {
			isIpMismatch := c.GetBool(model.CtxKeyIsIPMismatch)
			if !isIpMismatch {
				waf.ShowBlockPage(c, model.BlockIP(singleton.DB, realIP, model.WAFBlockReasonTypeBruteForceToken, model.BlockIDToken))
				return
			}
		}

		c.Next()
	}
}
