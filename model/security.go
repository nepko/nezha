package model

// CtxKeyLoginBlocked 用于在 authenticator 中携带登录封锁详情到 unauthorized 回调。
const CtxKeyLoginBlocked = "LoginBlockedInfo"

// LoginBlockedInfo 描述一次被拒绝的登录尝试的封锁信息，返回给前端展示。
type LoginBlockedInfo struct {
	Type      string `json:"type"`       // "account_locked" | "ip_banned" | "cidr_denied"
	Remaining int64  `json:"remaining"`  // 剩余封锁秒数
	Message   string `json:"message"`    // 可读提示
}

// LoginProtectionConfig 登录暴力破解防护配置（前后端传输用）。
type LoginProtectionConfig struct {
	Enabled          bool   `json:"enabled"`
	MaxAttempts      int    `json:"max_attempts"`
	LockMinutes      int    `json:"lock_minutes"`
	BanIPThreshold   int    `json:"ban_ip_threshold"`
	BanMinutes       int    `json:"ban_minutes"`
	AllowedCIDRs     string `json:"allowed_cidrs"`
}

// LoginLockEntry 当前被锁定账号或被封禁 IP 的视图对象。
type LoginLockEntry struct {
	Kind      string `json:"kind"`       // "account" | "ip"
	Target    string `json:"target"`     // 用户名或 IP
	Remaining int64  `json:"remaining"`  // 剩余秒数
	Reason    string `json:"reason"`
}

// LoginAttempt 记录每一次登录尝试（成功/失败），用于暴力破解审计与溯源。
// 与 AuditLog 互补：AuditLog 记录所有运维操作，LoginAttempt 聚焦登录且带
// (username,ip) 复合索引，便于快速统计某账号/某 IP 的失败次数。CreatedAt 即尝试时间。
type LoginAttempt struct {
	Common
	Username string `gorm:"index:idx_login_attempt_user_ip;size:64" json:"username"`
	IP       string `gorm:"index:idx_login_attempt_user_ip;size:64" json:"ip"`
	UserID   uint64 `gorm:"index;default:0" json:"user_id"`
	Success  bool   `json:"success"`
	Action   string `gorm:"size:32" json:"action"`
}
