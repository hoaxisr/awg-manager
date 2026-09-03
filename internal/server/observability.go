package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

var slowReqLog = slog.New(slog.NewTextHandler(os.Stderr, nil)).
	With(slog.String("component", "slow-http"))

func registerPprofRoutes(mux *http.ServeMux) {
	const prefix = "/debug/pprof/"
	mux.HandleFunc(prefix, pprof.Index)
	mux.HandleFunc(prefix+"cmdline", pprof.Cmdline)
	mux.HandleFunc(prefix+"profile", pprof.Profile)
	mux.HandleFunc(prefix+"symbol", pprof.Symbol)
	mux.HandleFunc(prefix+"trace", pprof.Trace)
	mux.Handle(prefix+"goroutine", pprof.Handler("goroutine"))
	mux.Handle(prefix+"heap", pprof.Handler("heap"))
	mux.Handle(prefix+"allocs", pprof.Handler("allocs"))
	mux.Handle(prefix+"block", pprof.Handler("block"))
	mux.Handle(prefix+"mutex", pprof.Handler("mutex"))
	mux.Handle(prefix+"threadcreate", pprof.Handler("threadcreate"))
}

func skipSlowRequestLog(path string) bool {
	if strings.HasPrefix(path, "/debug/pprof") {
		return true
	}
	switch path {
	case "/mcp",
		"/api/events",
		"/api/diagnostics/stream",
		"/api/singbox/subscriptions/get-stream",
		"/api/terminal/ws",
		"/api/test/speed/stream",
		"/api/system-tunnels/test-speed",
		"/api/singbox/tunnels/test/speed/stream":
		return true
	default:
		if strings.HasPrefix(path, "/api/singbox/clash") {
			return true
		}
		return false
	}
}

func (s *Server) slowRequestMiddleware(threshold time.Duration, next http.Handler) http.Handler {
	if threshold <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipSlowRequestLog(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		d := time.Since(start)
		if d < threshold {
			return
		}
		ms := float64(d.Microseconds()) / 1000
		slowReqLog.Warn("slow HTTP request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Float64("duration_ms", ms),
		)
		if s.loggingService != nil {
			s.loggingService.AppLog(
				logging.LevelWarn,
				logging.GroupSystem,
				logging.SubProfiling,
				"slow-http",
				fmt.Sprintf("%s %s", r.Method, r.URL.Path),
				fmt.Sprintf("%.1f ms (threshold exceeded)", ms),
			)
		}
	})
}
