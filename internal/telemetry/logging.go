// Package telemetry wires logging, tracing and profiling for Cloud Run.
//
// Logs are JSON lines on stdout using the field names Cloud Logging's agent
// understands (severity, message, time) plus the trace correlation fields,
// so every log line written while serving a request appears nested under
// that request's trace in Cloud Trace and Logs Explorer. Nothing here needs
// GCP to run: without a project the tracer still creates spans (so log lines
// still carry trace ids) but exports nothing.
package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Cloud Logging special fields.
// https://cloud.google.com/logging/docs/structured-logging#special-payload-fields
const (
	FieldTrace        = "logging.googleapis.com/trace"
	FieldSpanID       = "logging.googleapis.com/spanId"
	FieldTraceSampled = "logging.googleapis.com/trace_sampled"
)

// NewLogger returns a JSON slog.Logger writing Cloud Logging fields to w.
// projectID builds the fully qualified trace name; when it is empty the bare
// trace id is logged, which still groups lines locally.
func NewLogger(w io.Writer, projectID string, level slog.Leveler) *slog.Logger {
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{
		AddSource:   false,
		Level:       level,
		ReplaceAttr: replaceCloudLoggingAttr,
	})
	return slog.New(&traceHandler{inner: inner, projectID: projectID})
}

// replaceCloudLoggingAttr renames slog's built-in keys to Cloud Logging's.
func replaceCloudLoggingAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.LevelKey:
		level, _ := a.Value.Any().(slog.Level)
		return slog.String("severity", severity(level))
	case slog.MessageKey:
		a.Key = "message"
	case slog.TimeKey:
		a.Key = "time"
	}
	return a
}

// severity maps slog levels onto Cloud Logging LogSeverity names.
func severity(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARNING"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// traceHandler adds trace correlation fields taken from the span in the
// record's context. Callers must use the *Context logging methods
// (InfoContext, ErrorContext...) for correlation to happen.
//
// Limitation: after WithGroup the trace fields would be nested inside the
// group and Cloud Logging would not see them, so this service only uses
// slog.Group attributes, never Logger.WithGroup.
type traceHandler struct {
	inner     slog.Handler
	projectID string
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(
			slog.String(FieldTrace, TraceName(h.projectID, sc.TraceID().String())),
			slog.String(FieldSpanID, sc.SpanID().String()),
			slog.Bool(FieldTraceSampled, sc.IsSampled()),
		)
	}
	if err := h.inner.Handle(ctx, r); err != nil {
		return fmt.Errorf("failed to write log record: %w", err)
	}
	return nil
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{inner: h.inner.WithAttrs(attrs), projectID: h.projectID}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{inner: h.inner.WithGroup(name), projectID: h.projectID}
}

// TraceName returns the value Cloud Logging expects in the trace field:
// projects/<project>/traces/<traceId>.
func TraceName(projectID, traceID string) string {
	if projectID == "" {
		return traceID
	}
	return "projects/" + projectID + "/traces/" + traceID
}
