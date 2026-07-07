package model

import (
	"github.com/goccy/go-json"
	"gorm.io/gorm"
)

// QuickCommand 快捷命令
type QuickCommand struct {
	Common
	Name    string `json:"name"`
	Command string `json:"command"`
	Servers []uint64 `gorm:"-" json:"servers"` // 目标服务器 ID 列表，空表示全部
	ServersRaw string `gorm:"default:'[]'" json:"-"`
}

func (q *QuickCommand) BeforeSave(tx *gorm.DB) error {
	data, err := json.Marshal(q.Servers)
	if err != nil {
		return err
	}
	q.ServersRaw = string(data)
	return nil
}

func (q *QuickCommand) AfterFind(tx *gorm.DB) error {
	if q.ServersRaw != "" {
		return json.Unmarshal([]byte(q.ServersRaw), &q.Servers)
	}
	return nil
}

type QuickCommandForm struct {
	Name    string   `json:"name" validate:"required"`
	Command string   `json:"command" validate:"required"`
	Servers []uint64 `json:"servers"`
}

// CommandHistory 命令历史记录
type CommandHistory struct {
	Common
	ServerID uint64 `json:"server_id" gorm:"index"`
	Command  string `json:"command"`
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// TerminalSession 终端会话记录（用于审计和断线重连）
type TerminalSession struct {
	Common
	ServerID  uint64 `json:"server_id" gorm:"index"`
	SessionID string `json:"session_id" gorm:"uniqueIndex"`
	UserID    uint64 `json:"user_id"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	// Recording 终端会话录制数据（JSON 格式，记录所有输入输出）
	Recording string `json:"recording,omitempty" gorm:"type:text"`
}

// TerminalSessionEvent 终端会话事件（用于审计日志）
type TerminalSessionEvent struct {
	ID        uint64 `gorm:"primaryKey" json:"id"`
	SessionID string `json:"session_id" gorm:"index"`
	Type      uint8  `json:"type"` // 1=input, 2=output
	Data      string `json:"data" gorm:"type:text"`
	Timestamp int64  `json:"timestamp"`
}

const (
	TerminalEventInput  = 1
	TerminalEventOutput = 2
)

// BatchCommandResult 批量命令执行结果
type BatchCommandResult struct {
	ServerID   uint64 `json:"server_id"`
	ServerName string `json:"server_name"`
	Success    bool   `json:"success"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
}

type BatchCommandRequest struct {
	Command string   `json:"command" validate:"required"`
	Servers []uint64 `json:"servers"`
}

// TerminalTheme 终端主题配置
type TerminalTheme struct {
	Name           string `json:"name"`
	Background     string `json:"background"`
	Foreground     string `json:"foreground"`
	Cursor         string `json:"cursor"`
	CursorAccent   string `json:"cursorAccent"`
	Selection      string `json:"selection"`
	Black          string `json:"black"`
	Red            string `json:"red"`
	Green          string `json:"green"`
	Yellow         string `json:"yellow"`
	Blue           string `json:"blue"`
	Magenta        string `json:"magenta"`
	Cyan           string `json:"cyan"`
	White          string `json:"white"`
	BrightBlack    string `json:"brightBlack"`
	BrightRed      string `json:"brightRed"`
	BrightGreen    string `json:"brightGreen"`
	BrightYellow   string `json:"brightYellow"`
	BrightBlue     string `json:"brightBlue"`
	BrightMagenta  string `json:"brightMagenta"`
	BrightCyan     string `json:"brightCyan"`
	BrightWhite    string `json:"brightWhite"`
}

// CommandPolicy 命令权限策略
type CommandPolicy struct {
	Common
	Name      string `json:"name"`
	Type      uint8  `json:"type"` // 1=whitelist, 2=blacklist
	Commands  string `json:"commands" gorm:"type:text"` // JSON array of regex patterns
	CommandsRaw string `gorm:"-" json:"commands_raw,omitempty"`
	Enabled   bool   `json:"enabled"`
	RequireApproval bool `json:"require_approval"` // 命中后进入审批流而非直接拒绝
}

func (cp *CommandPolicy) BeforeSave(tx *gorm.DB) error {
	data, err := json.Marshal(cp.CommandsRaw)
	if err != nil {
		return err
	}
	cp.Commands = string(data)
	return nil
}

func (cp *CommandPolicy) AfterFind(tx *gorm.DB) error {
	if cp.Commands != "" {
		return json.Unmarshal([]byte(cp.Commands), &cp.CommandsRaw)
	}
	return nil
}

const (
	CommandPolicyWhitelist = 1
	CommandPolicyBlacklist = 2
)

// CommandApproval 高危命令审批单
type CommandApproval struct {
	Common
	Command   string `json:"command" gorm:"type:text"`
	UserID    uint64 `json:"user_id"`
	Username  string `json:"username"`
	ServerIDs string `json:"server_ids" gorm:"type:text"` // JSON 数组
	Status    uint8  `json:"status"` // 1=pending 2=approved 3=rejected
	Approver  uint64 `json:"approver,omitempty"`
	Reason    string `json:"reason,omitempty" gorm:"type:text"`
}

const (
	CommandApprovalPending  = 1
	CommandApprovalApproved = 2
	CommandApprovalRejected = 3
)
