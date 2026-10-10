package ses

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
)

// Message is one email ready to go out.
type Message struct {
	From             string // "Brightside <hello@news.brightside.co>"
	ReplyTo          string
	To               string
	Subject          string
	HTML             string
	Text             string
	Headers          map[string]string // List-Unsubscribe, List-Unsubscribe-Post
	ConfigurationSet string
	Tags             map[string]string // SES message tags: campaign_id, send_id
}

// Sender sends one email and returns SES's message ID.
type Sender interface {
	Send(ctx context.Context, m Message) (string, error)
}

// What can go wrong, as the sending worker needs to know it. Only the first
// two mean SES certainly did not send the message. Anything else (a timeout,
// a reset connection, an unexpected server error) might have come after SES
// accepted it, so it must never be retried: an unknown outcome is better
// than an email sent twice.
var (
	// ErrThrottled: over the account's send rate. Wait and try again.
	ErrThrottled = errors.New("ses: throttled")
	// ErrUnavailable: SES answered that it couldn't take the request now
	// (503 ServiceUnavailable). Not sent; safe to retry later.
	ErrUnavailable = errors.New("ses: service unavailable")
	// ErrRejected: SES refused this message for good (bad address, content).
	ErrRejected = errors.New("ses: message rejected")
	// ErrSendingPaused: the account or configuration set can't send at
	// all. Stop the campaign; retrying won't help.
	ErrSendingPaused = errors.New("ses: sending paused")
)

// Retryable reports whether err certainly means the message wasn't sent.
func Retryable(err error) bool {
	return errors.Is(err, ErrThrottled) || errors.Is(err, ErrUnavailable)
}

// Send uses SES v2 SendEmail with simple content, so SES builds the MIME
// message (both parts, DKIM-signed) and adds our headers.
func (a *AWS) Send(ctx context.Context, m Message) (string, error) {
	in := &sesv2.SendEmailInput{
		FromEmailAddress:     aws.String(m.From),
		Destination:          &types.Destination{ToAddresses: []string{m.To}},
		ConfigurationSetName: aws.String(m.ConfigurationSet),
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(m.Subject), Charset: aws.String("UTF-8")},
			Body: &types.Body{
				Html: &types.Content{Data: aws.String(m.HTML), Charset: aws.String("UTF-8")},
				Text: &types.Content{Data: aws.String(m.Text), Charset: aws.String("UTF-8")},
			},
			Headers: headers(m.Headers),
		}},
	}
	if m.ReplyTo != "" {
		in.ReplyToAddresses = []string{m.ReplyTo}
	}
	for k, v := range m.Tags {
		in.EmailTags = append(in.EmailTags, types.MessageTag{Name: aws.String(k), Value: aws.String(v)})
	}
	// No SDK retries: it would re-send after a timeout, behind our back, when
	// SES may already have accepted the first attempt.
	out, err := a.client.SendEmail(ctx, in, func(o *sesv2.Options) { o.Retryer = aws.NopRetryer{} })
	if err != nil {
		return "", classify(err)
	}
	return aws.ToString(out.MessageId), nil
}

func headers(h map[string]string) []types.MessageHeader {
	var out []types.MessageHeader
	for k, v := range h {
		out = append(out, types.MessageHeader{Name: aws.String(k), Value: aws.String(v)})
	}
	return out
}

func classify(err error) error {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return err // network, timeout, cancelled: the outcome is unknown
	}
	switch apiErr.ErrorCode() {
	case "TooManyRequestsException", "Throttling", "ThrottlingException", "LimitExceededException":
		return fmt.Errorf("%w: %s", ErrThrottled, apiErr.ErrorMessage())
	case "ServiceUnavailable", "ServiceUnavailableException":
		return fmt.Errorf("%w: %s", ErrUnavailable, apiErr.ErrorMessage())
	case "MessageRejected", "BadRequestException", "NotFoundException":
		return fmt.Errorf("%w: %s", ErrRejected, apiErr.ErrorMessage())
	case "AccountSuspendedException", "SendingPausedException", "MailFromDomainNotVerifiedException":
		return fmt.Errorf("%w: %s", ErrSendingPaused, apiErr.ErrorMessage())
	}
	return err
}

// --- fake ---

// FakeSender records messages instead of sending them. Addresses at the SES
// mailbox simulator behave like SES accepting them (the bounce or complaint
// arrives later as an event, which M4 handles); a To containing
// "reject@" is refused, "throttle@" is throttled on the first try, and
// "flaky@" is accepted but answered with a timeout (an ambiguous outcome).
type FakeSender struct {
	mu        sync.Mutex
	Sent      []Message
	throttled map[string]bool
	n         int
}

func (f *FakeSender) Send(_ context.Context, m Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(m.To, "reject@"):
		return "", fmt.Errorf("%w: fake rejection", ErrRejected)
	case strings.Contains(m.To, "flaky@"):
		f.n++
		f.Sent = append(f.Sent, m)          // SES took it…
		return "", context.DeadlineExceeded // …but the answer never came
	case strings.Contains(m.To, "throttle@") && !f.throttled[m.To]:
		if f.throttled == nil {
			f.throttled = map[string]bool{}
		}
		f.throttled[m.To] = true
		return "", fmt.Errorf("%w: fake throttle", ErrThrottled)
	}
	f.n++
	f.Sent = append(f.Sent, m)
	return fmt.Sprintf("fake-%06d", f.n), nil
}

// Messages returns a copy of everything sent so far.
func (f *FakeSender) Messages() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.Sent...)
}
