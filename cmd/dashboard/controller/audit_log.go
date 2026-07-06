package controller

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

// listAuditLog 获取审计日志列表
// @Summary List audit logs
// @Description List audit logs with filters
// @Tags auth required
// @Param page query int false "Page number"
// @Param page_size query int false "Page size"
// @Param action query string false "Filter by action"
// @Param resource query string false "Filter by resource"
// @Param user_id query uint false "Filter by user ID"
// @Param start_time query string false "Start time (RFC3339)"
// @Param end_time query string false "End time (RFC3339)"
// @Produce json
// @Success 200 {object} model.CommonResponse[[]model.AuditLog]
// @Router /audit-log [get]
func listAuditLog(c *gin.Context) ([]model.AuditLog, error) {
	var query model.AuditLogQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		return nil, err
	}

	if query.PageSize == 0 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	if query.Page < 1 {
		query.Page = 1
	}
	offset := (query.Page - 1) * query.PageSize

	db := singleton.DB.Model(&model.AuditLog{})

	if query.Action != "" {
		db = db.Where("action = ?", query.Action)
	}
	if query.Resource != "" {
		db = db.Where("resource = ?", query.Resource)
	}
	if query.UserID > 0 {
		db = db.Where("user_id = ?", query.UserID)
	}
	if query.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, query.StartTime); err == nil {
			db = db.Where("created_at >= ?", t)
		}
	}
	if query.EndTime != "" {
		if t, err := time.Parse(time.RFC3339, query.EndTime); err == nil {
			db = db.Where("created_at <= ?", t)
		}
	}

	var total int64
	db.Count(&total)

	var logs []model.AuditLog
	if err := db.Order("id DESC").Offset(offset).Limit(query.PageSize).Find(&logs).Error; err != nil {
		return nil, err
	}

	// 补充 username（联表查询效率更高，但这里数据量小，N+1 可接受）
	for i := range logs {
		var u model.User
		if err := singleton.DB.First(&u, logs[i].UserID).Error; err == nil {
			logs[i].Username = u.Username
		}
	}

	// 构造分页响应
	c.Set("total", total)
	c.Set("offset", offset)
	c.Set("limit", query.PageSize)

	return logs, nil
}

// getAuditLogActions 获取所有审计操作类型（用于前端下拉过滤）
// @Summary Get audit log action types
// @Description Get all available audit action types
// @Tags auth required
// @Produce json
// @Success 200 {object} model.CommonResponse[[]string]
// @Router /audit-log/actions [get]
func getAuditLogActions(c *gin.Context) ([]string, error) {
	actions := []string{
		model.AuditActionLogin,
		model.AuditActionLoginFailed,
		model.AuditActionLogout,
		model.AuditActionConfigUpdate,
		model.AuditActionUserCreate,
		model.AuditActionUserUpdate,
		model.AuditActionUserDelete,
		model.AuditActionServerCreate,
		model.AuditActionServerUpdate,
		model.AuditActionServerDelete,
		model.AuditActionTerminalCreate,
		model.AuditActionFMCreate,
		model.AuditActionCronCreate,
		model.AuditActionCronUpdate,
		model.AuditActionCronDelete,
		model.AuditActionServiceCreate,
		model.AuditActionServiceUpdate,
		model.AuditActionServiceDelete,
		model.AuditActionAlertRuleCreate,
		model.AuditActionAlertRuleUpdate,
		model.AuditActionAlertRuleDelete,
		model.AuditActionDDNSCreate,
		model.AuditActionDDNSUpdate,
		model.AuditActionDDNSDelete,
		model.AuditActionNATCreate,
		model.AuditActionNATUpdate,
		model.AuditActionNATDelete,
		model.AuditActionNotifyCreate,
		model.AuditActionNotifyUpdate,
		model.AuditActionNotifyDelete,
	}
	return actions, nil
}

// cleanAuditLog 清理旧审计日志（管理员手动触发）
// @Summary Clean old audit logs
// @Description Delete audit logs older than specified days
// @Tags auth required
// @Accept json
// @Param days body int true "Delete logs older than this many days"
// @Produce json
// @Success 200 {object} model.CommonResponse[string]
// @Router /audit-log/clean [post]
func cleanAuditLog(c *gin.Context) (string, error) {
	type CleanReq struct {
		Days int `json:"days"`
	}
	var req CleanReq
	if err := c.ShouldBind(&req); err != nil {
		return "", err
	}
	if req.Days < 1 {
		req.Days = 90 // 默认保留 90 天
	}

	cutoff := time.Now().AddDate(0, 0, -req.Days)
	result := singleton.DB.Where("created_at < ?", cutoff).Delete(&model.AuditLog{})
	if result.Error != nil {
		return "", result.Error
	}

	return "cleaned " + strconv.FormatInt(result.RowsAffected, 10) + " records", nil
}
