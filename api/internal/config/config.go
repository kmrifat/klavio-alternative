// Package config loads runtime settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type Env string

const (
	EnvDevelopment Env = "development"
	EnvProduction  Env = "production"
)

type Config struct {
	Env            Env
	HTTPAddr       string
	DatabaseURL    string
	RedisURL       string
	LogLevel       slog.Level
	LogJSON        bool
	AllowedOrigins []string // web app origins: CORS and passkey origins

	AppURL         string // web app base URL, for invite links
	WebAuthnRPID   string // passkey scope: the registrable domain, e.g. yourco.com
	WebAuthnRPName string

	// Cloudflare Access. Both set = every admin request must carry a valid Access token.
	AccessTeamDomain string // e.g. acme.cloudflareaccess.com
	AccessAUD        string

	SESMode        string // "aws" or "fake"
	SESRegion      string
	SESEventBusARN string // EventBridge bus that receives SES events
	DNSResolver    string // host:port of the DNS server used for brand DNS checks

	// Unsubscribe links are HMAC-signed with this (32+ chars). Rotate by moving
	// the current value to UNSUBSCRIBE_SECRET_OLD: links in sent email keep working.
	UnsubscribeSecret    string
	UnsubscribeSecretOld string
	// PublicBaseURLOverride replaces https://<brand link domain> in public
	// links, for local development (e.g. http://localhost:8080).
	PublicBaseURLOverride string
}

// devUnsubscribeSecret is only for local development; production must set its own.
const devUnsubscribeSecret = "dev-only-unsubscribe-secret-not-for-production"

// Load reads configuration from the environment. getenv is injectable for tests;
// pass os.Getenv in production.
func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Env:         Env(get("APP_ENV", string(EnvDevelopment))),
		HTTPAddr:    get("HTTP_ADDR", ":8080"),
		DatabaseURL: get("DATABASE_URL", ""),
		RedisURL:    get("REDIS_URL", ""),
	}

	var errs []error

	switch cfg.Env {
	case EnvDevelopment, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, cfg.Env))
	}

	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if cfg.RedisURL == "" {
		errs = append(errs, errors.New("REDIS_URL is required"))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(get("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	switch format := get("LOG_FORMAT", defaultLogFormat(cfg.Env)); format {
	case "json":
		cfg.LogJSON = true
	case "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", format))
	}

	for _, o := range strings.Split(get("ALLOWED_ORIGINS", "http://localhost:5173"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
		}
	}

	cfg.AppURL = get("APP_URL", "http://localhost:5173")
	cfg.WebAuthnRPID = get("WEBAUTHN_RP_ID", "localhost")
	cfg.WebAuthnRPName = get("WEBAUTHN_RP_NAME", "Mail Console")
	cfg.AccessTeamDomain = get("CF_ACCESS_TEAM_DOMAIN", "")
	cfg.AccessAUD = get("CF_ACCESS_AUD", "")
	cfg.SESMode = get("SES_MODE", map[Env]string{EnvDevelopment: "fake", EnvProduction: "aws"}[cfg.Env])
	cfg.SESRegion = get("SES_REGION", "us-east-1")
	cfg.SESEventBusARN = get("SES_EVENT_BUS_ARN", "")
	cfg.DNSResolver = get("DNS_RESOLVER", "1.1.1.1:53")
	cfg.UnsubscribeSecret = get("UNSUBSCRIBE_SECRET", "")
	cfg.UnsubscribeSecretOld = get("UNSUBSCRIBE_SECRET_OLD", "")
	cfg.PublicBaseURLOverride = get("PUBLIC_BASE_URL_OVERRIDE", "")
	if cfg.UnsubscribeSecret == "" && cfg.Env == EnvDevelopment {
		cfg.UnsubscribeSecret = devUnsubscribeSecret
	}
	for name, v := range map[string]string{"UNSUBSCRIBE_SECRET": cfg.UnsubscribeSecret, "UNSUBSCRIBE_SECRET_OLD": cfg.UnsubscribeSecretOld} {
		if v != "" && len(v) < 32 {
			errs = append(errs, fmt.Errorf("%s must be at least 32 characters", name))
		}
	}

	if (cfg.AccessTeamDomain == "") != (cfg.AccessAUD == "") {
		errs = append(errs, errors.New("set both CF_ACCESS_TEAM_DOMAIN and CF_ACCESS_AUD, or neither"))
	}
	switch cfg.SESMode {
	case "fake":
	case "aws":
		if cfg.SESEventBusARN == "" {
			errs = append(errs, errors.New("SES_EVENT_BUS_ARN is required when SES_MODE=aws"))
		}
	default:
		errs = append(errs, fmt.Errorf("SES_MODE must be aws or fake, got %q", cfg.SESMode))
	}
	if cfg.Env == EnvProduction {
		if cfg.AccessTeamDomain == "" {
			errs = append(errs, errors.New("CF_ACCESS_TEAM_DOMAIN and CF_ACCESS_AUD are required in production"))
		}
		if cfg.SESMode != "aws" {
			errs = append(errs, errors.New("SES_MODE must be aws in production"))
		}
		if getenv("WEBAUTHN_RP_ID") == "" {
			errs = append(errs, errors.New("WEBAUTHN_RP_ID is required in production"))
		}
		if cfg.UnsubscribeSecret == "" || cfg.UnsubscribeSecret == devUnsubscribeSecret {
			errs = append(errs, errors.New("UNSUBSCRIBE_SECRET is required in production (32+ random characters)"))
		}
		if cfg.UnsubscribeSecretOld == devUnsubscribeSecret {
			errs = append(errs, errors.New("UNSUBSCRIBE_SECRET_OLD can't be the public development secret"))
		}
		if cfg.PublicBaseURLOverride != "" {
			errs = append(errs, errors.New("PUBLIC_BASE_URL_OVERRIDE is for development only"))
		}
	}

	return cfg, errors.Join(errs...)
}

// SecureCookies is true outside development (HTTPS only).
func (c Config) SecureCookies() bool { return c.Env == EnvProduction }

func defaultLogFormat(env Env) string {
	if env == EnvProduction {
		return "json"
	}
	return "text"
}

// Logger builds the process-wide logger for this config.
func (c Config) Logger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogJSON {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
