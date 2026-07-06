package singleton

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/nezhahq/nezha/model"
)

// AuditLogger 审计日志写入辅助函数
// 在各 controller 中调用，记录关键操作
func WriteAuditLog(c *gin.Context, action string, resource string, resourceID uint64, detail string, success bool) {
	var userID uint64
	var username string

	if auth, ok := c.Get(model.CtxKeyAuthorizedUser); ok {
		if u, ok := auth.(*model.User); ok && u != nil {
			userID = u.ID
			username = u.Username
		}
	}

	// 获取真实 IP
	ip := GetRealIP(c)

	// 获取 User-Agent
	ua := c.GetHeader("User-Agent")
	if len(ua) > 512 {
		ua = ua[:512]
	}

	log := &model.AuditLog{
		UserID:    userID,
		Username:   username,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Detail:     detail,
		IP:         ip,
		UA:         ua,
		Success:    success,
	}

	// 异步写入，不阻塞主流程
	go func() {
		if err := DB.Create(log).Error; err != nil {
			// 静默失败，不影响主流程
		}
	}()
}

// GetRealIP 从上下文中获取真实 IP（考虑 WAF RealIp 中间件）
func GetRealIP(c *gin.Context) string {
	if ip, ok := c.Get(model.CtxKeyRealIPStr); ok {
		return ip.(string)
	}
	// 尝试从 X-Forwarded-For 获取
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	return c.ClientIP()
}

// WriteLoginAuditLog 登录审计专用（登录时用户尚未进入 context）
func WriteLoginAuditLog(c *gin.Context, username string, userID uint64, action string, success bool) {
	ip := GetRealIP(c)
	ua := c.GetHeader("User-Agent")
	if len(ua) > 512 {
		ua = ua[:512]
	}

	log := &model.AuditLog{
		UserID:   userID,
		Username: username,
		Action:    action,
		Resource:  "user",
		Detail:    username,
		IP:        ip,
		UA:        ua,
		Success:   success,
	}

	go func() {
		if err := DB.Create(log).Error; err != nil {
			// 静默失败
		}
	}()
}
