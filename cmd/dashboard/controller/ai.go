package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/nezhahq/nezha/service/singleton"
)

const aiDefaultTimeout = 120 * time.Second

type aiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiChatRequest struct {
	Messages    []aiChatMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens"`
}

// jsonString 将字符串安全编码为 JSON 字符串字面量（含转义与引号）。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
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
	}
	body, err := json.Marshal(payload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 绑定客户端上下文：用户断开连接即取消上游请求，避免泄漏。
	upstreamReq, err := http.NewRequestWithContext(
		c.Request.Context(),
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

	client := &http.Client{Timeout: aiDefaultTimeout}
	resp, err := client.Do(upstreamReq)
	if err != nil {
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
					var chunk struct {
						Choices []struct {
							Delta struct {
								Content string `json:"content"`
							} `json:"delta"`
						} `json:"choices"`
						Error *struct {
							Message string `json:"message"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(data), &chunk); err != nil {
						continue
					}
					if chunk.Error != nil && chunk.Error.Message != "" {
						emit(map[string]any{"error": chunk.Error.Message, "done": true})
						return
					}
					for _, ch := range chunk.Choices {
						if ch.Delta.Content != "" {
							emit(map[string]any{"delta": ch.Delta.Content, "done": false})
						}
					}
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				emit(map[string]any{"error": "读取 AI 流失败: " + readErr.Error(), "done": true})
			}
			break
		}
	}

	emit(map[string]any{"delta": "", "done": true})
}
