package campaigns

import (
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/auth"
	"github.com/kmrifat/klavio-alternative/api/internal/brands"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
	"github.com/kmrifat/klavio-alternative/api/internal/templates"
)

type authorizer interface {
	RequireStepUp(r *http.Request) (*auth.Principal, error)
}

// Who may do what:
//   - anyone who can see the brand: read campaigns
//   - editor: create and edit drafts, send tests to the seed list
//   - sender: schedule (passkey step-up), cancel, pause, resume (step-up)
//   - brand admin (step-up): the seed list and who email comes from
func (s *Service) Routes(mux *http.ServeMux, az authorizer) {
	h := func(f httpx.HandlerFunc) http.HandlerFunc { return httpx.Handle(s.log, f) }
	withAZ := func(f func(http.ResponseWriter, *http.Request, authorizer) error) http.HandlerFunc {
		return h(func(w http.ResponseWriter, r *http.Request) error { return f(w, r, az) })
	}
	base := "/admin/v1/brands/{slug}"

	mux.HandleFunc("GET "+base+"/campaigns", h(s.list))
	mux.HandleFunc("POST "+base+"/campaigns", h(s.create))
	mux.HandleFunc("GET "+base+"/campaigns/{id}", h(s.get))
	mux.HandleFunc("PUT "+base+"/campaigns/{id}", h(s.update))
	mux.HandleFunc("GET "+base+"/campaigns/{id}/review", h(s.review))
	mux.HandleFunc("POST "+base+"/campaigns/{id}/schedule", withAZ(s.schedule))
	mux.HandleFunc("POST "+base+"/campaigns/{id}/agent-send", h(s.agentSend))
	mux.HandleFunc("POST "+base+"/campaigns/{id}/cancel", h(s.cancel))
	mux.HandleFunc("POST "+base+"/campaigns/{id}/pause", h(s.pause))
	mux.HandleFunc("POST "+base+"/campaigns/{id}/resume", withAZ(s.resume))
	mux.HandleFunc("POST "+base+"/campaigns/{id}/test", h(s.test))
	mux.HandleFunc("GET "+base+"/campaigns/{id}/monitor", h(s.monitorHandler))
	mux.HandleFunc("GET "+base+"/campaigns/{id}/report", h(s.reportHandler))

	mux.HandleFunc("GET "+base+"/seeds", h(s.listSeeds))
	mux.HandleFunc("POST "+base+"/seeds", withAZ(s.addSeed))
	mux.HandleFunc("DELETE "+base+"/seeds/{email}", withAZ(s.removeSeed))
	mux.HandleFunc("GET "+base+"/sender", h(s.getSender))
	mux.HandleFunc("PUT "+base+"/sender", withAZ(s.putSender))
}

// visibleBrand loads the {slug} brand (404 for brands the user can't see).
func (s *Service) visibleBrand(r *http.Request) (store.Brand, *auth.Principal, error) {
	return brands.Visible(r, s.store)
}

// brandWithRole adds a minimum role. Company approvers see every brand and
// count as viewers, so they can read the campaigns they're asked to approve.
func (s *Service) brandWithRole(r *http.Request, min store.BrandRole) (store.Brand, *auth.Principal, error) {
	return brands.WithRole(r, s.store, min)
}

// campaign loads {id} of the brand, with a minimum role.
func (s *Service) campaign(r *http.Request, min store.BrandRole) (store.Brand, *auth.Principal, store.Campaign, error) {
	b, p, err := s.brandWithRole(r, min)
	if err != nil {
		return b, p, store.Campaign{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return b, p, store.Campaign{}, httpx.ErrNotFound
	}
	c, err := s.store.CampaignByID(r.Context(), b.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return b, p, c, httpx.ErrNotFound
	}
	return b, p, c, err
}

func bad(code, format string, args ...any) error {
	return httpx.Errorf(http.StatusBadRequest, code, format, args...)
}

func conflict(code, format string, args ...any) error {
	return httpx.Errorf(http.StatusConflict, code, format, args...)
}

// campaignError maps service errors to HTTP answers.
func campaignError(w http.ResponseWriter, err error, rv *Review) error {
	var pe *ProblemsError
	switch {
	case errors.As(err, &pe):
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    map[string]string{"code": "not_ready", "message": "the campaign can't be scheduled yet"},
			"problems": pe.Problems,
		})
		return nil
	case errors.Is(err, ErrConfirmMismatch):
		msg := "type the number of people exactly as shown"
		if rv != nil {
			msg = "the audience is now " + rv.ConfirmText + " people; check it and type that number"
		}
		return conflict("confirm_mismatch", "%s", msg)
	case errors.Is(err, ErrNeedsApproval):
		reasons := []string{}
		if rv != nil {
			for _, r := range rv.ApprovalReasons {
				reasons = append(reasons, r.Text())
			}
		}
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{"code": "approval_required",
				"message": "this send needs a second person's approval, and nobody else can approve it yet"},
			"reasons": reasons,
		})
		return nil
	case errors.Is(err, store.ErrStale):
		return conflict("stale", "someone else changed this campaign; reload to see it")
	case errors.Is(err, ErrNotEditable):
		return conflict("not_editable", "the campaign has been scheduled; cancel it to make changes")
	case errors.Is(err, ErrFinished):
		return conflict("finished", "the campaign has already finished")
	case errors.Is(err, ErrNotRunning):
		return conflict("not_running", "the campaign isn't in a state that allows that")
	}
	return err
}

