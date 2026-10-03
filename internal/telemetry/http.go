package telemetry

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Handler instruments one route: otelhttp opens a server span (continuing
// the caller's trace via the global propagator) named after the route
// pattern, and an access log line is written inside that span so it carries
// the trace correlation fields.
func Handler(pattern string, h http.Handler, logger *slog.Logger) http.Handler {
	return otelhttp.NewHandler(accessLog(h, logger), pattern)
}

// accessLog writes one line per request in Cloud Logging's httpRequest
// format. Health probes are logged at DEBUG to keep the log quiet.
func accessLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		switch {
		case rec.status >= http.StatusInternalServerError:
			level = slog.LevelError
		case r.URL.Path == "/healthz":
			level = slog.LevelDebug
		}
		logger.LogAttrs(r.Context(), level, fmt.Sprintf("%s %s %d", r.Method, r.URL.Path, rec.status),
			slog.Group("httpRequest",
				slog.String("requestMethod", r.Method),
				slog.String("requestUrl", r.URL.RequestURI()),
				slog.Int("status", rec.status),
				slog.String("responseSize", fmt.Sprint(rec.bytes)),
				slog.String("userAgent", r.UserAgent()),
				slog.String("remoteIp", r.RemoteAddr),
				slog.String("protocol", r.Proto),
				slog.String("latency", fmt.Sprintf("%.6fs", time.Since(start).Seconds())),
			),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	if err != nil {
		return n, fmt.Errorf("failed to write response: %w", err)
	}
	return n, nil
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
