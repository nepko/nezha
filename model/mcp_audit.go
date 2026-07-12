package model

import "time"

// MCPAuditLog 记录每一次 MCP tool 调用，用于事后追责与异常检测。
// 写入是 best-effort：失败仅打日志，不阻塞业务请求。
type MCPAuditLog struct {
	ID         uint64    `gorm:"primaryKey" json:"id"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
	UserID     uint64    `gorm:"index" json:"user_id"`
	TokenID    uint64    `gorm:"index" json:"token_id"`
	Tool       string    `gorm:"type:varchar(64);index" json:"tool"`
	ServerID   uint64    `gorm:"index" json:"server_id,omitempty"`
	ArgsHash   string    `gorm:"type:char(64)" json:"args_hash"`
	ArgsPeek   string    `gorm:"type:varchar(512)" json:"args_peek,omitempty"`
	Outcome    string    `gorm:"type:varchar(32);index" json:"outcome"`
	ErrorCode  string    `gorm:"type:varchar(32)" json:"error_code,omitempty"`
	ErrorMsg   string    `gorm:"type:varchar(512)" json:"error_msg,omitempty"`
	DurationMs int64     `json:"duration_ms"`
	IP         string    `gorm:"type:varchar(64)" json:"ip"`
}

func (MCPAuditLog) TableName() string {
	return "mcp_audit_logs"
}

const (
	MCPOutcomeOK             = "ok"
	MCPOutcomeScopeDenied    = "scope_denied"
	MCPOutcomePermDenied     = "permission_denied"
	MCPOutcomeServerOffline  = "server_offline"
	MCPOutcomeAgentTimeout   = "agent_timeout"
	MCPOutcomeAgentError     = "agent_error"
	// MCPOutcomeMCPDisabled 区分 “管理员按下 kill switch 把 MCP 关了” 与
	// “agent 真出故障” 两种语义：前者属 forbidden 类、不应该触发 agent
	// 故障告警；详见 service/rpc/mcp_rpc.go 里 ErrMCPDisabled 的注释。
	MCPOutcomeMCPDisabled    = "mcp_disabled"
	MCPOutcomeInvalidArgs    = "invalid_args"
	MCPOutcomeRateLimited    = "rate_limited"
	MCPOutcomeUnsupportedAgent = "unsupported_agent"
	MCPOutcomeInternalError  = "internal_error"
	// MCPOutcomePolicyBlocked 命令策略（黑名单命中无需审批 / 白名单未命中）
	// 直接拦截 server.exec 的执行；与快捷命令/批量命令/AI 助手的策略校验共用
	// evaluateCommandPolicy，避免 MCP 工具成为命令策略的绕过点。
	MCPOutcomePolicyBlocked = "policy_blocked"
	// MCPOutcomePolicyApprovalRequired 命令策略命中"需审批"项。MCP server.exec
	// 是同步、程序化调用（LLM/脚本），无法等待人工审批，因此直接拒绝并提示
	// 改走快捷命令/批量命令的审批流程下发。
	MCPOutcomePolicyApprovalRequired = "policy_approval_required"
)
