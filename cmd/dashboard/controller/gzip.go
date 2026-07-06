package controller

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// gzipMimeTypes 仅对这些内容类型做 gzip，避免对已被自身压缩的格式（图片/
// 字体）二次压缩造成 CPU 浪费且无收益。
var gzipMimeTypes = map[string]bool{
	"text/html":                true,
	"text/css":                 true,
	"text/plain":               true,
	"application/javascript":   true,
	"application/x-javascript": true,
	"application/json":         true,
	"application/xml":          true,
	"image/svg+xml":            true,
	"application/wasm":         true,
}

// gzipMiddleware 对支持 gzip 的客户端，按内容类型流式压缩响应体，
// 显著减小前端静态资源与 JSON API 的传输体积（无需引入第三方压缩库）。
func gzipMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.Contains(c.Request.Header.Get("Accept-Encoding"), "gzip") {
			c.Next()
			return
		}
		// 已存在内容编码（如预压缩资源）则不再压缩。
		if c.Writer.Header().Get("Content-Encoding") != "" {
			c.Next()
			return
		}
		c.Header("Vary", "Accept-Encoding")
		w := &gzipWriter{ResponseWriter: c.Writer, level: gzip.DefaultCompression}
		c.Writer = w
		c.Next()
		_ = w.Close()
	}
}

// gzipWriter 在判定内容类型可压缩时，把响应体直接流式 gzip 写入底层 writer；
// 不可压缩则透传。Content-Encoding 在首次写入前设置，确保响应头正确。
type gzipWriter struct {
	gin.ResponseWriter
	gz    *gzip.Writer
	level int
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if w.gz == nil {
		mime := strings.ToLower(strings.TrimSpace(strings.Split(w.Header().Get("Content-Type"), ";")[0]))
		if !gzipMimeTypes[mime] {
			return w.ResponseWriter.Write(b)
		}
		gz, err := gzip.NewWriterLevel(w.ResponseWriter, w.level)
		if err != nil {
			return w.ResponseWriter.Write(b)
		}
		w.gz = gz
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
	}
	return w.gz.Write(b)
}

func (w *gzipWriter) Close() error {
	if w.gz == nil {
		return nil
	}
	err := w.gz.Close()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return err
}

// Flush 透传，避免影响 SSE 等需要刷新的响应。
func (w *gzipWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// 确保实现 io.Closer，便于在 defer 中安全关闭。
var _ io.Closer = (*gzipWriter)(nil)
