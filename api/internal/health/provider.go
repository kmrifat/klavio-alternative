package health

import (
	"net/http"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// SendingConfig is how the app reaches Amazon SES. It comes from environment
// variables (SES_MODE, SES_EVENT_BUS_ARN, SES_EVENTS_QUEUE_URL,
// SES_MAX_SEND_RATE) and is shown read-only on Settings → Sending providers.
type SendingConfig struct {
	Mode           string // "aws" or "fake"
	MaxSendRate    float64
	EventBusARN    string
	EventsQueueURL string
}

type sendingProvider struct {
	Provider string `json:"provider"` // always "amazon_ses" for now
	// Mode: "aws" sends real email; "fake" sends nothing (development).
	Mode   string `json:"mode"`
	Region string `json:"region"`
	// AppMaxSendRate: the sending worker's own cap, emails per second.
	AppMaxSendRate float64         `json:"app_max_send_rate"`
	EventBusARN    string          `json:"event_bus_arn"`
	EventsQueueURL string          `json:"events_queue_url"`
	Account        accountMeters   `json:"account"`
	Brands         []providerBrand `json:"brands"`
}

// providerBrand is what SES holds for one brand.
type providerBrand struct {
	Slug                string            `json:"slug"`
	Name                string            `json:"name"`
	Color               string            `json:"color"`
	Status              store.BrandStatus `json:"status"`
	SendingDomain       string            `json:"sending_domain"`
	MailFromDomain      string            `json:"mail_from_domain"`
	SESConfigurationSet string            `json:"ses_configuration_set"`
	SESIdentityStatus   string            `json:"ses_identity_status"`
}

// sendingProvider is company-level like the Safety center: it shows the
// account quota and every brand's SES setup.
func (h *Handlers) sendingProvider(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	if !p.SeesAllBrands() {
		return httpx.ErrForbidden
	}
	ctx := r.Context()
	s := h.Service
	cfg := s.cfg.Sending
	out := sendingProvider{Provider: "amazon_ses", Mode: cfg.Mode, Region: s.ses.Region(), AppMaxSendRate: cfg.MaxSendRate,
		EventBusARN: cfg.EventBusARN, EventsQueueURL: cfg.EventsQueueURL, Brands: []providerBrand{}}
	if out.Account, err = h.accountMeters(r); err != nil {
		return err
	}
	all, err := h.Store.ListBrands(ctx)
	if err != nil {
		return err
	}
	for _, b := range all {
		if !p.CanViewBrand(b.ID) {
			continue
		}
		out.Brands = append(out.Brands, providerBrand{Slug: b.Slug, Name: b.Name, Color: b.Color, Status: b.Status,
			SendingDomain: b.SendingDomain, MailFromDomain: b.MailFromDomain,
			SESConfigurationSet: b.SESConfigurationSet, SESIdentityStatus: b.SESIdentityStatus})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}
