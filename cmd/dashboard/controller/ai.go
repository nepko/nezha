package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	pb "github.com/nezhahq/nezha/proto"
	"github.com/gin-gonic/gin"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

const (
	// aiDialTimeout 仅约束建连阶段（DNS/TLS/响应头），避免卡在建连。
	aiDialTimeout = 15 * time.Second
	// aiMaxStreamSeconds 单次对话的硬上限，防止模型/上游异常导致流永不结束、
	// 占满服务端 goroutine。用户主动断开仍由请求 context 即时取消。
	aiMaxStreamSeconds = 600
	// aiMaxToolRounds 单轮对话内工具调用的最大迭代次数，防止模型死循环。
	aiMaxToolRounds = 6
	// aiDefaultCompressionTokens 对话 token 估算超过该值即触发摘要压缩。
	aiDefaultCompressionTokens = 4000
)

// ---- OpenAI 消息内部格式 ----

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
}

type oaToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function oaToolFuncCall `json:"function"`
}

type oaToolFuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaToolDef struct {
	Type     string     `json:"type"`
	Function oaToolFunc `json:"function"`
}

type oaToolFunc struct {
	Name        string `json:"name"`
	Description  string `json:"description"`
	Parameters   any    `json:"parameters"`
}

// ---- 工具执行 ----

type aiToolExecutor func(c *gin.Context, args map[string]any) (string, error)

// aiToolRegistry 工具名 → 执行器。所有工具在 ScopeAdminAll 下运行（/ai/chat 已限定管理员）。
var aiToolRegistry = map[string]aiToolExecutor{
	"list_servers":       aiToolListServers,
	"get_server_metrics": aiToolGetServerMetrics,
	"list_alert_rules":   aiToolListAlertRules,
	"execute_command":    aiToolExecuteCommand,
}

// aiToolDefs 返回全部工具定义（OpenAI tools 格式）。
func aiToolDefs() []oaToolDef {
	return []oaToolDef{
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "list_servers",
				Description: "列出当前所有被监控服务器的基础信息：id、名称、是否在线、系统平台、架构、CPU/内存/磁盘使用率。用于回答“有哪些服务器”“哪台负载高”等问题。",
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
				Name:        "get_server_metrics",
				Description: "获取指定服务器的实时指标：cpu_usage、memory_usage、disk_usage、load、network_io。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"server_id": map[string]any{"type": "integer", "description": "服务器 id"},
						"metrics":   map[string]any{"type": "string", "description": "逗号分隔的指标名，可选，默认 cpu_usage,memory_usage,disk_usage,load"},
					},
					"required": []string{"server_id"},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "list_alert_rules",
				Description: "列出当前已启用的告警规则（名称、触发模式、关联通知组），用于排查“为什么收到告警”“有哪些监控项”。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit": map[string]any{"type": "integer", "description": "返回条数上限，默认 20"},
					},
					"required": []string{},
				},
			},
		},
		{
			Type: "function",
			Function: oaToolFunc{
				Name:        "execute_command",
				Description: "在指定服务器上执行一条 shell 命令（经命令策略校验，高危命令会转为待审批）。命令为 fire-and-forget，返回“已发送”状态，不保证实时回显输出；如需查看效果可随后调用 get_server_metrics。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"server_id": map[string]any{"type": "integer", "description": "目标服务器 id"},
						"command":   map[string]any{"type": "string", "description": "要执行的 shell 命令"},
					},
					"required": []string{"server_id", "command"},
				},
			},
		},
	}
}

// aiAllowedToolDefs 根据配置白名单过滤工具定义。
func aiAllowedToolDefs(conf *singleton.ConfigClass) []oaToolDef {
	all := aiToolDefs()
	if conf == nil || conf.AIAllowedTools == "" {
		return all
	}
	allowed := map[string]bool{}
	for _, name := range strings.Split(conf.AIAllowedTools, ",") {
		if n := strings.TrimSpace(name); n != "" {
			allowed[n] = true
		}
	}
	out := make([]oaToolDef, 0, len(all))
	for _, d := range all {
		if allowed[d.Function.Name] {
			out = append(out, d)
		}
	}
	return out
}

// ---- 工具执行器实现 ----

