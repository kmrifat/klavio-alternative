package tracking

import (
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Hit is one request to a tracked link or the pixel.
type Hit struct {
	At          time.Time
	Method      string
	UserAgent   string
	IP          netip.Addr
	Flags       byte
	LinkNo      int
	LinkCount   int
	DeliveredAt *time.Time // from SES (nil = not known yet)
}

// PriorClick is an earlier click on the same email.
type PriorClick struct {
	ID     int64
	At     time.Time
	LinkNo int
	IsBot  bool
}

// Thresholds for the click heuristics.
const (
	// TooFastAfterDelivery: security gateways open every link as the email arrives.
	TooFastAfterDelivery = 10 * time.Second
	// BurstWindow: clicks this close together on many links aren't a person reading.
	BurstWindow    = 10 * time.Second
	BurstMinLinks  = 3
	botHeadRequest = "head_request"
)

// scannerAgents are user-agent fragments of mail security scanners, link
// previewers and scripts (lower case).
var scannerAgents = []string{
	"bot", "crawler", "spider", "preview", "scanner", "headless", "phantomjs", "python-requests", "python-urllib",
	"go-http-client", "curl/", "wget/", "java/", "okhttp", "libwww", "httpclient", "facebookexternalhit",
	"whatsapp", "skypeuripreview", "barracuda", "proofpoint", "mimecast", "ironport", "symantec", "trend micro",
	"forcepoint", "sophos", "fortinet", "microsoft office protocol discovery", "ms-office", "safelinks",
}

// Classifier holds what the heuristics can be configured with.
type Classifier struct {
	// ScannerNets: address ranges of mail security scanners (TRACKING_SCANNER_CIDRS).
	ScannerNets []netip.Prefix
}

func (c Classifier) scannerIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range c.ScannerNets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func scannerUA(ua string) bool {
	ua = strings.ToLower(ua)
	for _, s := range scannerAgents {
		if strings.Contains(ua, s) {
			return true
		}
	}
	return false
}

// Click decides whether a click came from a person. burst lists earlier
// clicks (by id) that this click shows to be a bot burst too.
func (c Classifier) Click(h Hit, prior []PriorClick) (bot bool, reason string, burst []int64) {
	switch {
	case h.Flags&Honeypot != 0:
		return true, "honeypot_link", nil
	case h.Method == http.MethodHead:
		return true, botHeadRequest, nil
	case strings.TrimSpace(h.UserAgent) == "":
		return true, "no_user_agent", nil
	case scannerUA(h.UserAgent):
		return true, "scanner_user_agent", nil
	case h.IP.IsValid() && c.scannerIP(h.IP):
		return true, "scanner_ip", nil
	case h.DeliveredAt != nil && h.At.Sub(*h.DeliveredAt) < TooFastAfterDelivery:
		return true, "too_fast_after_delivery", nil
	}
	// Many different links within seconds: a scanner walking the email.
	links := map[int]bool{h.LinkNo: true}
	var inBurst []int64
	for _, p := range prior {
		if d := h.At.Sub(p.At); d >= 0 && d <= BurstWindow {
			links[p.LinkNo] = true
			if !p.IsBot {
				inBurst = append(inBurst, p.ID)
			}
		}
	}
	need := BurstMinLinks
	if h.LinkCount > 1 && h.LinkCount < need {
		need = h.LinkCount // a 2-link email with both clicked at once
	}
	if len(links) >= need && h.LinkCount > 1 {
		return true, "many_links_at_once", inBurst
	}
	return false, "", nil
}

// applePrivacyNet: Apple's own range, which Mail Privacy Protection's proxy uses.
var applePrivacyNet = netip.MustParsePrefix("17.0.0.0/8")

// Open decides whether a pixel load says anything about a person. Apple Mail
// Privacy Protection loads every image for every email, so its opens are
// machine opens; so are scanners. Opens are an estimate at best.
func (c Classifier) Open(h Hit) (machine bool, reason string) {
	ua := strings.TrimSpace(h.UserAgent)
	switch {
	case h.Method == http.MethodHead:
		return true, botHeadRequest
	case ua == "Mozilla/5.0" || (h.IP.IsValid() && applePrivacyNet.Contains(h.IP.Unmap())):
		return true, "apple_privacy_proxy"
	case ua == "" || scannerUA(ua) || (h.IP.IsValid() && c.scannerIP(h.IP)):
		return true, "scanner"
	case h.DeliveredAt != nil && h.At.Sub(*h.DeliveredAt) < TooFastAfterDelivery:
		return true, "too_fast_after_delivery"
	}
	return false, ""
}
