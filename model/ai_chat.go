package model

import "time"

// AIChatConversation 按用户持久化的 AI 对话（仅存可见的 user/assistant 文本，
// 工具中间消息不落库）。Messages 为 OpenAI 格式 JSON 数组（role/content），
// 由后端在保存时按需做长上下文摘要压缩。
type AIChatConversation struct {
	UserID    uint64    `gorm:"primaryKey" json:"user_id"`
	Messages  string    `gorm:"type:text" json:"messages"`
	UpdatedAt time.Time `json:"updated_at"`
}