func aiToolListServers(c *gin.Context, args map[string]any) (string, error) {
	servers := singleton.ServerShared.GetList()
	out := make([]map[string]any, 0, len(servers))
	for _, s := range servers {
		entry := map[string]any{
			"id":     s.ID,
			"name":   s.Name,
			"online": s.GetTaskStream() != nil,
		}
		if s.Host != nil {
			entry["platform"] = s.Host.Platform
			entry["arch"] = s.Host.Arch
			entry["version"] = s.Host.Version
		}
		if s.State != nil {
			entry["cpu"] = s.State.CPU
			if s.Host != nil && s.Host.MemTotal > 0 {
				entry["mem_used_percent"] = float64(s.State.MemUsed) / float64(s.Host.MemTotal) * 100
			}
			if s.Host != nil && s.Host.DiskTotal > 0 {
				entry["disk_used_percent"] = float64(s.State.DiskUsed) / float64(s.Host.DiskTotal) * 100
			}
			entry["load1"] = s.State.Load1
		}
		out = append(out, entry)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func aiToolGetServerMetrics(c *gin.Context, args map[string]any) (string, error) {
	serverID := toFloat64ID(args["server_id"])
	if serverID == 0 {
		return "", fmt.Errorf("缺少 server_id")
	}
	metrics := "cpu_usage,memory_usage,disk_usage,load"
	if m, ok := args["metrics"].(string); ok && m != "" {
		metrics = m
	}
	server, ok := singleton.ServerShared.Get(uint64(serverID))
	if !ok {
		return "", fmt.Errorf("服务器不存在: %d", int(serverID))
	}
	metricList := strings.Split(metrics, ",")
	metricData := map[string]any{}
	if server.State != nil {
		for _, metric := range metricList {
			switch strings.TrimSpace(metric) {
			case "cpu_usage":
				metricData["cpu_usage"] = gin.H{"current": server.State.CPU, "unit": "%"}
			case "memory_usage":
				if server.Host != nil && server.Host.MemTotal > 0 {
					metricData["memory_usage"] = gin.H{
						"current": float64(server.State.MemUsed) / float64(server.Host.MemTotal) * 100,
						"unit":    "%",
					}
				}
			case "disk_usage":
				if server.Host != nil && server.Host.DiskTotal > 0 {
					metricData["disk_usage"] = gin.H{
						"current": float64(server.State.DiskUsed) / float64(server.Host.DiskTotal) * 100,
						"unit":    "%",
					}
				}
			case "load":
				metricData["load"] = gin.H{"load1": server.State.Load1, "load5": server.State.Load5, "load15": server.State.Load15}
			case "network_io":
				metricData["network_io"] = gin.H{
					"in_speed":  server.State.NetInSpeed,
					"out_speed": server.State.NetOutSpeed,
					"unit":      "bytes/s",
				}
			}
		}
	}
	b, _ := json.Marshal(gin.H{
		"server_id":   int(serverID),
		"server_name": server.Name,
		"online":      server.GetTaskStream() != nil,
		"metrics":     metricData,
	})
	return string(b), nil
}

func aiToolListAlertRules(c *gin.Context, args map[string]any) (string, error) {
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if limit > 100 {
		limit = 100
	}
	var rules []model.AlertRule
	if err := singleton.DB.Where("enable = ?", true).Order("id ASC").Limit(limit).Find(&rules).Error; err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(rules))
	for _, r := range rules {
		out = append(out, map[string]any{
			"id":           r.ID,
			"name":         r.Name,
			"trigger_mode": r.TriggerMode,
			"rule_count":   len(r.Rules),
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// aiToolExecuteCommand 复用既有命令策略校验（命中黑名单拦截/需审批转为待审），
// 通过则经 agent TaskStream 下发。安全护栏与现有批量执行一致，不额外放宽。
func aiToolExecuteCommand(c *gin.Context, args map[string]any) (string, error) {
	serverID := toFloat64ID(args["server_id"])
	command, _ := args["command"].(string)
	command = strings.TrimSpace(command)
	if serverID == 0 || command == "" {
		return "", fmt.Errorf("缺少 server_id 或 command")
	}
	decision, err := evaluateCommandPolicy(command)
	if err != nil {
		return "", err
	}
	if decision.Blocked {
		return "命令被命令策略拒绝: " + decision.Reason, nil
	}
	server, ok := singleton.ServerShared.Get(uint64(serverID))
	if !ok || server.GetTaskStream() == nil {
		return fmt.Sprintf("服务器 %d 不在线，无法执行", int(serverID)), nil
	}
	if decision.NeedsApproval {
		var username string
		if u := aiCurrentUser(c); u != nil {
			username = u.Username
		}
		if _, e := createCommandApproval(command, aiCurrentUserID(c), username, []uint64{uint64(serverID)}); e != nil {
			return "", e
		}
		return "命令命中需审批策略，已创建审批单，待管理员通过后执行", nil
	}
	task := &pb.Task{
		Id:   uint64(time.Now().UnixNano()),
		Type: model.TaskTypeCommand,
		Data: command,
	}
	server.SendTask(task)
	return fmt.Sprintf("命令已发送至服务器 %d: %s", int(serverID), command), nil
}

func toFloat64ID(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		f, _ := fmt.Sscanf(strings.TrimSpace(n), "%g", new(float64))
		_ = f
		var out float64
		fmt.Sscanf(strings.TrimSpace(n), "%g", &out)
		return out
	}
	return 0
}

func aiCurrentUser(c *gin.Context) *model.User {
	if v, ok := c.Get(model.CtxKeyAuthorizedUser); ok {
		if u, ok := v.(*model.User); ok {
			return u
		}
	}
	return nil
}

func aiCurrentUserID(c *gin.Context) uint64 {
	if u := aiCurrentUser(c); u != nil {
		return uint64(u.ID)
	}
	return 0
}

// ---- 上游调用 ----

// aiUpstreamClient 构造仅约束建连超时的 http.Client（流式读取由 context 控制）。
func aiUpstreamClient() *http.Client {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: aiDialTimeout}).DialContext,
		TLSHandshakeTimeout:   aiDialTimeout,
		ResponseHeaderTimeout: aiDialTimeout,
	}
	return &http.Client{Transport: transport}
}

// emitFn 向 SSE 流写入一条事件对象。
type emitFn func(obj map[string]any)

// aiWriteEvent 向 SSE 流写入一条事件对象。
func aiWriteEvent(c *gin.Context, flusher http.Flusher, obj map[string]any) {
	b, _ := json.Marshal(obj)
	fmt.Fprintf(c.Writer, "data: %s\n\n", string(b))
	if flusher != nil {
		flusher.Flush()
	}
}

// aiStreamRound 执行一轮模型调用并流式回传，累积助手消息（含可能的 tool_calls）。
// 返回本轮助手消息与 usage。tools 为空表示不提供工具。
func aiStreamRound(ctx context.Context, conf *singleton.ConfigClass, messages []oaMessage, tools []oaToolDef, reqTemperature float64, reqMaxTokens int, c *gin.Context, flusher http.Flusher, emit emitFn) (oaMessage, map[string]any, error) {
	temperature := reqTemperature
	if temperature == 0 {
		temperature = conf.AITemperature
	}
	if temperature == 0 {
		temperature = 0.3
	}
	maxTokens := reqMaxTokens
	if maxTokens == 0 {
		maxTokens = conf.AIMaxTokens
	}
	if maxTokens == 0 {
		maxTokens = 1024
	}
	baseURL := strings.TrimRight(conf.AIBaseURL, "/")
	payload := map[string]any{
		"model":       conf.AIModel,
		"messages":    messages,
		"stream":      true,
		"temperature": temperature,
		"max_tokens":  maxTokens,
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return oaMessage{}, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return oaMessage{}, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+conf.AIApiKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := aiUpstreamClient().Do(req)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return oaMessage{}, nil, err
		}
		return oaMessage{}, nil, fmt.Errorf("调用 AI 服务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return oaMessage{}, nil, fmt.Errorf("AI 服务返回错误: %s", strings.TrimSpace(string(errBody)))
	}

	var contentAcc strings.Builder
	var toolAccs []*oaToolCall
	var usage map[string]any

	decodeChunk := func(data string) (string, map[string]any, string) {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index *int   `json:"index"`
						ID    string `json:"id"`
						Type  string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", nil, ""
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return "", nil, chunk.Error.Message
		}
		var delta string
		for _, ch := range chunk.Choices {
			delta += ch.Delta.Content
			for _, tc := range ch.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				for len(toolAccs) <= idx {
					toolAccs = append(toolAccs, &oaToolCall{})
				}
				a := toolAccs[idx]
				if tc.ID != "" {
					a.ID = tc.ID
				}
				if tc.Type != "" {
					a.Type = tc.Type
				}
				if tc.Function.Name != "" {
					a.Function.Name = tc.Function.Name
				}
				a.Function.Arguments += tc.Function.Arguments
			}
		}
		if chunk.Usage != nil {
			usage = map[string]any{
				"prompt_tokens":     chunk.Usage.PromptTokens,
				"completion_tokens": chunk.Usage.CompletionTokens,
				"total_tokens":      chunk.Usage.TotalTokens,
			}
		}
		return delta, usage, ""
	}

	buf := make([]byte, 4096)
	var acc []byte
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			for {
				idx := bytes.Index(acc, []byte("\n\n"))
				if idx < 0 {
					break
				}
				eventBlock := string(acc[:idx])
				acc = acc[idx+2:]
				for _, line := range strings.Split(eventBlock, "\n") {
					line = strings.TrimSpace(line)
					if !strings.HasPrefix(line, "data:") {
						continue
					}
					data := strings.TrimSpace(line[len("data:"):])
					if data == "[DONE]" {
						continue
					}
					delta, u, errMsg := decodeChunk(data)
					if errMsg != "" {
					emit(map[string]any{"error": errMsg, "done": true})
					return oaMessage{}, nil, fmt.Errorf("%s", errMsg)
					}
					if delta != "" {
						contentAcc.WriteString(delta)
						emit(map[string]any{"delta": delta, "done": false})
					}
					if u != nil {
						usage = u
						emit(map[string]any{"usage": u, "done": false})
					}
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				if ctx.Err() == context.Canceled {
					return oaMessage{}, nil, readErr
				}
				emit(map[string]any{"error": "读取 AI 流失败: " + readErr.Error(), "done": true})
				return oaMessage{}, nil, readErr
			}
			break
		}
	}

	result := oaMessage{Role: "assistant", Content: contentAcc.String()}
	for _, a := range toolAccs {
		if a == nil {
			continue
		}
		tc := *a
		if tc.Type == "" {
			tc.Type = "function"
		}
		result.ToolCalls = append(result.ToolCalls, tc)
	}
	return result, usage, nil
}

