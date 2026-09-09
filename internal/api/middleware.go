package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type recorder struct {
	http.ResponseWriter
	status int
	size   int
}

func (r *recorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(data)
	r.size += n
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func middleware(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapped := &recorder{ResponseWriter: w}
		wrapped.Header().Set("X-Content-Type-Options", "nosniff")
		wrapped.Header().Set("Cache-Control", "no-store")
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("handler panic", "panic", recovered, "stack", string(debug.Stack()))
				if wrapped.status == 0 {
					respond(wrapped, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "internal", "detail": "服务暂时无法完成请求"}})
				}
			}
			status := wrapped.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("http request", "method", r.Method, "path", r.URL.Path, "status", status, "bytes", wrapped.size, "elapsed", time.Since(started))
		}()
		next.ServeHTTP(wrapped, r)
	})
}
