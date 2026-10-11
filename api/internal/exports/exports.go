// Package exports lets brand admins download contacts with personal data:
// every export needs a second admin's approval, the file is watermarked, and
// only the requester can download it for 24 hours.
package exports

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/anomaly"
	"github.com/kmrifat/klavio-alternative/api/internal/approvals"
	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// DownloadWindow: how long an approved export can be downloaded.
const DownloadWindow = 24 * time.Hour

// GrowthTolerance: an approved export may include this many more contacts
// than requested (or 10%, whichever is more) before it must be asked again.
const GrowthTolerance = 50

type Service struct {
	store     *store.Store
	approvals *approvals.Service
	anomaly   *anomaly.Service
	auth      stepUpper
	log       *slog.Logger
	now       func() time.Time
}

type stepUpper interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

func NewService(st *store.Store, ap *approvals.Service, an *anomaly.Service, au stepUpper, log *slog.Logger) *Service {
	s := &Service{store: st, approvals: ap, anomaly: an, auth: au, log: log, now: time.Now}
	ap.Register(approvals.KindExport, approvals.Definition{
		Label: "Export with personal data", Execute: s.execute, Describe: s.describeLive,
	})
	return s
}

// Filter picks the contacts to export (same as the S3 list filters).
type Filter struct {
	Tiers  []store.Tier        `json:"tiers"`
	Status store.ContactStatus `json:"status"`
	Tag    string              `json:"tag"`
}

func (f Filter) contactFilter() store.ContactFilter {
	return store.ContactFilter{Tiers: f.Tiers, Status: f.Status, Tag: f.Tag}
}

func (f Filter) describe() string {
	var parts []string
	if len(f.Tiers) > 0 {
		t := make([]string, len(f.Tiers))
		for i, x := range f.Tiers {
			t[i] = string(x)
		}
		parts = append(parts, "tiers "+strings.Join(t, "+"))
	}
	if f.Status != "" {
		parts = append(parts, "status "+string(f.Status))
	}
	if f.Tag != "" {
		parts = append(parts, "tag "+f.Tag)
	}
	if len(parts) == 0 {
		return "all contacts"
	}
	return strings.Join(parts, ", ")
}

type payload struct {
	BrandID uuid.UUID `json:"brand_id"`
	Filter  Filter    `json:"filter"`
}

func (s *Service) Routes(mux *http.ServeMux) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.log, f) }
	mux.HandleFunc("POST /admin/v1/brands/{slug}/exports", h(s.request))
	mux.HandleFunc("GET /admin/v1/exports", h(s.mine))
	mux.HandleFunc("GET /admin/v1/exports/{id}/download", h(s.download))
}

