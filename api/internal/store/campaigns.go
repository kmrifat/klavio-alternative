package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// --- brand sender ---

// BrandSender is who a brand's email comes from.
type BrandSender struct {
	FromName  string `db:"from_name" json:"from_name"` // "" = the brand name
	FromLocal string `db:"from_local" json:"from_local"`
	ReplyTo   string `db:"reply_to" json:"reply_to"`
}

func (s *Store) BrandSender(ctx context.Context, brandID uuid.UUID) (BrandSender, error) {
	rows, err := s.db.Query(ctx, `SELECT from_name, from_local, reply_to FROM brands WHERE id = $1`, brandID)
	return one[BrandSender](rows, err)
}

func (s *Store) SetBrandSender(ctx context.Context, brandID uuid.UUID, b BrandSender) error {
	_, err := s.db.Exec(ctx, `UPDATE brands SET from_name = $2, from_local = $3, reply_to = $4, updated_at = now() WHERE id = $1`,
		brandID, b.FromName, b.FromLocal, b.ReplyTo)
	return translate(err)
}

// --- seed addresses ---

type SeedAddress struct {
	Email     string    `db:"email" json:"email"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

func (s *Store) SeedAddresses(ctx context.Context, brandID uuid.UUID) ([]SeedAddress, error) {
	rows, err := s.db.Query(ctx, `SELECT email, created_at FROM seed_addresses WHERE brand_id = $1 ORDER BY email`, brandID)
	return all[SeedAddress](rows, err)
}

func (s *Store) AddSeedAddress(ctx context.Context, brandID uuid.UUID, email string, by uuid.UUID) error {
	_, err := s.db.Exec(ctx, `INSERT INTO seed_addresses (brand_id, email, added_by) VALUES ($1, $2, $3)`, brandID, email, by)
	return translate(err)
}

func (s *Store) RemoveSeedAddress(ctx context.Context, brandID uuid.UUID, email string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM seed_addresses WHERE brand_id = $1 AND email = $2`, brandID, email)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- campaigns ---

type CampaignState string

const (
	CampaignDraft         CampaignState = "draft"
	CampaignNeedsApproval CampaignState = "needs_approval"
	CampaignScheduled     CampaignState = "scheduled"
	CampaignTestSlice     CampaignState = "test_slice"
	CampaignHolding       CampaignState = "holding"
	CampaignSending       CampaignState = "sending"
	CampaignPaused        CampaignState = "paused"
	CampaignDone          CampaignState = "done"
	CampaignCancelled     CampaignState = "cancelled"
)

// Editable reports whether a campaign's content and audience may change.
func (s CampaignState) Editable() bool { return s == CampaignDraft || s == CampaignNeedsApproval }

// Running: past scheduling and not finished.
func (s CampaignState) Running() bool {
	return s == CampaignTestSlice || s == CampaignHolding || s == CampaignSending || s == CampaignPaused
}

type Campaign struct {
	ID              uuid.UUID      `db:"id" json:"id"`
	BrandID         uuid.UUID      `db:"brand_id" json:"-"`
	Name            string         `db:"name" json:"name"`
	TopicID         uuid.UUID      `db:"topic_id" json:"topic_id"`
	TemplateID      *uuid.UUID     `db:"template_id" json:"template_id"`
	Subject         string         `db:"subject" json:"subject"`
	Preheader       string         `db:"preheader" json:"preheader"`
	IncludeAtRisk   bool           `db:"include_at_risk" json:"include_at_risk"`
	State           CampaignState  `db:"state" json:"state"`
	PausedFrom      *CampaignState `db:"paused_from" json:"paused_from"`
	PauseReason     string         `db:"pause_reason" json:"pause_reason"`
	WaitingReason   string         `db:"waiting_reason" json:"waiting_reason"`
	Version         int            `db:"version" json:"version"`
	HTML            string         `db:"html" json:"-"`
	TextBody        string         `db:"text_body" json:"-"`
	FromHeader      string         `db:"from_header" json:"from_header"`
	ReplyTo         string         `db:"reply_to" json:"reply_to"`
	TestSlicePct    *float64       `db:"test_slice_pct" json:"test_slice_pct"`
	HoldMinutes     *int           `db:"hold_minutes" json:"hold_minutes"`
	ScheduledAt     *time.Time     `db:"scheduled_at" json:"scheduled_at"`
	StartAt         *time.Time     `db:"start_at" json:"start_at"`
	ConfirmedBy     *uuid.UUID     `db:"confirmed_by" json:"confirmed_by"`
	ConfirmedAt     *time.Time     `db:"confirmed_at" json:"confirmed_at"`
	ApprovalID      *uuid.UUID     `db:"approval_id" json:"approval_id"`
	ApprovalReasons []string       `db:"approval_reasons" json:"approval_reasons"`
	HoldUntil       *time.Time     `db:"hold_until" json:"hold_until"`
	AudienceCount   int            `db:"audience_count" json:"audience_count"`
	ExcludedCount   int            `db:"excluded_count" json:"excluded_count"`
	CreatedBy       *uuid.UUID     `db:"created_by" json:"created_by"`
	UpdatedBy       *uuid.UUID     `db:"updated_by" json:"updated_by"`
	CreatedAt       time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time      `db:"updated_at" json:"updated_at"`
	FinishedAt      *time.Time     `db:"finished_at" json:"finished_at"`
}

