package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"

	pb "github.com/nezhahq/nezha/proto"
	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

// ListQuickCommands 列出所有快捷命令
func listQuickCommands(c *gin.Context) ([]*model.QuickCommand, error) {
	var commands []model.QuickCommand
	if err := singleton.DB.Find(&commands).Error; err != nil {
		return nil, err
	}
	result := make([]*model.QuickCommand, len(commands))
	for i := range commands {
		commands[i].AfterFind(singleton.DB)
		result[i] = &commands[i]
	}
	return result, nil
}

// CreateQuickCommand 创建快捷命令
func createQuickCommand(c *gin.Context) (*model.QuickCommand, error) {
	var form model.QuickCommandForm
	if err := c.ShouldBindJSON(&form); err != nil {
		return nil, err
	}

	cmd := model.QuickCommand{
		Name:    form.Name,
		Command: form.Command,
		Servers: form.Servers,
	}
	if err := singleton.DB.Create(&cmd).Error; err != nil {
		return nil, err
	}
	return &cmd, nil
}

// UpdateQuickCommand 更新快捷命令
func updateQuickCommand(c *gin.Context) (*model.QuickCommand, error) {
	id := c.Param("id")
	var cmd model.QuickCommand
	if err := singleton.DB.First(&cmd, id).Error; err != nil {
		return nil, err
	}

	var form model.QuickCommandForm
	if err := c.ShouldBindJSON(&form); err != nil {
		return nil, err
	}

	cmd.Name = form.Name
	cmd.Command = form.Command
	cmd.Servers = form.Servers
	if err := singleton.DB.Save(&cmd).Error; err != nil {
		return nil, err
	}
	return &cmd, nil
}

// DeleteQuickCommand 删除快捷命令
func deleteQuickCommand(c *gin.Context) (*model.CommonResponse[any], error) {
	id := c.Param("id")
	if err := singleton.DB.Delete(&model.QuickCommand{}, id).Error; err != nil {
		return nil, err
	}
	return &model.CommonResponse[any]{
		Success: true,
	}, nil
}

// BatchDeleteQuickCommand 批量删除快捷命令
func batchDeleteQuickCommand(c *gin.Context) (*model.CommonResponse[any], error) {
	var ids []uint64
	if err := c.ShouldBindJSON(&ids); err != nil {
		return nil, err
	}
	if err := singleton.DB.Delete(&model.QuickCommand{}, ids).Error; err != nil {
		return nil, err
	}
	return &model.CommonResponse[any]{
		Success: true,
	}, nil
}