// --- campaigns ---

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.visibleBrand(r)
	if err != nil {
		return err
	}
	list, err := s.store.ListCampaigns(r.Context(), b.ID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []store.Campaign{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"campaigns": list})
	return nil
}

type draftInput struct {
	Name          string     `json:"name"`
	TopicID       uuid.UUID  `json:"topic_id"`
	TemplateID    *uuid.UUID `json:"template_id"`
	Subject       string     `json:"subject"`
	Preheader     string     `json:"preheader"`
	IncludeAtRisk bool       `json:"include_at_risk"`
	Version       int        `json:"version"`
}

// validate checks a draft and that its topic and template are the brand's.
func (s *Service) validate(r *http.Request, b store.Brand, in *draftInput) (store.CampaignDraftInput, error) {
	in.Name, in.Subject, in.Preheader = strings.TrimSpace(in.Name), oneLine(in.Subject), oneLine(in.Preheader)
	switch {
	case in.Name == "":
		return store.CampaignDraftInput{}, bad("invalid_name", "give the campaign a name")
	case len([]rune(in.Name)) > 200:
		return store.CampaignDraftInput{}, bad("invalid_name", "the name is too long (200 characters at most)")
	case len([]rune(in.Subject)) > 250 || len([]rune(in.Preheader)) > 250:
		return store.CampaignDraftInput{}, bad("too_long", "keep the subject and preview text under 250 characters")
	}
	if err := checkSubjectTags(in.Subject); err != nil {
		return store.CampaignDraftInput{}, err
	}
	topics, err := s.store.ListTopics(r.Context(), b.ID)
	if err != nil {
		return store.CampaignDraftInput{}, err
	}
	if !slices.ContainsFunc(topics, func(t store.Topic) bool { return t.ID == in.TopicID }) {
		return store.CampaignDraftInput{}, bad("invalid_topic", "choose one of this brand's topics")
	}
	if in.TemplateID != nil {
		if _, err := s.store.TemplateByID(r.Context(), b.ID, *in.TemplateID); errors.Is(err, store.ErrNotFound) {
			return store.CampaignDraftInput{}, bad("invalid_template", "choose one of this brand's templates")
		} else if err != nil {
			return store.CampaignDraftInput{}, err
		}
	}
	return store.CampaignDraftInput{
		Name: in.Name, TopicID: in.TopicID, TemplateID: in.TemplateID, Subject: in.Subject,
		Preheader: in.Preheader, IncludeAtRisk: in.IncludeAtRisk,
	}, nil
}

var subjectTagRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)`)

// checkSubjectTags: only personal and brand merge tags belong in a subject.
func checkSubjectTags(subject string) error {
	for _, m := range subjectTagRe.FindAllStringSubmatch(subject, -1) {
		ok := false
		for _, t := range templates.Tags {
			if t.Name == m[1] && !t.URL {
				ok = true
			}
		}
		if !ok {
			return bad("invalid_subject", "{{%s}} can't be used in a subject", m[1])
		}
	}
	return nil
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	b, p, err := s.brandWithRole(r, store.RoleEditor)
	if err != nil {
		return err
	}
	var in draftInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	d, err := s.validate(r, b, &in)
	if err != nil {
		return err
	}
	var c store.Campaign
	err = s.store.InTx(r.Context(), func(tx *store.Store) error {
		if c, err = tx.CreateCampaign(r.Context(), b.ID, d, p.User.ID); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "campaign.created", "campaign", c.ID.String(), &b.ID), nil, auditDraft(c))
	})
	if err != nil {
		return err
	}
	return s.writeCampaign(w, r, http.StatusCreated, b, c)
}

func auditDraft(c store.Campaign) map[string]any {
	return map[string]any{"name": c.Name, "topic_id": c.TopicID, "template_id": c.TemplateID, "subject": c.Subject,
		"include_at_risk": c.IncludeAtRisk}
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) error {
	b, p, c, err := s.campaign(r, store.RoleEditor)
	if err != nil {
		return err
	}
	var in draftInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	d, err := s.validate(r, b, &in)
	if err != nil {
		return err
	}
	var out store.Campaign
	err = s.store.InTx(r.Context(), func(tx *store.Store) error {
		if out, err = tx.UpdateCampaignDraft(r.Context(), b.ID, c.ID, d, in.Version, p.User.ID); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "campaign.updated", "campaign", c.ID.String(), &b.ID), auditDraft(c), auditDraft(out))
	})
	if errors.Is(err, store.ErrStale) && !c.State.Editable() {
		err = ErrNotEditable
	}
	if err != nil {
		return campaignError(w, err, nil)
	}
	return s.writeCampaign(w, r, http.StatusOK, b, out)
}

// writeCampaign answers with a campaign and what's known about its audience:
// live counts for a draft, the stored snapshot and progress once scheduled.
func (s *Service) writeCampaign(w http.ResponseWriter, r *http.Request, status int, b store.Brand, c store.Campaign) error {
	out := map[string]any{"campaign": c}
	if c.State.Editable() {
		sum, err := s.audience(r.Context(), s.store, b.ID, c)
		if err != nil {
			return err
		}
		out["audience"] = sum
	} else {
		breakdown, err := s.store.AudienceBreakdown(r.Context(), c.ID)
		if err != nil {
			return err
		}
		progress, err := s.store.SendProgress(r.Context(), c.ID)
		if err != nil {
			return err
		}
		out["breakdown"], out["progress"] = breakdown, progress
	}
	httpx.WriteJSON(w, status, out)
	return nil
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	b, _, c, err := s.campaign(r, store.RoleViewer)
	if err != nil {
		return err
	}
	return s.writeCampaign(w, r, http.StatusOK, b, c)
}

func (s *Service) review(w http.ResponseWriter, r *http.Request) error {
	b, _, c, err := s.campaign(r, store.RoleViewer)
	if err != nil {
		return err
	}
	rv, _, err := s.Review(r.Context(), b, c)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, reviewView(rv))
	return nil
}

func reviewView(rv Review) map[string]any {
	reasons := []map[string]string{}
	for _, r := range rv.ApprovalReasons {
		reasons = append(reasons, map[string]string{"code": string(r), "text": r.Text()})
	}
	return map[string]any{
		"audience": rv.Audience, "approval_reasons": reasons, "problems": rv.Problems,
		"test_slice_pct": rv.TestSlicePct, "hold_minutes": rv.HoldMinutes, "from_header": rv.FromHeader,
		"confirm_text": rv.ConfirmText, "cancel_window_minutes": int(CancelWindow.Minutes()),
	}
}

func (s *Service) schedule(w http.ResponseWriter, r *http.Request, az authorizer) error {
	b, _, c, err := s.campaign(r, store.RoleSender)
	if err != nil {
		return err
	}
	p, err := az.RequireStepUp(r)
	if err != nil {
		return err
	}
	var in ScheduleInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	out, rv, err := s.Schedule(r.Context(), b, c, in, p.User, auth.AuditEntry(r, "", "", "", &b.ID))
	if err != nil {
		return campaignError(w, err, &rv)
	}
	return s.writeCampaign(w, r, http.StatusOK, b, out)
}

// agentSend: an AI agent asks for a draft to be sent; a person approves it
// with their passkey (see agent.go). People send from the campaign page.
func (s *Service) agentSend(w http.ResponseWriter, r *http.Request) error {
	b, p, c, err := s.campaign(r, store.RoleSender)
	if err != nil {
		return err
	}
	if p.Agent == nil {
		return httpx.Errorf(http.StatusForbidden, "agents_only", "this is how AI agents ask for a send; people send from the campaign page")
	}
	var in struct {
		ScheduledAt *time.Time `json:"scheduled_at"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	a, rv, err := s.RequestAgentSend(r.Context(), b, c, in.ScheduledAt, p, auth.AuditEntry(r, "", "", "", &b.ID))
	if errors.Is(err, ErrAlreadyRequested) {
		return conflict("already_requested", "%s", ErrAlreadyRequested.Error())
	}
	if err != nil {
		return campaignError(w, err, &rv)
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"approval": map[string]any{"id": a.ID, "status": a.Status, "summary": a.Summary, "expires_at": a.ExpiresAt},
		"audience": rv.ConfirmText, "approval_reasons": rv.ApprovalReasons,
	})
	return nil
}