const campaignCols = `id, brand_id, name, topic_id, template_id, subject, preheader, include_at_risk, state, paused_from,
	pause_reason, waiting_reason, version, html, text_body, from_header, reply_to, test_slice_pct::float8 AS test_slice_pct, hold_minutes,
	scheduled_at, start_at, confirmed_by, confirmed_at, approval_id, approval_reasons, hold_until, audience_count,
	excluded_count, created_by, updated_by, created_at, updated_at, finished_at`

// CampaignDraftInput is what an editor sets on a draft.
type CampaignDraftInput struct {
	Name          string
	TopicID       uuid.UUID
	TemplateID    *uuid.UUID
	Subject       string
	Preheader     string
	IncludeAtRisk bool
}

func (s *Store) CreateCampaign(ctx context.Context, brandID uuid.UUID, in CampaignDraftInput, by uuid.UUID) (Campaign, error) {
	rows, err := s.db.Query(ctx, `
		INSERT INTO campaigns (brand_id, name, topic_id, template_id, subject, preheader, include_at_risk, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		RETURNING `+campaignCols,
		brandID, in.Name, in.TopicID, in.TemplateID, in.Subject, in.Preheader, in.IncludeAtRisk, by)
	return one[Campaign](rows, err)
}

func (s *Store) CampaignByID(ctx context.Context, brandID, id uuid.UUID) (Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE brand_id = $1 AND id = $2`, brandID, id)
	return one[Campaign](rows, err)
}

// LockCampaign reads a campaign and holds it until the transaction ends.
func (s *Store) LockCampaign(ctx context.Context, id uuid.UUID) (Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE id = $1 FOR UPDATE`, id)
	return one[Campaign](rows, err)
}

func (s *Store) ListCampaigns(ctx context.Context, brandID uuid.UUID) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE brand_id = $1 ORDER BY updated_at DESC LIMIT 200`, brandID)
	return all[Campaign](rows, err)
}

// UpdateCampaignDraft saves an edit. Any edit sends the campaign back to
// draft (an approval no longer applies). ErrStale if the version moved or
// the campaign is no longer editable.
func (s *Store) UpdateCampaignDraft(ctx context.Context, brandID, id uuid.UUID, in CampaignDraftInput, fromVersion int, by uuid.UUID) (Campaign, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE campaigns SET name = $3, topic_id = $4, template_id = $5, subject = $6, preheader = $7, include_at_risk = $8,
			state = 'draft', approval_id = NULL, approval_reasons = '{}', version = version + 1, updated_by = $10, updated_at = now()
		WHERE brand_id = $1 AND id = $2 AND version = $9 AND state IN ('draft', 'needs_approval')
		RETURNING `+campaignCols,
		brandID, id, in.Name, in.TopicID, in.TemplateID, in.Subject, in.Preheader, in.IncludeAtRisk, fromVersion, by)
	c, err := one[Campaign](rows, err)
	if errors.Is(err, ErrNotFound) {
		if _, gerr := s.CampaignByID(ctx, brandID, id); gerr == nil {
			return Campaign{}, ErrStale
		}
	}
	return c, err
}

