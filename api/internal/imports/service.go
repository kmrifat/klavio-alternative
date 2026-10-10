package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// Runner starts the slow steps in the background (river jobs in production,
// direct calls in tests).
type Runner interface {
	StartCheck(ctx context.Context, brandID, importID uuid.UUID) error
	StartCommit(ctx context.Context, brandID, importID uuid.UUID) error
}

type Service struct {
	store    *store.Store
	resolver MXResolver
	runner   Runner
	log      *slog.Logger
	now      func() time.Time
}

func NewService(st *store.Store, resolver MXResolver, log *slog.Logger) *Service {
	return &Service{store: st, resolver: resolver, log: log, now: time.Now}
}

// SetRunner is called once the job client exists (it needs the service first).
func (s *Service) SetRunner(r Runner) { s.runner = r }

// Upload parses the file and stores it with a suggested column mapping.
func (s *Service) Upload(ctx context.Context, brandID, by uuid.UUID, filename string, data []byte) (store.Import, error) {
	t, err := Parse(data)
	if err != nil {
		return store.Import{}, httpx.Errorf(http.StatusBadRequest, "bad_file", "%v", err)
	}
	return s.store.CreateImport(ctx, brandID, by, filename, data, len(t.Rows), t.Headers, SuggestMapping(t.Headers))
}

// Preview returns the first rows so the person can see the column mapping is right.
func (s *Service) Preview(ctx context.Context, imp store.Import, n int) ([][]string, error) {
	data, err := s.store.ImportFile(ctx, imp.ID)
	if err != nil || data == nil {
		return [][]string{}, err
	}
	t, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return t.Rows[:min(n, len(t.Rows))], nil
}

type SetupInput struct {
	Mapping  Mapping     `json:"mapping"`
	Consent  Consent     `json:"consent"`
	TopicIDs []uuid.UUID `json:"topic_ids"`
}

// Setup saves the mapping and consent answers. Ticking the attestation records
// who signed it and when.
func (s *Service) Setup(ctx context.Context, imp store.Import, by store.User, in SetupInput) error {
	if imp.Status != store.ImportUploaded && imp.Status != store.ImportChecked && imp.Status != store.ImportFailed {
		return httpx.Errorf(http.StatusConflict, "import_locked", "this import is %s and can't be changed", imp.Status)
	}
	if err := in.Mapping.Validate(imp.Headers); err != nil {
		return httpx.Errorf(http.StatusBadRequest, "bad_mapping", "%v", err)
	}
	if len(in.TopicIDs) == 0 {
		return httpx.Errorf(http.StatusBadRequest, "no_topics", "choose at least one topic these people agreed to")
	}
	topics, err := s.store.ListTopics(ctx, imp.BrandID)
	if err != nil {
		return err
	}
	for _, id := range in.TopicIDs {
		if !slices.ContainsFunc(topics, func(t store.Topic) bool { return t.ID == id }) {
			return httpx.Errorf(http.StatusBadRequest, "unknown_topic", "topic %s doesn't belong to this brand", id)
		}
	}
	in.Consent.AttestedBy, in.Consent.AttestedByEmail, in.Consent.AttestedAt = nil, "", nil
	if in.Consent.Attested {
		now := s.now()
		in.Consent.AttestedBy, in.Consent.AttestedByEmail, in.Consent.AttestedAt = &by.ID, by.Email, &now
	}
	return s.store.UpdateImportSetup(ctx, imp.ID, in.Mapping, in.Consent, in.TopicIDs)
}

// RequestCheck queues the safety check.
func (s *Service) RequestCheck(ctx context.Context, imp store.Import) error {
	var m Mapping
	if err := json.Unmarshal(imp.Mapping, &m); err != nil || m.Validate(imp.Headers) != nil {
		return httpx.Errorf(http.StatusBadRequest, "bad_mapping", "match the email column first")
	}
	err := s.store.TransitionImport(ctx, imp.ID, []store.ImportStatus{store.ImportUploaded, store.ImportChecked, store.ImportFailed}, store.ImportChecking)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.Errorf(http.StatusConflict, "import_busy", "this import is already being checked or imported")
	}
	if err != nil {
		return err
	}
	return s.runner.StartCheck(ctx, imp.BrandID, imp.ID)
}

