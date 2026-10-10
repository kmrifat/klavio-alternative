package campaigns

import (
	"context"
	"fmt"

	"github.com/kmrifat/klavio-alternative/api/internal/mailer"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
)

// Mailer sends one-off brand emails (M7's double opt-in confirmations and
// re-permission requests) through SES: from the brand's sender, in its
// configuration set, and paced together with campaign sends so the account
// rate holds.
func (s *Service) Mailer() mailer.Sender { return serviceMailer{s} }

type serviceMailer struct{ s *Service }

func (m serviceMailer) Send(ctx context.Context, msg mailer.Message) error {
	b := msg.Brand
	if !brandCanSend(b) {
		return fmt.Errorf("brand %s can't send right now (status %s)", b.Slug, b.Status)
	}
	sender, err := m.s.store.BrandSender(ctx, b.ID)
	if err != nil {
		return err
	}
	if err := m.s.pacer.Wait(ctx); err != nil {
		return err
	}
	_, err = m.s.sender.Send(ctx, ses.Message{
		From: FromHeader(b, sender), ReplyTo: sender.ReplyTo, To: msg.To,
		Subject: oneLine(msg.Subject), HTML: msg.HTML, Text: msg.Text, Headers: msg.Headers,
		ConfigurationSet: b.SESConfigurationSet,
		Tags:             map[string]string{"kind": msg.Kind, "brand_id": b.ID.String()},
	})
	return err
}
