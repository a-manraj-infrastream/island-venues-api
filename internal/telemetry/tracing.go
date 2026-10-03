package telemetry

import (
	"context"
	"fmt"
	"log/slog"

	"cloud.google.com/go/profiler"
	// Both packages are deprecated upstream (the exporter is archived after
	// 2027-01-01 in favour of OTLP to telemetry.googleapis.com). They are
	// used deliberately: the service contract names this exporter and
	// requires X-Cloud-Trace-Context propagation. See README "Known gaps".
	texporter "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace" //nolint:staticcheck // mandated by the service contract; migration tracked in README
	gcppropagator "github.com/GoogleCloudPlatform/opentelemetry-operations-go/propagator" //nolint:staticcheck // X-Cloud-Trace-Context is required by the contract
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Config holds what telemetry needs from the environment.
type Config struct {
	// ProjectID enables the Cloud Trace exporter and the profiler when set.
	ProjectID      string
	ServiceName    string
	ServiceVersion string
}

// Propagator extracts and injects both W3C traceparent/baggage and Google's
// X-Cloud-Trace-Context. Extraction runs left to right and later propagators
// overwrite earlier ones, so W3C wins when a request carries both headers
// (Cloud Run's front end sets both, consistently).
func Propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		gcppropagator.CloudTraceFormatPropagator{}, //nolint:staticcheck // see import comment
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

// SetupTracing installs a global TracerProvider and propagator and returns a
// shutdown function that flushes pending spans. A real SDK provider is always
// installed so spans (and therefore log trace ids) exist even locally; only
// the exporter depends on ProjectID.
func SetupTracing(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build telemetry resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		// Respect the caller's sampling decision (Cloud Run's front end makes
		// one); sample everything we originate. Fine for a demo's traffic.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	}
	if cfg.ProjectID != "" {
		exporter, err := texporter.New(texporter.WithProjectID(cfg.ProjectID)) //nolint:staticcheck // see import comment
		if err != nil {
			return nil, fmt.Errorf("failed to create cloud trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(Propagator())

	return func(ctx context.Context) error {
		if err := tp.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to flush tracer provider: %w", err)
		}
		return nil
	}, nil
}

// StartProfiler starts Cloud Profiler when a project is configured. A
// profiler failure is logged, not fatal: losing profiles must never take the
// API down.
func StartProfiler(cfg Config, logger *slog.Logger) {
	if cfg.ProjectID == "" {
		logger.Info("cloud profiler disabled (GOOGLE_CLOUD_PROJECT not set)")
		return
	}
	err := profiler.Start(profiler.Config{
		Service:        cfg.ServiceName,
		ServiceVersion: cfg.ServiceVersion,
		ProjectID:      cfg.ProjectID,
	})
	if err != nil {
		logger.Error("failed to start cloud profiler", slog.Any("error", err))
		return
	}
	logger.Info("cloud profiler started", slog.String("service", cfg.ServiceName), slog.String("version", cfg.ServiceVersion))
}
