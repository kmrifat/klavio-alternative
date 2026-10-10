package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/testdb"
)

func newBrand(t *testing.T, st *store.Store, slug string) store.Brand {
	t.Helper()
	b, err := st.CreateBrand(context.Background(), store.Brand{
		Slug: slug, Name: slug, Color: "#000000", PostalAddress: "x",
		SendingDomain: "news." + slug + ".co", MailFromDomain: "bounce.news." + slug + ".co",
		LinkDomain: "go." + slug + ".co", SESConfigurationSet: "brand-" + slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func daysAgo(n int) *time.Time {
	t := time.Now().AddDate(0, 0, -n)
	return &t
}

func TestTiers(t *testing.T) {
	st := store.New(testdb.New(t))
	ctx := context.Background()
	b := newBrand(t, st, "brightside")

	cases := []struct {
		name string
		c    store.NewContact
		want store.Tier
	}{
		{"new signup", store.NewContact{ConsentedAt: daysAgo(3), TrackingAllowed: true}, store.TierActive},
		{"clicked recently", store.NewContact{ConsentedAt: daysAgo(400), LastClickAt: daysAgo(5), TrackingAllowed: true}, store.TierActive},
		{"warm", store.NewContact{ConsentedAt: daysAgo(60), TrackingAllowed: true}, store.TierWarm},
		{"at risk", store.NewContact{ConsentedAt: daysAgo(120), TrackingAllowed: true}, store.TierAtRisk},
		{"sunset", store.NewContact{ConsentedAt: daysAgo(200), TrackingAllowed: true}, store.TierSunset},
		{"no dates", store.NewContact{TrackingAllowed: true}, store.TierSunset},
		{"4-5 star import counts as activity", store.NewContact{ConsentedAt: daysAgo(900), EngagedAt: daysAgo(0), TrackingAllowed: true}, store.TierActive},
		// EU without tracking consent: opt-in date only, never Active.
		{"no tracking, new signup", store.NewContact{ConsentedAt: daysAgo(3)}, store.TierWarm},
		{"no tracking, 60 days", store.NewContact{ConsentedAt: daysAgo(60)}, store.TierWarm},
		{"no tracking, 100 days", store.NewContact{ConsentedAt: daysAgo(100)}, store.TierAtRisk},
		{"no tracking ignores clicks and stars", store.NewContact{ConsentedAt: daysAgo(200), LastClickAt: daysAgo(1), EngagedAt: daysAgo(1)}, store.TierSunset},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.c.BrandID, c.c.Email, c.c.Status, c.c.ConsentBasis = b.ID, strings.ReplaceAll(c.name, " ", "")+"@x.com", store.ContactSubscribed, "single_opt_in"
			id, err := st.InsertContact(ctx, c.c)
			if err != nil {
				t.Fatalf("case %d: %v", i, err)
			}
			got, err := st.ContactByID(ctx, b.ID, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Tier != c.want {
				t.Fatalf("tier = %s, want %s", got.Tier, c.want)
			}
		})
	}

	// Later activity can't make a no-tracking contact Active either.
	id, _ := st.InsertContact(ctx, store.NewContact{BrandID: b.ID, Email: "eu@x.com", Status: store.ContactSubscribed,
		ConsentBasis: "single_opt_in", ConsentedAt: daysAgo(150)})
	if err := st.MergeImportedFields(ctx, id, store.NewContact{LastClickAt: daysAgo(1), EngagedAt: daysAgo(0)}); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.ContactByID(ctx, b.ID, id); c.Tier != store.TierAtRisk || c.LastClickAt != nil {
		t.Fatalf("no-tracking contact after merge: tier %s, last click %v", c.Tier, c.LastClickAt)
	}
}

func TestContactTopicsStayInBrand(t *testing.T) {
	st := store.New(testdb.New(t))
	ctx := context.Background()
	a, b := newBrand(t, st, "alpha"), newBrand(t, st, "beta")
	topicA, _ := st.CreateTopic(ctx, a.ID, "News")
	topicB, _ := st.CreateTopic(ctx, b.ID, "News")
	cid, err := st.InsertContact(ctx, store.NewContact{BrandID: a.ID, Email: "p@x.com", Status: store.ContactSubscribed, ConsentBasis: "single_opt_in", TrackingAllowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddContactTopics(ctx, cid, []uuid.UUID{topicA.ID}); err != nil {
		t.Fatalf("own brand's topic: %v", err)
	}
	if err := st.AddContactTopics(ctx, cid, []uuid.UUID{topicB.ID}); err == nil {
		t.Fatal("linking another brand's topic should fail")
	}
}

func TestConsentRecordsAreAppendOnly(t *testing.T) {
	pool := testdb.New(t)
	st := store.New(pool)
	ctx := context.Background()
	b := newBrand(t, st, "brightside")
	if err := st.AddConsentRecord(ctx, store.NewConsentRecord{BrandID: b.ID, ContactID: uuid.New(), Email: "p@x.com", Kind: "opt_in", Method: "manual"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE consent_records SET source = 'edited'`, `DELETE FROM consent_records`} {
		if _, err := pool.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: got %v", q, err)
		}
	}
}