// request files the export for approval. Brand admins only.
func (s *Service) request(w http.ResponseWriter, r *http.Request) error {
	b, p, err := brands.WithRole(r, s.store, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Filter Filter `json:"filter"`
		Reason string `json:"reason"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" {
		return httpx.Errorf(http.StatusBadRequest, "reason_required", "say what the export is for — the approver sees it")
	}
	for _, t := range in.Filter.Tiers {
		if !t.Valid() {
			return httpx.Errorf(http.StatusBadRequest, "bad_tier", "unknown tier %q", t)
		}
	}
	ctx := r.Context()

	var exp store.Export
	var frozen bool
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		// One export request per person at a time, so the "3rd in 24 hours"
		// count can't be dodged by sending them all at once.
		if err := tx.Lock(ctx, "exports:"+p.User.ID.String()); err != nil {
			return err
		}
		// A request waiting on the lock may belong to someone frozen meanwhile.
		if u, err := tx.UserByID(ctx, p.User.ID); err != nil {
			return err
		} else if u.FrozenAt != nil {
			return httpx.Errorf(http.StatusForbidden, "account_frozen", "your account was frozen after unusual activity; an owner has been alerted")
		}
		rows, err := tx.CountContacts(ctx, b.ID, in.Filter.contactFilter())
		if err != nil {
			return err
		}
		if rows == 0 {
			return httpx.Errorf(http.StatusBadRequest, "empty_export", "no contacts match these filters")
		}
		recent, err := tx.RecentExportCount(ctx, p.User.ID)
		if err != nil {
			return err
		}
		a, err := s.approvals.Request(ctx, tx, approvals.Request{
			Kind: approvals.KindExport, BrandID: &b.ID, RequestedBy: p.User, Reason: in.Reason,
			Summary: fmt.Sprintf("Export %d %s contacts (%s)", rows, b.Name, in.Filter.describe()),
			Payload: payload{BrandID: b.ID, Filter: in.Filter},
			Audit:   auth.AuditEntry(r, "", "", "", nil),
		})
		if errors.Is(err, approvals.ErrNoApprover) {
			return httpx.Errorf(http.StatusConflict, "no_approver", "exports need a second admin, and nobody else can approve for %s yet", b.Name)
		}
		if err != nil {
			return err
		}
		filter, _ := json.Marshal(in.Filter)
		exp, err = tx.CreateExport(ctx, store.Export{BrandID: b.ID, RequestedBy: p.User.ID, ApprovalID: a.ID, Filter: filter, RowCount: rows})
		if err != nil {
			return err
		}
		frozen, err = s.anomaly.CheckExport(ctx, tx, p.User, b, rows, recent+1)
		return err
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"export": exp, "requester_frozen": frozen})
	return nil
}

// requesterMayExport: still a brand admin of the brand (or owner), not frozen.
func requesterMayExport(ctx context.Context, tx *store.Store, userID, brandID uuid.UUID) (store.User, bool, error) {
	u, err := tx.UserByID(ctx, userID)
	if err != nil {
		return u, false, err
	}
	if u.FrozenAt != nil {
		return u, false, nil
	}
	if u.CompanyRole == store.CompanyOwner {
		return u, true, nil
	}
	roles, err := tx.MembershipsForUser(ctx, userID)
	return u, roles[brandID] == store.RoleBrandAdmin, err
}

// describeLive tells the approver how many contacts match right now.
func (s *Service) describeLive(ctx context.Context, a store.Approval) string {
	exp, err := s.store.ExportByApproval(ctx, a.ID)
	if err != nil {
		return ""
	}
	var f Filter
	if json.Unmarshal(exp.Filter, &f) != nil {
		return ""
	}
	n, err := s.store.CountContacts(ctx, exp.BrandID, f.contactFilter())
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d contacts match these filters now (%d when requested)", n, exp.RowCount)
}

var oneLine = strings.NewReplacer("\r", " ", "\n", " ")

// csvSafe stops spreadsheet formula injection: a cell starting with = + - @
// (or a tab/CR) is prefixed with ' so Excel and Sheets treat it as text.
// So is each part after a ; , | or tab: Excel in many European locales
// splits a CSV on ;, so "x;=cmd" would start a new cell with a formula.
func csvSafe(v string) string {
	var b strings.Builder
	start := true
	for _, r := range v {
		if start && strings.ContainsRune("=+-@\t\r", r) {
			b.WriteByte('\'')
		}
		b.WriteRune(r)
		start = strings.ContainsRune(";,|\t", r)
	}
	return b.String()
}

// execute builds the watermarked CSV when the export is approved.
func (s *Service) execute(ctx context.Context, tx *store.Store, a store.Approval) (any, error) {
	exp, err := tx.ExportByApproval(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	var f Filter
	if err := json.Unmarshal(exp.Filter, &f); err != nil {
		return nil, err
	}
	b, err := tx.BrandByID(ctx, exp.BrandID)
	if err != nil {
		return nil, err
	}
	// The requester must still be allowed to export this brand now.
	requester, ok, err := requesterMayExport(ctx, tx, exp.RequestedBy, exp.BrandID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, approvals.Reject("%s no longer has export rights for %s", requester.Email, b.Name)
	}
	contacts, err := tx.AllContactsForExport(ctx, exp.BrandID, f.contactFilter())
	if err != nil {
		return nil, err
	}
	// The approver agreed to roughly the requested size (shown with the live
	// count). If the list has grown well past it since, ask again. Large
	// exports themselves are allowed: they were flagged (and non-owners
	// frozen) when requested, and an approver chose to approve.
	if grew := len(contacts) - exp.RowCount; grew > max(GrowthTolerance, exp.RowCount/10) {
		return nil, approvals.Reject("the filters now match %d contacts, not the %d that were requested; ask again", len(contacts), exp.RowCount)
	}
	now := s.now()
	var buf bytes.Buffer
	// Watermark: who, what, when — so a leaked file can be traced. One line, always.
	fmt.Fprintf(&buf, "# CONFIDENTIAL — %s contacts (%s). Export %s for %s, approved %s UTC. Personal data: don't share or keep after use.\n",
		oneLine.Replace(b.Name), oneLine.Replace(f.describe()), exp.ID, requester.Email, now.UTC().Format("2006-01-02 15:04"))
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"email", "first_name", "last_name", "status", "tier", "consent", "consent_source", "consented_at", "tags", "export_id"})
	for _, c := range contacts {
		consented := ""
		if c.ConsentedAt != nil {
			consented = c.ConsentedAt.UTC().Format(time.RFC3339)
		}
		_ = cw.Write([]string{
			csvSafe(c.Email), csvSafe(c.FirstName), csvSafe(c.LastName), string(c.Status), string(c.Tier),
			c.ConsentBasis, csvSafe(c.ConsentSource), consented, csvSafe(strings.Join(c.Tags, ";")), exp.ID.String(),
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return nil, err
	}
	if err := tx.SetExportFile(ctx, exp.ID, buf.Bytes(), len(contacts), now, now.Add(DownloadWindow)); err != nil {
		return nil, err
	}
	return map[string]any{"export_id": exp.ID, "rows": len(contacts), "expires_at": now.Add(DownloadWindow)}, nil
}

type mineItem struct {
	store.Export
	BrandName string                `json:"brand_name"`
	Status    string                `json:"status"` // waiting | ready | expired | rejected
	Approval  *store.ApprovalStatus `json:"approval_status"`
}

func (s *Service) mine(w http.ResponseWriter, r *http.Request) error {
	p, err := auth.Require(r)
	if err != nil {
		return err
	}
	list, err := s.store.ExportsBy(r.Context(), p.User.ID)
	if err != nil {
		return err
	}
	brandsList, err := s.store.ListBrands(r.Context())
	if err != nil {
		return err
	}
	names := map[uuid.UUID]string{}
	for _, b := range brandsList {
		names[b.ID] = b.Name
	}
	out := []mineItem{}
	now := s.now()
	for _, e := range list {
		a, err := s.store.ApprovalByID(r.Context(), e.ApprovalID)
		if err != nil {
			return err
		}
		it := mineItem{Export: e, BrandName: names[e.BrandID], Approval: &a.Status}
		switch {
		case a.Status == store.ApprovalPending:
			it.Status = "waiting"
		case a.Status == store.ApprovalApproved && e.HasFile && e.ExpiresAt != nil && e.ExpiresAt.After(now):
			it.Status = "ready"
		case a.Status == store.ApprovalApproved:
			it.Status = "expired"
		default:
			it.Status = string(a.Status)
		}
		out = append(out, it)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"exports": out})
	return nil
}

// download: only the person who asked, within 24 hours of approval. Every
// download is audited.
// download hands over the file, with a passkey confirmation: a stolen
// session alone mustn't be enough to take personal data away.
func (s *Service) download(w http.ResponseWriter, r *http.Request) error {
	p, err := s.auth.RequireStepUp(r)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return httpx.ErrNotFound
	}
	ctx := r.Context()
	exp, err := s.store.ExportByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && exp.RequestedBy != p.User.ID) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	// Still allowed to see this brand's personal data? (Rights can change after approval.)
	if _, ok, err := requesterMayExport(ctx, s.store, p.User.ID, exp.BrandID); err != nil {
		return err
	} else if !ok {
		return httpx.Errorf(http.StatusForbidden, "export_forbidden", "you no longer have export rights for this brand")
	}
	file, err := s.store.TakeExportFile(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.Errorf(http.StatusGone, "export_unavailable", "this export isn't approved yet or its 24-hour download window has passed")
	}
	if err != nil {
		return err
	}
	if err := s.store.Audit(ctx, auth.AuditEntry(r, "export.downloaded", "export", id.String(), &exp.BrandID), nil,
		map[string]any{"rows": exp.RowCount}); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="contacts-export-%s.csv"`, id.String()[:8]))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(file)
	return nil
}
