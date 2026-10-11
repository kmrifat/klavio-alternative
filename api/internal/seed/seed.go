// Package seed fills a development database with demo data: three brands,
// about 20,000 contacts and half a year of campaigns with their feedback,
// so every screen has something realistic to show.
//
// Demo addresses and domains end in .test, a name reserved so it can never
// receive email, and demo brands can't pass the DNS check, so nothing here
// can ever be sent for real. Each brand's contacts are random but the same
// on every run; who opened, clicked or bounced can differ between runs.
package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/ses"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/templates"
)

// Brand is one demo brand.
type Brand struct {
	Slug, Name, Color, Domain, Postal string
	Contacts                          int
	Topics                            []string // after "Newsletter"
	Tags                              []string
	// BounceyLastCampaign: the latest campaign bounces a little over 1%,
	// so the brand shows the brakes' "watch" state.
	BounceyLastCampaign bool
}

var Brands = []Brand{
	{Slug: "northwind", Name: "Northwind Outdoors", Color: "#15803d", Domain: "northwind.test",
		Postal: "Northwind Outdoors, 400 Pine St, Portland, OR 97204", Contacts: 9000,
		Topics: []string{"New arrivals", "Trail guides"}, Tags: []string{"hiker", "camper", "vip", "loyalty", "wholesale"}},
	{Slug: "lumen", Name: "Lumen Skincare", Color: "#db2777", Domain: "lumen.test",
		Postal: "Lumen Skincare, 18 Rue Oberkampf, 75011 Paris", Contacts: 7000,
		Topics: []string{"Offers"}, Tags: []string{"sensitive-skin", "subscriber-box", "vip", "sample-request"}},
	{Slug: "harbor", Name: "Harbor Books", Color: "#1d4ed8", Domain: "harbor.test",
		Postal: "Harbor Books, 2 Quay Street, Bristol BS1 4DB", Contacts: 4000,
		Topics: []string{"Events"}, Tags: []string{"book-club", "fiction", "kids", "vip"}, BounceyLastCampaign: true},
}

// campaigns every brand gets, oldest first: name, subject, days ago.
var campaigns = []struct {
	name, subject string
	daysAgo       int
}{
	{"Spring newsletter", "What's new this spring", 160},
	{"Customer stories", "How {{first_name | you}} and others use our products", 130},
	{"Mid-season sale", "20% off, this weekend only", 100},
	{"Summer guide", "Our summer picks", 70},
	{"Back in stock", "The favourites are back", 35},
	{"Autumn newsletter", "Autumn is here", 5},
}

// IsDemo: a brand this package made (and may delete).
func IsDemo(slug, sendingDomain string) bool {
	for _, b := range Brands {
		if b.Slug == slug && strings.EqualFold(sendingDomain, "news."+b.Domain) {
			return true
		}
	}
	return false
}

// ErrAlreadySeeded: the demo brands exist; Run with reset to start over.
var ErrAlreadySeeded = errors.New("the demo brands are already there; run it with reset to replace them")

type Options struct {
	// Reset deletes the demo brands and everything under them first.
	Reset bool
	// Scale multiplies the number of contacts (0 = 1, the full ~20,000).
	Scale float64
}

