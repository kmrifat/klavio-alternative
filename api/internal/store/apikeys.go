package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// --- consent wordings ---

type ConsentWording struct {
	ID        uuid.UUID  `db:"id" json:"id"`
	BrandID   uuid.UUID  `db:"brand_id" json:"-"`
	Label     string     `db:"label" json:"label"`
	Text      string     `db:"text" json:"text"`
	CreatedBy *uuid.UUID `db:"created_by" json:"created_by"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	RetiredAt *time.Time `db:"retired_at" json:"retired_at"`
}

const wordingCols = `id, brand_id, label, text, created_by, created_at, retired_at`

func (s *Store) CreateWording(ctx context.Context, brandID uuid.UUID, label, text string, by uuid.UUID) (ConsentWording, error) {
	rows, err := s.db.Query(ctx, `INSERT INTO consent_wordings (brand_id, label, text, created_by) VALUES ($1, $2, $3, $4)
		RETURNING `+wordingCols, brandID, label, text, by)
	return one[ConsentWording](rows, err)
}

func (s *Store) ListWordings(ctx context.Context, brandID uuid.UUID) ([]ConsentWording, error) {
	rows, err := s.db.Query(ctx, `SELECT `+wordingCols+` FROM consent_wordings WHERE brand_id = $1
		ORDER BY retired_at IS NOT NULL, created_at DESC`, brandID)
	return all[ConsentWording](rows, err)
}

func (s *Store) WordingByID(ctx context.Context, brandID, id uuid.UUID) (ConsentWording, error) {
	rows, err := s.db.Query(ctx, `SELECT `+wordingCols+` FROM consent_wordings WHERE brand_id = $1 AND id = $2`, brandID, id)
	return one[ConsentWording](rows, err)
}

// RetireWording: new sign-ups can't use it any more; past consent keeps pointing at it.
func (s *Store) RetireWording(ctx context.Context, brandID, id uuid.UUID) error {
	return expectOne(s.db.Exec(ctx, `UPDATE consent_wordings SET retired_at = now() WHERE brand_id = $1 AND id = $2 AND retired_at IS NULL`, brandID, id))
}

// --- API keys ---

type APIKey struct {
	ID          uuid.UUID  `db:"id" json:"id"`
	BrandID     uuid.UUID  `db:"brand_id" json:"-"`
	Name        string     `db:"name" json:"name"`
	Prefix      string     `db:"prefix" json:"prefix"`
	KeyHash     []byte     `db:"key_hash" json:"-"`
	Scopes      []string   `db:"scopes" json:"scopes"`
	IPAllowlist []string   `db:"ip_allowlist" json:"ip_allowlist"`
	CreatedBy   *uuid.UUID `db:"created_by" json:"created_by"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	ExpiresAt   time.Time  `db:"expires_at" json:"expires_at"`
	ApprovalID  *uuid.UUID `db:"approval_id" json:"approval_id"`
	Active      bool       `db:"active" json:"active"`
	RevokedAt   *time.Time `db:"revoked_at" json:"revoked_at"`
	RevokedBy   *uuid.UUID `db:"revoked_by" json:"revoked_by"`
	LastUsedAt  *time.Time `db:"last_used_at" json:"last_used_at"`
}

const apiKeyCols = `id, brand_id, name, prefix, key_hash, scopes, ip_allowlist, created_by, created_at, expires_at,
	approval_id, active, revoked_at, revoked_by, last_used_at`

func (s *Store) CreateAPIKey(ctx context.Context, k APIKey) (APIKey, error) {
	rows, err := s.db.Query(ctx, `
		INSERT INTO api_keys (brand_id, name, prefix, key_hash, scopes, ip_allowlist, created_by, created_at, expires_at, approval_id, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+apiKeyCols,
		k.BrandID, k.Name, k.Prefix, k.KeyHash, k.Scopes, k.IPAllowlist, k.CreatedBy, k.CreatedAt, k.ExpiresAt, k.ApprovalID, k.Active)
	return one[APIKey](rows, err)
}

func (s *Store) SetAPIKeyApproval(ctx context.Context, id, approvalID uuid.UUID) error {
	return expectOne(s.db.Exec(ctx, `UPDATE api_keys SET approval_id = $2 WHERE id = $1`, id, approvalID))
}

// ActivateAPIKey: the approval went through (ErrNotFound if revoked meanwhile).
func (s *Store) ActivateAPIKey(ctx context.Context, id uuid.UUID) error {
	return expectOne(s.db.Exec(ctx, `UPDATE api_keys SET active = true WHERE id = $1 AND revoked_at IS NULL`, id))
}

func (s *Store) APIKeyByApproval(ctx context.Context, approvalID uuid.UUID) (APIKey, error) {
	rows, err := s.db.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE approval_id = $1`, approvalID)
	return one[APIKey](rows, err)
}

func (s *Store) APIKeyByID(ctx context.Context, brandID, id uuid.UUID) (APIKey, error) {
	rows, err := s.db.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE brand_id = $1 AND id = $2`, brandID, id)
	return one[APIKey](rows, err)
}

func (s *Store) APIKeyByHash(ctx context.Context, hash []byte) (APIKey, error) {
	rows, err := s.db.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE key_hash = $1`, hash)
	return one[APIKey](rows, err)
}

