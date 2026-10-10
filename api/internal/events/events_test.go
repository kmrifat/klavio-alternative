package events

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

// sesBody builds an EventBridge message like SES sends.
func sesBody(id, eventType, extra string, recipients ...string) []byte {
	return []byte(fmt.Sprintf(`{"version":"0","id":%q,"detail-type":"x","source":"aws.ses","time":"2026-10-10T09:00:00Z","detail":{
		"eventType":%q,
		"mail":{"timestamp":"2026-10-10T08:59:00Z","messageId":"msg-%s","destination":[%s],
			"tags":{"ses:configuration-set":["brand-acme"],"send_id":["42"],"campaign_id":["c1"]}}%s}}`,
		id, eventType, id, quoteAll(recipients), extra))
}

func quoteAll(s []string) string {
	q := make([]string, len(s))
	for i, x := range s {
		q[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(q, ",")
}

func bounce(id, typ, sub, to string) []byte {
	return sesBody(id, "Bounce", fmt.Sprintf(`,"bounce":{"bounceType":%q,"bounceSubType":%q,"bouncedRecipients":[{"emailAddress":%q}],"timestamp":"2026-10-10T09:00:01Z"}`, typ, sub, to), to)
}

func complaint(id, to string) []byte {
	return sesBody(id, "Complaint", fmt.Sprintf(`,"complaint":{"complainedRecipients":[{"emailAddress":%q}],"complaintFeedbackType":"abuse","timestamp":"2026-10-10T09:05:00Z"}`, to), to)
}

func TestParse(t *testing.T) {
	e, err := Parse(bounce("e1", "Permanent", "General", "Ann@Example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != Bounce || e.Subtype != "permanent/General" || !e.Permanent() || e.SendID != 42 || e.ConfigurationSet != "brand-acme" ||
		e.MessageID != "msg-e1" || len(e.Recipients) != 1 || !e.OccurredAt.Equal(time.Date(2026, 10, 10, 9, 0, 1, 0, time.UTC)) {
		t.Fatalf("bounce: %+v", e)
	}
	if e, _ := Parse(bounce("e2", "Transient", "ContentRejected", "a@x.com")); e.SoftCounts() {
		t.Fatal("content rejected counted against the person")
	}
	if e, _ := Parse(bounce("e3", "Transient", "MailboxFull", "a@x.com")); !e.SoftCounts() {
		t.Fatal("mailbox full not counted")
	}
	if e, err := Parse(sesBody("e4", "Delivery", `,"delivery":{"recipients":["a@x.com"],"timestamp":"2026-10-10T09:00:02Z"}`, "a@x.com")); err != nil || e.Type != Delivery {
		t.Fatalf("delivery: %+v %v", e, err)
	}
	if _, err := Parse([]byte(`{"id":"x","source":"aws.ec2","detail":{}}`)); err != ErrNotSES {
		t.Fatalf("non-SES: %v", err)
	}
	for _, bad := range []string{`not json`, `{"id":"x","source":"aws.ses","detail":{"eventType":"Nope","mail":{"messageId":"m"}}}`,
		`{"id":"x","source":"aws.ses","detail":{"eventType":"Bounce","mail":{"messageId":"m"},"bounce":{"bounceType":"Permanent"}}}`} {
		if _, err := Parse([]byte(bad)); err == nil || err == ErrNotSES {
			t.Errorf("accepted %s", bad)
		}
	}
}

type fixture struct {
	st    *store.Store
	brand store.Brand
	ann   store.Contact
	p     *Processor
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	st := store.New(testdb.New(t))
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "acme", Name: "Acme", Color: "#000000", PostalAddress: "1 Main St",
		SendingDomain: "news.acme.test", MailFromDomain: "bounce.news.acme.test", LinkDomain: "go.acme.test", SESConfigurationSet: "brand-acme"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.InsertContact(ctx, store.NewContact{BrandID: b.ID, Email: "ann@example.com", Status: store.ContactSubscribed, ConsentBasis: "double_opt_in"})
	if err != nil {
		t.Fatal(err)
	}
	ann, _ := st.ContactByID(ctx, b.ID, id)
	return fixture{st: st, brand: b, ann: ann, p: &Processor{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
}

func (f fixture) process(t *testing.T, body []byte) Outcome {
	t.Helper()
	e, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.p.Process(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f fixture) suppressed(t *testing.T, email string) *string {
	t.Helper()
	c, err := f.st.ContactByEmail(context.Background(), f.brand.ID, email)
	if err != nil {
		t.Fatal(err)
	}
	return c.SuppressionReason
}

func TestHardBounceAndComplaintSuppress(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	out := f.process(t, bounce("b1", "Permanent", "NoEmail", "ANN@example.com"))
	if len(out.Suppressed) != 1 || f.suppressed(t, "ann@example.com") == nil || *f.suppressed(t, "ann@example.com") != "hard_bounce" {
		t.Fatalf("hard bounce: %+v", out)
	}
	hist, _ := f.st.ConsentHistory(ctx, f.ann.ID)
	if len(hist) != 1 || hist[0].Kind != "bounced" || hist[0].Method != "ses" || !strings.Contains(hist[0].Source, "msg-b1") {
		t.Fatalf("consent record: %+v", hist)
	}
	// SQS delivers the same message again: nothing happens twice.
	if out := f.process(t, bounce("b1", "Permanent", "NoEmail", "ann@example.com")); !out.Duplicate || len(out.Suppressed) != 0 {
		t.Fatalf("redelivery: %+v", out)
	}
	if hist, _ := f.st.ConsentHistory(ctx, f.ann.ID); len(hist) != 1 {
		t.Fatalf("redelivery wrote %d consent records", len(hist))
	}

	// A complaint about someone unknown to the brand still suppresses company-wide.
	f.process(t, complaint("c1", "stranger@example.org"))
	supp, _ := f.st.SuppressedHashes(ctx, [][]byte{store.EmailHash("stranger@example.org")})
	if len(supp) != 1 {
		t.Fatal("complaint didn't suppress")
	}
}

func TestSoftBouncesSuppressOnTheThird(t *testing.T) {
	f := setup(t)
	f.process(t, bounce("s1", "Transient", "MailboxFull", "ann@example.com"))
	f.process(t, bounce("s2", "Transient", "General", "ann@example.com"))
	// Our fault, not theirs: doesn't count.
	f.process(t, bounce("s3", "Transient", "ContentRejected", "ann@example.com"))
	if f.suppressed(t, "ann@example.com") != nil {
		t.Fatal("suppressed after 2 soft bounces")
	}
	f.process(t, bounce("s2", "Transient", "General", "ann@example.com")) // duplicate
	if f.suppressed(t, "ann@example.com") != nil {
		t.Fatal("a redelivered soft bounce counted twice")
	}
	out := f.process(t, bounce("s4", "Undetermined", "Undetermined", "ann@example.com"))
	if len(out.Suppressed) != 1 || *f.suppressed(t, "ann@example.com") != "soft_bounce" {
		t.Fatalf("third soft bounce: %+v", out)
	}
}

func TestReaderPoisonGoesToDLQ(t *testing.T) {
	f := setup(t)
	q := &Fake{}
	q.Push(bounce("r1", "Permanent", "General", "ann@example.com"))
	q.Push([]byte(`{"broken`))
	q.Push([]byte(`{"id":"x","source":"aws.ec2","detail":{}}`))
	r := &Reader{Queue: q, Processor: f.p, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for range 8 {
		if _, err := r.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if q.Pending() != 0 || len(q.DeadLetters()) != 1 || string(q.DeadLetters()[0].Body) != `{"broken` {
		t.Fatalf("pending %d, dead letters %v", q.Pending(), q.DeadLetters())
	}
	if f.suppressed(t, "ann@example.com") == nil {
		t.Fatal("good message wasn't processed")
	}
}
