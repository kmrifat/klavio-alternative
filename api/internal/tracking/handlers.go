package tracking

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/kmrifat/klavio-alternative/api/internal/httpx"
	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

// pixelGIF is a transparent 1×1 GIF.
var pixelGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
}

// Recorder writes clicks and opens. Handlers hand hits to it after
// answering, so a slow database never delays a redirect.
type Recorder struct {
	Store      *store.Store
	Classifier Classifier
	Log        *slog.Logger
	queue      chan func(context.Context)
}

// Start runs the background writer until ctx ends. Without Start, hits are
// written inline (tests).
func (r *Recorder) Start(ctx context.Context, buffer int) {
	r.queue = make(chan func(context.Context), buffer)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-r.queue:
				job(context.WithoutCancel(ctx))
			}
		}
	}()
}

func (r *Recorder) enqueue(ctx context.Context, job func(context.Context)) {
	if r.queue == nil {
		job(context.WithoutCancel(ctx))
		return
	}
	select {
	case r.queue <- job:
	default:
		r.Log.WarnContext(ctx, "tracking queue full; a hit was not recorded")
	}
}

// Click records one click: classify it against the email's other recent
// clicks (marking earlier ones as bots if this one gives them away) and
// store it. It becomes engagement when it settles.
func (r *Recorder) Click(ctx context.Context, l Link, h Hit) error {
	return r.Store.InTx(ctx, func(tx *store.Store) error {
		// The token is signed, but the send may have been deleted since.
		ref, err := tx.SendByID(ctx, l.SendID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (ref.BrandID != l.BrandID || ref.ContactID != l.ContactID)) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.LockSendClicks(ctx, l.SendID); err != nil {
			return err
		}
		if h.DeliveredAt, err = tx.DeliveredAt(ctx, l.SendID); err != nil {
			return err
		}
		prior, err := tx.ClicksSince(ctx, l.SendID, h.At.Add(-max(BurstWindow, TaintWindow)))
		if err != nil {
			return err
		}
		pc := make([]PriorClick, len(prior))
		for i, p := range prior {
			pc[i] = PriorClick{ID: p.ID, At: p.At, LinkNo: p.LinkNo, IsBot: p.IsBot, IP: p.IP, UserAgent: p.UserAgent}
		}
		bot, reason, burst := r.Classifier.Click(h, pc)
		if len(burst) > 0 {
			if err := tx.MarkClicksBot(ctx, burst, reason); err != nil {
				return err
			}
		}
		// Engagement comes later, when the click settles (Settle): until
		// then a burst or the honeypot can still show it was a scanner.
		_, err = tx.InsertClick(ctx, store.Click{SendID: l.SendID, BrandID: l.BrandID, ContactID: l.ContactID, LinkNo: l.LinkNo,
			URL: l.URL, At: h.At, Method: h.Method, IP: ipString(h.IP), UserAgent: truncate(h.UserAgent, 500), IsBot: bot, BotReason: reason})
		return err
	})
}

// Settle is the minutely job: clicks past the settle window are final, and
// the people behind the human ones count as engaged.
func (r *Recorder) Settle(ctx context.Context) error {
	return r.Store.SettleClicks(ctx, time.Now().Add(-SettleWindow))
}

// Open records a pixel load (never used for tiers or brakes).
func (r *Recorder) Open(ctx context.Context, px Pixel, h Hit) error {
	ref, err := r.Store.SendByID(ctx, px.SendID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (ref.BrandID != px.BrandID || ref.ContactID != px.ContactID)) {
		return nil
	}
	if err != nil {
		return err
	}
	if h.DeliveredAt, err = r.Store.DeliveredAt(ctx, px.SendID); err != nil {
		return err
	}
	machine, reason := r.Classifier.Open(h)
	return r.Store.InsertOpen(ctx, store.Open{SendID: px.SendID, BrandID: px.BrandID, ContactID: px.ContactID, At: h.At,
		IP: ipString(h.IP), UserAgent: truncate(h.UserAgent, 500), IsMachine: machine, Reason: reason})
}

func ipString(ip netip.Addr) string {
	if !ip.IsValid() {
		return ""
	}
	return ip.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type Handlers struct {
	Signer   *Signer
	Recorder *Recorder
	Log      *slog.Logger
	now      func() time.Time
}

func (h *Handlers) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// Routes: /c/<token> (GET and HEAD: redirect) and /o/<token>.gif (pixel), on
// each brand's link domain. Outside /admin: no session, no CORS.
func (h *Handlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+ClickPath+"{token}", h.click) // also HEAD
	mux.HandleFunc("GET "+OpenPath+"{token}", h.open)
}

func noStore(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Cache-Control", "no-store, private")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Robots-Tag", "noindex, nofollow")
	hd.Set("X-Content-Type-Options", "nosniff")
}

func (h *Handlers) hit(r *http.Request) Hit {
	ip, _ := netip.ParseAddr(httpx.ClientIP(r))
	return Hit{At: h.clock(), Method: r.Method, UserAgent: r.UserAgent(), IP: ip}
}

func (h *Handlers) click(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	l, err := h.Signer.Click(r.PathValue("token"))
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("This link isn't valid. Check you copied all of it.\n"))
		return
	}
	// The person goes on at once; the click is written afterwards.
	http.Redirect(w, r, l.URL, http.StatusFound)
	if l.Flags&NoTrack != 0 {
		return
	}
	hit := h.hit(r)
	hit.Flags, hit.LinkNo, hit.LinkCount = l.Flags, l.LinkNo, l.LinkCount
	h.Recorder.enqueue(r.Context(), func(ctx context.Context) {
		if err := h.Recorder.Click(ctx, l, hit); err != nil {
			h.Log.ErrorContext(ctx, "recording a click", "send_id", l.SendID, "err", err)
		}
	})
}

func (h *Handlers) open(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	w.Header().Set("Content-Type", "image/gif")
	px, err := h.Signer.Open(strings.TrimSuffix(r.PathValue("token"), ".gif"))
	_, _ = w.Write(pixelGIF) // always a pixel: a broken image looks bad in the email
	if err != nil || px.Flags&NoTrack != 0 || r.Method == http.MethodHead {
		return
	}
	hit := h.hit(r)
	h.Recorder.enqueue(r.Context(), func(ctx context.Context) {
		if err := h.Recorder.Open(ctx, px, hit); err != nil {
			h.Log.ErrorContext(ctx, "recording an open", "send_id", px.SendID, "err", err)
		}
	})
}