// aiCompleteOnce 非流式一次性调用（用于对话摘要压缩）。
func aiCompleteOnce(ctx context.Context, conf *singleton.ConfigClass, messages []oaMessage) (string, error) {
	temperature := conf.AITemperature
	if temperature == 0 {
		temperature = 0.2
	}
	baseURL := strings.TrimRight(conf.AIBaseURL, "/")
	payload := map[string]any{
		"model":       conf.AIModel,
		"messages":    messages,
		"stream":      false,
		"temperature": temperature,
		"max_tokens":  1024,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+conf.AIApiKey)
	resp, err := aiUpstreamClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("AI 服务返回错误: %s", strings.TrimSpace(string(errBody)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("AI 服务未返回内容")
	}
	return out.Choices[0].Message.Content, nil
}

// estimateTokens 用字符数粗略估算 token（中文约 1 字≈1 token，英文≈4 字符）。
func estimateTokens(msgs []oaMessage) int {
	n := 0
	for _, m := range msgs {
		n += len([]rune(m.Content)) + 8
	}
	return n
}

// aiCompressHistory 当对话超过阈值时，将较早的轮次摘要为一条 assistant 消息，
// 保留最近一轮 user/assistant 以保留当前意图。失败则原样返回（降级不压缩）。
func aiCompressHistory(ctx context.Context, conf *singleton.ConfigClass, msgs []oaMessage) []oaMessage {
	threshold := conf.AICompressionThreshold
	if threshold <= 0 {
		threshold = aiDefaultCompressionTokens
	}
	if estimateTokens(msgs) <= threshold || len(msgs) <= 4 {
		return msgs
	}
	keep := msgs[len(msgs)-2:]
	toSum := msgs[:len(msgs)-2]
	var sb strings.Builder
	for _, m := range toSum {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	summary, err := aiCompleteOnce(ctx, conf, []oaMessage{
		{Role: "system", Content: "请将以下运维对话记录压缩为简洁的中文要点摘要，保留关键事实：服务器名/id、指标数值、结论、待办事项与未决问题。不要编造。"},
		{Role: "user", Content: sb.String()},
	})
	if err != nil || strings.TrimSpace(summary) == "" {
		return msgs
	}
	compressed := []oaMessage{{Role: "assistant", Content: "[历史摘要] " + summary}}
	compressed = append(compressed, keep...)
	return compressed
}

// aiChatStream 终端 AI 助手（Agent 版）：转发到 OpenAI 兼容 /chat/completions，
// 支持工具调用循环（查数据/执行动作），并以 SSE 流式回传。仅管理员可用。
// 明文 API Key 仅在服务端持有，绝不进入响应体。
func aiChatStream(c *gin.Context) {
	conf := singleton.Conf
	if conf == nil || !conf.AIEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "AI 助手未启用"})
		return
	}
	if conf.AIBaseURL == "" || conf.AIApiKey == "" || conf.AIModel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "AI 助手未正确配置（缺少 BaseURL / API Key / Model）"})
		return
	}

	var req struct {
		Messages    []oaMessage `json:"messages"`
		Temperature float64     `json:"temperature"`
		MaxTokens   int         `json:"max_tokens"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "messages 不能为空"})
		return
	}

	tools := []oaToolDef{}
	if conf.AIToolsEnabled {
		tools = aiAllowedToolDefs(conf)
	}

	// 进入 SSE 流式响应。
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
	flusher, _ := c.Writer.(http.Flusher)
	emit := func(obj map[string]any) { aiWriteEvent(c, flusher, obj) }

	ctx, cancel := context.WithTimeout(c.Request.Context(), aiMaxStreamSeconds*time.Second)
	defer cancel()

	messages := make([]oaMessage, len(req.Messages))
	copy(messages, req.Messages)

	for round := 0; round <= aiMaxToolRounds; round++ {
		assistant, usage, err := aiStreamRound(ctx, conf, messages, tools, req.Temperature, req.MaxTokens, c, flusher, emit)
		if err != nil {
			if ctx.Err() == context.Canceled {
				return
			}
			return
		}
		messages = append(messages, assistant)
		if usage != nil {
			emit(map[string]any{"usage": usage, "done": false})
		}
		if len(assistant.ToolCalls) == 0 {
			break
		}
		// 回传本轮工具调用，并执行、回传结果。
		for _, tc := range assistant.ToolCalls {
			emit(map[string]any{"tool_call": map[string]any{"name": tc.Function.Name, "arguments": tc.Function.Arguments}})
			args := map[string]any{}
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			var result string
			if exec, ok := aiToolRegistry[tc.Function.Name]; ok {
				r, e := exec(c, args)
				if e != nil {
					result = "工具执行失败: " + e.Error()
				} else {
					result = r
				}
			} else {
				result = "未知工具: " + tc.Function.Name
			}
			emit(map[string]any{"tool_result": map[string]any{"name": tc.Function.Name, "content": result}})
			messages = append(messages, oaMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}

	emit(map[string]any{"delta": "", "done": true})
}

// aiGetHistory 读取当前用户的持久化 AI 对话。
func aiGetHistory(c *gin.Context) (any, error) {
	uid := aiCurrentUserID(c)
	if uid == 0 {
		return gin.H{"messages": []oaMessage{}}, nil
	}
	var conv model.AIChatConversation
	err := singleton.DB.First(&conv, "user_id = ?", uid).Error
	if err != nil {
		return gin.H{"messages": []oaMessage{}}, nil
	}
	var msgs []oaMessage
	if conv.Messages != "" {
		_ = json.Unmarshal([]byte(conv.Messages), &msgs)
	}
	if msgs == nil {
		msgs = []oaMessage{}
	}
	return gin.H{"messages": msgs}, nil
}

// aiSaveHistory 保存（upsert）当前用户的 AI 对话，保存前做长上下文压缩。
func aiSaveHistory(c *gin.Context) (any, error) {
	uid := aiCurrentUserID(c)
	if uid == 0 {
		return nil, fmt.Errorf("未认证")
	}
	var req struct {
		Messages []oaMessage `json:"messages"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	conf := singleton.Conf
	ctx := c.Request.Context()
	msgs := req.Messages
	if conf != nil && conf.AIToolsEnabled {
		msgs = aiCompressHistory(ctx, conf, msgs)
	}
	data, err := json.Marshal(msgs)
	if err != nil {
		return nil, err
	}
	var conv model.AIChatConversation
	err = singleton.DB.First(&conv, "user_id = ?", uid).Error
	if err != nil {
		conv = model.AIChatConversation{UserID: uid}
	}
	conv.Messages = string(data)
	if err := singleton.DB.Save(&conv).Error; err != nil {
		return nil, err
	}
	return gin.H{"ok": true}, nil
}

// aiClearHistory 清空当前用户的 AI 对话。
func aiClearHistory(c *gin.Context) (any, error) {
	uid := aiCurrentUserID(c)
	if uid == 0 {
		return nil, fmt.Errorf("未认证")
	}
	if err := singleton.DB.Delete(&model.AIChatConversation{}, "user_id = ?", uid).Error; err != nil {
		return nil, err
	}
	return gin.H{"ok": true}, nil
}

// aiListTools 返回当前允许使用的工具清单（不含任何密钥），供前端展示。
func aiListTools(c *gin.Context) (any, error) {
	conf := singleton.Conf
	if conf == nil || !conf.AIEnabled || !conf.AIToolsEnabled {
		return gin.H{"enabled": false, "tools": []oaToolDef{}}, nil
	}
	return gin.H{"enabled": true, "tools": aiAllowedToolDefs(conf)}, nil
}
