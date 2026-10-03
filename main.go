// Command island-venues-api serves the Island Venues booking API.
//
// All code lives in internal/ packages; this file only reads configuration,
// wires the dependencies and runs the HTTP server until SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/a-manraj-infrastream/island-venues-api/internal/api"
	"github.com/a-manraj-infrastream/island-venues-api/internal/config"
	"github.com/a-manraj-infrastream/island-venues-api/internal/events"
	"github.com/a-manraj-infrastream/island-venues-api/internal/identity"
	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
	"github.com/a-manraj-infrastream/island-venues-api/internal/telemetry"
	"github.com/a-manraj-infrastream/island-venues-api/internal/webhook"
)

// shutdownTimeout stays under Cloud Run's 10 s SIGTERM grace period, leaving
// time to flush spans after the HTTP server drains.
const shutdownTimeout = 6 * time.Second

func main() {
	// JSON logging from the very first line, even before config is read, so
	// start-up failures are still parsed correctly by Cloud Logging.
	slog.SetDefault(telemetry.NewLogger(os.Stdout, "", slog.LevelInfo))
	if err := run(); err != nil {
		slog.Error("island-venues-api stopped with an error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	logger := telemetry.NewLogger(os.Stdout, cfg.ProjectID, cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	tcfg := telemetry.Config{ProjectID: cfg.ProjectID, ServiceName: cfg.ServiceName, ServiceVersion: cfg.ServiceVersion}
	shutdownTracing, err := telemetry.SetupTracing(ctx, tcfg)
	if err != nil {
		return fmt.Errorf("failed to set up tracing: %w", err)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := shutdownTracing(flushCtx); err != nil {
			logger.Error("failed to flush traces", slog.Any("error", err))
		}
	}()
	telemetry.StartProfiler(tcfg, logger)

	st, closeStore, err := openStore(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeStore()

	pub, err := openPublisher(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := pub.Close(); err != nil {
			logger.Error("failed to close publisher", slog.Any("error", err))
		}
	}()

	resolver := identity.NewResolver(identity.Options{
		IAPAudience:   cfg.IAPAudience,
		StaffAudience: cfg.IAPStaffAudience,
		DevMode:       cfg.DevMode,
	})
	if resolver.Mode() == identity.ModeDev {
		logger.Warn("DEV_MODE: trusting the X-Dev-User header; never use this outside local development")
	}
	if resolver.Mode() == identity.ModeDisabled {
		logger.Warn("no identity provider configured (IAP_AUDIENCE unset): signed-in and staff routes will return 401")
	}
	if cfg.PaymentWebhookSecret == "" {
		logger.Warn("PAYMENT_WEBHOOK_SECRET unset: /webhooks/payments will return 503")
	}

	srv, err := api.New(api.Deps{
		Store:     st,
		Publisher: pub,
		Identity:  resolver,
		Webhook:   webhook.NewHandler(cfg.PaymentWebhookSecret, st, logger),
		Logger:    logger,
	})
	if err != nil {
		return fmt.Errorf("failed to build api: %w", err)
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("island-venues-api listening",
			slog.String("port", cfg.Port),
			slog.String("identity", resolver.Mode().String()),
			slog.Bool("postgres", cfg.DatabaseURL != ""),
			slog.Bool("pubsub", cfg.PublishEnabled()),
			slog.String("version", cfg.ServiceVersion),
		)
		serveErr <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to serve http: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received, draining requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("failed to shut down http server: %w", err)
	}
	logger.Info("http server stopped")
	return nil
}

// openStore picks PostgreSQL when DATABASE_URL is set, memory otherwise.
func openStore(ctx context.Context, cfg config.Config, logger *slog.Logger) (store.Store, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Info("DATABASE_URL unset: using the in-memory store (data is lost on restart)")
		return store.NewMemory(), func() {}, nil
	}
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pg, err := store.OpenPostgres(connectCtx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open postgres store: %w", err)
	}
	logger.Info("connected to postgres and applied migrations")
	return pg, pg.Close, nil
}

// openPublisher picks Pub/Sub when PUBSUB_TOPIC and GOOGLE_CLOUD_PROJECT are
// set, the logging no-op publisher otherwise.
func openPublisher(ctx context.Context, cfg config.Config, logger *slog.Logger) (events.Publisher, error) {
	if !cfg.PublishEnabled() {
		logger.Info("PUBSUB_TOPIC or GOOGLE_CLOUD_PROJECT unset: booking-created events are logged, not published")
		return events.LogPublisher{Logger: logger}, nil
	}
	pub, err := events.NewPubSubPublisher(ctx, cfg.ProjectID, cfg.PubSubTopic)
	if err != nil {
		return nil, fmt.Errorf("failed to open pubsub publisher: %w", err)
	}
	return pub, nil
}