// CampaignSchedule is what gets frozen when a campaign is confirmed.
type CampaignSchedule struct {
	HTML, Text, FromHeader, ReplyTo string
	TestSlicePct                    float64
	HoldMinutes                     int
	ScheduledAt, StartAt            time.Time
	ConfirmedBy                     uuid.UUID
	ApprovalReasons                 []string
	AudienceCount, ExcludedCount    int
	State                           CampaignState // scheduled, or needs_approval
	ApprovalID                      *uuid.UUID
}

func (s *Store) ScheduleCampaign(ctx context.Context, id uuid.UUID, sc CampaignSchedule) (Campaign, error) {
	if sc.ApprovalReasons == nil {
		sc.ApprovalReasons = []string{}
	}
	rows, err := s.db.Query(ctx, `
		UPDATE campaigns SET state = $2, html = $3, text_body = $4, from_header = $5, reply_to = $6, test_slice_pct = $7,
			hold_minutes = $8, scheduled_at = $9, start_at = $10, confirmed_by = $11, confirmed_at = now(),
			approval_reasons = $12, audience_count = $13, excluded_count = $14, approval_id = $15, updated_at = now()
		WHERE id = $1
		RETURNING `+campaignCols,
		id, sc.State, sc.HTML, sc.Text, sc.FromHeader, sc.ReplyTo, sc.TestSlicePct, sc.HoldMinutes, sc.ScheduledAt,
		sc.StartAt, sc.ConfirmedBy, sc.ApprovalReasons, sc.AudienceCount, sc.ExcludedCount, sc.ApprovalID)
	return one[Campaign](rows, err)
}

// SetCampaignState moves a campaign from one of the given states to `to`.
// It reports false (and changes nothing) if the campaign wasn't in any of them.
func (s *Store) SetCampaignState(ctx context.Context, id uuid.UUID, from []CampaignState, to CampaignState) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE campaigns SET state = $3, updated_at = now(),
			finished_at = CASE WHEN $3 IN ('done', 'cancelled') THEN now() ELSE finished_at END
		WHERE id = $1 AND state = ANY($2)`, id, from, to)
	return err == nil && tag.RowsAffected() == 1, err
}

// PauseCampaign pauses a running campaign, remembering where to resume.
func (s *Store) PauseCampaign(ctx context.Context, id uuid.UUID, reason string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE campaigns SET paused_from = state, state = 'paused', pause_reason = $2, updated_at = now()
		WHERE id = $1 AND state IN ('test_slice', 'holding', 'sending')`, id, reason)
	return err == nil && tag.RowsAffected() == 1, err
}

// ResumeCampaign puts a paused campaign back where it was.
func (s *Store) ResumeCampaign(ctx context.Context, id uuid.UUID) (CampaignState, bool, error) {
	var st CampaignState
	err := s.db.QueryRow(ctx, `
		UPDATE campaigns SET state = paused_from, paused_from = NULL, pause_reason = '', waiting_reason = '', updated_at = now()
		WHERE id = $1 AND state = 'paused'
		RETURNING state`, id).Scan(&st)
	if errors.Is(translate(err), ErrNotFound) {
		return "", false, nil
	}
	return st, err == nil, err
}

func (s *Store) SetCampaignHold(ctx context.Context, id uuid.UUID, until time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE campaigns SET hold_until = $2, updated_at = now() WHERE id = $1`, id, until)
	return err
}

// UsualAudienceSize is the median audience of the brand's last five
// finished campaigns, or 0 if it has none.
func (s *Store) UsualAudienceSize(ctx context.Context, brandID uuid.UUID) (int, error) {
	var n *float64
	err := s.db.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY audience_count) FROM (
			SELECT audience_count FROM campaigns WHERE brand_id = $1 AND state = 'done' ORDER BY finished_at DESC LIMIT 5
		) recent`, brandID).Scan(&n)
	if err != nil || n == nil {
		return 0, err
	}
	return int(*n), nil
}

