package contactsapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/cache"
	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/imports"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

//go:embed openapi.json
var openAPISpec []byte

// Limits for API callers.
const (
	PerKeyPerMinute      = 120
	ConfirmEmailsPerDay  = 3 // per address: stops the API being used to mail-bomb someone
	maxBody              = 64 << 10
	maxIdempotencyKeyLen = 100
)

// ConfirmationQueue sends the double opt-in email in the background.
type ConfirmationQueue interface {
	QueueConfirmation(ctx context.Context, brandID, contactID uuid.UUID) error
}

type API struct {
	Store   *store.Store
	Limiter cache.Limiter
	Queue   ConfirmationQueue
	Log     *slog.Logger
	now     func() time.Time
}

func (a *API) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// Routes: the public API under /api/v1. No cookies and no CORS: it's for
// servers, and a key in a web page would be public.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(openAPISpec)
	})
	mux.HandleFunc("POST /api/v1/contacts", a.handle(ScopeWrite, true, a.subscribe))
	mux.HandleFunc("GET /api/v1/contacts/{email}", a.handle(ScopeRead, false, a.lookup))
	mux.HandleFunc("POST /api/v1/contacts/{email}/unsubscribe", a.handle(ScopeWrite, true, a.unsubscribe))
}

// call is one authenticated request.
type call struct {
	r     *http.Request
	key   store.APIKey
	brand store.Brand
	body  []byte
	email string // for the call log, masked when logged
}

type result struct {
	status int
	body   any
}

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

func fail(status int, code, msg string) *apiError { return &apiError{status, code, msg} }

func errBody(code, msg string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": msg}}
}

func (a *API) handle(scope string, idempotent bool, fn func(c *call) (result, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		c := &call{r: r}
		res, err := a.serve(w, c, scope, idempotent, fn)
		code := ""
		var ae *apiError
		switch {
		case errors.As(err, &ae):
			res, code = result{ae.status, errBody(ae.code, ae.message)}, ae.code
		case err != nil:
			a.Log.ErrorContext(r.Context(), "contacts api", "err", err)
			res, code = result{http.StatusInternalServerError, errBody("internal", "something went wrong; retry with the same Idempotency-Key")}, "internal"
		}
		if res.status != 0 { // 0: already written (idempotent replay)
			if res.status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "60")
			}
			httpx.WriteJSON(w, res.status, res.body)
		}
		if c.key.ID != uuid.Nil {
			status := res.status
			if status == 0 {
				status = http.StatusOK
			}
			logErr := a.Store.LogAPICall(context.WithoutCancel(r.Context()), store.APICall{
				KeyID: &c.key.ID, BrandID: c.brand.ID, Method: r.Method, Path: r.Pattern[strings.Index(r.Pattern, " ")+1:],
				Status: status, IP: httpx.ClientIP(r), EmailMask: maskEmail(c.email), ErrorCode: code,
			})
			if logErr != nil {
				a.Log.WarnContext(r.Context(), "logging api call", "err", logErr)
			}
		}
	}
}