// Run adds the demo data in one transaction.
func Run(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, opts Options) error {
	if opts.Scale <= 0 {
		opts.Scale = 1
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var owner uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE company_role = 'owner' ORDER BY created_at LIMIT 1`).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("no owner yet: run bootstrap and accept the invite first")
		}
		if err != nil {
			return err
		}
		slugs := make([]string, len(Brands))
		for i, b := range Brands {
			slugs[i] = b.Slug
		}
		rows, err := tx.Query(ctx, `SELECT id, slug, sending_domain::text FROM brands WHERE slug = ANY($1)`, slugs)
		if err != nil {
			return err
		}
		type existing struct {
			id             uuid.UUID
			slug, sendFrom string
		}
		var found []existing
		for rows.Next() {
			var e existing
			if err := rows.Scan(&e.id, &e.slug, &e.sendFrom); err != nil {
				return err
			}
			found = append(found, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range found {
			if !IsDemo(e.slug, e.sendFrom) {
				return fmt.Errorf("a real brand already uses the slug %q; the demo data needs it", e.slug)
			}
		}
		if len(found) > 0 && !opts.Reset {
			return ErrAlreadySeeded
		}
		for _, e := range found {
			if err := deleteBrand(ctx, tx, e.id); err != nil {
				return err
			}
			log.InfoContext(ctx, "demo brand removed", "brand", e.slug)
		}

		now := time.Now().UTC()
		for i, b := range Brands {
			started := time.Now()
			b.Contacts = max(50, int(float64(b.Contacts)*opts.Scale))
			// Each brand's own seed, so its contacts don't depend on how
			// the brands before it turned out.
			if _, err := tx.Exec(ctx, `SELECT setseed($1)`, 0.2026+float64(i)/10); err != nil {
				return err
			}
			if err := seedBrand(ctx, tx, b, owner, now); err != nil {
				return fmt.Errorf("%s: %w", b.Slug, err)
			}
			log.InfoContext(ctx, "demo brand added", "brand", b.Slug, "contacts", b.Contacts, "took", time.Since(started).Round(time.Millisecond))
		}
		return nil
	})
}

// deleteBrand removes a demo brand. Nearly everything cascades from the
// brand; suppressions and segments' counts don't, and consent records are
// append-only, so they stay behind (filed under hashes of .test addresses).
func deleteBrand(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM suppressions WHERE brand_id = $1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM brands WHERE id = $1`, id)
	return err
}

func seedBrand(ctx context.Context, tx pgx.Tx, b Brand, owner uuid.UUID, now time.Time) error {
	exec := func(sql string, args ...any) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}
	send := "news." + b.Domain
	var brandID uuid.UUID
	err := tx.QueryRow(ctx, `INSERT INTO brands (slug, name, color, postal_address, sending_domain, mail_from_domain, link_domain,
			ses_configuration_set, ses_identity_status, status, dns_checked_at, warmup_started_on, from_local, reply_to, created_at)
		VALUES ($1::text, $2, $3, $4, $5::text, 'bounce.' || $5::text, 'go.' || $6::text, 'brand-' || $1::text, 'SUCCESS', 'ready', $7, $8, 'hello',
			'support@' || $6::text, $9)
		RETURNING id`,
		b.Slug, b.Name, b.Color, b.Postal, send, b.Domain, now, now.AddDate(0, 0, -200), now.AddDate(-2, 0, 0)).Scan(&brandID)
	if err != nil {
		return err
	}

	// DNS as the brand form would set it up, all passing. In development
	// the daily DNS check looks these up for real and marks them failing.
	setup, err := (&ses.Fake{}).SetUpBrand(ctx, send, "bounce."+send, "brand-"+b.Slug)
	if err != nil {
		return err
	}
	for _, r := range brands.RequiredRecords(store.Brand{SendingDomain: send, MailFromDomain: "bounce." + send}, setup.DKIMTokens, "us-east-1") {
		if err := exec(`INSERT INTO brand_dns_records (brand_id, purpose, type, name, value, status, checked_at) VALUES ($1, $2, $3, $4, $5, 'ok', $6)`,
			brandID, r.Purpose, r.Type, r.Name, r.Value, now); err != nil {
			return err
		}
	}
	// The shop's own domain, so templates may link to it.
	if err := exec(`INSERT INTO link_allowlist (brand_id, domain, added_by) VALUES ($1, $2, $3)`, brandID, b.Domain, owner); err != nil {
		return err
	}

	topics := append([]string{"Newsletter"}, b.Topics...)
	topicIDs := make([]uuid.UUID, len(topics))
	for i, name := range topics {
		if err := tx.QueryRow(ctx, `INSERT INTO topics (brand_id, name) VALUES ($1, $2) RETURNING id`, brandID, name).Scan(&topicIDs[i]); err != nil {
			return err
		}
	}

	if err := seedContacts(ctx, tx, b, brandID, topicIDs, owner, now); err != nil {
		return err
	}
	tmpl, err := seedTemplates(ctx, tx, b, brandID, owner)
	if err != nil {
		return err
	}
	if err := seedCampaigns(ctx, tx, b, brandID, topicIDs[0], tmpl, owner, now); err != nil {
		return err
	}
	return seedSegments(ctx, tx, brandID, topics, topicIDs, owner)
}

