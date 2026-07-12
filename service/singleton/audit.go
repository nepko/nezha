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

	attempt := &model.LoginAttempt{
		Username: username,
		IP:       ip,
		UserID:   userID,
		Success:  success,
		Action:   action,
	}

	go func() {
		// DB 可能在测试中将全局 singleton.DB 置 nil（cleanup 竞态）或生产瞬时
		// 不可用；登录审计是 fire-and-forget，应静默失败而非让异步 goroutine
		// 以 nil 指针 panic 拖垮整个进程。
		if DB == nil {
			return
		}
		if err := DB.Create(log).Error; err != nil {
			// 静默失败
		}
		// 二开：同步写入独立的登录尝试审计表（带 (username,ip) 复合索引），
		// 供暴力破解溯源与后续审计页查询。失败静默，不影响主流程。
		if err := DB.Create(attempt).Error; err != nil {
			// 静默失败
		}
	}()
}