func (a *API) serve(w http.ResponseWriter, c *call, scope string, idempotent bool, fn func(c *call) (result, error)) (result, error) {
	r := c.r
	ctx := r.Context()
	if err := a.authenticate(c); err != nil {
		return result{}, err
	}
	if !slices.Contains(c.key.Scopes, scope) {
		return result{}, fail(http.StatusForbidden, "missing_scope", "this key doesn't have the "+scope+" scope")
	}
	ok, err := a.Limiter.Allow(ctx, "api:key:"+c.key.ID.String(), PerKeyPerMinute, time.Minute)
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{}, fail(http.StatusTooManyRequests, "rate_limited", "too many requests for this key; slow down and retry after a minute")
	}
	_ = a.Store.TouchAPIKey(ctx, c.key.ID)

	if r.Method == http.MethodPost {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			return result{}, fail(http.StatusUnsupportedMediaType, "json_required", "send Content-Type: application/json")
		}
		c.body, err = io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			return result{}, fail(http.StatusBadRequest, "bad_body", "couldn't read the request body")
		}
		if len(c.body) > maxBody {
			return result{}, fail(http.StatusRequestEntityTooLarge, "too_large", "the request body is too large")
		}
	}

	idem := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !idempotent || idem == "" {
		return fn(c)
	}
	if len(idem) > maxIdempotencyKeyLen {
		return result{}, fail(http.StatusBadRequest, "bad_idempotency_key", "Idempotency-Key is at most 100 characters")
	}
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "\x00"))
	h.Write(c.body)
	hash := h.Sum(nil)
	claimed, prev, err := a.Store.ClaimIdempotency(ctx, c.key.ID, idem, hash)
	if err != nil {
		return result{}, err
	}
	if !claimed {
		switch {
		case !bytes.Equal(prev.RequestHash, hash):
			return result{}, fail(http.StatusUnprocessableEntity, "idempotency_mismatch", "this Idempotency-Key was used for a different request")
		case prev.Status == 0:
			return result{}, fail(http.StatusConflict, "request_in_progress", "a request with this Idempotency-Key is still running")
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(prev.Status)
		_, _ = w.Write(prev.Response)
		c.email = emailFrom(c)
		return result{}, nil
	}
	res, err := fn(c)
	var ae *apiError
	if errors.As(err, &ae) {
		res, err = result{ae.status, errBody(ae.code, ae.message)}, nil
	}
	if err != nil {
		_ = a.Store.ReleaseIdempotency(context.WithoutCancel(ctx), c.key.ID, idem)
		return res, err
	}
	raw, _ := json.Marshal(res.body)
	if err := a.Store.FinishIdempotency(context.WithoutCancel(ctx), c.key.ID, idem, res.status, raw); err != nil {
		return res, err
	}
	return res, nil
}

func (a *API) authenticate(c *call) error {
	r := c.r
	ctx := r.Context()
	scheme, secret, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(secret, keyPrefix) || len(secret) > 200 {
		return fail(http.StatusUnauthorized, "missing_key", "send your API key as Authorization: Bearer kla_…")
	}
	k, err := a.Store.APIKeyByHash(ctx, hashKey(strings.TrimSpace(secret)))
	if errors.Is(err, store.ErrNotFound) {
		return fail(http.StatusUnauthorized, "invalid_key", "this API key isn't valid")
	}
	if err != nil {
		return err
	}
	switch {
	case k.RevokedAt != nil:
		return fail(http.StatusUnauthorized, "key_revoked", "this API key was revoked")
	case !k.ExpiresAt.After(a.clock()):
		return fail(http.StatusUnauthorized, "key_expired", "this API key has expired; create a new one")
	case !k.Active:
		return fail(http.StatusForbidden, "key_pending_approval", "this API key is waiting for a second admin's approval")
	}
	c.key = k
	if c.brand, err = a.Store.BrandByID(ctx, k.BrandID); err != nil {
		return err
	}
	if len(k.IPAllowlist) > 0 {
		ip, err := netip.ParseAddr(httpx.ClientIP(r))
		allowed := false
		for _, s := range k.IPAllowlist {
			if p, perr := netip.ParsePrefix(s); perr == nil && err == nil && p.Contains(ip.Unmap()) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fail(http.StatusForbidden, "ip_not_allowed", "this key can't be used from this IP address")
		}
	}
	return nil
}

// --- endpoints ---

type consentInput struct {
	WordingID     uuid.UUID `json:"wording_id"`
	PageURL       string    `json:"page_url"`
	IP            string    `json:"ip"`
	ClickTracking bool      `json:"click_tracking"`
}

type subscribeInput struct {
	Email     string       `json:"email"`
	FirstName string       `json:"first_name"`
	LastName  string       `json:"last_name"`
	Topics    []uuid.UUID  `json:"topics"`
	Tags      []string     `json:"tags"`
	Country   string       `json:"country"`
	Timezone  string       `json:"timezone"`
	Consent   consentInput `json:"consent"`
}

