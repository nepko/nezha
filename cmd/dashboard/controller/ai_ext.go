package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/pkg/tsdb"
	"github.com/nezhahq/nezha/service/singleton"
)

// ---------------------------------------------------------------------------
// AI 每日 token 预算（防滥用，API Key 仅存于服务端）
// 轻量内存计数：按「日期 + 用户」聚合，跨日自动清零。重启后计数归零，
// 不影响安全（预算只是软限流，真正的成本上限由上游供应商侧控制）。
// ---------------------------------------------------------------------------

type aiDailyUsage struct {
	mu    sync.Mutex
	date  string
	usage map[uint64]int
}

var aiUsageStore = &aiDailyUsage{usage: map[uint64]int{}}

func aiToday() string { return time.Now().Format("2006-01-02") }

func aiUsedToday(userID uint64) int {
	aiUsageStore.mu.Lock()
	defer aiUsageStore.mu.Unlock()
	if aiUsageStore.date != aiToday() {
		aiUsageStore.date = aiToday()
		aiUsageStore.usage = map[uint64]int{}
	}
	return aiUsageStore.usage[userID]
}

func aiAddUsage(userID uint64, n int) {
	if n <= 0 {
		return
	}
	aiUsageStore.mu.Lock()
	defer aiUsageStore.mu.Unlock()
	if aiUsageStore.date != aiToday() {
		aiUsageStore.date = aiToday()
		aiUsageStore.usage = map[uint64]int{}
	}
	aiUsageStore.usage[userID] += n
}

// aiBudgetAllow 在请求开始时检查剩余预算（budget<=0 表示不限）。
func aiBudgetAllow(userID uint64, budget int) bool {
	if budget <= 0 {
		return true
	}
	return aiUsedToday(userID) < budget
}

// ---------------------------------------------------------------------------
// AI 审计：工具调用与对话均落库到现有 audit_logs，满足事后追溯。
// ---------------------------------------------------------------------------

func aiWriteToolAudit(c *gin.Context, tool string, argsJSON, result string, success bool) {
	if len(argsJSON) > 512 {
		argsJSON = argsJSON[:512]
	}
	if len(result) > 512 {
		result = result[:512]
	}
	singleton.WriteAuditLog(c, model.AuditActionAIToolCall, "ai_tool", 0,
		tool+" args="+argsJSON+" => "+result, success)
}

// ---------------------------------------------------------------------------
// 扩展工具执行器（复用既有命令策略/权限校验，不新开安全旁路）
// ---------------------------------------------------------------------------

func toUint64Slice(v any) []uint64 {
	var out []uint64
	switch arr := v.(type) {
	case []any:
		for _, e := range arr {
			out = append(out, uint64(toFloat64ID(e)))
		}
	case []float64:
		for _, e := range arr {
			out = append(out, uint64(e))
		}
	}
	return out
}

// aiToolGetServerMetricsHistory 拉取某服务器指定指标的时序数据，支持 1d/7d/30d，
// 用于回答“CPU 趋势”“上周流量”等趋势类问题。复用 server.go 的 serverMetricMap 与 TSDB 查询。
func aiToolGetServerMetricsHistory(c *gin.Context, args map[string]any) (string, error) {
	serverID := toFloat64ID(args["server_id"])
	if serverID == 0 {
		return "", fmt.Errorf("缺少 server_id")
	}
	metric := "cpu"
	if m, ok := args["metric"].(string); ok && m != "" {
		metric = m
	}
	period := "1d"
	if p, ok := args["period"].(string); ok && p != "" {
		period = p
	}
	mt, ok := serverMetricMap[metric]
	if !ok {
		return "", fmt.Errorf("不支持的指标: %s（可选: cpu/memory/disk/load/net_in_speed 等）", metric)
	}
	server, ok := singleton.ServerShared.Get(uint64(serverID))
	if !ok {
		return "", fmt.Errorf("服务器不存在: %d", int(serverID))
	}
	if !server.HasPermission(c) {
		return fmt.Sprintf("无法访问服务器 %d", int(serverID)), nil
	}
	pd, err := tsdb.ParseQueryPeriod(period)
	if err != nil {
		return "", err
	}
	if !singleton.TSDBEnabled() {
		return "TSDB 未启用，无法查询历史指标", nil
	}
	points, err := singleton.TSDBShared.QueryServerMetrics(uint64(serverID), mt, pd)
	if err != nil {
		return "", err
	}
	// 控制回传体量，避免把整段时序塞满对话上下文。
	if len(points) > 120 {
		points = points[len(points)-120:]
	}
	b, _ := json.Marshal(gin.H{
		"server_id":   int(serverID),
		"server_name": server.Name,
		"metric":      metric,
		"period":      period,
		"points":      points,
	})
	return string(b), nil
}

