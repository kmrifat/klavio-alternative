package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDebugDefaults(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x"}
	dev, err := Load(envFrom(base))
	if err != nil || !dev.Debug {
		t.Fatalf("development: Debug %v, %v; want on", dev.Debug, err)
	}
	prod := map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x",
		"CF_ACCESS_TEAM_DOMAIN": "acme.cloudflareaccess.com", "CF_ACCESS_AUD": "aud123",
		"SES_EVENT_BUS_ARN":    "arn:aws:events:us-east-1:123:event-bus/default",
		"SES_EVENTS_QUEUE_URL": "https://sqs.us-east-1.amazonaws.com/123/mail-ses-events",
		"WEBAUTHN_RP_ID":       "example.com", "UNSUBSCRIBE_SECRET": "prod-secret-0123456789abcdef0123456789",
		"APP_URL": "https://app.example.com", "ALLOWED_ORIGINS": "https://app.example.com",
	}
	cfg, err := Load(envFrom(prod))
	if err != nil || cfg.Debug {
		t.Fatalf("production: Debug %v, %v; want off", cfg.Debug, err)
	}
	prod["DEBUG"] = "true" // allowed (the app logs a warning)
	if cfg, err = Load(envFrom(prod)); err != nil || !cfg.Debug {
		t.Fatalf("production with DEBUG=true: %v, %v", cfg.Debug, err)
	}
	base["DEBUG"] = "yes"
	if _, err := Load(envFrom(base)); err == nil || !strings.Contains(err.Error(), "DEBUG must be true or false") {
		t.Fatalf("DEBUG=yes: %v", err)
	}
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
		"SES_EVENTS_QUEUE_URL":  "https://sqs.us-east-1.amazonaws.com/123/mail-ses-events",
		"WEBAUTHN_RP_ID":        "example.com",
		"UNSUBSCRIBE_SECRET":    "prod-secret-0123456789abcdef0123456789",
		"APP_URL":               "https://app.example.com",
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
	for _, want := range []string{"CF_ACCESS_TEAM_DOMAIN", "SES_MODE must be aws", "WEBAUTHN_RP_ID", "UNSUBSCRIBE_SECRET", "ALLOWED_ORIGINS and APP_URL are required"} {
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

func TestProductionRejectsDevSecretAsOld(t *testing.T) {
	_, err := Load(envFrom(map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x",
		"CF_ACCESS_TEAM_DOMAIN": "a.cloudflareaccess.com", "CF_ACCESS_AUD": "x", "WEBAUTHN_RP_ID": "example.com",
		"SES_EVENT_BUS_ARN": "arn:x", "UNSUBSCRIBE_SECRET": "prod-secret-0123456789abcdef0123456789",
		"UNSUBSCRIBE_SECRET_OLD": devUnsubscribeSecret,
	}))
	if err == nil || !strings.Contains(err.Error(), "UNSUBSCRIBE_SECRET_OLD") {
		t.Fatalf("got %v", err)
	}
}