// ExecuteBatchCommand 批量执行命令
func executeBatchCommand(c *gin.Context) (*model.CommonResponse[[]*model.BatchCommandResult], error) {
	var req model.BatchCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, err
	}

	serverList := singleton.ServerShared.GetSortedList()
	results := make([]*model.BatchCommandResult, 0)

	for _, server := range serverList {
		if server == nil || server.TaskStream == nil {
			continue
		}

		// 如果指定了服务器列表，只执行指定的
		if len(req.Servers) > 0 {
			found := false
			for _, sid := range req.Servers {
				if server.ID == sid {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		if !server.HasPermission(c) {
			continue
		}

		result := &model.BatchCommandResult{
			ServerID:   server.ID,
			ServerName: server.Name,
		}

		taskData, err := json.Marshal(req.Command)
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		if err := server.TaskStream.Send(&pb.Task{
			Type: model.TaskTypeCommand,
			Data: string(taskData),
		}); err != nil {
			result.Error = err.Error()
		} else {
			result.Success = true
			result.Output = "命令已下发"
		}

		results = append(results, result)
	}

	return &model.CommonResponse[[]*model.BatchCommandResult]{
		Success: true,
		Data:    results,
	}, nil
}

// ListCommandHistory 列出命令历史
func listCommandHistory(c *gin.Context) ([]*model.CommandHistory, error) {
	serverID := c.Query("server_id")
	var history []model.CommandHistory

	query := singleton.DB.Order("created_at DESC").Limit(100)
	if serverID != "" {
		query = query.Where("server_id = ?", serverID)
	}

	if err := query.Find(&history).Error; err != nil {
		return nil, err
	}

	result := make([]*model.CommandHistory, len(history))
	for i := range history {
		result[i] = &history[i]
	}
	return result, nil
}

// AddCommandHistory 添加命令历史
func addCommandHistory(c *gin.Context) (*model.CommandHistory, error) {
	var history model.CommandHistory
	if err := c.ShouldBindJSON(&history); err != nil {
		return nil, err
	}
	if err := singleton.DB.Create(&history).Error; err != nil {
		return nil, err
	}
	return &history, nil
}

// ListTerminalSessions 列出终端会话
func listTerminalSessions(c *gin.Context) ([]*model.TerminalSession, error) {
	serverID := c.Query("server_id")
	var sessions []model.TerminalSession

	query := singleton.DB.Order("created_at DESC").Limit(50)
	if serverID != "" {
		query = query.Where("server_id = ?", serverID)
	}

	if err := query.Find(&sessions).Error; err != nil {
		return nil, err
	}

	result := make([]*model.TerminalSession, len(sessions))
	for i := range sessions {
		result[i] = &sessions[i]
	}
	return result, nil
}

// GetTerminalSessionEvents 获取终端会话事件（审计日志）
func getTerminalSessionEvents(c *gin.Context) ([]*model.TerminalSessionEvent, error) {
	sessionID := c.Param("id")
	var events []model.TerminalSessionEvent

	if err := singleton.DB.Where("session_id = ?", sessionID).Order("timestamp ASC").Find(&events).Error; err != nil {
		return nil, err
	}

	result := make([]*model.TerminalSessionEvent, len(events))
	for i := range events {
		result[i] = &events[i]
	}
	return result, nil
}

// ListCommandPolicies 列出命令策略
func listCommandPolicies(c *gin.Context) ([]*model.CommandPolicy, error) {
	var policies []model.CommandPolicy
	if err := singleton.DB.Find(&policies).Error; err != nil {
		return nil, err
	}
	result := make([]*model.CommandPolicy, len(policies))
	for i := range policies {
		policies[i].AfterFind(singleton.DB)
		result[i] = &policies[i]
	}
	return result, nil
}

// CreateCommandPolicy 创建命令策略
func createCommandPolicy(c *gin.Context) (*model.CommandPolicy, error) {
	var policy model.CommandPolicy
	if err := c.ShouldBindJSON(&policy); err != nil {
		return nil, err
	}
	if err := singleton.DB.Create(&policy).Error; err != nil {
		return nil, err
	}
	return &policy, nil
}

// UpdateCommandPolicy 更新命令策略
func updateCommandPolicy(c *gin.Context) (*model.CommandPolicy, error) {
	id := c.Param("id")
	var policy model.CommandPolicy
	if err := singleton.DB.First(&policy, id).Error; err != nil {
		return nil, err
	}

	var form model.CommandPolicy
	if err := c.ShouldBindJSON(&form); err != nil {
		return nil, err
	}

	policy.Name = form.Name
	policy.Type = form.Type
	policy.CommandsRaw = form.CommandsRaw
	policy.Enabled = form.Enabled

	if err := singleton.DB.Save(&policy).Error; err != nil {
		return nil, err
	}
	return &policy, nil
}

// DeleteCommandPolicy 删除命令策略
func deleteCommandPolicy(c *gin.Context) (*model.CommonResponse[any], error) {
	id := c.Param("id")
	if err := singleton.DB.Delete(&model.CommandPolicy{}, id).Error; err != nil {
		return nil, err
	}
	return &model.CommonResponse[any]{
		Success: true,
	}, nil
}

// BatchDeleteCommandPolicy 批量删除命令策略
func batchDeleteCommandPolicy(c *gin.Context) (*model.CommonResponse[any], error) {
	var ids []uint64
	if err := c.ShouldBindJSON(&ids); err != nil {
		return nil, err
	}
	if err := singleton.DB.Delete(&model.CommandPolicy{}, ids).Error; err != nil {
		return nil, err
	}
	return &model.CommonResponse[any]{
		Success: true,
	}, nil
}
