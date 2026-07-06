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