// aiToolListCronTasks 列出计划/触发任务（含命令与目标服务器），用于“有哪些定时任务”“某任务何时跑”。
func aiToolListCronTasks(c *gin.Context, args map[string]any) (string, error) {
	limit := 50
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if limit > 200 {
		limit = 200
	}
	var crons []model.Cron
	if err := singleton.DB.Order("id ASC").Limit(limit).Find(&crons).Error; err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(crons))
	for _, cr := range crons {
		if !cr.HasPermission(c) {
			continue
		}
		out = append(out, map[string]any{
			"id":                     cr.ID,
			"name":                   cr.Name,
			"task_type":              cr.TaskType,
			"scheduler":              cr.Scheduler,
			"command":                cr.Command,
			"notification_group_id":  cr.NotificationGroupID,
			"cover":                  cr.Cover,
			"servers":                cr.Servers,
			"last_result":            cr.LastResult,
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// aiToolListNotificationGroups 列出通知组（仅 id/name，不泄露 webhook 密钥）。
func aiToolListNotificationGroups(c *gin.Context, args map[string]any) (string, error) {
	var groups []model.NotificationGroup
	if err := singleton.DB.Order("id ASC").Find(&groups).Error; err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, map[string]any{"id": g.ID, "name": g.Name})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// aiToolListServices 列出服务监控项（不含敏感凭据）。
func aiToolListServices(c *gin.Context, args map[string]any) (string, error) {
	var svcs []model.Service
	if err := singleton.DB.Order("id ASC").Find(&svcs).Error; err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, map[string]any{
			"id":                     s.ID,
			"name":                   s.Name,
			"type":                   s.Type,
			"target":                 s.Target,
			"cover":                  s.Cover,
			"notification_group_id":  s.NotificationGroupID,
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// aiToolListServerGroups 列出服务器分组。
func aiToolListServerGroups(c *gin.Context, args map[string]any) (string, error) {
	var groups []model.ServerGroup
	if err := singleton.DB.Order("id ASC").Find(&groups).Error; err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, map[string]any{"id": g.ID, "name": g.Name})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// aiToolGetAlertRuleDetail 获取某告警规则的详细配置（触发模式、规则条目、通知组）。
func aiToolGetAlertRuleDetail(c *gin.Context, args map[string]any) (string, error) {
	ruleID := toFloat64ID(args["rule_id"])
	if ruleID == 0 {
		return "", fmt.Errorf("缺少 rule_id")
	}
	var rule model.AlertRule
	if err := singleton.DB.First(&rule, ruleID).Error; err != nil {
		return "", err
	}
	b, _ := json.Marshal(gin.H{
		"id":                     rule.ID,
		"name":                   rule.Name,
		"trigger_mode":           rule.TriggerMode,
		"enable":                 rule.Enable,
		"notification_group_id":  rule.NotificationGroupID,
		"rule_count":             len(rule.Rules),
		"rules":                  rule.Rules,
	})
	return string(b), nil
}

// aiToolGetTrafficSummary 聚合当前在线服务器的实时入/出带宽，回答“总带宽占用”“谁在吃流量”。
func aiToolGetTrafficSummary(c *gin.Context, args map[string]any) (string, error) {
	servers := singleton.ServerShared.GetList()
	var inSum, outSum float64
	online, total := 0, len(servers)
	perServer := make([]map[string]any, 0)
	for _, s := range servers {
		if s.GetTaskStream() == nil {
			continue
		}
		if !s.HasPermission(c) {
			continue
		}
		online++
		var in, out float64
		if s.State != nil {
			in = float64(s.State.NetInSpeed)
			out = float64(s.State.NetOutSpeed)
		}
		inSum += in
		outSum += out
		perServer = append(perServer, map[string]any{
			"id":          s.ID,
			"name":        s.Name,
			"net_in":      in,
			"net_out":     out,
		})
	}
	b, _ := json.Marshal(gin.H{
		"total_servers": total,
		"online":        online,
		"net_in_sum":    inSum,
		"net_out_sum":   outSum,
		"per_server":    perServer,
	})
	return string(b), nil
}

// aiToolBatchExecuteCommand 向多台服务器批量下发同一命令，复用既有命令策略校验与审批通道。
// 命中黑名单直接拒绝；需审批则建单待审；其余经 dispatchCommandToServers 下发。
func aiToolBatchExecuteCommand(c *gin.Context, args map[string]any) (string, error) {
	command, _ := args["command"].(string)
	command = strings.TrimSpace(command)
	serverIDs := toUint64Slice(args["server_ids"])
	if command == "" || len(serverIDs) == 0 {
		return "", fmt.Errorf("缺少 command 或 server_ids")
	}
	decision, err := evaluateCommandPolicy(command)
	if err != nil {
		return "", err
	}
	if decision.Blocked {
		return "命令被命令策略拒绝: " + decision.Reason, nil
	}
	if decision.NeedsApproval {
		var username string
		if u := aiCurrentUser(c); u != nil {
			username = u.Username
		}
		if _, e := createCommandApproval(command, aiCurrentUserID(c), username, serverIDs); e != nil {
			return "", e
		}
		return "命令命中需审批策略，已创建批量审批单，待管理员通过后执行", nil
	}
	results, err := dispatchCommandToServers(c, command, serverIDs)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(results)
	return string(b), nil
}

// aiToolRestartServer 重启指定服务器。高危动作：即使命令策略允许也强制走审批单，
// 由管理员确认后再经批量通道下发 reboot，避免 AI 误重启生产机。
func aiToolRestartServer(c *gin.Context, args map[string]any) (string, error) {
	serverID := toFloat64ID(args["server_id"])
	if serverID == 0 {
		return "", fmt.Errorf("缺少 server_id")
	}
	decision, err := evaluateCommandPolicy("reboot")
	if err == nil && decision.Blocked {
		return "重启命令被命令策略拒绝: " + decision.Reason, nil
	}
	if _, ok := singleton.ServerShared.Get(uint64(serverID)); !ok {
		return fmt.Sprintf("服务器 %d 不存在", int(serverID)), nil
	}
	var username string
	if u := aiCurrentUser(c); u != nil {
		username = u.Username
	}
	if _, e := createCommandApproval("reboot", aiCurrentUserID(c), username, []uint64{uint64(serverID)}); e != nil {
		return "", e
	}
	return fmt.Sprintf("已为服务器 %d 创建重启审批单，待管理员通过后执行", int(serverID)), nil
}

// ---------------------------------------------------------------------------
// 扩展工具定义（OpenAI tools 格式），由 ai.go 的 aiToolDefs 合并。
// ---------------------------------------------------------------------------

func aiExtToolDefs() []oaToolDef {
	return []oaToolDef{
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "get_server_metrics_history",
				Description: "获取指定服务器某指标的历史时序数据，用于回答趋势类问题（如“CPU 上周走势”“磁盘增长趋势”）。支持 period: 1d/7d/30d。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"server_id": map[string]any{"type": "integer", "description": "服务器 id"},
						"metric":    map[string]any{"type": "string", "description": "指标名，如 cpu/memory/disk/load1/net_in_speed/net_out_speed，默认 cpu"},
						"period":    map[string]any{"type": "string", "description": "时间范围，1d/7d/30d，默认 1d"},
					},
					"required": []string{"server_id"},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "list_cron_tasks",
				Description: "列出当前计划任务/触发任务（名称、调度表达式、命令、目标服务器、上次执行结果），用于排查“有哪些定时任务”“某任务何时执行”。",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"limit": map[string]any{"type": "integer", "description": "返回条数上限，默认 50"}},
					"required":   []string{},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "list_notification_groups",
				Description: "列出通知组（仅 id 与名称），用于回答“告警发到哪个通知组”。",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
					"required":   []string{},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "list_services",
				Description: "列出服务监控项（名称、类型、探测目标、覆盖范围、通知组），用于“哪些服务在监控”“某服务探测什么”。",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
					"required":   []string{},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "list_server_groups",
				Description: "列出服务器分组（id 与名称），用于按分组理解服务器拓扑。",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
					"required":   []string{},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "get_alert_rule_detail",
				Description: "获取某条告警规则的完整配置：触发模式、具体规则条目、绑定的通知组，用于“这条告警是怎么触发的”。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"rule_id": map[string]any{"type": "integer", "description": "告警规则 id"},
					},
					"required": []string{"rule_id"},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "get_traffic_summary",
				Description: "聚合当前所有在线服务器的实时入/出带宽与总量，回答“总带宽占用多少”“哪台在吃流量”。",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
					"required":   []string{},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "batch_execute_command",
				Description: "向多台服务器批量下发同一条 shell 命令（逐台经命令策略校验，高危命令转待审批）。返回每台的执行结果。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"server_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "目标服务器 id 列表"},
						"command":    map[string]any{"type": "string", "description": "要执行的 shell 命令"},
					},
					"required": []string{"server_ids", "command"},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "restart_server",
				Description: "重启指定服务器（高危）。无论命令策略如何都会先创建审批单，由管理员确认后才下发，避免误重启。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"server_id": map[string]any{"type": "integer", "description": "目标服务器 id"},
					},
					"required": []string{"server_id"},
				},
			},
		},
	}
}
