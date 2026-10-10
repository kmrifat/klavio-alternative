// Package postmaster reads Google Postmaster Tools: Gmail's own view of a
// sending domain (the spam rate Gmail users report, and Gmail's reputation
// for the domain). It is the only way to see Gmail's spam-button rate, which
// is what gets bulk senders blocked (Gmail's limit is 0.3%; aim for < 0.1%).
//
// Setup (once): verify each sending domain in https://postmaster.google.com,
// then create a Google Cloud service account with the Postmaster Tools API
// enabled, add its email as a user on each domain, download its JSON key and
// point POSTMASTER_CREDENTIALS_FILE at it. A file with an authorized_user
// refresh token (gcloud auth application-default login) works too.
//
// The key is a secret: keep it out of the repository and the image; mount it.
package postmaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	Scope          = "https://www.googleapis.com/auth/postmaster.readonly"
	DefaultBaseURL = "https://gmailpostmastertools.googleapis.com/v1"
)

var (
	// ErrNotConnected: no credentials configured.
	ErrNotConnected = errors.New("google postmaster tools is not connected")
	// ErrDomainNotFound: the domain isn't verified in Postmaster Tools, or the
	// credentials weren't added as a user on it.
	ErrDomainNotFound = errors.New("domain not set up in google postmaster tools")
)

// Stat is one day of Gmail data for a domain. Postmaster Tools only reports
// days with enough Gmail traffic (a few hundred emails), so gaps are normal.
type Stat struct {
	Day              time.Time
	SpamRatio        *float64 // user-reported spam ratio, 0.001 = 0.1%
	DomainReputation string   // HIGH, MEDIUM, LOW, BAD ("" = not reported)
}

// Client talks to the Postmaster Tools API. A nil *Client is "not connected".
type Client struct {
	HTTP    *http.Client
	BaseURL string
}

// New returns nil, nil when credentialsFile is empty (not connected).
func New(ctx context.Context, credentialsFile string) (*Client, error) {
	if credentialsFile == "" {
		return nil, nil
	}
	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("postmaster credentials: %w", err)
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("postmaster credentials: not a JSON key file")
	}
	// Only the two types this is documented for: loading arbitrary credential
	// types from a file can make the library fetch URLs named in it.
	var t google.CredentialsType
	switch head.Type {
	case string(google.ServiceAccount):
		t = google.ServiceAccount
	case string(google.AuthorizedUser):
		t = google.AuthorizedUser
	default:
		return nil, fmt.Errorf("postmaster credentials: type %q not supported (use a service_account or authorized_user file)", head.Type)
	}
	creds, err := google.CredentialsFromJSONWithType(ctx, data, t, Scope)
	if err != nil {
		return nil, fmt.Errorf("postmaster credentials: %w", err)
	}
	hc := oauth2.NewClient(context.WithoutCancel(ctx), creds.TokenSource)
	hc.Timeout = 30 * time.Second
	return &Client{HTTP: hc, BaseURL: DefaultBaseURL}, nil
}

// TrafficStats returns the domain's daily stats between from and to
// (inclusive, UTC days), newest first.
func (c *Client) TrafficStats(ctx context.Context, domain string, from, to time.Time) ([]Stat, error) {
	if c == nil {
		return nil, ErrNotConnected
	}
	q := url.Values{}
	from, to = from.UTC(), to.UTC()
	q.Set("startDate.year", strconv.Itoa(from.Year()))
	q.Set("startDate.month", strconv.Itoa(int(from.Month())))
	q.Set("startDate.day", strconv.Itoa(from.Day()))
	// endDate is exclusive in the API.
	end := to.AddDate(0, 0, 1)
	q.Set("endDate.year", strconv.Itoa(end.Year()))
	q.Set("endDate.month", strconv.Itoa(int(end.Month())))
	q.Set("endDate.day", strconv.Itoa(end.Day()))
	q.Set("pageSize", "100")

	var out []Stat
	for page := 0; page < 10; page++ {
		u := strings.TrimSuffix(c.BaseURL, "/") + "/domains/" + url.PathEscape(strings.ToLower(domain)) + "/trafficStats?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusForbidden:
			return nil, ErrDomainNotFound
		case res.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("postmaster tools: HTTP %d", res.StatusCode)
		}
		var r struct {
			TrafficStats []struct {
				Name                  string   `json:"name"` // domains/x.com/trafficStats/20260101
				UserReportedSpamRatio *float64 `json:"userReportedSpamRatio"`
				DomainReputation      string   `json:"domainReputation"`
			} `json:"trafficStats"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("postmaster tools: %w", err)
		}
		for _, s := range r.TrafficStats {
			day, err := time.Parse("20060102", s.Name[strings.LastIndex(s.Name, "/")+1:])
			if err != nil {
				continue
			}
			rep := s.DomainReputation
			if rep == "REPUTATION_CATEGORY_UNSPECIFIED" {
				rep = ""
			}
			out = append(out, Stat{Day: day, SpamRatio: s.UserReportedSpamRatio, DomainReputation: rep})
		}
		if r.NextPageToken == "" {
			break
		}
		q.Set("pageToken", r.NextPageToken)
	}
	slices.SortFunc(out, func(a, b Stat) int { return b.Day.Compare(a.Day) })
	return out, nil
}
