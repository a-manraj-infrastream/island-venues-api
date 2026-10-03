package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(t *testing.T, c Config)
	}{
		{"defaults", nil, false, func(t *testing.T, c Config) {
			if c.Port != "8080" || c.ServiceName != DefaultServiceName || c.DevMode || c.PublishEnabled() {
				t.Fatalf("defaults = %+v", c)
			}
		}},
		{"publish needs topic and project", map[string]string{"PUBSUB_TOPIC": "booking-created"}, false, func(t *testing.T, c Config) {
			if c.PublishEnabled() {
				t.Fatal("PublishEnabled() = true without GOOGLE_CLOUD_PROJECT")
			}
		}},
		{"publish enabled", map[string]string{"PUBSUB_TOPIC": "booking-created", "GOOGLE_CLOUD_PROJECT": "p"}, false, func(t *testing.T, c Config) {
			if !c.PublishEnabled() {
				t.Fatal("PublishEnabled() = false")
			}
		}},
		{"dev mode locally", map[string]string{"DEV_MODE": "true"}, false, func(t *testing.T, c Config) {
			if !c.DevMode {
				t.Fatal("DevMode = false")
			}
		}},
		{"dev mode refused on cloud run", map[string]string{"DEV_MODE": "true", "K_SERVICE": "island-venues-api"}, true, nil},
		{"dev mode inert with iap on cloud run", map[string]string{"DEV_MODE": "true", "K_SERVICE": "x", "IAP_AUDIENCE": "/projects/1/global/backendServices/2"}, false, nil},
		{"staff audience without iap audience", map[string]string{"IAP_STAFF_AUDIENCE": "/projects/1/global/backendServices/2"}, true, nil},
		{"bad dev mode", map[string]string{"DEV_MODE": "yes please"}, true, nil},
		{"log level from manifests", map[string]string{"LOG_LEVEL": "debug"}, false, func(t *testing.T, c Config) {
			if c.LogLevel != slog.LevelDebug {
				t.Fatalf("LogLevel = %v, want DEBUG", c.LogLevel)
			}
		}},
		{"unknown log level falls back to info", map[string]string{"LOG_LEVEL": "loud"}, false, func(t *testing.T, c Config) {
			if c.LogLevel != slog.LevelInfo {
				t.Fatalf("LogLevel = %v, want INFO", c.LogLevel)
			}
		}},
		{"version defaults to dev", nil, false, func(t *testing.T, c Config) {
			if c.ServiceVersion != "dev" {
				t.Fatalf("ServiceVersion = %q, want dev", c.ServiceVersion)
			}
		}},
		{"version from engine image", map[string]string{"RELEASE_VERSION": "v1.2.3", "INFRASTREAM_APP_VERSION": "v9"}, false, func(t *testing.T, c Config) {
			if c.ServiceVersion != "v1.2.3" {
				t.Fatalf("ServiceVersion = %q, want v1.2.3", c.ServiceVersion)
			}
		}},
		{"version from engine deployment", map[string]string{"INFRASTREAM_APP_VERSION": "v1.2.4"}, false, func(t *testing.T, c Config) {
			if c.ServiceVersion != "v1.2.4" {
				t.Fatalf("ServiceVersion = %q, want v1.2.4", c.ServiceVersion)
			}
		}},
		{"explicit version wins", map[string]string{"SERVICE_VERSION": "v2", "RELEASE_VERSION": "v1"}, false, func(t *testing.T, c Config) {
			if c.ServiceVersion != "v2" {
				t.Fatalf("ServiceVersion = %q, want v2", c.ServiceVersion)
			}
		}},
		{"webhook secret loses a trailing newline only", map[string]string{"PAYMENT_WEBHOOK_SECRET": " s3cret \r\n"}, false, func(t *testing.T, c Config) {
			if c.PaymentWebhookSecret != " s3cret " {
				t.Fatalf("PaymentWebhookSecret = %q, want %q", c.PaymentWebhookSecret, " s3cret ")
			}
		}},
		{"bad port", map[string]string{"PORT": "eighty"}, true, nil},
		{"port out of range", map[string]string{"PORT": "70000"}, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}