// RequestCommit queues the import itself, if the last check passed.
func (s *Service) RequestCommit(ctx context.Context, imp store.Import) error {
	if imp.Status != store.ImportChecked || imp.CheckResult == nil {
		return httpx.Errorf(http.StatusConflict, "not_checked", "run the safety check first")
	}
	var res CheckResult
	if err := json.Unmarshal(imp.CheckResult, &res); err != nil {
		return err
	}
	if !res.Gate.Passed {
		return httpx.Errorf(http.StatusConflict, "import_gate_failed", "this import didn't pass the safety check; an admin review path arrives with Approvals (M5)")
	}
	err := s.store.TransitionImport(ctx, imp.ID, []store.ImportStatus{store.ImportChecked}, store.ImportImporting)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.Errorf(http.StatusConflict, "import_busy", "this import is already running")
	}
	if err != nil {
		return err
	}
	return s.runner.StartCommit(ctx, imp.BrandID, imp.ID)
}

type loaded struct {
	imp     store.Import
	mapping Mapping
	consent Consent
	rows    []Row
}

func (s *Service) load(ctx context.Context, brandID, id uuid.UUID) (loaded, error) {
	var l loaded
	imp, err := s.store.ImportByID(ctx, brandID, id)
	if err != nil {
		return l, err
	}
	data, err := s.store.ImportFile(ctx, id)
	if err != nil {
		return l, err
	}
	if data == nil {
		return l, errors.New("the uploaded file is gone")
	}
	t, err := Parse(data)
	if err != nil {
		return l, err
	}
	l.imp = imp
	if err := json.Unmarshal(imp.Mapping, &l.mapping); err != nil {
		return l, err
	}
	if err := json.Unmarshal(imp.Consent, &l.consent); err != nil {
		return l, err
	}
	l.rows = Rows(t, l.mapping)
	return l, nil
}

// facts looks up suppressions and existing contacts for the file's addresses.
func (s *Service) facts(ctx context.Context, brandID uuid.UUID, rows []Row) (Facts, error) {
	f := Facts{Suppressed: map[string]bool{}, Existing: map[string]store.Contact{}, NoMailbox: map[string]bool{}, Now: s.now()}
	var emails []string
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Email != "" && !seen[r.Email] {
			seen[r.Email] = true
			emails = append(emails, r.Email)
		}
	}
	for chunk := range slices.Chunk(emails, 2000) {
		hashes := make([][]byte, len(chunk))
		for i, e := range chunk {
			hashes[i] = store.EmailHash(e)
		}
		sup, err := s.store.SuppressedHashes(ctx, hashes)
		if err != nil {
			return f, err
		}
		for _, e := range chunk {
			if sup[string(store.EmailHash(e))] {
				f.Suppressed[e] = true
			}
		}
		existing, err := s.store.ExistingContacts(ctx, brandID, chunk)
		if err != nil {
			return f, err
		}
		for e, c := range existing {
			f.Existing[e] = c
		}
	}
	return f, nil
}

// RunCheck is the background safety check.
func (s *Service) RunCheck(ctx context.Context, brandID, id uuid.UUID) error {
	err := s.runCheck(ctx, brandID, id)
	if err != nil {
		s.log.ErrorContext(ctx, "import check failed", "import", id, "err", err)
		return s.store.FailImport(ctx, id, "The safety check failed: "+err.Error())
	}
	return nil
}

