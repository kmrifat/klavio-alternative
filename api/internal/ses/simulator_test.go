package ses

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestMailboxSimulator sends real email through SES to the mailbox
// simulator, which accepts it without delivering anywhere and answers with
// a delivery, bounce or complaint event. It needs AWS credentials and a
// verified sending domain, so it only runs when asked:
//
//	SES_SIMULATOR_TEST=1 SES_TEST_FROM=hello@news.brand.example \
//	SES_TEST_CONFIGURATION_SET=brand-x SES_REGION=us-east-1 go test ./internal/ses -run Simulator
//
// The events (bounce → suppression, complaint → suppression) are M4's to
// handle; here we check SES takes each message and returns an id.
func TestMailboxSimulator(t *testing.T) {
	if os.Getenv("SES_SIMULATOR_TEST") == "" {
		t.Skip("set SES_SIMULATOR_TEST=1 (with AWS credentials) to send to the SES mailbox simulator")
	}
	from, set := os.Getenv("SES_TEST_FROM"), os.Getenv("SES_TEST_CONFIGURATION_SET")
	if from == "" || set == "" {
		t.Fatal("SES_TEST_FROM and SES_TEST_CONFIGURATION_SET are required")
	}
	region := os.Getenv("SES_REGION")
	if region == "" {
		region = "us-east-1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a, err := NewAWS(ctx, region, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{
		"success@simulator.amazonses.com",
		"bounce@simulator.amazonses.com",
		"complaint@simulator.amazonses.com",
		"suppressionlist@simulator.amazonses.com",
	} {
		id, err := a.Send(ctx, Message{
			From: from, To: to, Subject: "Simulator test", ConfigurationSet: set,
			HTML: "<p>Simulator test</p>", Text: "Simulator test",
			Headers: map[string]string{
				"List-Unsubscribe":      "<https://example.com/u/test>",
				"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
			},
			Tags: map[string]string{"test": "simulator"},
		})
		if err != nil {
			t.Errorf("%s: %v", to, err)
			continue
		}
		t.Logf("%s: accepted as %s", to, id)
	}
}

func TestFakeSender(t *testing.T) {
	f := &FakeSender{}
	ctx := context.Background()
	if _, err := f.Send(ctx, Message{To: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Send(ctx, Message{To: "reject@example.com"}); err == nil {
		t.Error("reject@ should be refused")
	}
	if _, err := f.Send(ctx, Message{To: "throttle@example.com"}); err == nil {
		t.Error("throttle@ should be throttled the first time")
	}
	if _, err := f.Send(ctx, Message{To: "throttle@example.com"}); err != nil {
		t.Errorf("throttle@ should go through the second time: %v", err)
	}
	if n := len(f.Messages()); n != 2 {
		t.Errorf("recorded %d messages, want 2", n)
	}
}
