package confirm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/freqcap"
	"github.com/kmrifat/klavio-alternative/api/internal/mailer"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
	"github.com/kmrifat/klavio-alternative/api/internal/unsubscribe"
	"github.com/kmrifat/klavio-alternative/api/internal/warmup"
)

type failing struct{ calls int }

func (f *failing) Send(context.Context, mailer.Message) error {
	f.calls++
	return errors.New("SES said no")
}

func repermissionSetup(t *testing.T, sender mailer.Sender) (*Service, *store.Store, store.Brand) {
	t.Helper()
	ctx := context.Background()
	st := store.New(testdb.New(t))
	b, err := st.CreateBrand(ctx, store.Brand{Slug: "acme", Name: "Acme", Color: "#000000", PostalAddress: "1 Main St",
		SendingDomain: "news.acme.test", MailFromDomain: "bounce.news.acme.test", LinkDomain: "go.acme.test", SESConfigurationSet: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ExecForTest(ctx, `UPDATE brands SET status = 'ready', repermission_on = true`); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if _, err := st.InsertContact(ctx, store.NewContact{BrandID: b.ID, Email: fmt.Sprintf("q%d@example.com", i), Status: store.ContactQuarantined, ConsentBasis: "single_opt_in"}); err != nil {
			t.Fatal(err)
		}
	}
	signer, _ := NewSigner("confirm-test-secret-aaaaaaaaaaaaaaaaaaaa")
	unsub, _ := unsubscribe.NewSigner("confirm-test-secret-aaaaaaaaaaaaaaaaaaaa")
	svc := NewService(Deps{Store: st, Signer: signer, Unsub: unsub, Sender: sender, Caps: freqcap.NewService(st),
		Warmup: warmup.NewService(st), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return svc, st, b
}

func counts(t *testing.T, st *store.Store, b store.Brand) (asked, freq, warm int, started bool) {
	t.Helper()
	ctx := context.Background()
	asked, _ = st.RepermissionSentSince(ctx, b.ID, time.Now().Add(-time.Hour))
	ws, _ := st.WarmupStart(ctx, b.ID)
	warm, _ = st.SentOn(ctx, b.ID, time.Now().UTC().Truncate(24*time.Hour))
	return asked, countSends(t, st), warm, ws != nil
}

func countSends(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.QueryIntForTest(context.Background(), `SELECT count(*) FROM marketing_sends`, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNoRepermissionWithoutASender(t *testing.T) {
	svc, st, b := repermissionSetup(t, mailer.Unconfigured{})
	if svc.CanSend() {
		t.Fatal("Unconfigured counts as a sender")
	}
	if err := svc.SendRepermissions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if asked, sends, warm, started := counts(t, st, b); asked != 0 || sends != 0 || warm != 0 || started {
		t.Fatalf("reserved without a sender: asked %d, freqcap %d, warm-up %d, started %v", asked, sends, warm, started)
	}
}

func TestFailedRepermissionGivesEverythingBack(t *testing.T) {
	f := &failing{}
	svc, st, b := repermissionSetup(t, f)
	if err := svc.SendRepermissions(context.Background()); err == nil {
		t.Fatal("failures should be reported")
	}
	if f.calls != 5 {
		t.Fatalf("%d send attempts", f.calls)
	}
	if asked, sends, warm, started := counts(t, st, b); asked != 0 || sends != 0 || warm != 0 || started {
		t.Fatalf("after failed sends: asked %d, freqcap %d, warm-up %d, started %v", asked, sends, warm, started)
	}
	// Next run retries the same people.
	mem := &mailer.Memory{}
	svc.SetSender(mem)
	if err := svc.SendRepermissions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mem.Sent()) != 5 {
		t.Fatalf("retry sent %d", len(mem.Sent()))
	}
	if asked, sends, warm, started := counts(t, st, b); asked != 5 || sends != 5 || warm != 5 || !started {
		t.Fatalf("after sending: asked %d, freqcap %d, warm-up %d, started %v", asked, sends, warm, started)
	}
}

// unknown answers like an SES timeout after the request went out.
type unknown struct{ calls int }

func (u *unknown) Send(context.Context, mailer.Message) error {
	u.calls++
	return fmt.Errorf("%w: read tcp: i/o timeout", mailer.ErrOutcomeUnknown)
}

// An email that may have gone out counts as sent and is never retried: a
// duplicate can't be undone, a lost one can be asked for again.
func TestUnknownOutcomeIsNotRetried(t *testing.T) {
	u := &unknown{}
	svc, st, b := repermissionSetup(t, u)
	ctx := context.Background()
	if err := svc.SendRepermissions(ctx); err != nil {
		t.Fatalf("an unknown outcome is not a failure to retry: %v", err)
	}
	if asked, sends, warm, _ := counts(t, st, b); asked != 5 || sends != 5 || warm != 5 {
		t.Fatalf("kept as sent: asked %d, freqcap %d, warm-up %d", asked, sends, warm)
	}
	if err := svc.SendRepermissions(ctx); err != nil {
		t.Fatal(err)
	}
	if u.calls != 5 {
		t.Fatalf("%d send attempts, want 5 (no retry)", u.calls)
	}

	id, err := st.InsertContact(ctx, store.NewContact{BrandID: b.ID, Email: "new@example.com", Status: store.ContactPending, ConsentBasis: "double_opt_in"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SendConfirmation(ctx, b.ID, id, uuid.Nil); err != nil {
		t.Fatalf("confirmation with an unknown outcome should be done, not retried: %v", err)
	}
	if u.calls != 6 {
		t.Fatalf("%d send attempts, want 6", u.calls)
	}
}