func (s *Service) runCheck(ctx context.Context, brandID, id uuid.UUID) error {
	l, err := s.load(ctx, brandID, id)
	if err != nil {
		return err
	}
	f, err := s.facts(ctx, brandID, l.rows)
	if err != nil {
		return err
	}

	// Only look up domains that could still be imported.
	Classify(l.rows, f)
	var domains []string
	seen := map[string]bool{}
	for _, r := range l.rows {
		if d := domainOf(r.Email); importable(r.Outcome) && !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	noMailbox := DomainsWithoutMailbox(ctx, s.resolver, domains)
	for _, d := range noMailbox {
		f.NoMailbox[d] = true
	}
	Classify(l.rows, f)

	res := Summarize(l.rows, l.consent, l.mapping, noMailbox, s.now())
	return s.store.SetImportCheckResult(ctx, id, res)
}

// ImportResult is stored when the import finishes.
type ImportResult struct {
	Created      int       `json:"created"`
	Quarantined  int       `json:"quarantined"`
	Updated      int       `json:"updated"`
	Unsubscribed int       `json:"left_unsubscribed"`
	Suppressed   int       `json:"left_suppressed"`
	Rejected     int       `json:"rejected"`
	FinishedAt   time.Time `json:"finished_at"`
}

// RunCommit imports in one transaction: all rows or none. It re-reads
// suppressions and contacts, since they may have changed since the check.
func (s *Service) RunCommit(ctx context.Context, brandID, id uuid.UUID) error {
	err := s.runCommit(ctx, brandID, id)
	if err != nil {
		s.log.ErrorContext(ctx, "import failed", "import", id, "err", err)
		return s.store.FailImport(ctx, id, "The import failed and nothing was imported: "+err.Error())
	}
	return nil
}

func (s *Service) runCommit(ctx context.Context, brandID, id uuid.UUID) error {
	l, err := s.load(ctx, brandID, id)
	if err != nil {
		return err
	}
	var check CheckResult
	if err := json.Unmarshal(l.imp.CheckResult, &check); err != nil {
		return err
	}
	if len(l.consent.Problems()) > 0 {
		return errGateFailed
	}
	f, err := s.facts(ctx, brandID, l.rows)
	if err != nil {
		return err
	}
	for _, d := range check.NoMailboxDomains {
		f.NoMailbox[d] = true
	}
	Classify(l.rows, f)

	importedAt := s.now()
	res := ImportResult{FinishedAt: importedAt}
	source := "Import: " + l.consent.Label()
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		for _, r := range l.rows {
			switch r.Outcome {
			case Ready, Quarantine:
				status := store.ContactSubscribed
				if r.Outcome == Quarantine {
					status = store.ContactQuarantined
				}
				cid, err := tx.InsertContact(ctx, store.NewContact{
					BrandID: brandID, Email: r.Email, FirstName: r.FirstName, LastName: r.LastName, Status: status,
					ConsentBasis: l.consent.basis(), ConsentSource: source, ConsentedAt: r.ConsentedAt,
					LastClickAt: r.LastEngagedAt, EngagedAt: engagedAt(r, importedAt), TrackingAllowed: true,
					Tags: r.Tags, Timezone: r.Timezone, Country: r.Country, ImportID: &id,
				})
				if err != nil {
					return fmt.Errorf("line %d: %w", r.Line, err)
				}
				if err := tx.AddContactTopics(ctx, cid, l.imp.TopicIDs); err != nil {
					return err
				}
				occurred := s.now()
				if r.ConsentedAt != nil {
					occurred = *r.ConsentedAt
				}
				if err := tx.AddConsentRecord(ctx, store.NewConsentRecord{
					BrandID: brandID, ContactID: cid, Email: r.Email, Kind: "import_attested", Method: "import",
					Source: source + " · collected " + l.consent.CollectedPeriod, Wording: l.consent.Wording,
					OccurredAt: occurred, RecordedBy: l.consent.AttestedBy, ImportID: &id,
				}); err != nil {
					return err
				}
				if r.Outcome == Ready {
					res.Created++
				} else {
					res.Quarantined++
				}
			case Existing:
				if err := tx.MergeImportedFields(ctx, r.ExistingID, store.NewContact{
					FirstName: r.FirstName, LastName: r.LastName, Tags: r.Tags, LastClickAt: r.LastEngagedAt,
					EngagedAt: engagedAt(r, importedAt), Timezone: r.Timezone, Country: r.Country,
				}); err != nil {
					return err
				}
				res.Updated++
			case Unsubscribed:
				res.Unsubscribed++
			case Suppressed:
				res.Suppressed++
			default:
				res.Rejected++
			}
		}
		if err := tx.FinishImport(ctx, id, res); err != nil {
			return err
		}
		return tx.Audit(ctx, store.AuditEntry{
			ActorID: l.consent.AttestedBy, ActorEmail: l.consent.AttestedByEmail, BrandID: &brandID,
			Action: "import.finished", TargetType: "import", TargetID: l.imp.Filename,
		}, nil, res)
	})
	return err
}

// engagedAt: a 4–5 star rating counts as activity at the moment of import,
// then ages like a click.
func engagedAt(r Row, importedAt time.Time) *time.Time {
	if r.Engaged() {
		return &importedAt
	}
	return nil
}