var firstNames = []string{"Ava", "Ben", "Chloe", "Daniel", "Ella", "Finn", "Grace", "Hugo", "Isla", "Jack", "Kai", "Lena", "Mia", "Noah",
	"Olivia", "Pablo", "Quinn", "Ruby", "Sam", "Tara", "Uma", "Victor", "Willow", "Xavier", "Yara", "Zoe", "Amir", "Bea", "Carlos",
	"Dina", "Emil", "Fatima", "Gabriel", "Hana", "Ivan", "Jonas", "Keiko", "Liam", "Maya", "Nora", "Omar", "Priya", "Rosa", "Sven",
	"Theo", "Valentina", "Wei", "Yusuf", "Anouk", "Bruno", "Clara", "Diego", "Elif", "Felix", "Greta", "Henrik", "Ines", "Jules"}

var lastNames = []string{"Smith", "Jones", "Garcia", "Müller", "Martin", "Brown", "Rossi", "Dubois", "Kim", "Nguyen", "Patel", "Silva",
	"Novak", "Larsen", "Kowalski", "Haddad", "Okafor", "Tanaka", "Schmidt", "Moreau", "Wilson", "Taylor", "Anderson", "Thomas",
	"Jackson", "White", "Harris", "Clark", "Lewis", "Walker", "Hall", "Young", "King", "Wright", "Lopez", "Hill", "Green", "Adams",
	"Baker", "Nelson", "Carter", "Mitchell", "Perez", "Roberts", "Turner", "Phillips", "Campbell", "Parker", "Evans", "Edwards"}