func (s *Service) cancel(w http.ResponseWriter, r *http.Request) error {
	b, _, c, err := s.campaign(r, store.RoleSender)
	if err != nil {
		return err
	}
	out, err := s.Cancel(r.Context(), c, auth.AuditEntry(r, "", "", "", &b.ID))
	if err != nil {
		return campaignError(w, err, nil)
	}
	return s.writeCampaign(w, r, http.StatusOK, b, out)
}

func (s *Service) pause(w http.ResponseWriter, r *http.Request) error {
	b, p, c, err := s.campaign(r, store.RoleSender)
	if err != nil {
		return err
	}
	out, err := s.Pause(r.Context(), c, "paused by "+p.User.Name, auth.AuditEntry(r, "", "", "", &b.ID))
	if err != nil {
		return campaignError(w, err, nil)
	}
	return s.writeCampaign(w, r, http.StatusOK, b, out)
}

func (s *Service) resume(w http.ResponseWriter, r *http.Request, az authorizer) error {
	b, _, c, err := s.campaign(r, store.RoleSender)
	if err != nil {
		return err
	}
	if _, err := az.RequireStepUp(r); err != nil {
		return err
	}
	if b.Status != store.BrandReady {
		return conflict("brand_not_ready", "the brand is %s; it has to be ready before sending resumes", b.Status)
	}
	// Paused by its own brake: its bounces and complaints are counted from
	// the start, so it would pause again at once. It's done; send a new one.
	evs, err := s.store.CampaignSafetyEvents(r.Context(), c.ID)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if e.Action == "paused" {
			return conflict("campaign_brake", "this campaign was paused by its brake (%s), and its results so far would pause it again. "+
				"Cancel it, find what went wrong (usually the list), and send a new campaign to an engaged audience", e.Reason)
		}
	}
	if b.BrakeState == "slowed" && !slices.Contains(c.ApprovalReasons, string(ReasonBrandSlowed)) {
		return conflict("brand_slowed", "the brand's brake has it slowed down: every send needs a second admin's approval. "+
			"Resume once the brand recovers, or send a new campaign through approval")
	}
	out, err := s.Resume(r.Context(), c, auth.AuditEntry(r, "", "", "", &b.ID))
	if err != nil {
		return campaignError(w, err, nil)
	}
	return s.writeCampaign(w, r, http.StatusOK, b, out)
}