// --- audience ---

// ExclusionReason says why a topic member won't get a campaign.
type ExclusionReason string

const (
	ExcludedSuppressed   ExclusionReason = "suppressed"
	ExcludedUnsubscribed ExclusionReason = "unsubscribed"
	ExcludedQuarantined  ExclusionReason = "quarantined"
	ExcludedPending      ExclusionReason = "pending_confirmation"
	ExcludedSunset       ExclusionReason = "sunset"
	ExcludedAtRisk       ExclusionReason = "at_risk"
)

// audienceQuery classifies every member of a topic: reason NULL = included.
// $1 brand, $2 topic, $3 include at-risk. Order of precedence: suppressed,
// then contact status, then engagement tier.
const audienceQuery = `
	SELECT c.id AS contact_id, c.import_id, CASE
		WHEN s.email_hash IS NOT NULL THEN 'suppressed'
		WHEN c.status = 'unsubscribed' THEN 'unsubscribed'
		WHEN c.status = 'quarantined' THEN 'quarantined'
		WHEN c.status = 'pending_confirmation' THEN 'pending_confirmation'
		WHEN ` + tierExpr + ` = 'sunset' THEN 'sunset'
		WHEN ` + tierExpr + ` = 'at_risk' AND NOT $3 THEN 'at_risk'
	END AS reason
	FROM contact_topics ct
	JOIN contacts c ON c.id = ct.contact_id
	LEFT JOIN suppressions s ON s.email_hash = c.email_hash
	WHERE ct.brand_id = $1 AND ct.topic_id = $2`

// AudienceSummary counts a would-be audience: included, and excluded by reason.
type AudienceSummary struct {
	Included int                     `json:"included"`
	Excluded map[ExclusionReason]int `json:"excluded"`
	// FirstTimeImported: included people from an import who never got a campaign.
	FirstTimeImported int `json:"first_time_imported"`
	// AtRisk: included people in the at-risk tier (only when allowed).
	AtRisk int `json:"at_risk"`
}

func (s *Store) AudienceSummary(ctx context.Context, brandID, topicID uuid.UUID, includeAtRisk bool) (AudienceSummary, error) {
	sum := AudienceSummary{Excluded: map[ExclusionReason]int{}}
	rows, err := s.db.Query(ctx, `
		WITH a AS (`+audienceQuery+`)
		SELECT coalesce(reason, ''), count(*),
			count(*) FILTER (WHERE reason IS NULL AND import_id IS NOT NULL
				AND NOT EXISTS (SELECT 1 FROM sends sd WHERE sd.contact_id = a.contact_id AND sd.status = 'sent'))
		FROM a GROUP BY 1`, brandID, topicID, includeAtRisk)
	if err != nil {
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var n, firstImport int
		if err := rows.Scan(&reason, &n, &firstImport); err != nil {
			return sum, err
		}
		if reason == "" {
			sum.Included, sum.FirstTimeImported = n, firstImport
		} else {
			sum.Excluded[ExclusionReason(reason)] = n
		}
	}
	if err := rows.Err(); err != nil || !includeAtRisk {
		return sum, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT count(*) FROM contact_topics ct JOIN contacts c ON c.id = ct.contact_id
		LEFT JOIN suppressions s ON s.email_hash = c.email_hash
		WHERE ct.brand_id = $1 AND ct.topic_id = $2 AND s.email_hash IS NULL AND c.status = 'subscribed'
		  AND `+tierExpr+` = 'at_risk'`, brandID, topicID).Scan(&sum.AtRisk)
	return sum, err
}

// SnapshotAudience writes the audience of a campaign being scheduled, with
// a random test slice of testPct percent (at least one person). Returns
// included and excluded counts.
func (s *Store) SnapshotAudience(ctx context.Context, c Campaign, testPct float64) (included, excluded int, err error) {
	if _, err := s.db.Exec(ctx, `DELETE FROM campaign_audience WHERE campaign_id = $1`, c.ID); err != nil {
		return 0, 0, err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO campaign_audience (campaign_id, contact_id, excluded_reason, stage)
		SELECT $4, contact_id, reason,
			CASE WHEN reason IS NULL THEN CASE WHEN rn <= greatest(1, ceil(n * $5 / 100.0)) THEN 'test' ELSE 'rest' END END
		FROM (
			SELECT a.contact_id, a.reason,
				count(*) FILTER (WHERE a.reason IS NULL) OVER () AS n,
				CASE WHEN a.reason IS NULL THEN row_number() OVER (PARTITION BY a.reason IS NULL ORDER BY random()) END AS rn
			FROM (`+audienceQuery+`) a
		) x`, c.BrandID, c.TopicID, c.IncludeAtRisk, c.ID, testPct)
	if err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE stage IS NOT NULL), count(*) FILTER (WHERE excluded_reason IS NOT NULL)
		FROM campaign_audience WHERE campaign_id = $1`, c.ID).Scan(&included, &excluded)
	return included, excluded, err
}

// AudienceBreakdown counts a campaign's stored snapshot by reason and stage (S5).
func (s *Store) AudienceBreakdown(ctx context.Context, campaignID uuid.UUID) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `
		SELECT coalesce(excluded_reason, 'included_' || stage), count(*) FROM campaign_audience
		WHERE campaign_id = $1 GROUP BY 1`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// --- sends ---

// Recipient is an included audience member about to get a send row.
type Recipient struct {
	ContactID uuid.UUID `db:"contact_id"`
	Timezone  string    `db:"timezone"`
}

// StageRecipients lists a stage's audience that has no send row yet.
func (s *Store) StageRecipients(ctx context.Context, campaignID uuid.UUID, stage string) ([]Recipient, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ca.contact_id, coalesce(c.timezone, '') AS timezone
		FROM campaign_audience ca LEFT JOIN contacts c ON c.id = ca.contact_id
		WHERE ca.campaign_id = $1 AND ca.stage = $2
		  AND NOT EXISTS (SELECT 1 FROM sends sd WHERE sd.campaign_id = ca.campaign_id AND sd.contact_id = ca.contact_id)`,
		campaignID, stage)
	return all[Recipient](rows, err)
}

