package model

// TerminalRecordingChunk 终端会话录制分块。
// 终端双向字节流（用户输入 user→agent、agent 输出 agent→user）按帧落库，
// 回放时按 seq 重排、按 ts 定位时间轴。分块存储避免把整段会话塞进单一
// 文本字段导致行过大、难以增量写入与清理。
type TerminalRecordingChunk struct {
	ID        uint   `gorm:"primaryKey" json:"-"`
	SessionID string `json:"session_id" gorm:"index:idx_rec_session;size:64"`
	ServerID  uint64 `json:"server_id" gorm:"index:idx_rec_server"`
	Seq       int64  `json:"seq"`      // 会话内全局递增序号（双向共用一个计数器保证回放顺序）
	Ts        int64  `json:"ts"`       // Unix 毫秒，回放时间轴定位
	Direction uint8  `json:"direction"` // 1=用户输入(user→agent) 2=agent输出(agent→user)
	Data      []byte `json:"data" gorm:"type:blob"`
}

// RecordingSessionMeta 录制会话聚合信息，用于回放列表展示。
type RecordingSessionMeta struct {
	SessionID  string `json:"session_id"`
	ServerID   uint64 `json:"server_id"`
	ServerName string `json:"server_name,omitempty"`
	Chunks     int64  `json:"chunks"`
	StartTs    int64  `json:"start_ts"`
	EndTs      int64  `json:"end_ts"`
}

const (
	// RecordingDirectionInput 用户输入（user→agent）。
	RecordingDirectionInput = 1
	// RecordingDirectionOutput agent 输出（agent→user）。
	RecordingDirectionOutput = 2
)
