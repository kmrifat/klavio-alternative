// Package events reads Amazon SES sending events (delivered, bounced,
// complained, …) from the SQS queue EventBridge fills (infra/aws/events.tf),
// stores them, and acts on the ones that matter for safety: hard bounces and
// complaints suppress the address company-wide at once, and three soft
// bounces in 30 days do too (PLAN §6.3).
//
// Processing is idempotent: each event is stored once (EventBridge's event
// id), so a message SQS delivers twice has its effects once. A message that
// keeps failing is left on the queue; after 5 receives SQS moves it to the
// dead-letter queue (redrive policy) for a human to look at.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Type of an SES event, as stored.
type Type string

const (
	Send             Type = "send"
	Delivery         Type = "delivery"
	Bounce           Type = "bounce"
	Complaint        Type = "complaint"
	Reject           Type = "reject"
	DeliveryDelay    Type = "delivery_delay"
	RenderingFailure Type = "rendering_failure"
	Open             Type = "open"
	Click            Type = "click"
	Subscription     Type = "subscription"
)

var sesTypes = map[string]Type{
	"Send": Send, "Delivery": Delivery, "Bounce": Bounce, "Complaint": Complaint, "Reject": Reject,
	"DeliveryDelay": DeliveryDelay, "Rendering Failure": RenderingFailure, "RenderingFailure": RenderingFailure,
	"Open": Open, "Click": Click, "Subscription": Subscription,
}

// Event is one SES event, flattened.
type Event struct {
	ID               string // EventBridge event id: the dedupe key
	FeedbackID       string // SES's id of a bounce or complaint (a second dedupe key)
	Type             Type
	Subtype          string // bounce: "permanent/General"; complaint: feedback type; delay: delay type
	MessageID        string // SES message id
	ConfigurationSet string // which brand sent it
	SendID           int64  // from the send_id tag (0 = none, e.g. a test send)
	CampaignID       string // from the campaign_id tag
	Recipients       []string
	OccurredAt       time.Time
	Detail           json.RawMessage // the SES event as received
}

// Permanent: a hard bounce (the address doesn't exist or never will take mail).
func (e Event) Permanent() bool {
	return e.Type == Bounce && strings.HasPrefix(e.Subtype, "permanent/")
}

// SoftCounts: a transient bounce that says something about the recipient
// (full mailbox, temporary failure), as opposed to our message (too big,
// content or attachment rejected), which shouldn't cost the person anything.
func (e Event) SoftCounts() bool {
	if e.Type != Bounce || e.Permanent() {
		return false
	}
	switch e.Subtype {
	case "transient/MessageTooLarge", "transient/ContentRejected", "transient/AttachmentRejected":
		return false
	}
	return true
}

// ErrNotSES: the message isn't an SES event (it's deleted, not retried).
var ErrNotSES = errors.New("events: not an SES event")

type envelope struct {
	ID     string          `json:"id"`
	Source string          `json:"source"`
	Time   time.Time       `json:"time"`
	Detail json.RawMessage `json:"detail"`
}

type address struct {
	EmailAddress string `json:"emailAddress"`
}

type sesEvent struct {
	EventType string `json:"eventType"`
	Mail      struct {
		Timestamp   time.Time           `json:"timestamp"`
		MessageID   string              `json:"messageId"`
		Destination []string            `json:"destination"`
		Tags        map[string][]string `json:"tags"`
	} `json:"mail"`
	Bounce *struct {
		FeedbackID        string    `json:"feedbackId"`
		BounceType        string    `json:"bounceType"`
		BounceSubType     string    `json:"bounceSubType"`
		BouncedRecipients []address `json:"bouncedRecipients"`
		Timestamp         time.Time `json:"timestamp"`
	} `json:"bounce"`
	Complaint *struct {
		FeedbackID            string    `json:"feedbackId"`
		ComplainedRecipients  []address `json:"complainedRecipients"`
		ComplaintFeedbackType string    `json:"complaintFeedbackType"`
		Timestamp             time.Time `json:"timestamp"`
	} `json:"complaint"`
	Delivery *struct {
		Recipients []string  `json:"recipients"`
		Timestamp  time.Time `json:"timestamp"`
	} `json:"delivery"`
	DeliveryDelay *struct {
		DelayType         string    `json:"delayType"`
		DelayedRecipients []address `json:"delayedRecipients"`
		Timestamp         time.Time `json:"timestamp"`
	} `json:"deliveryDelay"`
	Reject *struct {
		Reason string `json:"reason"`
	} `json:"reject"`
	Open *struct {
		Timestamp time.Time `json:"timestamp"`
	} `json:"open"`
	Click *struct {
		Timestamp time.Time `json:"timestamp"`
	} `json:"click"`
}

func addrs(list []address) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		if a.EmailAddress != "" {
			out = append(out, a.EmailAddress)
		}
	}
	return out
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// Parse reads an SQS message body: an EventBridge event wrapping an SES event.
func Parse(body []byte) (Event, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Event{}, fmt.Errorf("events: not JSON: %w", err)
	}
	if env.Source != "aws.ses" {
		return Event{}, ErrNotSES
	}
	var se sesEvent
	if err := json.Unmarshal(env.Detail, &se); err != nil {
		return Event{}, fmt.Errorf("events: bad SES detail: %w", err)
	}
	t, ok := sesTypes[se.EventType]
	if env.ID == "" || !ok || se.Mail.MessageID == "" {
		return Event{}, fmt.Errorf("events: incomplete SES event (id %q, type %q)", env.ID, se.EventType)
	}
	e := Event{ID: env.ID, Type: t, MessageID: se.Mail.MessageID, Detail: env.Detail, Recipients: se.Mail.Destination}
	if v := se.Mail.Tags["ses:configuration-set"]; len(v) > 0 {
		e.ConfigurationSet = v[0]
	}
	if v := se.Mail.Tags["send_id"]; len(v) > 0 {
		e.SendID, _ = strconv.ParseInt(v[0], 10, 64)
	}
	if v := se.Mail.Tags["campaign_id"]; len(v) > 0 {
		e.CampaignID = v[0]
	}
	var at time.Time
	switch {
	case se.Bounce != nil:
		e.Subtype = strings.ToLower(se.Bounce.BounceType) + "/" + se.Bounce.BounceSubType
		e.Recipients, at, e.FeedbackID = addrs(se.Bounce.BouncedRecipients), se.Bounce.Timestamp, se.Bounce.FeedbackID
	case se.Complaint != nil:
		e.Subtype = se.Complaint.ComplaintFeedbackType
		e.Recipients, at, e.FeedbackID = addrs(se.Complaint.ComplainedRecipients), se.Complaint.Timestamp, se.Complaint.FeedbackID
	case se.Delivery != nil:
		e.Recipients, at = se.Delivery.Recipients, se.Delivery.Timestamp
	case se.DeliveryDelay != nil:
		e.Subtype = se.DeliveryDelay.DelayType
		e.Recipients, at = addrs(se.DeliveryDelay.DelayedRecipients), se.DeliveryDelay.Timestamp
	case se.Reject != nil:
		e.Subtype = se.Reject.Reason
	case se.Open != nil:
		at = se.Open.Timestamp
	case se.Click != nil:
		at = se.Click.Timestamp
	}
	if (t == Bounce || t == Complaint) && len(e.Recipients) == 0 {
		return Event{}, fmt.Errorf("events: %s without recipients", t)
	}
	e.OccurredAt = firstTime(at, env.Time, se.Mail.Timestamp)
	if e.OccurredAt.IsZero() {
		return Event{}, errors.New("events: event without a time")
	}
	return e, nil
}