// QueueSends creates send rows. Existing ones are left alone (never twice).
func (s *Store) QueueSends(ctx context.Context, c Campaign, stage string, contactIDs []uuid.UUID, sendAfter []time.Time) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO sends (campaign_id, brand_id, contact_id, stage, send_after)
		SELECT $1, $2, id, $3, after FROM unnest($4::uuid[], $5::timestamptz[]) AS t(id, after)
		ON CONFLICT (campaign_id, contact_id) DO NOTHING`, c.ID, c.BrandID, stage, contactIDs, sendAfter)
	return err
}

// ClaimedSend is a send taken by the worker, with what's needed to send it
// and to re-check, right before sending, that the person still wants it.
type ClaimedSend struct {
	ID         int64     `db:"id"`
	ContactID  uuid.UUID `db:"contact_id"`
	Attempts   int       `db:"attempts"`
	Email      *string   `db:"email"` // nil: the contact was erased
	FirstName  string    `db:"first_name"`
	LastName   string    `db:"last_name"`
	Status     *string   `db:"status"`
	InTopic    bool      `db:"in_topic"`
	Suppressed bool      `db:"suppressed"`
	Timezone   string    `db:"timezone"` // for quiet hours when it has to be rescheduled
	Token      uuid.UUID `db:"claim_token"`
}

// SendClaim names one claimed send: its row and the claim that holds it.
// Every update of a claimed send matches both, so a worker whose claim was
// taken over (it stalled, and its rows were recovered and claimed again)
// changes nothing.
type SendClaim struct {
	ID    int64
	Token uuid.UUID
}

func (c ClaimedSend) Claim() SendClaim { return SendClaim{c.ID, c.Token} }

// ClaimSends marks up to n due, queued sends of a stage as 'sending',
// under a new claim token.
func (s *Store) ClaimSends(ctx context.Context, c Campaign, stage string, n int) ([]ClaimedSend, error) {
	rows, err := s.db.Query(ctx, `
		WITH claimed AS (
			UPDATE sends SET status = 'sending', claimed_at = now(), called_at = NULL, attempts = attempts + 1, claim_token = $5
			WHERE id IN (
				SELECT id FROM sends WHERE campaign_id = $1 AND stage = $2 AND status = 'queued' AND send_after <= now()
				ORDER BY send_after, id LIMIT $3 FOR UPDATE SKIP LOCKED)
			RETURNING id, contact_id, attempts, claim_token)
		SELECT cl.id, cl.contact_id, cl.attempts, cl.claim_token, c.email::text AS email, coalesce(c.first_name, '') AS first_name,
			coalesce(c.last_name, '') AS last_name, c.status,
			EXISTS (SELECT 1 FROM contact_topics ct WHERE ct.contact_id = cl.contact_id AND ct.topic_id = $4) AS in_topic,
			EXISTS (SELECT 1 FROM suppressions sp WHERE sp.email_hash = c.email_hash) AS suppressed,
			coalesce(c.timezone, '') AS timezone
		FROM claimed cl LEFT JOIN contacts c ON c.id = cl.contact_id
		ORDER BY cl.id`, c.ID, stage, n, c.TopicID, uuid.New())
	return all[ClaimedSend](rows, err)
}

// MarkSent records SES's answer. A send already written off as 'unknown'
// (its worker stalled past the recovery time, then got the answer) is
// corrected: the claim is still its own, as nobody re-claims an unknown.
func (s *Store) MarkSent(ctx context.Context, cl SendClaim, messageID string) error {
	_, err := s.db.Exec(ctx, `UPDATE sends SET status = 'sent', ses_message_id = $3, sent_at = now(), error = ''
		WHERE id = $1 AND claim_token = $2 AND status IN ('sending', 'unknown')`, cl.ID, cl.Token, messageID)
	return err
}

func (s *Store) MarkSendFailed(ctx context.Context, cl SendClaim, msg string) error {
	_, err := s.db.Exec(ctx, `UPDATE sends SET status = 'failed', error = $3 WHERE id = $1 AND claim_token = $2 AND status = 'sending'`, cl.ID, cl.Token, msg)
	return err
}

func (s *Store) MarkSendSkipped(ctx context.Context, cl SendClaim, reason string) error {
	_, err := s.db.Exec(ctx, `UPDATE sends SET status = 'skipped', skip_reason = $3 WHERE id = $1 AND claim_token = $2 AND status = 'sending'`, cl.ID, cl.Token, reason)
	return err
}

// RequeueSend puts a send back in the queue for a later try.
func (s *Store) RequeueSend(ctx context.Context, cl SendClaim, after time.Time, msg string) error {
	_, err := s.db.Exec(ctx, `UPDATE sends SET status = 'queued', send_after = $3, error = $4, claim_token = NULL
		WHERE id = $1 AND claim_token = $2 AND status = 'sending'`, cl.ID, cl.Token, after, msg)
	return err
}

// ReleaseSends puts sends of one claim back untouched (the campaign was
// paused between claiming and sending). The attempt isn't counted.
func (s *Store) ReleaseSends(ctx context.Context, token uuid.UUID, ids []int64) error {
	_, err := s.db.Exec(ctx, `UPDATE sends SET status = 'queued', attempts = attempts - 1, claim_token = NULL
		WHERE id = ANY($1) AND claim_token = $2 AND status = 'sending'`, ids, token)
	return err
}

// MarkCalling stamps a send right before SES is called. false: the claim
// is no longer this worker's (it stalled and the send was recovered), so it
// must not send.
func (s *Store) MarkCalling(ctx context.Context, cl SendClaim) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE sends SET called_at = now() WHERE id = $1 AND claim_token = $2 AND status = 'sending' AND called_at IS NULL`, cl.ID, cl.Token)
	return err == nil && tag.RowsAffected() == 1, err
}

