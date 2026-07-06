package model

type AuditLogQuery struct {
	Page      int    `form:"page"`
	PageSize  int    `form:"page_size"`
	Action    string `form:"action,omitempty"`
	Resource  string `form:"resource,omitempty"`
	UserID    uint64 `form:"user_id,omitempty"`
	StartTime string `form:"start_time,omitempty"` // RFC3339
	EndTime   string `form:"end_time,omitempty"`
}
