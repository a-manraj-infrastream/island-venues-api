// Package config reads the service's environment in one place, so business
// code never calls os.Getenv.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// DefaultServiceName is used when SERVICE_NAME is unset.
const DefaultServiceName = "island-venues-api"

// Config is the service configuration.
type Config struct {
	Port                 string
	DatabaseURL          string
	IAPAudience          string
	IAPStaffAudience     string
	DevMode              bool
	PubSubTopic          string
	ProjectID            string
	PaymentWebhookSecret string
	ServiceName          string
	ServiceVersion       string
	// LogLevel is LOG_LEVEL (DEBUG, INFO, WARN or ERROR; default INFO).
	LogLevel slog.Level
	// CloudRunService is K_SERVICE, set by Cloud Run on every instance.
	CloudRunService string
}

// Load builds a Config from getenv (os.Getenv in main, a map in tests).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:             strings.TrimSpace(getenv("PORT")),
		DatabaseURL:      strings.TrimSpace(getenv("DATABASE_URL")),
		IAPAudience:      strings.TrimSpace(getenv("IAP_AUDIENCE")),
		IAPStaffAudience: strings.TrimSpace(getenv("IAP_STAFF_AUDIENCE")),
		PubSubTopic:      strings.TrimSpace(getenv("PUBSUB_TOPIC")),
		ProjectID:        strings.TrimSpace(getenv("GOOGLE_CLOUD_PROJECT")),
		// Only line endings are trimmed: a value added to Secret Manager with
		// `echo ... | gcloud secrets versions add --data-file=-` carries a
		// trailing newline the payment provider does not sign with, which
		// would make every webhook call fail with 401. Other whitespace is
		// kept, since it could be part of a deliberately chosen key.
		PaymentWebhookSecret: strings.TrimRight(getenv("PAYMENT_WEBHOOK_SECRET"), "\r\n"),
		ServiceName:          strings.TrimSpace(getenv("SERVICE_NAME")),
		ServiceVersion:       strings.TrimSpace(getenv("SERVICE_VERSION")),
		CloudRunService:      strings.TrimSpace(getenv("K_SERVICE")),
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if port, err := strconv.Atoi(cfg.Port); err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("failed to parse PORT %q: must be 1-65535", cfg.Port)
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = DefaultServiceName
	}
	// The engine-built image exports RELEASE_VERSION and the engine sets
	// INFRASTREAM_APP_VERSION on the Cloud Run service; use them so traces and
	// profiles carry the deployed version without extra configuration.
	for _, fallback := range []string{"RELEASE_VERSION", "INFRASTREAM_APP_VERSION"} {
		if cfg.ServiceVersion == "" {
			cfg.ServiceVersion = strings.TrimSpace(getenv(fallback))
		}
	}
	if cfg.ServiceVersion == "" {
		cfg.ServiceVersion = "dev"
	}
	// The manifests set LOG_LEVEL (debug in development). An unknown value
	// falls back to INFO rather than stopping the service over a log setting.
	if err := cfg.LogLevel.UnmarshalText([]byte(strings.TrimSpace(getenv("LOG_LEVEL")))); err != nil {
		cfg.LogLevel = slog.LevelInfo
	}

	if raw := strings.TrimSpace(getenv("DEV_MODE")); raw != "" {
		dev, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("failed to parse DEV_MODE %q: %w", raw, err)
		}
		cfg.DevMode = dev
	}
	// DEV_MODE trusts an unsigned header. On Cloud Run without IAP that would
	// let anyone impersonate any user, so refuse to start rather than run
	// wide open. (With IAP_AUDIENCE set, IAP wins and DEV_MODE is inert.)
	if cfg.DevMode && cfg.IAPAudience == "" && cfg.CloudRunService != "" {
		return Config{}, errors.New("DEV_MODE=true is not allowed on Cloud Run (K_SERVICE is set); set IAP_AUDIENCE instead")
	}
	if cfg.IAPStaffAudience != "" && cfg.IAPAudience == "" {
		return Config{}, errors.New("IAP_STAFF_AUDIENCE requires IAP_AUDIENCE")
	}
	return cfg, nil
}

// PublishEnabled reports whether events go to Pub/Sub rather than the log.
func (c Config) PublishEnabled() bool {
	return c.PubSubTopic != "" && c.ProjectID != ""
}