// MarkSendUnknown records that SES may or may not have sent it. It is never retried.
func (s *Store) MarkSendUnknown(ctx context.Context, cl SendClaim, msg string) error {
	_, err := s.db.Exec(ctx, `UPDATE sends SET status = 'unknown', error = $3 WHERE id = $1 AND claim_token = $2 AND status = 'sending'`, cl.ID, cl.Token, msg)
	return err
}

// MarkStuckSends cleans up sends left in 'sending' longer than d by a worker
// that died. Those that reached SES (called_at set) become 'unknown': the
// email may have gone out, and sending it again could mean sending twice.
// Those that never did go back to the queue.
func (s *Store) MarkStuckSends(ctx context.Context, campaignID uuid.UUID, d time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE sends SET status = 'unknown', error = 'worker stopped while sending; not retried to avoid a duplicate'
		WHERE campaign_id = $1 AND status = 'sending' AND called_at IS NOT NULL AND claimed_at < now() - $2::interval`, campaignID, d.String())
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE sends SET status = 'queued', attempts = greatest(attempts - 1, 0), claim_token = NULL
		WHERE campaign_id = $1 AND status = 'sending' AND called_at IS NULL AND claimed_at < now() - $2::interval`, campaignID, d.String()); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountStageSends counts a stage's send rows, whatever their status.