var countryRe = regexp.MustCompile(`^[A-Za-z]{2}$`)

func bad422(code, msg string) error { return fail(http.StatusUnprocessableEntity, code, msg) }

func decode(c *call, v any) error {
	dec := json.NewDecoder(bytes.NewReader(c.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fail(http.StatusBadRequest, "bad_json", "invalid JSON: "+err.Error())
	}
	return nil
}

func (in *subscribeInput) validate() error {
	in.Email = imports.NormalizeEmail(in.Email)
	if in.Email == "" {
		return bad422("invalid_email", "that isn't a valid email address")
	}
	if imports.IsDisposable(in.Email) {
		return bad422("disposable_email", "disposable email addresses can't subscribe")
	}
	in.FirstName, in.LastName = strings.TrimSpace(in.FirstName), strings.TrimSpace(in.LastName)
	if len([]rune(in.FirstName)) > 100 || len([]rune(in.LastName)) > 100 {
		return bad422("invalid_name", "names are at most 100 characters")
	}
	if in.Country = strings.ToUpper(strings.TrimSpace(in.Country)); in.Country != "" && !countryRe.MatchString(in.Country) {
		return bad422("invalid_country", "country is a two-letter code, e.g. DE")
	}
	if in.Timezone = strings.TrimSpace(in.Timezone); in.Timezone != "" {
		if _, err := time.LoadLocation(in.Timezone); err != nil || len(in.Timezone) > 64 {
			return bad422("invalid_timezone", "timezone is an IANA name, e.g. Europe/Berlin")
		}
	}
	if len(in.Tags) > 20 {
		return bad422("invalid_tags", "at most 20 tags")
	}
	for i, t := range in.Tags {
		if t = strings.TrimSpace(t); t == "" || len([]rune(t)) > 50 {
			return bad422("invalid_tags", "tags are 1 to 50 characters")
		}
		in.Tags[i] = t
	}
	if in.Consent.WordingID == uuid.Nil {
		return bad422("consent_required", "consent.wording_id is required: the wording the person agreed to")
	}
	if u := strings.TrimSpace(in.Consent.PageURL); u != "" {
		pu, err := url.Parse(u)
		if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" || len(u) > 2000 {
			return bad422("invalid_page_url", "consent.page_url must be the http(s) address of the signup page")
		}
		in.Consent.PageURL = u
	}
	if in.Consent.IP != "" {
		ip, err := netip.ParseAddr(strings.TrimSpace(in.Consent.IP))
		if err != nil {
			return bad422("invalid_ip", "consent.ip must be the person's IP address")
		}
		in.Consent.IP = ip.String()
	}
	return nil
}

