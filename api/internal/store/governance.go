package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// --- approvals ---

type ApprovalStatus string

const (
	ApprovalPending   ApprovalStatus = "pending"
	ApprovalApproved  ApprovalStatus = "approved"
	ApprovalRejected  ApprovalStatus = "rejected"
	ApprovalCancelled ApprovalStatus = "cancelled"
	ApprovalExpired   ApprovalStatus = "expired"
)

type Approval struct {
	ID           uuid.UUID       `db:"id" json:"id"`
	Kind         string          `db:"kind" json:"kind"`
	BrandID      *uuid.UUID      `db:"brand_id" json:"brand_id"`
	RequestedBy  uuid.UUID       `db:"requested_by" json:"requested_by"`
	RequestedAt  time.Time       `db:"requested_at" json:"requested_at"`
	Summary      string          `db:"summary" json:"summary"`
	Reason       string          `db:"reason" json:"reason"`
	Payload      json.RawMessage `db:"payload" json:"payload"`
	Status       ApprovalStatus  `db:"status" json:"status"`
	DecidedBy    *uuid.UUID      `db:"decided_by" json:"decided_by"`
	DecidedAt    *time.Time      `db:"decided_at" json:"decided_at"`
	DecisionNote string          `db:"decision_note" json:"decision_note"`
	Result       json.RawMessage `db:"result" json:"result"`
	ExpiresAt    time.Time       `db:"expires_at" json:"expires_at"`
	TargetUserID *uuid.UUID      `db:"target_user_id" json:"target_user_id"`
	TargetEmail  *string         `db:"target_email" json:"target_email"`
}

const approvalCols = `id, kind, brand_id, requested_by, requested_at, summary, reason, payload, status,
	decided_by, decided_at, decision_note, result, expires_at, target_user_id, target_email`

func (s *Store) CreateApproval(ctx context.Context, a Approval) (Approval, error) {
	rows, err := s.db.Query(ctx, `
		INSERT INTO approvals (kind, brand_id, requested_by, summary, reason, payload, expires_at, target_user_id, target_email)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+approvalCols,
		a.Kind, a.BrandID, a.RequestedBy, a.Summary, a.Reason, a.Payload, a.ExpiresAt, a.TargetUserID, a.TargetEmail)
	return one[Approval](rows, err)
}

func (s *Store) ApprovalByID(ctx context.Context, id uuid.UUID) (Approval, error) {
	rows, err := s.db.Query(ctx, `SELECT `+approvalCols+` FROM approvals WHERE id = $1`, id)
	return one[Approval](rows, err)
}

// LockApproval loads an approval and locks it, so two admins can't decide at once.
func (s *Store) LockApproval(ctx context.Context, id uuid.UUID) (Approval, error) {
	rows, err := s.db.Query(ctx, `SELECT `+approvalCols+` FROM approvals WHERE id = $1 FOR UPDATE`, id)
	return one[Approval](rows, err)
}

// DecideApproval closes a pending approval. ErrNotFound if it wasn't pending.
func (s *Store) DecideApproval(ctx context.Context, id uuid.UUID, status ApprovalStatus, by *uuid.UUID, note string, result any) error {
	r, err := jsonOrNil(result)
	if err != nil {
		return err
	}
	return expectOne(s.db.Exec(ctx, `
		UPDATE approvals SET status = $2, decided_by = $3, decided_at = now(), decision_note = $4, result = $5
		WHERE id = $1 AND status = 'pending'`, id, status, by, note, r))
}

type ApprovalFilter struct {
	Status      ApprovalStatus // "" = any
	RequestedBy *uuid.UUID
	// Decidable narrows to live (pending, unexpired) requests by someone else;
	// BrandIDs, when set, keeps only those brands' requests.
	DecidableBy *uuid.UUID
	BrandIDs    []uuid.UUID
	Limit       int
}

func (s *Store) ListApprovals(ctx context.Context, f ApprovalFilter) ([]Approval, error) {
	where, args := []string{"true"}, []any{}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if f.Status != "" {
		where = append(where, "status = "+arg(f.Status))
	}
	if f.RequestedBy != nil {
		where = append(where, "requested_by = "+arg(*f.RequestedBy))
	}
	if f.DecidableBy != nil {
		where = append(where, "status = 'pending' AND expires_at > now() AND requested_by <> "+arg(*f.DecidableBy))
	}
	if f.BrandIDs != nil {
		where = append(where, "brand_id = ANY("+arg(f.BrandIDs)+")")
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+approvalCols+` FROM approvals WHERE `+strings.Join(where, " AND ")+
		` ORDER BY requested_at DESC LIMIT `+arg(f.Limit), args...)
	return all[Approval](rows, err)
}

// OverdueApprovals lists pending requests past their deadline.
func (s *Store) OverdueApprovals(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM approvals WHERE status = 'pending' AND expires_at <= now() LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TwoAdminsSince: when the company first had two admins (nil = never).
func (s *Store) TwoAdminsSince(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := s.db.QueryRow(ctx, `SELECT two_admins_since FROM company_state WHERE id = 1`).Scan(&t)
	return t, translate(err)
}

// HadTwoAdmins: has the company ever had two owners/approvers? (one-way flag)
func (s *Store) HadTwoAdmins(ctx context.Context) (bool, error) {
	var had bool
	err := s.db.QueryRow(ctx, `SELECT two_admins_since IS NOT NULL FROM company_state WHERE id = 1`).Scan(&had)
	return had, translate(err)
}

// OtherAdminsExcluding counts unfrozen owners/approvers other than the given users.
func (s *Store) OtherAdminsExcluding(ctx context.Context, except ...uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users
		WHERE company_role IN ('owner', 'approver') AND frozen_at IS NULL AND NOT (id = ANY($1))`, except).Scan(&n)
	return n, err
}

