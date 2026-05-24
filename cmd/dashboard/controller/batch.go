package controller

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	pb "github.com/nezhahq/nezha/proto"
	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

// BatchServerOperation 批量服务器操作请求
// @Summary 批量服务器操作
// @Description 对多个服务器执行批量操作
// @Tags 批量操作
// @Accept json
// @Produce json
// @Param request body model.BatchOperationRequest true "批量操作请求"
// @Success 200 {object} model.CommonResponse[model.BatchOperationResponse]
// @Failure 400 {object} model.CommonResponse[any]
// @Router /batch/servers [post]
// @Security BearerAuth
func BatchServerOperation(c *gin.Context) (model.BatchOperationResponse, error) {
	var req model.BatchOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return model.BatchOperationResponse{}, &gin.Error{Err: err, Type: gin.ErrorTypeBind}
	}

	if len(req.ServerIDs) == 0 {
		return model.BatchOperationResponse{}, fmt.Errorf("服务器ID列表不能为空")
	}

	req.UserID = c.GetUint("user_id")

	var results []model.ServerOperationResult
	var successCount, failedCount int

	// 获取所有服务器 map
	serverMap := singleton.ServerShared.GetList()

	// 执行批量操作
	for _, id := range req.ServerIDs {
		server, ok := serverMap[uint64(id)]
		if ok {
			result := model.ServerOperationResult{
				ServerID:   id,
				ServerName: server.Name,
			}

			switch req.Operation {
			case "restart":
				if server.TaskStream != nil {
					task := &pb.Task{
						Id:   uint64(time.Now().UnixNano()),
						Type: model.TaskTypeCommand,
						Data: "systemctl reboot",
					}
					server.TaskStream.Send(task)
					result.Success = true
					result.Message = "重启命令已发送"
					successCount++
				} else {
					result.Success = false
					result.Message = "服务器离线"
					failedCount++
				}

			case "execute":
				if server.TaskStream != nil && req.Command != "" {
					task := &pb.Task{
						Id:   uint64(time.Now().UnixNano()),
						Type: model.TaskTypeCommand,
						Data: req.Command,
					}
					server.TaskStream.Send(task)
					result.Success = true
					result.Message = "命令已发送: " + req.Command
					successCount++
				} else {
					result.Success = false
					result.Message = "服务器离线或命令为空"
					failedCount++
				}

			case "shutdown":
				if server.TaskStream != nil {
					task := &pb.Task{
						Id:   uint64(time.Now().UnixNano()),
						Type: model.TaskTypeCommand,
						Data: "systemctl poweroff",
					}
					server.TaskStream.Send(task)
					result.Success = true
					result.Message = "关机命令已发送"
					successCount++
				} else {
					result.Success = false
					result.Message = "服务器离线"
					failedCount++
				}

			default:
				result.Success = false
				result.Message = "不支持的操作类型"
				failedCount++
			}

			results = append(results, result)
		}
	}

	// 保存操作历史
	serverIDsJSON, _ := json.Marshal(req.ServerIDs)
	resultJSON, _ := json.Marshal(results)
	history := model.BatchOperationHistory{
		UserID:    req.UserID,
		Operation: req.Operation,
		ServerIDs: string(serverIDsJSON),
		Command:   req.Command,
		Result:    string(resultJSON),
		Total:     len(req.ServerIDs),
		Success:   successCount,
		Failed:    failedCount,
	}
	singleton.DB.Create(&history)

	return model.BatchOperationResponse{
		TotalCount:   len(req.ServerIDs),
		SuccessCount: successCount,
		FailedCount:  failedCount,
		Results:      results,
		Operation:    req.Operation,
	}, nil
}

// GetBatchOperationHistory 获取批量操作历史
// @Summary 获取批量操作历史
// @Description 获取用户的批量操作历史记录
// @Tags 批量操作
// @Produce json
// @Param page query int false "页码" default(1)
// @Param size query int false "每页数量" default(10)
// @Success 200 {object} model.CommonResponse[model.BatchHistoryResponse]
// @Router /batch/history [get]
// @Security BearerAuth
func GetBatchOperationHistory(c *gin.Context) (model.BatchHistoryResponse, error) {
	userID := c.GetUint("user_id")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))

	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 10
	}

	offset := (page - 1) * size

	var histories []model.BatchOperationHistory
	var total int64

	tx := singleton.DB.Where("user_id = ?", userID).Order("created_at DESC")
	tx.Model(&model.BatchOperationHistory{}).Count(&total)
	tx.Offset(offset).Limit(size).Find(&histories)

	return model.BatchHistoryResponse{
		Total:     total,
		Page:      page,
		PageSize:  size,
		Histories: histories,
	}, nil
}

// GetServerMetrics 获取服务器自定义监控指标
// @Summary 获取服务器自定义监控指标
// @Description 获取服务器的自定义监控数据和历史趋势
// @Tags 服务器监控
// @Produce json
// @Param server_id path uint true "服务器ID"
// @Param metrics query string false "指标名称(逗号分隔)" default("disk_usage,memory_usage,cpu_usage")
// @Param period query string false "时间周期" default("24h") Enums(1h,6h,24h,7d,30d)
// @Success 200 {object} model.CommonResponse[model.BatchServerMetricsResponse]
// @Router /batch/servers/{server_id}/metrics [get]
// @Security BearerAuth
func GetServerMetrics(c *gin.Context) (model.BatchServerMetricsResponse, error) {
	serverID, _ := strconv.ParseUint(c.Param("server_id"), 10, 32)
	metrics := c.DefaultQuery("metrics", "disk_usage,memory_usage,cpu_usage")
	period := c.DefaultQuery("period", "24h")

	server, ok := singleton.ServerShared.Get(serverID)
	if !ok {
		return model.BatchServerMetricsResponse{}, fmt.Errorf("服务器不存在")
	}

	metricList := strings.Split(metrics, ",")
	metricData := make(map[string]interface{})

	if server.State != nil {
		for _, metric := range metricList {
			switch metric {
			case "cpu_usage":
				metricData["cpu_usage"] = gin.H{
					"current": server.State.CPU,
					"unit":    "%",
				}
			case "memory_usage":
				if server.Host != nil {
					memoryPercent := float64(server.State.MemUsed) / float64(server.Host.MemTotal) * 100
					metricData["memory_usage"] = gin.H{
						"current": memoryPercent,
						"used":    server.State.MemUsed,
						"total":   server.Host.MemTotal,
						"unit":    "%",
					}
				}
			case "disk_usage":
				if server.Host != nil {
					diskPercent := float64(server.State.DiskUsed) / float64(server.Host.DiskTotal) * 100
					metricData["disk_usage"] = gin.H{
						"current": diskPercent,
						"used":    server.State.DiskUsed,
						"total":   server.Host.DiskTotal,
						"unit":    "%",
					}
				}
			case "network_io":
				metricData["network_io"] = gin.H{
					"in_speed":  server.State.NetInSpeed,
					"out_speed": server.State.NetOutSpeed,
					"in_total":  server.State.NetInTransfer,
					"out_total": server.State.NetOutTransfer,
					"unit":      "bytes",
				}
			case "load":
				metricData["load"] = gin.H{
					"load1":  server.State.Load1,
					"load5":  server.State.Load5,
					"load15": server.State.Load15,
				}
			}
		}
	}

	return model.BatchServerMetricsResponse{
		ServerID:   uint(serverID),
		ServerName: server.Name,
		Metrics:    metricData,
		Period:     period,
		Timestamp:  time.Now().Unix(),
	}, nil
}