// seedContacts adds the brand's contacts, their topics and their consent.
func seedContacts(ctx context.Context, tx pgx.Tx, b Brand, brandID uuid.UUID, topics []uuid.UUID, owner uuid.UUID, now time.Time) error {
	// One row per contact with the random choices made once; the inserts
	// below read from it.
	if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS demo_people, demo_unsub`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `CREATE TEMP TABLE demo_people ON COMMIT DROP AS
		SELECT g AS n, random() AS r_status, random() AS r_basis, random() AS r_source, random() AS r_country, random() AS r_tz,
		       random() AS r_track, random() AS r_tags, random() AS r_tag1, random() AS r_tag2,
		       random() AS r_import, $2::timestamptz - (random() * interval '730 days') AS created_at,
		       ($3::text[])[1 + floor(random() * array_length($3::text[], 1))::int] AS first_name,
		       ($4::text[])[1 + floor(random() * array_length($4::text[], 1))::int] AS last_name
		FROM generate_series(1, $1) g`, b.Contacts, now, firstNames, lastNames)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO contacts (brand_id, email, email_hash, first_name, last_name, status, status_before_unsubscribe,
			consent_basis, consent_source, consented_at, tracking_allowed, tags, timezone, country, created_at, updated_at, imported_click_at)
		SELECT $1, e.email, sha256(convert_to(e.email, 'UTF8')), p.first_name, p.last_name,
			CASE WHEN p.r_status < 0.88 THEN 'subscribed' WHEN p.r_status < 0.94 THEN 'unsubscribed'
			     WHEN p.r_status < 0.97 THEN 'pending_confirmation' ELSE 'quarantined' END,
			CASE WHEN p.r_status >= 0.88 AND p.r_status < 0.94 THEN 'subscribed' END,
			CASE WHEN p.r_basis < 0.6 THEN 'double_opt_in' WHEN p.r_basis < 0.9 THEN 'single_opt_in' ELSE 'soft_opt_in' END,
			CASE WHEN p.r_import < 0.3 THEN 'Imported from Mailchimp' WHEN p.r_source < 0.5 THEN 'Website footer form'
			     WHEN p.r_source < 0.8 THEN 'Checkout' ELSE 'Pop-up (10% off)' END,
			p.created_at,
			NOT (cc.country IN ('DE', 'FR', 'NL') AND p.r_track < 0.4),
			CASE WHEN p.r_tags < 0.45 THEN '{}'::text[]
			     WHEN p.r_tags < 0.85 THEN ARRAY[($2::text[])[1 + floor(p.r_tag1 * array_length($2::text[], 1))::int]]
			     ELSE ARRAY(SELECT DISTINCT unnest(ARRAY[($2::text[])[1 + floor(p.r_tag1 * array_length($2::text[], 1))::int],
			                                             ($2::text[])[1 + floor(p.r_tag2 * array_length($2::text[], 1))::int]]))
			END,
			cc.tz, cc.country, p.created_at, p.created_at,
			CASE WHEN p.r_import < 0.3 AND p.r_track < 0.6 THEN p.created_at - (p.r_tz * interval '400 days') END
		FROM demo_people p
		CROSS JOIN LATERAL (SELECT lower(translate(p.first_name || '.' || p.last_name, 'üéè ', 'uee')) || p.n || '@' ||
			(ARRAY['mail.test', 'inbox.test', 'post.test', 'webmail.test'])[1 + p.n % 4] AS email) e
		CROSS JOIN LATERAL (SELECT
			CASE WHEN p.r_country < 0.40 THEN 'US' WHEN p.r_country < 0.55 THEN 'GB' WHEN p.r_country < 0.67 THEN 'DE'
			     WHEN p.r_country < 0.76 THEN 'CA' WHEN p.r_country < 0.84 THEN 'FR' WHEN p.r_country < 0.90 THEN 'AU'
			     WHEN p.r_country < 0.95 THEN 'NL' ELSE '' END AS country) c0
		CROSS JOIN LATERAL (SELECT c0.country,
			CASE c0.country
			  WHEN 'US' THEN (ARRAY['America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles'])[1 + floor(p.r_tz * 4)::int]
			  WHEN 'GB' THEN 'Europe/London' WHEN 'DE' THEN 'Europe/Berlin' WHEN 'CA' THEN 'America/Toronto'
			  WHEN 'FR' THEN 'Europe/Paris' WHEN 'AU' THEN 'Australia/Sydney' WHEN 'NL' THEN 'Europe/Amsterdam' ELSE '' END AS tz) cc`,
		brandID, b.Tags)
	if err != nil {
		return fmt.Errorf("contacts: %w", err)
	}

	// Everyone is on the newsletter; some on the other topics too.
	if _, err := tx.Exec(ctx, `INSERT INTO contact_topics (brand_id, contact_id, topic_id)
		SELECT $1, id, $2 FROM contacts WHERE brand_id = $1`, brandID, topics[0]); err != nil {
		return err
	}
	for i, t := range topics[1:] {
		share := []float64{0.4, 0.2}[min(i, 1)]
		if _, err := tx.Exec(ctx, `INSERT INTO contact_topics (brand_id, contact_id, topic_id)
			SELECT $1, id, $2 FROM contacts WHERE brand_id = $1 AND random() < $3`, brandID, t, share); err != nil {
			return err
		}
	}

	// Proof of consent for everyone, and the unsubscribes. When each
	// unsubscribe happened is picked here; campaigns stop going to them
	// after it.
	if _, err := tx.Exec(ctx, `INSERT INTO consent_records (brand_id, contact_id, email_hash, kind, method, source, wording, occurred_at, recorded_by)
		SELECT brand_id, id, email_hash, CASE WHEN consent_source LIKE 'Imported%' THEN 'import_attested' ELSE 'opt_in' END,
			CASE WHEN consent_source LIKE 'Imported%' THEN 'import' ELSE 'api' END, consent_source,
			'Yes, send me news and offers by email. I can unsubscribe at any time.', consented_at,
			CASE WHEN consent_source LIKE 'Imported%' THEN $2::uuid END
		FROM contacts WHERE brand_id = $1`, brandID, owner); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE demo_unsub ON COMMIT DROP AS
		SELECT id, created_at + random() * ($2::timestamptz - created_at) AS at FROM contacts WHERE brand_id = $1 AND status = 'unsubscribed'`,
		brandID, now)
	return err
}

// templateContent: the body of every demo email (the brand layout adds the
// header, footer, address and unsubscribe link).
const templateContent = `<mj-section><mj-column>
<mj-text font-size="22px" font-weight="700">%s</mj-text>
<mj-text>Hi {{first_name | there}},</mj-text>
<mj-text>%s</mj-text>
<mj-button href="https://%s/%s">%s</mj-button>
<mj-text>Questions? Just reply to this email.</mj-text>
</mj-column></mj-section>`

type template struct {
	id         uuid.UUID
	html, text string
}

func seedTemplates(ctx context.Context, tx pgx.Tx, b Brand, brandID uuid.UUID, owner uuid.UUID) (template, error) {
	cc := templates.CheckContext{BrandDomains: []string{"news." + b.Domain, "go." + b.Domain}, Allowlist: []string{b.Domain}, PostalAddress: b.Postal}
	var first template
	for i, t := range []struct{ name, heading, body, path, button string }{
		{"Monthly newsletter", "This month at " + b.Name, "A few things we think you'll like, picked by our team.", "new", "See what's new"},
		{"Sale announcement", "Our sale starts now", "Save on favourites for the next three days. While stocks last.", "sale", "Shop the sale"},
	} {
		content := fmt.Sprintf(templateContent, t.heading, t.body, b.Domain, t.path, t.button)
		c, issues, err := templates.Build(ctx, templates.DefaultLayout(b.Color), content, cc)
		if err != nil {
			return first, err
		}
		for _, is := range issues {
			if is.Severity == templates.SeverityError {
				return first, fmt.Errorf("template %q: %s", t.name, is.Message)
			}
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO templates (brand_id, name, mjml, html, text_body, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id`, brandID, t.name, content, c.HTML, c.Text, owner).Scan(&id); err != nil {
			return first, err
		}
		if i == 0 {
			first = template{id: id, html: c.HTML, text: c.Text}
		}
	}
	return first, nil
}

