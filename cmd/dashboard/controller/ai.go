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

	"github.com/gin-gonic/gin"

	"github.com/nezhahq/nezha/service/singleton"
)

const (
	// aiDialTimeout 仅约束建连阶段（DNS/TLS/响应头），避免卡在建连。
	aiDialTimeout = 15 * time.Second
	// aiMaxStreamSeconds 单次对话的硬上限，防止模型/上游异常导致流永不结束、
	// 占满服务端 goroutine。用户主动断开仍由请求 context 即时取消。
	aiMaxStreamSeconds = 600
)

type aiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiChatRequest struct {
	Messages    []aiChatMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens"`
}

// aiChatStream 终端 AI 助手：把请求转发到 OpenAI 兼容的 /chat/completions，
// 并以 SSE 将增量内容流式回传前端。仅管理员可用（路由已加 ScopeAdminAll）。
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

	var req aiChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "messages 不能为空"})
		return
	}

	temperature := req.Temperature
	if temperature == 0 {
		temperature = conf.AITemperature
	}
	if temperature == 0 {
		temperature = 0.3
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = conf.AIMaxTokens
	}
	if maxTokens == 0 {
		maxTokens = 1024
	}

	baseURL := strings.TrimRight(conf.AIBaseURL, "/")
	payload := map[string]any{
		"model":       conf.AIModel,
		"messages":    req.Messages,
		"stream":      true,
		"temperature": temperature,
		"max_tokens":  maxTokens,
		// 请求 usage 统计，便于前端展示 token 消耗。
		"stream_options": map[string]any{"include_usage": true},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 客户端断开即取消上游请求；叠加上限防止异常流永驻。
	ctx, cancel := context.WithTimeout(c.Request.Context(), aiMaxStreamSeconds*time.Second)
	defer cancel()

	upstreamReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+"/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+conf.AIApiKey)
	upstreamReq.Header.Set("Accept", "text/event-stream")

	// 关键修复：http.Client.Timeout 会把「读取整段流式响应」计入超时，
	// 导致长对话在旧值下被掐断。改为仅在建连阶段做超时，流式读取交由
	// 请求 context（用户断开 / 上限）控制。
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: aiDialTimeout}).DialContext,
		TLSHandshakeTimeout:   aiDialTimeout,
		ResponseHeaderTimeout: aiDialTimeout,
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		// 用户主动断开（context canceled）不应记为网关错误。
		if ctx.Err() == context.Canceled {
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "调用 AI 服务失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		c.JSON(resp.StatusCode, gin.H{"error": "AI 服务返回错误: " + strings.TrimSpace(string(errBody))})
		return
	}

	// 进入 SSE 流式响应。
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	flusher, _ := c.Writer.(http.Flusher)

	emit := func(obj map[string]any) {
		b, _ := json.Marshal(obj)
		fmt.Fprintf(c.Writer, "data: %s\n\n", string(b))
		if flusher != nil {
			flusher.Flush()
		}
	}

	// decodeSSE 解析单条 OpenAI 风格 SSE data：返回增量文本、usage 与上游错误。
	decodeSSE := func(data string) (delta string, usage map[string]any, errMsg string) {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
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
		for _, ch := range chunk.Choices {
			delta += ch.Delta.Content
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
					delta, usage, errMsg := decodeSSE(data)
					if errMsg != "" {
						emit(map[string]any{"error": errMsg, "done": true})
						return
					}
					if delta != "" {
						emit(map[string]any{"delta": delta, "done": false})
					}
					if usage != nil {
						emit(map[string]any{"usage": usage, "done": false})
					}
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				if ctx.Err() == context.Canceled {
					return
				}
				emit(map[string]any{"error": "读取 AI 流失败: " + readErr.Error(), "done": true})
			}
			break
		}
	}

	emit(map[string]any{"delta": "", "done": true})
}