func (s *Store) CountStageSends(ctx context.Context, campaignID uuid.UUID, stage string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM sends WHERE campaign_id = $1 AND stage = $2`, campaignID, stage).Scan(&n)
	return n, err
}

// AcquireStepLease lets one worker at a time run a campaign's step. It
// returns the holder's token (ok false: someone else holds it).
func (s *Store) AcquireStepLease(ctx context.Context, id uuid.UUID, d time.Duration) (token uuid.UUID, ok bool, err error) {
	token = uuid.New()
	tag, err := s.db.Exec(ctx, `
		UPDATE campaigns SET step_lease_until = now() + $2::interval, step_lease_holder = $3
		WHERE id = $1 AND (step_lease_until IS NULL OR step_lease_until < now())`, id, d.String(), token)
	return token, err == nil && tag.RowsAffected() == 1, err
}

// ReleaseStepLease gives up a lease if it is still this holder's. false:
// it expired and another step took it over.
func (s *Store) ReleaseStepLease(ctx context.Context, id, token uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE campaigns SET step_lease_until = NULL, step_lease_holder = NULL
		WHERE id = $1 AND step_lease_holder = $2`, id, token)
	return err == nil && tag.RowsAffected() == 1, err
}

// StepQueued reports whether a campaign step job is waiting, or running
// and alive. A running job whose lease has run out is treated as dead (a
// frozen process), so the sweep can take over without waiting for river
// to rescue it.
func (s *Store) StepQueued(ctx context.Context, id uuid.UUID) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM river_job WHERE kind = 'campaigns.step' AND args->>'campaign_id' = ($1::uuid)::text
			AND (state IN ('available', 'scheduled', 'retryable', 'pending')
				OR (state = 'running' AND EXISTS (SELECT 1 FROM campaigns WHERE id = $1::uuid AND step_lease_until > now()))))`, id).Scan(&ok)
	return ok, err
}

// NextSendAt is when the next queued send of a stage becomes due (nil: none left).
func (s *Store) NextSendAt(ctx context.Context, campaignID uuid.UUID, stage string) (*time.Time, error) {
	var t *time.Time
	err := s.db.QueryRow(ctx, `SELECT min(send_after) FROM sends WHERE campaign_id = $1 AND stage = $2 AND status IN ('queued', 'sending')`,
		campaignID, stage).Scan(&t)
	return t, err
}

// SendProgress counts a campaign's sends by stage and status.
type SendProgress map[string]map[string]int

func (s *Store) SendProgress(ctx context.Context, campaignID uuid.UUID) (SendProgress, error) {
	rows, err := s.db.Query(ctx, `SELECT stage, status, count(*) FROM sends WHERE campaign_id = $1 GROUP BY 1, 2`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := SendProgress{"test": {}, "rest": {}}
	for rows.Next() {
		var stage, status string
		var n int
		if err := rows.Scan(&stage, &status, &n); err != nil {
			return nil, err
		}
		out[stage][status] = n
	}
	return out, rows.Err()
}

// CampaignForStep reads a campaign by id alone (the worker has no brand in hand).
func (s *Store) CampaignForStep(ctx context.Context, id uuid.UUID) (Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE id = $1`, id)
	return one[Campaign](rows, err)
}