func (s *Service) test(w http.ResponseWriter, r *http.Request) error {
	b, _, c, err := s.campaign(r, store.RoleEditor)
	if err != nil {
		return err
	}
	var in struct {
		Emails []string `json:"emails"` // a subset of the seed list; empty = all of it
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	seeds, err := s.store.SeedAddresses(r.Context(), b.ID)
	if err != nil {
		return err
	}
	var to []string
	for _, sd := range seeds {
		if len(in.Emails) == 0 || slices.Contains(in.Emails, sd.Email) {
			to = append(to, sd.Email)
		}
	}
	switch {
	case len(to) == 0:
		return bad("no_seeds", "add internal addresses to the seed list first (a brand admin can)")
	case len(in.Emails) > len(to):
		return bad("not_a_seed", "tests only go to addresses on the brand's seed list")
	case len(to) > MaxTestRecipients:
		return bad("too_many", "send a test to at most %d addresses at once", MaxTestRecipients)
	}
	n, err := s.SendTest(r.Context(), b, c, to)
	var pe *ProblemsError
	if errors.As(err, &pe) {
		return campaignError(w, err, nil)
	}
	auditErr := s.store.Audit(r.Context(), auth.AuditEntry(r, "campaign.test_sent", "campaign", c.ID.String(), &b.ID), nil,
		map[string]any{"to": to[:n], "subject": c.Subject})
	if err != nil {
		return err
	}
	if auditErr != nil {
		return auditErr
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": n, "to": to})
	return nil
}

// --- seed list and sender ---

func normalizeAddress(raw string) (string, bool) {
	a, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || a.Name != "" || !strings.Contains(a.Address, ".") {
		return "", false
	}
	return strings.ToLower(a.Address), true
}

func (s *Service) listSeeds(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.visibleBrand(r)
	if err != nil {
		return err
	}
	list, err := s.store.SeedAddresses(r.Context(), b.ID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []store.SeedAddress{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"seeds": list})
	return nil
}

// MaxSeeds per brand: it's an internal test list, not an audience.
const MaxSeeds = 25

func (s *Service) addSeed(w http.ResponseWriter, r *http.Request, az authorizer) error {
	b, _, err := s.brandWithRole(r, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	p, err := az.RequireStepUp(r)
	if err != nil {
		return err
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	addr, ok := normalizeAddress(in.Email)
	if !ok {
		return bad("invalid_email", "%q is not an email address", in.Email)
	}
	seeds, err := s.store.SeedAddresses(r.Context(), b.ID)
	if err != nil {
		return err
	}
	if len(seeds) >= MaxSeeds {
		return bad("too_many", "a seed list holds at most %d addresses", MaxSeeds)
	}
	err = s.store.InTx(r.Context(), func(tx *store.Store) error {
		if err := tx.AddSeedAddress(r.Context(), b.ID, addr, p.User.ID); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "seed.added", "seed", addr, &b.ID), nil, map[string]string{"email": addr})
	})
	if errors.Is(err, store.ErrConflict) {
		return conflict("already_seed", "%s is already on the seed list", addr)
	}
	if err != nil {
		return err
	}
	return s.listSeeds(w, r)
}

func (s *Service) removeSeed(w http.ResponseWriter, r *http.Request, az authorizer) error {
	b, _, err := s.brandWithRole(r, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	if _, err := az.RequireStepUp(r); err != nil {
		return err
	}
	addr := strings.ToLower(r.PathValue("email"))
	err = s.store.InTx(r.Context(), func(tx *store.Store) error {
		if err := tx.RemoveSeedAddress(r.Context(), b.ID, addr); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "seed.removed", "seed", addr, &b.ID), map[string]string{"email": addr}, nil)
	})
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.listSeeds(w, r)
}

func (s *Service) writeSender(w http.ResponseWriter, b store.Brand, sender store.BrandSender) error {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sender": sender, "from_header": FromHeader(b, sender)})
	return nil
}

func (s *Service) getSender(w http.ResponseWriter, r *http.Request) error {
	b, _, err := s.visibleBrand(r)
	if err != nil {
		return err
	}
	sender, err := s.store.BrandSender(r.Context(), b.ID)
	if err != nil {
		return err
	}
	return s.writeSender(w, b, sender)
}

var fromLocalRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func (s *Service) putSender(w http.ResponseWriter, r *http.Request, az authorizer) error {
	b, _, err := s.brandWithRole(r, store.RoleBrandAdmin)
	if err != nil {
		return err
	}
	if _, err := az.RequireStepUp(r); err != nil {
		return err
	}
	var in store.BrandSender
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	in.FromName, in.FromLocal = oneLine(in.FromName), strings.ToLower(strings.TrimSpace(in.FromLocal))
	switch {
	case !fromLocalRe.MatchString(in.FromLocal):
		return bad("invalid_from", "the part before @%s may use letters, digits, dots, dashes and underscores", b.SendingDomain)
	case len([]rune(in.FromName)) > 100:
		return bad("invalid_from_name", "keep the sender name under 100 characters")
	}
	if strings.TrimSpace(in.ReplyTo) != "" {
		addr, ok := normalizeAddress(in.ReplyTo)
		if !ok {
			return bad("invalid_reply_to", "%q is not an email address", in.ReplyTo)
		}
		in.ReplyTo = addr
	} else {
		in.ReplyTo = ""
	}
	err = s.store.InTx(r.Context(), func(tx *store.Store) error {
		before, err := tx.BrandSender(r.Context(), b.ID)
		if err != nil {
			return err
		}
		if err := tx.SetBrandSender(r.Context(), b.ID, in); err != nil {
			return err
		}
		return tx.Audit(r.Context(), auth.AuditEntry(r, "brand.sender_updated", "brand", b.Slug, &b.ID), before, in)
	})
	if err != nil {
		return err
	}
	return s.writeSender(w, b, in)
}
