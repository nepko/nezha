package model

type TerminalForm struct {
	Protocol string `json:"protocol,omitempty"`
	ServerID uint64 `json:"server_id,omitempty"`
}

type CreateTerminalResponse struct {
	SessionID  string `json:"session_id,omitempty"`
	ServerID   uint64 `json:"server_id,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

type TerminalSessionInfo struct {
	SessionID     string `json:"session_id,omitempty"`
	ServerID      uint64 `json:"server_id,omitempty"`
	ServerName    string `json:"server_name,omitempty"`
	CreatorUserID uint64 `json:"creator_user_id,omitempty"`
	CreatedAt     int64  `json:"created_at,omitempty"`
	ClosedAt      int64  `json:"closed_at,omitempty"`
	Active        bool   `json:"active"`
}