// Lock serialises work on a named resource until the transaction ends
// (Postgres advisory lock). Only meaningful inside InTx.
func (s *Store) Lock(ctx context.Context, name string) error {
	_, err := s.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, name)
	return err
}

// LimitValue returns a limit's stored value (ok=false: still the default).
// TryLock is Lock without waiting: false when someone else holds it.
func (s *Store) TryLock(ctx context.Context, name string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, name).Scan(&ok)
	return ok, err
}

func (s *Store) LimitValue(ctx context.Context, key string) (float64, bool, error) {
	var v float64
	err := s.db.QueryRow(ctx, `SELECT value::float8 FROM safety_limits WHERE key = $1`, key).Scan(&v)
	if translate(err) == ErrNotFound {
		return 0, false, nil
	}
	return v, err == nil, err
}

// OtherAdmins counts unfrozen owners/company approvers other than the given
// user: is there anyone who could approve their request?
func (s *Store) OtherAdmins(ctx context.Context, except uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users
		WHERE id <> $1 AND company_role IN ('owner', 'approver') AND frozen_at IS NULL`, except).Scan(&n)
	return n, err
}

// OtherBrandAdmins counts unfrozen brand admins of the brand, other than the given user.
func (s *Store) OtherBrandAdmins(ctx context.Context, brandID, except uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.brand_id = $1 AND m.role = 'brand_admin' AND u.id <> $2 AND u.frozen_at IS NULL`, brandID, except).Scan(&n)
	return n, err
}

// --- safety limits ---

type LimitOverride struct {
	Key       string     `db:"key"`
	Value     float64    `db:"value"`
	UpdatedBy *uuid.UUID `db:"updated_by"`
	UpdatedAt time.Time  `db:"updated_at"`
}

func (s *Store) LimitOverrides(ctx context.Context) (map[string]LimitOverride, error) {
	rows, err := s.db.Query(ctx, `SELECT key, value::float8 AS value, updated_by, updated_at FROM safety_limits`)
	list, err := all[LimitOverride](rows, err)
	if err != nil {
		return nil, err
	}
	out := make(map[string]LimitOverride, len(list))
	for _, l := range list {
		out[l.Key] = l
	}
	return out, nil
}

func (s *Store) SetLimit(ctx context.Context, key string, value float64, by uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO safety_limits (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		key, value, by)
	return err
}

// --- exports ---

type Export struct {
	ID            uuid.UUID       `db:"id" json:"id"`
	BrandID       uuid.UUID       `db:"brand_id" json:"brand_id"`
	RequestedBy   uuid.UUID       `db:"requested_by" json:"requested_by"`
	ApprovalID    uuid.UUID       `db:"approval_id" json:"approval_id"`
	Filter        json.RawMessage `db:"filter" json:"filter"`
	RowCount      int             `db:"row_count" json:"row_count"`
	HasFile       bool            `db:"has_file" json:"has_file"`
	ReadyAt       *time.Time      `db:"ready_at" json:"ready_at"`
	ExpiresAt     *time.Time      `db:"expires_at" json:"expires_at"`
	DownloadCount int             `db:"download_count" json:"download_count"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
}

const exportCols = `id, brand_id, requested_by, approval_id, filter, row_count, file IS NOT NULL AS has_file,
	ready_at, expires_at, download_count, created_at`

func (s *Store) CreateExport(ctx context.Context, e Export) (Export, error) {
	rows, err := s.db.Query(ctx, `
		INSERT INTO exports (brand_id, requested_by, approval_id, filter, row_count) VALUES ($1, $2, $3, $4, $5)
		RETURNING `+exportCols, e.BrandID, e.RequestedBy, e.ApprovalID, e.Filter, e.RowCount)
	return one[Export](rows, err)
}

func (s *Store) ExportByApproval(ctx context.Context, approvalID uuid.UUID) (Export, error) {
	rows, err := s.db.Query(ctx, `SELECT `+exportCols+` FROM exports WHERE approval_id = $1`, approvalID)
	return one[Export](rows, err)
}

func (s *Store) ExportByID(ctx context.Context, id uuid.UUID) (Export, error) {
	rows, err := s.db.Query(ctx, `SELECT `+exportCols+` FROM exports WHERE id = $1`, id)
	return one[Export](rows, err)
}

func (s *Store) ExportsBy(ctx context.Context, userID uuid.UUID) ([]Export, error) {
	rows, err := s.db.Query(ctx, `SELECT `+exportCols+` FROM exports WHERE requested_by = $1 ORDER BY created_at DESC LIMIT 50`, userID)
	return all[Export](rows, err)
}

// RecentExportCount: export requests by the user in the last 24 hours.
func (s *Store) RecentExportCount(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM exports WHERE requested_by = $1 AND created_at > now() - interval '24 hours'`, userID).Scan(&n)
	return n, err
}