func (a *API) subscribe(c *call) (result, error) {
	var in subscribeInput
	if err := decode(c, &in); err != nil {
		return result{}, err
	}
	if err := in.validate(); err != nil {
		return result{}, err
	}
	c.email = in.Email
	ctx := c.r.Context()
	b := c.brand
	now := a.clock()

	var status string
	var newTopics []uuid.UUID // topics a subscriber asked for, waiting for their confirmation
	var queue uuid.UUID       // contact to send a confirmation to, after commit
	err := a.Store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.Lock(ctx, "contact:"+b.ID.String()+":"+in.Email); err != nil {
			return err
		}
		wording, err := tx.WordingByID(ctx, b.ID, in.Consent.WordingID)
		if errors.Is(err, store.ErrNotFound) {
			return bad422("unknown_wording", "consent.wording_id isn't one of this brand's consent wordings")
		}
		if err != nil {
			return err
		}
		if wording.RetiredAt != nil {
			return bad422("retired_wording", "that consent wording was retired; use the current one")
		}
		topics, err := tx.ListTopics(ctx, b.ID)
		if err != nil {
			return err
		}
		for _, id := range in.Topics {
			if !slices.ContainsFunc(topics, func(t store.Topic) bool { return t.ID == id }) {
				return bad422("unknown_topic", "topic "+id.String()+" isn't one of this brand's topics")
			}
		}
		allTopics := make([]uuid.UUID, len(topics))
		for i, t := range topics {
			allTopics[i] = t.ID
		}
		// Suppressed addresses (bounced, complained, removed) never get
		// email; the answer doesn't say so.
		supp, err := tx.SuppressedHashes(ctx, [][]byte{store.EmailHash(in.Email)})
		if err != nil {
			return err
		}
		if len(supp) > 0 {
			status = "pending_confirmation"
			return nil
		}

		existing, err := tx.ContactByEmail(ctx, b.ID, in.Email)
		var id uuid.UUID
		switch {
		case errors.Is(err, store.ErrNotFound):
			// New: the topics asked for, or all of them; confirmed with the sign-up.
			id, err = tx.InsertContact(ctx, store.NewContact{
				BrandID: b.ID, Email: in.Email, FirstName: in.FirstName, LastName: in.LastName,
				Status: store.ContactPending, ConsentBasis: "double_opt_in", ConsentSource: "api: " + c.key.Name,
				TrackingAllowed: in.Consent.ClickTracking, Tags: in.Tags, Timezone: in.Timezone, Country: in.Country,
			})
			if err != nil {
				return err
			}
			topicIDs := in.Topics
			if len(topicIDs) == 0 {
				topicIDs = allTopics
			}
			if err := tx.AddContactTopics(ctx, id, topicIDs); err != nil {
				return err
			}
			status, queue = "pending_confirmation", id
		case err != nil:
			return err
		default:
			id = existing.ID
			if err := tx.MergeImportedFields(ctx, id, store.NewContact{FirstName: in.FirstName, LastName: in.LastName,
				Timezone: in.Timezone, Country: in.Country, Tags: in.Tags}); err != nil {
				return err
			}
			current, err := tx.ContactTopicIDs(ctx, id)
			if err != nil {
				return err
			}
			if existing.Status == store.ContactSubscribed {
				// Already confirmed. Omitted topics change nothing; new
				// ones wait for a confirmation click like a sign-up.
				status = "subscribed"
				for _, t := range in.Topics {
					if !slices.Contains(current, t) && !slices.Contains(newTopics, t) {
						newTopics = append(newTopics, t)
					}
				}
				if len(newTopics) == 0 {
					return nil
				}
				if err := tx.RequestTopics(ctx, b.ID, id, newTopics); err != nil {
					return err
				}
				queue = id
			} else {
				// Pending, quarantined or unsubscribed: signing up again
				// means confirming again, for the topics asked for (or the
				// ones they had, or all).
				if existing.Status != store.ContactPending {
					if err := tx.SetContactStatus(ctx, id, store.ContactPending); err != nil {
						return err
					}
				}
				topicIDs := in.Topics
				if len(topicIDs) == 0 && len(current) == 0 {
					topicIDs = allTopics
				}
				if err := tx.AddContactTopics(ctx, id, topicIDs); err != nil {
					return err
				}
				status, queue = "pending_confirmation", id
			}
		}
		return tx.AddConsentRecord(ctx, store.NewConsentRecord{
			BrandID: b.ID, ContactID: id, Email: in.Email, Kind: "opt_in", Method: "api",
			Source: "API key " + c.key.Prefix + " (" + c.key.Name + ")", Wording: wording.Text, WordingID: &wording.ID,
			PageURL: in.Consent.PageURL, IP: in.Consent.IP, OccurredAt: now,
		})
	})
	if err != nil {
		return result{}, err
	}
	body := map[string]any{"email": in.Email, "status": status}
	code := http.StatusOK
	if status == "pending_confirmation" || len(newTopics) > 0 {
		code = http.StatusAccepted
	}
	if len(newTopics) > 0 {
		body["pending_topics"] = newTopics
	}
	if queue != uuid.Nil {
		body["confirmation_email"] = a.queueConfirmation(ctx, b.ID, queue, in.Email)
	}
	return result{code, body}, nil
}