func TestProxyTrustDefaults(t *testing.T) {
	dev, err := Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if dev.TrustCloudflareHeaders {
		t.Error("development trusts Cloudflare headers by default")
	}
	if got := fmt.Sprint(dev.TrustedProxies); got != "[127.0.0.1/32 ::1/128]" {
		t.Errorf("TrustedProxies = %s", got)
	}
	prod, err := Load(envFrom(map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x",
		"CF_ACCESS_TEAM_DOMAIN": "a.cloudflareaccess.com", "CF_ACCESS_AUD": "x", "WEBAUTHN_RP_ID": "example.com",
		"SES_EVENT_BUS_ARN": "arn:x", "UNSUBSCRIBE_SECRET": "prod-secret-0123456789abcdef0123456789",
		"SES_EVENTS_QUEUE_URL": "https://sqs.example/q",
		"APP_URL":              "https://app.example.com", "ALLOWED_ORIGINS": "https://app.example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !prod.TrustCloudflareHeaders {
		t.Error("production doesn't trust Cloudflare headers by default")
	}
}

func TestTrustedProxyCIDRs(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "TRUST_CLOUDFLARE_HEADERS": "true"}
	load := func(cidrs string) (Config, error) {
		env := map[string]string{"TRUSTED_PROXY_CIDRS": cidrs}
		for k, v := range base {
			env[k] = v
		}
		return Load(envFrom(env))
	}
	cfg, err := load(" 172.30.0.10 , 10.0.0.0/8,fd00::/8, ::ffff:192.168.0.0/112, 192.168.1.77/24 ")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(cfg.TrustedProxies); got != "[172.30.0.10/32 10.0.0.0/8 fd00::/8 192.168.0.0/16 192.168.1.0/24]" {
		t.Errorf("TrustedProxies = %s", got)
	}
	for in, want := range map[string]string{
		"0.0.0.0/0":       "would trust every client",
		"10.0.0.0/8,::/0": "would trust every client",
		"cloudflared":     "not an IP or CIDR",
		"10.0.0.0/33":     "not an IP or CIDR",
	} {
		if _, err := load(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", in, err, want)
		}
	}
	// Trust needs somebody to trust. (Unset falls back to loopback, so set it to a comma.)
	if _, err := load(","); err == nil || !strings.Contains(err.Error(), "needs TRUSTED_PROXY_CIDRS") {
		t.Errorf("empty proxies: %v", err)
	}
	base["TRUST_CLOUDFLARE_HEADERS"] = "yes"
	if _, err := load("127.0.0.1"); err == nil || !strings.Contains(err.Error(), "TRUST_CLOUDFLARE_HEADERS must be") {
		t.Errorf("bad bool: %v", err)
	}
}

func TestProductionNeedsTheEventsQueue(t *testing.T) {
	_, err := Load(envFrom(map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x",
		"CF_ACCESS_TEAM_DOMAIN": "a.cloudflareaccess.com", "CF_ACCESS_AUD": "x", "WEBAUTHN_RP_ID": "example.com",
		"SES_EVENT_BUS_ARN": "arn:x", "UNSUBSCRIBE_SECRET": "prod-secret-0123456789abcdef0123456789",
	}))
	if err == nil || !strings.Contains(err.Error(), "SES_EVENTS_QUEUE_URL") {
		t.Fatalf("got %v", err)
	}
	cfg, err := Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x",
		"TRACKING_SCANNER_CIDRS": "40.94.0.0/16, 203.0.113.9"}))
	if err != nil || len(cfg.TrackingScannerNets) != 2 {
		t.Fatalf("scanner nets: %v %v", cfg.TrackingScannerNets, err)
	}
	if _, err := Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "TRACKING_SCANNER_CIDRS": "nope"})); err == nil {
		t.Fatal("bad CIDR accepted")
	}
}

// Development switches off Access, secure cookies and the secret check, so
// it refuses a real domain; production needs the console's https address.
func TestDevelopmentStaysLocal(t *testing.T) {
	_, err := Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "WEBAUTHN_RP_ID": "yourco.com"}))
	if err == nil || !strings.Contains(err.Error(), "APP_ENV is development") {
		t.Fatalf("development on a real domain: %v", err)
	}
	if _, err := Load(envFrom(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x", "WEBAUTHN_RP_ID": "mail.test"})); err != nil {
		t.Fatalf("test domain: %v", err)
	}
	_, err = Load(envFrom(map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x",
		"CF_ACCESS_TEAM_DOMAIN": "a.cloudflareaccess.com", "CF_ACCESS_AUD": "x", "WEBAUTHN_RP_ID": "example.com",
		"SES_EVENT_BUS_ARN": "arn:x", "UNSUBSCRIBE_SECRET": "prod-secret-0123456789abcdef0123456789",
		"SES_EVENTS_QUEUE_URL": "https://sqs.example/q",
		"APP_URL":              "http://app.example.com", "ALLOWED_ORIGINS": "http://app.example.com",
	}))
	if err == nil || !strings.Contains(err.Error(), "must be https") {
		t.Fatalf("plain http console in production: %v", err)
	}
}