// seedCampaigns sends each demo campaign to the newsletter: the sends, what
// SES reported back, opens, clicks, unsubscribes, and what those do to
// contacts (suppressions, engagement). Then one draft for later.
func seedCampaigns(ctx context.Context, tx pgx.Tx, b Brand, brandID, newsletter uuid.UUID, t template, owner uuid.UUID, now time.Time) error {
	from := fmt.Sprintf("%s <hello@news.%s>", b.Name, b.Domain)
	for i, c := range campaigns {
		at := now.AddDate(0, 0, -c.daysAgo).Truncate(time.Hour).Add(10 * time.Hour)
		bounce := 0.005
		if b.BounceyLastCampaign && i == len(campaigns)-1 {
			bounce = 0.013
		}
		var id uuid.UUID
		err := tx.QueryRow(ctx, `INSERT INTO campaigns (brand_id, name, topic_id, template_id, subject, preheader, state, html, text_body,
				from_header, reply_to, test_slice_pct, hold_minutes, scheduled_at, start_at, confirmed_by, confirmed_at, created_by, updated_by,
				created_at, updated_at, finished_at)
			VALUES ($1, $2, $3, $4, $5, '', 'done', $6, $7, $8, $9, 10, 60, $10::timestamptz, $10::timestamptz, $11,
				$10::timestamptz - interval '1 day', $11, $11, $10::timestamptz - interval '3 days',
				$10::timestamptz + interval '6 hours', $10::timestamptz + interval '6 hours')
			RETURNING id`, brandID, c.name, newsletter, t.id, c.subject, t.html, t.text, from, "support@"+b.Domain, at, owner).Scan(&id)
		if err != nil {
			return err
		}
		steps := []struct {
			name string
			sql  string
			args []any
		}{
			// Who it went to: newsletter members who'd signed up and confirmed
			// by then and hadn't unsubscribed yet. The first 10% got the test
			// slice an hour earlier.
			{"audience", `INSERT INTO campaign_audience (campaign_id, contact_id, excluded_reason, stage)
				SELECT $1, c.id,
					CASE WHEN c.status = 'pending_confirmation' THEN 'pending_confirmation' WHEN c.status = 'quarantined' THEN 'quarantined' END,
					CASE WHEN c.status IN ('pending_confirmation', 'quarantined') THEN NULL WHEN random() < 0.1 THEN 'test' ELSE 'rest' END
				FROM contacts c
				WHERE c.brand_id = $2 AND c.created_at < $3
				  AND NOT EXISTS (SELECT 1 FROM demo_unsub u WHERE u.id = c.id AND u.at < $3::timestamptz + interval '3 hours')
				  AND NOT EXISTS (SELECT 1 FROM suppressions s WHERE s.email_hash = c.email_hash AND s.created_at < $3)`, []any{id, brandID, at}},
			{"excluded", `INSERT INTO campaign_audience (campaign_id, contact_id, excluded_reason)
				SELECT $1, c.id, 'suppressed' FROM contacts c
				WHERE c.brand_id = $2 AND c.created_at < $3
				  AND EXISTS (SELECT 1 FROM suppressions s WHERE s.email_hash = c.email_hash AND s.created_at < $3)`,
				[]any{id, brandID, at}},
			{"sends", `INSERT INTO sends (campaign_id, brand_id, contact_id, stage, send_after, status, ses_message_id, sent_at, called_at, claimed_at)
				SELECT $1, $2, a.contact_id, a.stage, t, 'sent', gen_random_uuid()::text, t, t, t
				FROM campaign_audience a
				CROSS JOIN LATERAL (SELECT $3::timestamptz + CASE WHEN a.stage = 'test' THEN interval '0' ELSE interval '1 hour' END
					+ random() * interval '50 minutes' AS t) x
				WHERE a.campaign_id = $1 AND a.stage IS NOT NULL`, []any{id, brandID, at}},
			{"counts", `UPDATE campaigns SET
				audience_count = (SELECT count(*) FROM campaign_audience WHERE campaign_id = $1 AND stage IS NOT NULL),
				excluded_count = (SELECT count(*) FROM campaign_audience WHERE campaign_id = $1 AND stage IS NULL) WHERE id = $1`, []any{id}},
			// What SES said: a delivery, a permanent bounce or a soft bounce
			// for each, a few delays, and the odd spam complaint.
			{"events", `WITH s AS (SELECT s.id, s.ses_message_id, s.sent_at, c.email_hash, random() AS r
					FROM sends s JOIN contacts c ON c.id = s.contact_id WHERE s.campaign_id = $1)
				INSERT INTO email_events (event_id, type, subtype, brand_id, ses_message_id, send_id, email_hash, feedback_id, occurred_at)
				SELECT gen_random_uuid()::text, e.type, e.subtype, $2, s.ses_message_id, s.id, s.email_hash, e.feedback, s.sent_at + e.after
				FROM s CROSS JOIN LATERAL (VALUES
					(CASE WHEN s.r < $3 THEN 'bounce' WHEN s.r < $3 + 0.004 THEN 'bounce' ELSE 'delivery' END,
					 CASE WHEN s.r < $3 THEN 'permanent/General' WHEN s.r < $3 + 0.004 THEN 'transient/MailboxFull' ELSE '' END,
					 CASE WHEN s.r < $3 + 0.004 THEN gen_random_uuid()::text ELSE '' END, interval '20 seconds'),
					(CASE WHEN s.r > 0.99 THEN 'delivery_delay' END, '', '', interval '2 minutes'),
					(CASE WHEN s.r > 0.5 AND s.r < 0.5002 THEN 'complaint' END, 'abuse', gen_random_uuid()::text, interval '5 hours')
				) e(type, subtype, feedback, after)
				WHERE e.type IS NOT NULL`, []any{id, brandID, bounce}},
			// Opens (a third are Apple Mail Privacy Protection or scanners)
			// and clicks (a few are link scanners, marked as bots).
			{"opens", `INSERT INTO opens (send_id, brand_id, contact_id, at, ip, user_agent, is_machine, reason)
				SELECT s.id, $2, s.contact_id, s.sent_at + random() * interval '3 days', '198.51.100.' || (1 + floor(random() * 250)::int),
					CASE WHEN m THEN 'Mozilla/5.0 (Apple Mail Privacy Protection)' ELSE 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0)' END,
					m, CASE WHEN m THEN 'apple_mpp' ELSE '' END
				FROM (SELECT s.*, random() < 0.35 AS m FROM sends s WHERE s.campaign_id = $1 AND random() < 0.45
					AND EXISTS (SELECT 1 FROM email_events e WHERE e.send_id = s.id AND e.type = 'delivery')) s`, []any{id, brandID}},
			{"clicks", `INSERT INTO clicks (send_id, brand_id, contact_id, link_no, url, at, ip, user_agent, is_bot, bot_reason, settled_at, recorded_at)
				SELECT s.id, $2, s.contact_id, s.link, CASE s.link WHEN 1 THEN $3 ELSE $4 END, s.t, '198.51.100.' || (1 + floor(random() * 250)::int),
					CASE WHEN s.bot THEN 'Microsoft Defender SafeLinks' ELSE 'Mozilla/5.0 (Macintosh)' END,
					s.bot, CASE WHEN s.bot THEN 'scanner' ELSE '' END, s.t + interval '1 minute', s.t
				FROM (SELECT s.id, s.contact_id, r < 0.008 AS bot, CASE WHEN random() < 0.8 THEN 1 ELSE 2 END AS link,
						s.sent_at + CASE WHEN r < 0.008 THEN interval '5 seconds' ELSE random() * interval '2 days' END AS t
					FROM (SELECT s.*, random() AS r FROM sends s WHERE s.campaign_id = $1) s
					WHERE s.r < 0.05 AND EXISTS (SELECT 1 FROM email_events e WHERE e.send_id = s.id AND e.type = 'delivery')) s`,
				[]any{id, brandID, "https://" + b.Domain + "/new", "https://" + b.Domain + "/"}},
		}
		for _, st := range steps {
			if _, err := tx.Exec(ctx, st.sql, st.args...); err != nil {
				return fmt.Errorf("campaign %q, %s: %w", c.name, st.name, err)
			}
		}
		// Bounced and complaining addresses are blocked from the next one on.
		if _, err := tx.Exec(ctx, `INSERT INTO suppressions (email_hash, reason, brand_id, created_at)
			SELECT DISTINCT ON (e.email_hash) e.email_hash, CASE WHEN e.type = 'complaint' THEN 'complaint' ELSE 'hard_bounce' END, $2, e.occurred_at
			FROM email_events e JOIN sends s ON s.id = e.send_id
			WHERE s.campaign_id = $1 AND (e.type = 'complaint' OR e.subtype LIKE 'permanent/%')
			ORDER BY e.email_hash, e.type = 'complaint' DESC
			ON CONFLICT (email_hash) DO NOTHING`, id, brandID); err != nil {
			return err
		}
	}

	finish := []struct {
		name string
		sql  string
	}{
		// Unsubscribes: from the link in the last email before, when there was one.
		{"unsubscribes", `INSERT INTO consent_records (brand_id, contact_id, email_hash, kind, method, source, occurred_at, campaign_id)
			SELECT c.brand_id, c.id, c.email_hash, 'unsubscribed', 'link', 'Unsubscribe link', u.at, last.campaign_id
			FROM demo_unsub u JOIN contacts c ON c.id = u.id
			LEFT JOIN LATERAL (SELECT s.campaign_id FROM sends s WHERE s.contact_id = c.id AND s.sent_at < u.at
				ORDER BY s.sent_at DESC LIMIT 1) last ON true
			WHERE c.brand_id = $1`},
		{"complaints", `INSERT INTO consent_records (brand_id, contact_id, email_hash, kind, method, source, occurred_at, campaign_id)
			SELECT s.brand_id, s.contact_id, e.email_hash, 'complained', 'ses', 'Marked as spam', e.occurred_at, s.campaign_id
			FROM email_events e JOIN sends s ON s.id = e.send_id WHERE s.brand_id = $1 AND e.type = 'complaint'`},
		// Engagement follows from the clicks (and, for imported contacts,
		// from the old tool's last click).
		{"engagement", `UPDATE contacts c SET
				last_click_at = greatest(k.at, c.imported_click_at),
				last_engaged_at = greatest(k.at, c.imported_click_at, c.consented_at)
			FROM contacts c2 LEFT JOIN (SELECT contact_id, max(at) AS at FROM clicks WHERE brand_id = $1 AND NOT is_bot GROUP BY 1) k
				ON k.contact_id = c2.id
			WHERE c2.brand_id = $1 AND c.id = c2.id`},
		// The cross-brand frequency cap counts the last 8 days.
		{"frequency cap", `INSERT INTO marketing_sends (email_hash, brand_id, contact_id, sent_at)
			SELECT c.email_hash, s.brand_id, s.contact_id, s.sent_at FROM sends s JOIN contacts c ON c.id = s.contact_id
			WHERE s.brand_id = $1 AND s.sent_at > now() - interval '8 days'`},
	}
	for _, st := range finish {
		if _, err := tx.Exec(ctx, st.sql, brandID); err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO campaigns (brand_id, name, topic_id, template_id, subject, created_by, updated_by)
		VALUES ($1, 'Winter preview (draft)', $2, $3, 'A first look at winter', $4, $4)`, brandID, newsletter, t.id, owner)
	return err
}

func seedSegments(ctx context.Context, tx pgx.Tx, brandID uuid.UUID, topicNames []string, topics []uuid.UUID, owner uuid.UUID) error {
	segs := []struct{ name, rules string }{
		{"Engaged VIPs", `{"match": "all", "groups": [{"match": "all", "conditions": [
			{"field": "tier", "op": "in", "value": ["active", "warm"]}, {"field": "tag", "op": "has", "value": "vip"}]}]}`},
		{"Slipping away", `{"match": "all", "groups": [{"match": "all", "conditions": [
			{"field": "tier", "op": "is", "value": "at_risk"}, {"field": "status", "op": "is", "value": "subscribed"}]}]}`},
		{"Europe, no click tracking", `{"match": "all", "groups": [{"match": "all", "conditions": [
			{"field": "country", "op": "in", "value": ["DE", "FR", "NL"]}, {"field": "last_click_at", "op": "is_empty"}]}]}`},
	}
	if len(topics) > 1 {
		segs = append(segs, struct{ name, rules string }{"Interested in " + strings.ToLower(topicNames[1]), fmt.Sprintf(`{"match": "all", "groups": [{"match": "all", "conditions": [
			{"field": "topic", "op": "subscribed", "value": %q}]}]}`, topics[1])})
	}
	for _, s := range segs {
		if _, err := tx.Exec(ctx, `INSERT INTO segments (brand_id, name, rules, created_by) VALUES ($1, $2, $3, $4)`, brandID, s.name, s.rules, owner); err != nil {
			return fmt.Errorf("segment %q: %w", s.name, err)
		}
	}
	return nil
}
