package model

type SettingForm struct {
	DNSServers                  string `json:"dns_servers,omitempty" validate:"optional"`
	IgnoredIPNotification       string `json:"ignored_ip_notification,omitempty" validate:"optional"`
	IPChangeNotificationGroupID uint64 `json:"ip_change_notification_group_id,omitempty"` // IP变更提醒的通知组
	Cover                       uint8  `json:"cover,omitempty"`
	SiteName                    string `json:"site_name,omitempty" minLength:"1"`
	Language                    string `json:"language,omitempty" minLength:"2"`
	InstallHost                 string `json:"install_host,omitempty" validate:"optional"`
	DashboardHost               string `json:"dashboard_host,omitempty" validate:"optional"`
	ReservedHosts               string `json:"reserved_hosts,omitempty" validate:"optional"`
	CustomCode                  string `json:"custom_code,omitempty" validate:"optional"`
	CustomCodeDashboard         string `json:"custom_code_dashboard,omitempty" validate:"optional"`
	WebRealIPHeader             string `json:"web_real_ip_header,omitempty" validate:"optional"`   // 前端真实IP
	AgentRealIPHeader            string `json:"agent_real_ip_header,omitempty" validate:"optional"` // Agent真实IP
	UserTemplate                string `json:"user_template,omitempty" validate:"optional"`

	AgentTLS                    bool `json:"tls,omitempty" validate:"optional"`
	EnableIPChangeNotification  bool `json:"enable_ip_change_notification,omitempty" validate:"optional"`
	EnablePlainIPInNotification bool `json:"enable_plain_ip_in_notification,omitempty" validate:"optional"`
	EnableMCP                   *bool `json:"enable_mcp,omitempty" validate:"optional"`

	// 二开：终端会话录制开关（默认关闭）。用指针，避免其它设置保存时把未传字段重置为 false。
	TerminalRecordingEnabled       *bool `json:"terminal_recording_enabled,omitempty" validate:"optional"`
	TerminalRecordingRetentionDays *int  `json:"terminal_recording_retention_days,omitempty" validate:"optional"`
	// 二开：文件管理器增强开关（默认关闭）。
	FMEnhancedEnabled *bool `json:"fm_enhanced_enabled,omitempty" validate:"optional"`
	// 二开：终端空闲超时（秒，0=不限制）。
	TerminalIdleTimeoutSeconds *int `json:"terminal_idle_timeout_seconds,omitempty" validate:"optional"`

	// 二开：终端 AI 助手（OpenAI 兼容）。ai_api_key 空字符串表示“保留已配置密钥”，
	// 仅在用户显式填写时才覆盖；绝不通过 GET /setting 回传明文。
	AIEnabled           *bool   `json:"ai_enabled,omitempty" validate:"optional"`
	AIBaseURL           string  `json:"ai_base_url,omitempty" validate:"optional"`
	AIApiKey            string  `json:"ai_api_key,omitempty" validate:"optional"`
	AIModel             string  `json:"ai_model,omitempty" validate:"optional"`
	AITemperature       *float64 `json:"ai_temperature,omitempty" validate:"optional"`
	AIMaxTokens         *int    `json:"ai_max_tokens,omitempty" validate:"optional"`
	// 二开：AI 每用户每日 token 预算（0=不限）。
	AITokenBudgetDaily  *int    `json:"ai_token_budget_daily,omitempty" validate:"optional"`
}

type Setting struct {
	ConfigForGuests
	ConfigDashboard

	IgnoredIPNotificationServerIDs map[uint64]bool `json:"ignored_ip_notification_server_ids,omitempty"`
	Oauth2Providers                []string        `json:"oauth2_providers,omitempty"`

	// 二开：终端与文件管理器增强开关（仅序列化到前端设置页，不参与 Config 嵌套）
	TerminalRecordingEnabled       bool `json:"terminal_recording_enabled,omitempty" gorm:"-"`
	TerminalRecordingRetentionDays int  `json:"terminal_recording_retention_days,omitempty" gorm:"-"`
	TerminalIdleTimeoutSeconds     int  `json:"terminal_idle_timeout_seconds,omitempty" gorm:"-"`
	FMEnhancedEnabled              bool `json:"fm_enhanced_enabled,omitempty" gorm:"-"`
	// 二开：命令审批总开关（默认关闭，GET /setting 回传前端设置页）。
	EnableCommandApproval bool `json:"enable_command_approval,omitempty" gorm:"-"`

	// 二开：终端 AI 助手开关与连接信息（不含明文 API Key，仅暴露是否已配置）。
	AIEnabled           bool `json:"ai_enabled,omitempty" gorm:"-"`
	AIBaseURL           string `json:"ai_base_url,omitempty" gorm:"-"`
	AIModel             string `json:"ai_model,omitempty" gorm:"-"`
	AITokenBudgetDaily  int  `json:"ai_token_budget_daily,omitempty" gorm:"-"`
	AIApiKeySet         bool   `json:"ai_api_key_set,omitempty" gorm:"-"`
}

type FrontendTemplate struct {
	Path       string `json:"path,omitempty"`
	Name       string `json:"name,omitempty"`
	Repository string `json:"repository,omitempty"`
	Author     string `json:"author,omitempty"`
	Version    string `json:"version,omitempty"`
	IsAdmin    bool   `json:"is_admin,omitempty"`
	IsOfficial bool   `json:"is_official,omitempty"`
}

type SettingResponse struct {
	Config Setting `json:"config"`

	Version           string             `json:"version,omitempty"`
	FrontendTemplates []FrontendTemplate `json:"frontend_templates,omitempty"`
	TSDBEnabled       bool               `json:"tsdb_enabled"`
}
