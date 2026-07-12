package model


// AuditAction 审计操作类型常量
const (
	AuditActionLogin         = "login"
	AuditActionLoginFailed   = "login_failed"
	AuditActionLoginBlocked  = "login_blocked"
	AuditActionLogout        = "logout"
	AuditActionConfigUpdate  = "config_update"
	AuditActionUserCreate   = "user_create"
	AuditActionUserUpdate   = "user_update"
	AuditActionUserDelete   = "user_delete"
	AuditActionServerCreate  = "server_create"
	AuditActionServerUpdate  = "server_update"
	AuditActionServerDelete  = "server_delete"
	AuditActionTerminalCreate = "terminal_create"
	AuditActionTerminalIdleDisconnect = "terminal_idle_disconnect"
	AuditActionFMCreate     = "fm_create"
	AuditActionCronCreate   = "cron_create"
	AuditActionCronUpdate   = "cron_update"
	AuditActionCronDelete   = "cron_delete"
	AuditActionServiceCreate = "service_create"
	AuditActionServiceUpdate = "service_update"
	AuditActionServiceDelete = "service_delete"
	AuditActionAlertRuleCreate = "alert_rule_create"
	AuditActionAlertRuleUpdate = "alert_rule_update"
	AuditActionAlertRuleDelete = "alert_rule_delete"
	AuditActionDDNSCreate   = "ddns_create"
	AuditActionDDNSUpdate   = "ddns_update"
	AuditActionDDNSDelete   = "ddns_delete"
	AuditActionNATCreate    = "nat_create"
	AuditActionNATUpdate    = "nat_update"
	AuditActionNATDelete    = "nat_delete"
	AuditActionNotifyCreate  = "notification_create"
	AuditActionNotifyUpdate  = "notification_update"
	AuditActionNotifyDelete  = "notification_delete"
	// 二开：AI 助手相关审计
	AuditActionAIQuery    = "ai_query"
	AuditActionAIToolCall = "ai_tool_call"
)

// AuditLog 审计日志模型
type AuditLog struct {
	Common
	UserID     uint64 `json:"user_id" gorm:"index"`
	Username    string `json:"username,omitempty" gorm:"-"`
	Action      string `json:"action" gorm:"type:varchar(32);index"`
	Resource    string `json:"resource,omitempty" gorm:"type:varchar(32)"`
	ResourceID  uint64 `json:"resource_id,omitempty"`
	Detail      string `json:"detail,omitempty" gorm:"type:text"`
	IP          string `json:"ip,omitempty" gorm:"type:varchar(64)"`
	UA          string `json:"ua,omitempty" gorm:"type:text"`
	Success     bool   `json:"success"`
}

func (AuditLog) TableName() string {
	return "audit_logs"
}
