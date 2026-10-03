package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// 文本类响应压缩。
//
// 之前所有响应都是未压缩的：列表页 HTML 25-53KB、
// app.css 约 60KB、alpine/htmx 各 40-50KB，
// 手机上每次切页都要重新走一遍，明显觉得卡。
// gzip 对这类文本通常能压到 1/4 左右，且标准库自带、无额外依赖。
var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return w
	},
}

// compressibleType 判断响应类型是否值得压缩。
//
// 只压缩明确知道的文本类型：未知类型（例如导出的 xlsx）不压缩，
// 它们本身就是压缩格式，再压一遍既浪费 CPU 又可能变大。
func compressibleType(ct string) bool {
	if ct == "" {
		return false
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case strings.HasPrefix(ct, "text/"):
		return true
	case ct == "application/json", ct == "application/javascript",
		ct == "application/xml", ct == "application/xhtml+xml",
		ct == "application/manifest+json", ct == "image/svg+xml",
		ct == "application/wasm":
		return true
	}
	return false
}

// acceptsGzip 解析 Accept-Encoding，判断客户端是否接受 gzip。
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		name, params, _ := strings.Cut(part, ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			if strings.Contains(params, "q=0") && !strings.Contains(params, "q=0.") {
				return false
			}
			return true
		}
		if strings.TrimSpace(name) == "*" {
			return true
		}
	}
	return false
}

type gzipWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	compressing bool
	wroteHeader bool
}

func (w *gzipWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	h := w.Header()
	// 已知类型且可压缩、且没有被别人压过时才压
	if h.Get("Content-Encoding") == "" && compressibleType(h.Get("Content-Type")) {
		w.compressing = true
		h.Del("Content-Length") // 压缩后长度会变，交给分块传输
		h.Set("Content-Encoding", "gzip")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.compressing {
		return w.ResponseWriter.Write(p)
	}
	return w.gz.Write(p)
}

// Flush 透传，保证流式响应不被缓冲住。
func (w *gzipWriter) Flush() {
	if w.gz != nil && w.compressing {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withGzip 在支持 gzip 的客户端上压缩文本响应。
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 无论是否压缩都要声明 Vary，避免中间缓存把两种响应搞混
		w.Header().Add("Vary", "Accept-Encoding")

		// 分段请求（Range）不能压缩，否则偏移量对不上
		if r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}

		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(w)
		gw := &gzipWriter{ResponseWriter: w, gz: gz}
		defer func() {
			if gw.compressing {
				_ = gz.Close()
			}
			gzipPool.Put(gz)
		}()
		next.ServeHTTP(gw, r)
	})
}