func (s *Store) SetExportFile(ctx context.Context, id uuid.UUID, file []byte, rows int, readyAt, expiresAt time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE exports SET file = $2, row_count = $3, ready_at = $4, expires_at = $5 WHERE id = $1`,
		id, file, rows, readyAt, expiresAt)
	return err
}

// TakeExportFile returns the file for a download and counts it; ErrNotFound if gone or expired.
func (s *Store) TakeExportFile(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var f []byte
	err := s.db.QueryRow(ctx, `
		UPDATE exports SET download_count = download_count + 1
		WHERE id = $1 AND file IS NOT NULL AND expires_at > now() RETURNING file`, id).Scan(&f)
	return f, translate(err)
}

// PurgeExpiredExports deletes the personal data of expired exports.
func (s *Store) PurgeExpiredExports(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE exports SET file = NULL WHERE file IS NOT NULL AND expires_at <= now()`)
	return tag.RowsAffected(), err
}

// AllContactsForExport streams every contact matching the filter (no paging).
func (s *Store) AllContactsForExport(ctx context.Context, brandID uuid.UUID, f ContactFilter) ([]Contact, error) {
	var out []Contact
	f.After, f.Limit = "", 200
	for {
		page, err := s.ListContacts(ctx, brandID, f)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < f.Limit {
			return out, nil
		}
		f.After = page[len(page)-1].Email
	}
}

// --- sign-ins, freezing, alerts ---

type SignIn struct {
	At      time.Time `db:"at"`
	Country string    `db:"country"`
	Device  string    `db:"device"`
}

func (s *Store) RecentSignIns(ctx context.Context, userID uuid.UUID, since time.Time) ([]SignIn, error) {
	rows, err := s.db.Query(ctx, `SELECT at, country, device FROM sign_ins WHERE user_id = $1 AND at > $2 ORDER BY at DESC`, userID, since)
	return all[SignIn](rows, err)
}

func (s *Store) RecordSignIn(ctx context.Context, userID uuid.UUID, ip, country, device string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO sign_ins (user_id, ip, country, device) VALUES ($1, $2, $3, $4)`, userID, ip, country, device)
	return err
}

// Freeze blocks sign-in and ends every session of the user.
func (s *Store) Freeze(ctx context.Context, userID uuid.UUID, reason string) error {
	if _, err := s.db.Exec(ctx, `UPDATE users SET frozen_at = now(), frozen_reason = $2 WHERE id = $1 AND frozen_at IS NULL`, userID, reason); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

func (s *Store) Unfreeze(ctx context.Context, userID uuid.UUID) error {
	return expectOne(s.db.Exec(ctx, `UPDATE users SET frozen_at = NULL, frozen_reason = '' WHERE id = $1 AND frozen_at IS NOT NULL`, userID))
}

type Alert struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	Kind           string          `db:"kind" json:"kind"`
	Severity       string          `db:"severity" json:"severity"`
	UserID         *uuid.UUID      `db:"user_id" json:"user_id"`
	BrandID        *uuid.UUID      `db:"brand_id" json:"brand_id"`
	Message        string          `db:"message" json:"message"`
	Details        json.RawMessage `db:"details" json:"details"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	AcknowledgedBy *uuid.UUID      `db:"acknowledged_by" json:"acknowledged_by"`
	AcknowledgedAt *time.Time      `db:"acknowledged_at" json:"acknowledged_at"`
}

const alertCols = `id, kind, severity, user_id, brand_id, message, details, created_at, acknowledged_by, acknowledged_at`

func (s *Store) CreateAlert(ctx context.Context, a Alert, details any) error {
	d, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO alerts (kind, severity, user_id, brand_id, message, details) VALUES ($1, $2, $3, $4, $5, $6)`,
		a.Kind, a.Severity, a.UserID, a.BrandID, a.Message, d)
	return err
}

func (s *Store) ListAlerts(ctx context.Context, openOnly bool, limit int) ([]Alert, error) {
	q := `SELECT ` + alertCols + ` FROM alerts`
	if openOnly {
		q += ` WHERE acknowledged_at IS NULL`
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY created_at DESC LIMIT $1`, limit)
	return all[Alert](rows, err)
}

func (s *Store) AcknowledgeAlert(ctx context.Context, id, by uuid.UUID) error {
	return expectOne(s.db.Exec(ctx, `UPDATE alerts SET acknowledged_by = $2, acknowledged_at = now() WHERE id = $1 AND acknowledged_at IS NULL`, id, by))
}
