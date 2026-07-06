package controller

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newGzipTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gzipMiddleware())
	r.GET("/json", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"hello": "world"})
	})
	r.GET("/html", func(c *gin.Context) {
		c.Header("Content-Type", "text/html")
		c.String(http.StatusOK, "<h1>nezha</h1>")
	})
	r.GET("/binary", func(c *gin.Context) {
		// 非压缩类型（图片）应透传，不被 gzip。
		c.Header("Content-Type", "image/png")
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write([]byte("fake-png-bytes"))
	})
	return r
}

func TestGzipMiddleware_CompressesText(t *testing.T) {
	r := newGzipTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/json", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Result().Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip Content-Encoding, got %q", w.Result().Header.Get("Content-Encoding"))
	}
	gr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, _ := io.ReadAll(gr)
	var got map[string]string
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["hello"] != "world" {
		t.Fatalf("unexpected body %q", decoded)
	}
}

func TestGzipMiddleware_HtmlCompressed(t *testing.T) {
	r := newGzipTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/html", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Result().Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip for html")
	}
	gr, _ := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	decoded, _ := io.ReadAll(gr)
	if string(decoded) != "<h1>nezha</h1>" {
		t.Fatalf("unexpected html body %q", decoded)
	}
}

func TestGzipMiddleware_NoGzipWhenUnsupported(t *testing.T) {
	r := newGzipTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/json", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Result().Header.Get("Content-Encoding") != "" {
		t.Fatalf("expected no gzip when client lacks Accept-Encoding")
	}
}

func TestGzipMiddleware_PassthroughBinary(t *testing.T) {
	r := newGzipTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/binary", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Result().Header.Get("Content-Encoding") != "" {
		t.Fatalf("expected binary to pass through uncompressed")
	}
	if w.Body.String() != "fake-png-bytes" {
		t.Fatalf("unexpected binary body %q", w.Body.String())
	}
}
