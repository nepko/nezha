package model

import (
	"time"

	"gorm.io/gorm"
)

// BatchOperationRequest 批量操作请求
// swagger:model BatchOperationRequest
type BatchOperationRequest struct {
	ServerIDs []uint  `json:"server_ids" binding:"required,min=1"`
	Operation string  `json:"operation" binding:"required,oneof=restart shutdown execute"`
	Command   string  `json:"command"` // 仅 execute 操作时使用
	UserID    uint    `json:"-"`      // 内部使用
}

// BatchOperationResponse 批量操作响应
// swagger:model BatchOperationResponse
type BatchOperationResponse struct {
	TotalCount   int                    `json:"total_count"`
	SuccessCount int                    `json:"success_count"`
	FailedCount  int                    `json:"failed_count"`
	Operation    string                 `json:"operation"`
	Results      []ServerOperationResult `json:"results"`
}

// ServerOperationResult 单个服务器操作结果
// swagger:model ServerOperationResult
type ServerOperationResult struct {
	ServerID   uint   `json:"server_id"`
	ServerName string `json:"server_name"`
	Success    bool   `json:"success"`
	Message    string `json:"message"`
}

// BatchOperationHistory 批量操作历史记录
// swagger:model BatchOperationHistory
type BatchOperationHistory struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UserID    uint           `json:"user_id"`
	Operation string         `json:"operation"`
	ServerIDs string         `json:"server_ids"` // JSON 数组字符串
	Command   string         `json:"command"`    // 执行的命令（如果有）
	Result    string         `json:"result"`     // JSON 结果
	Total     int            `json:"total"`
	Success   int            `json:"success"`
	Failed    int            `json:"failed"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// BatchHistoryResponse 批量操作历史响应
// swagger:model BatchHistoryResponse
type BatchHistoryResponse struct {
	Total     int64                      `json:"total"`
	Page      int                        `json:"page"`
	PageSize  int                        `json:"page_size"`
	Histories []BatchOperationHistory    `json:"histories"`
}

// BatchServerMetricsResponse 服务器监控指标响应
// swagger:model BatchServerMetricsResponse
type BatchServerMetricsResponse struct {
	ServerID   uint                   `json:"server_id"`
	ServerName string                 `json:"server_name"`
	Metrics    map[string]interface{} `json:"metrics"`
	Period     string                 `json:"period"`
	Timestamp  int64                  `json:"timestamp"`
}

// BatchOperationStats 批量操作统计
// swagger:model BatchOperationStats
type BatchOperationStats struct {
	Operation   string    `json:"operation"`
	TotalCount  int       `json:"total_count"`
	SuccessRate float64   `json:"success_rate"`
	LastUsed    time.Time `json:"last_used"`
}

// ServerGroupBatchRequest 服务器组批量操作请求
// swagger:model ServerGroupBatchRequest
type ServerGroupBatchRequest struct {
	GroupID   uint     `json:"group_id"`
	Operation string   `json:"operation"`
	Command   string   `json:"command"`
	Filters   []string `json:"filters"` // 过滤条件
}

// CustomMetric 自定义监控指标
// swagger:model CustomMetric
type CustomMetric struct {
	ID          uint            `gorm:"primaryKey" json:"id"`
	ServerID    uint            `json:"server_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Command     string          `json:"command"` // 获取指标的shell命令
	Unit        string          `json:"unit"`
	Threshold   float64         `json:"threshold"` // 告警阈值
	Enabled     bool            `json:"enabled"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	DeletedAt   gorm.DeletedAt  `gorm:"index" json:"-"`
}

// CustomMetricData 自定义指标数据
// swagger:model CustomMetricData
type CustomMetricData struct {
	MetricID  uint      `json:"metric_id"`
	Value     float64   `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

// BeforeCreate 创建前的钩子
func (b *BatchOperationHistory) BeforeCreate(tx *gorm.DB) error {
	// 这里可以添加创建前的逻辑
	return nil
}

// AfterCreate 创建后的钩子
func (b *BatchOperationHistory) AfterCreate(tx *gorm.DB) error {
	// 这里可以添加创建后的逻辑，比如发送通知等
	return nil
}