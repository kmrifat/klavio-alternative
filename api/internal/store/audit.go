package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type AuditEntry struct {
	ID         int64           `db:"id" json:"id"`
	At         time.Time       `db:"at" json:"at"`
	ActorID    *uuid.UUID      `db:"actor_id" json:"actor_id"`
	ActorEmail string          `db:"actor_email" json:"actor_email"`
	BrandID    *uuid.UUID      `db:"brand_id" json:"brand_id"`
	Action     string          `db:"action" json:"action"`
	TargetType string          `db:"target_type" json:"target_type"`
	TargetID   string          `db:"target_id" json:"target_id"`
	Before     json.RawMessage `db:"before" json:"before,omitempty"`
	After      json.RawMessage `db:"after" json:"after,omitempty"`
	IP         string          `db:"ip" json:"ip"`
	UserAgent  string          `db:"user_agent" json:"user_agent"`
}

// Audit appends an entry. before/after are marshalled to JSON (nil stays NULL).
func (s *Store) Audit(ctx context.Context, e AuditEntry, before, after any) error {
	b, err := jsonOrNil(before)
	if err != nil {
		return err
	}
	a, err := jsonOrNil(after)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO audit_log (actor_id, actor_email, brand_id, action, target_type, target_id, before, after, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ActorID, e.ActorEmail, e.BrandID, e.Action, e.TargetType, e.TargetID, b, a, e.IP, e.UserAgent)
	return err
}

func jsonOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, at, actor_id, actor_email, brand_id, action, target_type, target_id, before, after, ip, user_agent
		FROM audit_log ORDER BY at DESC, id DESC LIMIT $1`, limit)
	return all[AuditEntry](rows, err)
}
