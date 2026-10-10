package config

import (
	"log/slog"
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != EnvDevelopment {
		t.Errorf("Env = %q, want development", cfg.Env)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.LogJSON {
		t.Error("LogJSON = true, want text logs in development")
	}
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "http://localhost:5173" {
		t.Errorf("AllowedOrigins = %v", cfg.AllowedOrigins)
	}
}

func TestLoadProductionUsesJSONLogs(t *testing.T) {
	cfg, err := Load(envFrom(map[string]string{
		"APP_ENV":               "production",
		"DATABASE_URL":          "postgres://x",
		"REDIS_URL":             "redis://x",
		"ALLOWED_ORIGINS":       "https://app.example.com, https://admin.example.com ,",
		"CF_ACCESS_TEAM_DOMAIN": "acme.cloudflareaccess.com",
		"CF_ACCESS_AUD":         "aud123",
		"SES_EVENT_BUS_ARN":     "arn:aws:events:us-east-1:123:event-bus/default",
		"WEBAUTHN_RP_ID":        "example.com",
		"UNSUBSCRIBE_SECRET":    "prod-secret-0123456789abcdef0123456789",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.LogJSON {
		t.Error("LogJSON = false, want json logs in production")
	}
	want := []string{"https://app.example.com", "https://admin.example.com"}
	if strings.Join(cfg.AllowedOrigins, "|") != strings.Join(want, "|") {
		t.Errorf("AllowedOrigins = %v, want %v", cfg.AllowedOrigins, want)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(envFrom(map[string]string{
		"APP_ENV":    "staging",
		"LOG_LEVEL":  "loud",
		"LOG_FORMAT": "xml",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"APP_ENV", "DATABASE_URL", "REDIS_URL", "LOG_LEVEL", "LOG_FORMAT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadDevelopmentDefaults(t *testing.T) {
	cfg, err := Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SESMode != "fake" || cfg.WebAuthnRPID != "localhost" || cfg.SecureCookies() {
		t.Errorf("unexpected dev defaults: %+v", cfg)
	}
}

func TestLoadProductionRequiresSafetySettings(t *testing.T) {
	_, err := Load(envFrom(map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "SES_MODE": "fake",
	}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"CF_ACCESS_TEAM_DOMAIN", "SES_MODE must be aws", "WEBAUTHN_RP_ID", "UNSUBSCRIBE_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadAccessNeedsBothSettings(t *testing.T) {
	_, err := Load(envFrom(map[string]string{
		"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "CF_ACCESS_AUD": "aud",
	}))
	if err == nil || !strings.Contains(err.Error(), "set both") {
		t.Fatalf("got %v", err)
	}
}

func TestUnsubscribeSecret(t *testing.T) {
	cfg, err := Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x"}))
	if err != nil || len(cfg.UnsubscribeSecret) < 32 {
		t.Fatalf("dev default: %q %v", cfg.UnsubscribeSecret, err)
	}
	_, err = Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "UNSUBSCRIBE_SECRET": "short"}))
	if err == nil || !strings.Contains(err.Error(), "UNSUBSCRIBE_SECRET must be at least 32") {
		t.Fatalf("short secret: %v", err)
	}
}