func (s *Store) ListAPIKeys(ctx context.Context, brandID uuid.UUID) ([]APIKey, error) {
	rows, err := s.db.Query(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE brand_id = $1 ORDER BY created_at DESC`, brandID)
	return all[APIKey](rows, err)
}

func (s *Store) RevokeAPIKey(ctx context.Context, brandID, id uuid.UUID, by *uuid.UUID) error {
	return expectOne(s.db.Exec(ctx, `UPDATE api_keys SET revoked_at = now(), revoked_by = $3, active = false
		WHERE brand_id = $1 AND id = $2 AND revoked_at IS NULL`, brandID, id, by))
}

// TouchAPIKey records use, at most once a minute per key.
func (s *Store) TouchAPIKey(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id)
	return err
}

// --- API call log ---

type APICall struct {
	ID        int64      `db:"id" json:"id"`
	KeyID     *uuid.UUID `db:"key_id" json:"key_id"`
	KeyName   string     `db:"key_name" json:"key_name"`
	BrandID   uuid.UUID  `db:"brand_id" json:"-"`
	At        time.Time  `db:"at" json:"at"`
	Method    string     `db:"method" json:"method"`
	Path      string     `db:"path" json:"path"`
	Status    int        `db:"status" json:"status"`
	IP        string     `db:"ip" json:"ip"`
	EmailMask string     `db:"email_mask" json:"email_mask"`
	ErrorCode string     `db:"error_code" json:"error_code"`
}

func (s *Store) LogAPICall(ctx context.Context, c APICall) error {
	_, err := s.db.Exec(ctx, `INSERT INTO api_calls (key_id, brand_id, method, path, status, ip, email_mask, error_code)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, c.KeyID, c.BrandID, c.Method, c.Path, c.Status, c.IP, c.EmailMask, c.ErrorCode)
	return err
}

func (s *Store) RecentAPICalls(ctx context.Context, brandID uuid.UUID, limit int) ([]APICall, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.id, c.key_id, COALESCE(k.name, '') AS key_name, c.brand_id, c.at, c.method, c.path, c.status, c.ip, c.email_mask, c.error_code
		FROM api_calls c LEFT JOIN api_keys k ON k.id = c.key_id
		WHERE c.brand_id = $1 ORDER BY c.at DESC, c.id DESC LIMIT $2`, brandID, limit)
	return all[APICall](rows, err)
}

// PurgeAPIHousekeeping deletes call logs older than 30 days, idempotency
// records older than a day, and stale MCP sign-ins.
func (s *Store) PurgeAPIHousekeeping(ctx context.Context) (int64, error) {
	a, err := s.db.Exec(ctx, `DELETE FROM api_calls WHERE at < now() - interval '30 days'`)
	if err != nil {
		return 0, err
	}
	b, err := s.db.Exec(ctx, `DELETE FROM api_idempotency WHERE created_at < now() - interval '24 hours'`)
	if err != nil {
		return 0, err
	}
	// MCP sign-ins (OAuth): anyone can start one, so old requests go after
	// a day, and clients nobody connected with in 30 days go too.
	c, err := s.db.Exec(ctx, `DELETE FROM oauth_grants WHERE created_at < now() - interval '1 day'`)
	if err != nil {
		return 0, err
	}
	d, err := s.db.Exec(ctx, `DELETE FROM oauth_clients c WHERE created_at < now() - interval '30 days'
		AND NOT EXISTS (SELECT 1 FROM agent_tokens t WHERE t.oauth_client_id = c.id AND t.revoked_at IS NULL AND t.expires_at > now())
		AND NOT EXISTS (SELECT 1 FROM oauth_grants g WHERE g.client_id = c.id)`)
	return a.RowsAffected() + b.RowsAffected() + c.RowsAffected() + d.RowsAffected(), err
}

// --- idempotency ---

type IdempotentResponse struct {
	RequestHash []byte          `db:"request_hash"`
	Status      int             `db:"status"` // 0 = still running
	Response    json.RawMessage `db:"response"`
}

// IdempotencyStale: an in-progress claim older than this is presumed dead
// (the process stopped mid-request) and can be claimed again.
const IdempotencyStale = 2 * time.Minute

// ClaimIdempotency records that a request with this key is starting. ok is
// false when the key was used before; prev is that earlier request. A stale
// in-progress claim for the same request is taken over.
func (s *Store) ClaimIdempotency(ctx context.Context, keyID uuid.UUID, idemKey string, requestHash []byte) (ok bool, prev IdempotentResponse, err error) {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO api_idempotency (key_id, idem_key, request_hash, status, response) VALUES ($1, $2, $3, 0, 'null')
		ON CONFLICT (key_id, idem_key) DO UPDATE SET created_at = now()
		WHERE api_idempotency.status = 0 AND api_idempotency.request_hash = EXCLUDED.request_hash
			AND api_idempotency.created_at < now() - make_interval(secs => $4)`,
		keyID, idemKey, requestHash, IdempotencyStale.Seconds())
	if err != nil {
		return false, prev, err
	}
	if tag.RowsAffected() == 1 {
		return true, prev, nil
	}
	rows, err := s.db.Query(ctx, `SELECT request_hash, status, response FROM api_idempotency WHERE key_id = $1 AND idem_key = $2`, keyID, idemKey)
	prev, err = one[IdempotentResponse](rows, err)
	return false, prev, err
}

func (s *Store) FinishIdempotency(ctx context.Context, keyID uuid.UUID, idemKey string, status int, response json.RawMessage) error {
	_, err := s.db.Exec(ctx, `UPDATE api_idempotency SET status = $3, response = $4 WHERE key_id = $1 AND idem_key = $2`,
		keyID, idemKey, status, response)
	return err
}

// ReleaseIdempotency forgets a claim whose request failed, so it can be retried.
func (s *Store) ReleaseIdempotency(ctx context.Context, keyID uuid.UUID, idemKey string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM api_idempotency WHERE key_id = $1 AND idem_key = $2 AND status = 0`, keyID, idemKey)
	return err
}
