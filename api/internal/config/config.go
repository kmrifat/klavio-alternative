// Package config loads runtime settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"time"
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

	// Health (M6). BlocklistResolver is the host:port of your own recursive
	// DNS resolver: Spamhaus, SURBL and URIBL refuse queries that come through
	// big public resolvers. "" = the system resolver, "off" = no checks.
	BlocklistResolver string
	BlocklistEvery    time.Duration
	SpamhausDQSKey    string // optional Spamhaus Data Query Service key
	// PostmasterCredentialsFile: Google service-account JSON for Postmaster
	// Tools. Mount it as a secret; never commit it. "" = not connected.
	PostmasterCredentialsFile string
	// Cloudflare's CF-Connecting-IP / CF-IPCountry headers are believed only
	// when TrustCloudflareHeaders is on and the request's socket peer is in
	// TrustedProxies (the cloudflared sidecar). Anyone else could forge them.
	TrustCloudflareHeaders bool
	TrustedProxies         []netip.Prefix
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
	cfg.BlocklistResolver = get("BLOCKLIST_RESOLVER", "")
	cfg.SpamhausDQSKey = get("SPAMHAUS_DQS_KEY", "")
	cfg.PostmasterCredentialsFile = get("POSTMASTER_CREDENTIALS_FILE", "")
	if d, err := time.ParseDuration(get("BLOCKLIST_CHECK_INTERVAL", "6h")); err != nil || d < time.Hour {
		errs = append(errs, errors.New("BLOCKLIST_CHECK_INTERVAL must be a duration of at least 1h (the lists limit free use)"))
	} else {
		cfg.BlocklistEvery = d
	}
	switch v := get("TRUST_CLOUDFLARE_HEADERS", map[Env]string{EnvDevelopment: "false", EnvProduction: "true"}[cfg.Env]); v {
	case "true":
		cfg.TrustCloudflareHeaders = true
	case "false":
	default:
		errs = append(errs, fmt.Errorf("TRUST_CLOUDFLARE_HEADERS must be true or false, got %q", v))
	}
	for _, s := range strings.Split(get("TRUSTED_PROXY_CIDRS", "127.0.0.1/32,::1/128"), ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		p, err := parsePrefix(s)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("TRUSTED_PROXY_CIDRS: %q is not an IP or CIDR", s))
		case p.Bits() == 0:
			errs = append(errs, fmt.Errorf("TRUSTED_PROXY_CIDRS: %s would trust every client", s))
		default:
			cfg.TrustedProxies = append(cfg.TrustedProxies, p)
		}
	}
	if cfg.TrustCloudflareHeaders && len(cfg.TrustedProxies) == 0 {
		errs = append(errs, errors.New("TRUST_CLOUDFLARE_HEADERS=true needs TRUSTED_PROXY_CIDRS (the cloudflared address)"))
	}
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

// parsePrefix accepts a CIDR or a single address (a /32 or /128).
func parsePrefix(s string) (netip.Prefix, error) {
	if !strings.Contains(s, "/") {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if p.Addr().Is4In6() && p.Bits() >= 96 { // ::ffff:10.0.0.0/104 → 10.0.0.0/8
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	return p.Masked(), nil
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
