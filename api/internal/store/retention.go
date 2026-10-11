package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EventRetention: raw SES events (email_events) are deleted after this.
// Finished campaigns' totals are saved first and kept forever.
const EventRetention = 13 * 30 * 24 * time.Hour // ≈ 13 months

// purgeBatch keeps each delete short, so it never holds locks for long.
const purgeBatch = 5000

type campaignStats struct {
	Delivered      int             `db:"delivered"`
	Bounces        int             `db:"bounces"`
	Complaints     int             `db:"complaints"`
	SoftBounces    int             `db:"soft_bounces"`
	DeliveryDelays int             `db:"delivery_delays"`
	Providers      json.RawMessage `db:"providers"`
}

// savedStats: the campaign's saved totals, if its events have been (or are
// about to be) deleted.
func (s *Store) savedStats(ctx context.Context, campaignID uuid.UUID) (*campaignStats, error) {
	st, err := one[campaignStats](s.db.Query(ctx, `SELECT delivered, bounces, complaints, soft_bounces, delivery_delays, providers
		FROM campaign_stats WHERE campaign_id = $1`, campaignID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return &st, err
}

// CampaignFeedback: what happened to a campaign's emails so far. Once its
// totals are saved they're used instead of counting events, which may be gone.
func (s *Store) CampaignFeedback(ctx context.Context, campaignID uuid.UUID) (FeedbackCounts, error) {
	c, err := s.campaignFeedbackLive(ctx, campaignID)
	if err != nil {
		return c, err
	}
	st, err := s.savedStats(ctx, campaignID)
	if st != nil {
		c.Delivered, c.Bounces, c.Complaints = st.Delivered, st.Bounces, st.Complaints
	}
	return c, err
}

// CampaignEngagement: clicks, opens and soft feedback; saved totals win, as above.
func (s *Store) CampaignEngagement(ctx context.Context, campaignID uuid.UUID) (Engagement, error) {
	e, err := s.campaignEngagementLive(ctx, campaignID)
	if err != nil {
		return e, err
	}
	st, err := s.savedStats(ctx, campaignID)
	if st != nil {
		e.SoftBounces, e.DeliveryDelay = st.SoftBounces, st.DeliveryDelays
	}
	return e, err
}

// CampaignProviders: the report's per-provider rows; saved ones win, as above.
func (s *Store) CampaignProviders(ctx context.Context, campaignID uuid.UUID) ([]ProviderStat, error) {
	st, err := s.savedStats(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return s.campaignProvidersLive(ctx, campaignID)
	}
	var out []ProviderStat
	return out, json.Unmarshal(st.Providers, &out)
}

// SaveCampaignStats saves a campaign's totals from its events (once; later
// calls change nothing).
func (s *Store) SaveCampaignStats(ctx context.Context, campaignID uuid.UUID) error {
	return s.InTx(ctx, func(tx *Store) error {
		f, err := tx.campaignFeedbackLive(ctx, campaignID)
		if err != nil {
			return err
		}
		e, err := tx.campaignEngagementLive(ctx, campaignID)
		if err != nil {
			return err
		}
		p, err := tx.campaignProvidersLive(ctx, campaignID)
		if err != nil {
			return err
		}
		if p == nil {
			p = []ProviderStat{}
		}
		providers, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = tx.db.Exec(ctx, `INSERT INTO campaign_stats (campaign_id, delivered, bounces, complaints, soft_bounces, delivery_delays, providers)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (campaign_id) DO NOTHING`,
			campaignID, f.Delivered, f.Bounces, f.Complaints, e.SoftBounces, e.DeliveryDelay, providers)
		return err
	})
}

// RetentionResult says what one run of ApplyEventRetention did.
type RetentionResult struct {
	CampaignsSaved int
	EventsDeleted  int64
}

// ApplyEventRetention deletes SES events older than EventRetention. A
// finished campaign's totals are saved first; events of campaigns that
// haven't finished (or finished within the window) stay until they have.
// Events not tied to a send of ours go once they're old.
func (s *Store) ApplyEventRetention(ctx context.Context, now time.Time) (RetentionResult, error) {
	var out RetentionResult
	cutoff := now.Add(-EventRetention)
	rows, err := s.db.Query(ctx, `SELECT id FROM campaigns c
		WHERE state IN ('done', 'cancelled') AND finished_at < $1
		  AND NOT EXISTS (SELECT 1 FROM campaign_stats cs WHERE cs.campaign_id = c.id)`, cutoff)
	if err != nil {
		return out, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		if err := s.SaveCampaignStats(ctx, id); err != nil {
			return out, err
		}
		out.CampaignsSaved++
	}
	for {
		tag, err := s.db.Exec(ctx, `DELETE FROM email_events WHERE id IN (
			SELECT e.id FROM email_events e LEFT JOIN sends s ON s.id = e.send_id
			WHERE e.occurred_at < $1
			  AND (s.id IS NULL OR EXISTS (SELECT 1 FROM campaign_stats cs WHERE cs.campaign_id = s.campaign_id))
			LIMIT $2)`, cutoff, purgeBatch)
		if err != nil {
			return out, err
		}
		out.EventsDeleted += tag.RowsAffected()
		if tag.RowsAffected() < purgeBatch {
			return out, nil
		}
	}
}