// queueConfirmation sends the double opt-in email, at most a few a day per
// address and brand however often the API is called for it. It reports
// "sent", "limit_reached", or "not_sent" (the limiter or queue failed: no
// email rather than an unlimited one).
func (a *API) queueConfirmation(ctx context.Context, brandID, contactID uuid.UUID, email string) string {
	key := "api:confirm:" + brandID.String() + ":" + hex.EncodeToString(store.EmailHash(email))
	ok, err := a.Limiter.Allow(ctx, key, ConfirmEmailsPerDay, 24*time.Hour)
	switch {
	case err != nil:
		a.Log.ErrorContext(ctx, "confirmation rate limiter", "err", err)
		return "not_sent"
	case !ok:
		return "limit_reached"
	}
	if err := a.Queue.QueueConfirmation(ctx, brandID, contactID); err != nil {
		a.Log.ErrorContext(ctx, "queueing confirmation email", "err", err)
		return "not_sent"
	}
	return "sent"
}

func (a *API) pathEmail(c *call) (string, error) {
	e := imports.NormalizeEmail(c.r.PathValue("email"))
	if e == "" {
		return "", bad422("invalid_email", "that isn't a valid email address")
	}
	c.email = e
	return e, nil
}

func (a *API) lookup(c *call) (result, error) {
	email, err := a.pathEmail(c)
	if err != nil {
		return result{}, err
	}
	ctx := c.r.Context()
	ct, err := a.Store.ContactByEmail(ctx, c.brand.ID, email)
	if errors.Is(err, store.ErrNotFound) {
		return result{}, fail(http.StatusNotFound, "not_found", "no contact with this email")
	}
	if err != nil {
		return result{}, err
	}
	ids, err := a.Store.ContactTopicIDs(ctx, ct.ID)
	if err != nil {
		return result{}, err
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	status := string(ct.Status)
	if ct.SuppressionReason != nil {
		status = "suppressed"
	}
	return result{http.StatusOK, map[string]any{"email": ct.Email, "status": status, "topics": ids, "consented_at": ct.ConsentedAt}}, nil
}

func (a *API) unsubscribe(c *call) (result, error) {
	email, err := a.pathEmail(c)
	if err != nil {
		return result{}, err
	}
	ctx := c.r.Context()
	err = a.Store.InTx(ctx, func(tx *store.Store) error {
		ct, err := tx.ContactByEmail(ctx, c.brand.ID, email)
		if errors.Is(err, store.ErrNotFound) {
			return fail(http.StatusNotFound, "not_found", "no contact with this email")
		}
		if err != nil {
			return err
		}
		if ct, err = tx.LockContact(ctx, c.brand.ID, ct.ID); err != nil {
			return err
		}
		if ct.Status == store.ContactUnsubscribed {
			return nil
		}
		if err := tx.SetContactStatus(ctx, ct.ID, store.ContactUnsubscribed); err != nil {
			return err
		}
		return tx.AddConsentRecord(ctx, store.NewConsentRecord{
			BrandID: c.brand.ID, ContactID: ct.ID, Email: ct.Email, Kind: "unsubscribed", Method: "api",
			Source: "API key " + c.key.Prefix + " (" + c.key.Name + ")", OccurredAt: a.clock(),
		})
	})
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, map[string]any{"email": email, "status": "unsubscribed"}}, nil
}

// emailFrom finds the address of a replayed request, for the call log.
func emailFrom(c *call) string {
	if e := c.r.PathValue("email"); e != "" {
		return imports.NormalizeEmail(e)
	}
	var in struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(c.body, &in)
	return imports.NormalizeEmail(in.Email)
}

// maskEmail: a•••@example.com.
func maskEmail(e string) string {
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" {
		return ""
	}
	return string([]rune(local)[0]) + "•••@" + domain
}
