package store

import (
	"context"

	"github.com/google/uuid"
)

// --- campaign monitor (S7) and report (S11), M4 ---

// SendCounts: a campaign's sends by status (skipped split by reason).
type SendCounts struct {
	Queued  int            `json:"queued"`
	Sending int            `json:"sending"`
	Sent    int            `json:"sent"`
	Failed  int            `json:"failed"`
	Unknown int            `json:"unknown"`
	Skipped map[string]int `json:"skipped"`
}

func (s *Store) CampaignSendCounts(ctx context.Context, campaignID uuid.UUID) (SendCounts, error) {
	out := SendCounts{Skipped: map[string]int{}}
	rows, err := s.db.Query(ctx, `SELECT status, skip_reason, count(*) FROM sends WHERE campaign_id = $1 GROUP BY 1, 2`, campaignID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var status, reason string
		var n int
		if err := rows.Scan(&status, &reason, &n); err != nil {
			return out, err
		}
		switch status {
		case "queued":
			out.Queued += n
		case "sending":
			out.Sending += n
		case "sent":
			out.Sent += n
		case "failed":
			out.Failed += n
		case "unknown":
			out.Unknown += n
		case "skipped":
			out.Skipped[reason] += n
		}
	}
	return out, rows.Err()
}

// Engagement: clicks and opens of a campaign, with bots filtered out.
type Engagement struct {
	HumanClicks   int            `json:"human_clicks"`
	UniqueClicks  int            `json:"unique_clickers"`
	BotClicks     int            `json:"bot_clicks"`
	BotReasons    map[string]int `json:"bot_reasons"`
	HumanOpens    int            `json:"human_opens_est"` // unique people; an estimate
	MachineOpens  int            `json:"machine_opens"`
	SoftBounces   int            `json:"soft_bounces"`
	DeliveryDelay int            `json:"delivery_delays"`
}

func (s *Store) CampaignEngagement(ctx context.Context, campaignID uuid.UUID) (Engagement, error) {
	out := Engagement{BotReasons: map[string]int{}}
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE NOT k.is_bot), count(DISTINCT k.contact_id) FILTER (WHERE NOT k.is_bot), count(*) FILTER (WHERE k.is_bot)
		FROM clicks k JOIN sends s ON s.id = k.send_id WHERE s.campaign_id = $1`, campaignID).
		Scan(&out.HumanClicks, &out.UniqueClicks, &out.BotClicks)
	if err != nil {
		return out, err
	}
	rows, err := s.db.Query(ctx, `SELECT k.bot_reason, count(*) FROM clicks k JOIN sends s ON s.id = k.send_id
		WHERE s.campaign_id = $1 AND k.is_bot GROUP BY 1`, campaignID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var r string
		var n int
		if err := rows.Scan(&r, &n); err != nil {
			rows.Close()
			return out, err
		}
		out.BotReasons[r] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT count(DISTINCT o.contact_id) FILTER (WHERE NOT o.is_machine), count(*) FILTER (WHERE o.is_machine)
		FROM opens o JOIN sends s ON s.id = o.send_id WHERE s.campaign_id = $1`, campaignID).Scan(&out.HumanOpens, &out.MachineOpens)
	if err != nil {
		return out, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE e.type = 'bounce' AND e.subtype NOT LIKE 'permanent/%'), count(*) FILTER (WHERE e.type = 'delivery_delay')
		FROM email_events e JOIN sends s ON s.id = e.send_id WHERE s.campaign_id = $1`, campaignID).Scan(&out.SoftBounces, &out.DeliveryDelay)
	return out, err
}

// LinkStat: one link of the email.
type LinkStat struct {
	LinkNo      int    `db:"link_no" json:"link_no"`
	URL         string `db:"url" json:"url"`
	HumanClicks int    `db:"human_clicks" json:"human_clicks"`
	Unique      int    `db:"unique_clickers" json:"unique_clickers"`
	BotClicks   int    `db:"bot_clicks" json:"bot_clicks"`
}

func (s *Store) CampaignLinks(ctx context.Context, campaignID uuid.UUID, limit int) ([]LinkStat, error) {
	rows, err := s.db.Query(ctx, `
		SELECT k.link_no, min(k.url) AS url, count(*) FILTER (WHERE NOT k.is_bot) AS human_clicks,
			count(DISTINCT k.contact_id) FILTER (WHERE NOT k.is_bot) AS unique_clickers, count(*) FILTER (WHERE k.is_bot) AS bot_clicks
		FROM clicks k JOIN sends s ON s.id = k.send_id WHERE s.campaign_id = $1
		GROUP BY k.link_no ORDER BY 3 DESC, 1 LIMIT $2`, campaignID, limit)
	return all[LinkStat](rows, err)
}

// ProviderStat: results by mailbox provider (from the recipient's domain).
type ProviderStat struct {
	Provider   string `db:"provider" json:"provider"`
	Sent       int    `db:"sent" json:"sent"`
	Delivered  int    `db:"delivered" json:"delivered"`
	Bounces    int    `db:"bounces" json:"bounces"`
	Complaints int    `db:"complaints" json:"complaints"`
	Clickers   int    `db:"clickers" json:"clickers"`
}

// providerExpr groups recipient domains into the providers that matter for deliverability.
const providerExpr = `CASE
	WHEN d IN ('gmail.com', 'googlemail.com') THEN 'Gmail'
	WHEN d IN ('outlook.com', 'hotmail.com', 'live.com', 'msn.com') OR d LIKE 'hotmail.%' OR d LIKE 'outlook.%' OR d LIKE 'live.%' THEN 'Microsoft'
	WHEN d IN ('yahoo.com', 'ymail.com', 'aol.com', 'rocketmail.com') OR d LIKE 'yahoo.%' THEN 'Yahoo'
	WHEN d IN ('icloud.com', 'me.com', 'mac.com') THEN 'Apple'
	WHEN d IN ('gmx.de', 'gmx.net', 'web.de', 'gmx.com') THEN 'GMX / Web.de'
	ELSE 'Other' END`

func (s *Store) CampaignProviders(ctx context.Context, campaignID uuid.UUID) ([]ProviderStat, error) {
	rows, err := s.db.Query(ctx, `
		WITH r AS (
			SELECT s.id, s.contact_id, s.status, lower(split_part(c.email::text, '@', 2)) AS d
			FROM sends s JOIN contacts c ON c.id = s.contact_id WHERE s.campaign_id = $1)
		SELECT `+providerExpr+` AS provider,
			count(*) FILTER (WHERE r.status = 'sent') AS sent,
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM email_events e WHERE e.send_id = r.id AND e.type = 'delivery')) AS delivered,
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM email_events e WHERE e.send_id = r.id AND e.type = 'bounce' AND e.subtype LIKE 'permanent/%')) AS bounces,
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM email_events e WHERE e.send_id = r.id AND e.type = 'complaint')) AS complaints,
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM clicks k WHERE k.send_id = r.id AND NOT k.is_bot)) AS clickers
		FROM r GROUP BY 1 ORDER BY 2 DESC, 1`, campaignID)
	return all[ProviderStat](rows, err)
}

// CampaignSafetyEvents: what the brakes did to a campaign.
func (s *Store) CampaignSafetyEvents(ctx context.Context, campaignID uuid.UUID) ([]SafetyEvent, error) {
	rows, err := s.db.Query(ctx, `SELECT id, at, scope, brand_id, campaign_id, action, from_state, to_state, reason, metrics
		FROM safety_events WHERE campaign_id = $1 ORDER BY at DESC, id DESC LIMIT 20`, campaignID)
	return all[SafetyEvent](rows, err)
}
