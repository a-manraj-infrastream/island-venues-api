package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func parseLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(buf)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestLoggerFieldNames(t *testing.T) {
	tests := []struct {
		name     string
		log      func(*slog.Logger, context.Context)
		severity string
	}{
		{"debug", func(l *slog.Logger, c context.Context) { l.DebugContext(c, "m") }, "DEBUG"},
		{"info", func(l *slog.Logger, c context.Context) { l.InfoContext(c, "m") }, "INFO"},
		{"warn", func(l *slog.Logger, c context.Context) { l.WarnContext(c, "m") }, "WARNING"},
		{"error", func(l *slog.Logger, c context.Context) { l.ErrorContext(c, "m") }, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.log(NewLogger(&buf, "proj", slog.LevelDebug), context.Background())
			lines := parseLines(t, &buf)
			if len(lines) != 1 {
				t.Fatalf("got %d lines", len(lines))
			}
			l := lines[0]
			if l["severity"] != tt.severity || l["message"] != "m" || l["time"] == nil {
				t.Fatalf("line = %v", l)
			}
			for _, k := range []string{"level", "msg", FieldTrace} {
				if _, ok := l[k]; ok {
					t.Fatalf("unexpected key %q in %v", k, l)
				}
			}
		})
	}
}

func TestLoggerTraceCorrelation(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	tests := []struct {
		project   string
		wantTrace string
	}{
		{"island-venues-prod", "projects/island-venues-prod/traces/4bf92f3577b34da6a3ce929d0e0e4736"},
		{"", "4bf92f3577b34da6a3ce929d0e0e4736"},
	}
	for _, tt := range tests {
		t.Run(tt.project, func(t *testing.T) {
			var buf bytes.Buffer
			NewLogger(&buf, tt.project, slog.LevelInfo).With(slog.String("k", "v")).InfoContext(ctx, "hello")
			l := parseLines(t, &buf)[0]
			if l[FieldTrace] != tt.wantTrace || l[FieldSpanID] != "00f067aa0ba902b7" || l[FieldTraceSampled] != true || l["k"] != "v" {
				t.Fatalf("line = %v", l)
			}
		})
	}
}

// TestHandlerContinuesIncomingTrace checks the full chain: the propagator
// extracts the caller's trace (W3C or X-Cloud-Trace-Context), otelhttp opens
// a child span, and log lines written in the handler carry that trace id.
func TestHandlerContinuesIncomingTrace(t *testing.T) {
	shutdown, err := SetupTracing(context.Background(), Config{ServiceName: "test", ServiceVersion: "0"})
	if err != nil {
		t.Fatalf("SetupTracing() error = %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	const tid = "4bf92f3577b34da6a3ce929d0e0e4736"
	tests := []struct {
		name   string
		header string
		value  string
	}{
		{"w3c traceparent", "traceparent", "00-" + tid + "-00f067aa0ba902b7-01"},
		{"google cloud trace context", "X-Cloud-Trace-Context", tid + "/12345;o=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewLogger(&buf, "proj", slog.LevelInfo)
			var spanInHandler trace.SpanContext
			h := Handler("GET /x", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				spanInHandler = trace.SpanContextFromContext(r.Context())
				logger.InfoContext(r.Context(), "inside")
				w.WriteHeader(http.StatusTeapot)
			}), logger)

			req := httptest.NewRequest(http.MethodGet, "/x?q=1", nil)
			req.Header.Set(tt.header, tt.value)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if got := spanInHandler.TraceID().String(); got != tid {
				t.Fatalf("handler trace id = %s, want %s", got, tid)
			}
			lines := parseLines(t, &buf)
			if len(lines) != 2 {
				t.Fatalf("got %d log lines, want 2 (handler + access log)", len(lines))
			}
			for _, l := range lines {
				if l[FieldTrace] != "projects/proj/traces/"+tid {
					t.Fatalf("line without trace correlation: %v", l)
				}
			}
			access, ok := lines[1]["httpRequest"].(map[string]any)
			if !ok || access["status"] != float64(http.StatusTeapot) || access["requestMethod"] != "GET" || access["requestUrl"] != "/x?q=1" {
				t.Fatalf("access log = %v", lines[1])
			}
		})
	}
}

func TestStartProfilerDisabledWithoutProject(t *testing.T) {
	var buf bytes.Buffer
	StartProfiler(Config{ServiceName: "s"}, NewLogger(&buf, "", slog.LevelInfo))
	if !bytes.Contains(buf.Bytes(), []byte("profiler disabled")) {
		t.Fatalf("log = %s", buf.String())
	}
}