// CampaignState is a campaign's current state, read fresh.
func (s *Store) CampaignState(ctx context.Context, id uuid.UUID) (CampaignState, error) {
	var st CampaignState
	err := s.db.QueryRow(ctx, `SELECT state FROM campaigns WHERE id = $1`, id).Scan(&st)
	return st, translate(err)
}

// CampaignsWithWork lists campaigns whose next worker step is due,
// including those with sends stuck in 'sending' longer than stuckAfter (a
// worker died holding them).
func (s *Store) CampaignsWithWork(ctx context.Context, stuckAfter time.Duration) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id FROM campaigns c WHERE
			(state = 'scheduled' AND start_at <= now()) OR
			(state = 'holding' AND hold_until <= now()) OR
			(state IN ('test_slice', 'sending') AND (
				EXISTS (SELECT 1 FROM sends s WHERE s.campaign_id = c.id AND s.status = 'queued' AND s.send_after <= now())
				OR EXISTS (SELECT 1 FROM sends s WHERE s.campaign_id = c.id AND s.status = 'sending' AND s.claimed_at < now() - $1::interval)
				OR NOT EXISTS (SELECT 1 FROM sends s WHERE s.campaign_id = c.id AND s.status IN ('queued', 'sending'))))`, stuckAfter.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SkipQueuedSends marks every not-yet-sent send of a campaign as skipped.
func (s *Store) SkipQueuedSends(ctx context.Context, campaignID uuid.UUID, reason string) error {
	_, err := s.db.Exec(ctx, `UPDATE sends SET status = 'skipped', skip_reason = $2 WHERE campaign_id = $1 AND status = 'queued'`, campaignID, reason)
	return err
}

// RequeueSendUncounted is RequeueSend without counting the attempt (the
// failure wasn't the message's fault, e.g. throttling).
func (s *Store) RequeueSendUncounted(ctx context.Context, cl SendClaim, after time.Time, msg string) error {
	_, err := s.db.Exec(ctx, `UPDATE sends SET status = 'queued', send_after = $3, error = $4, attempts = attempts - 1, claim_token = NULL
		WHERE id = $1 AND claim_token = $2 AND status = 'sending'`, cl.ID, cl.Token, after, msg)
	return err
}

// SetCampaignWaiting records why a sending campaign is waiting ("" = it isn't).
func (s *Store) SetCampaignWaiting(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := s.db.Exec(ctx, `UPDATE campaigns SET waiting_reason = $2 WHERE id = $1 AND waiting_reason <> $2`, id, reason)
	return err
}

// DeferSends puts claimed sends back in the queue, each until its own time,
// without counting the attempt (the warm-up cap, not the message, held them
// back).
func (s *Store) DeferSends(ctx context.Context, token uuid.UUID, ids []int64, after []time.Time) error {
	_, err := s.db.Exec(ctx, `
		UPDATE sends s SET status = 'queued', send_after = t.after, attempts = s.attempts - 1, claim_token = NULL
		FROM unnest($1::bigint[], $2::timestamptz[]) AS t(id, after)
		WHERE s.id = t.id AND s.claim_token = $3 AND s.status = 'sending'`, ids, after, token)
	return err
}

// ApproveCampaign schedules a campaign whose approval came through.
func (s *Store) ApproveCampaign(ctx context.Context, id uuid.UUID, startAt time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE campaigns SET state = 'scheduled', start_at = $2, updated_at = now()
		WHERE id = $1 AND state = 'needs_approval'`, id, startAt)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrStale
	}
	return err
}

// ResetCampaignToDraft undoes scheduling when its approval was refused,
// cancelled or expired. A campaign that moved on (edited, cancelled) is
// left alone.
func (s *Store) ResetCampaignToDraft(ctx context.Context, id, approvalID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE campaigns SET state = 'draft', approval_id = NULL, start_at = NULL, confirmed_at = NULL,
			confirmed_by = NULL, html = '', text_body = '', audience_count = 0, excluded_count = 0, updated_at = now()
		WHERE id = $1 AND state = 'needs_approval' AND approval_id = $2`, id, approvalID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM campaign_audience WHERE campaign_id = $1`, id)
	return err
}
