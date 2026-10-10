package postmaster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotConnected(t *testing.T) {
	c, err := New(context.Background(), "")
	if c != nil || err != nil {
		t.Fatalf("New(\"\") = %v, %v", c, err)
	}
	if _, err := c.TrafficStats(context.Background(), "news.example.com", time.Now(), time.Now()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
}

func TestRejectsOtherCredentialTypes(t *testing.T) {
	f := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(f, []byte(`{"type":"external_account","credential_source":{"url":"http://169.254.169.254/"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), f); err == nil {
		t.Fatal("external_account credentials were accepted")
	}
}

func TestTrafficStats(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/domains/news.example.com/trafficStats":
			if r.URL.Query().Get("pageToken") == "" {
				gotQuery = r.URL.RawQuery
				w.Write([]byte(`{"trafficStats":[
					{"name":"domains/news.example.com/trafficStats/20261001","userReportedSpamRatio":0.0004,"domainReputation":"HIGH"}
				],"nextPageToken":"p2"}`))
				return
			}
			w.Write([]byte(`{"trafficStats":[
				{"name":"domains/news.example.com/trafficStats/20261003","userReportedSpamRatio":0.0021,"domainReputation":"LOW"},
				{"name":"domains/news.example.com/trafficStats/20261002","domainReputation":"REPUTATION_CATEGORY_UNSPECIFIED"}
			]}`))
		default:
			http.Error(w, `{"error":{"code":403}}`, http.StatusForbidden)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}

	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	got, err := c.TrafficStats(context.Background(), "News.Example.com", day(1), day(7))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got[0].Day.Equal(day(3)) || !got[2].Day.Equal(day(1)) {
		t.Fatalf("got %+v, want 3 days newest first", got)
	}
	if got[0].DomainReputation != "LOW" || *got[0].SpamRatio != 0.0021 {
		t.Errorf("day 3 = %+v", got[0])
	}
	if got[1].SpamRatio != nil || got[1].DomainReputation != "" {
		t.Errorf("day 2 should have no data: %+v", got[1])
	}
	// endDate is exclusive: asking up to the 7th sends the 8th.
	if want := "endDate.day=8"; !strings.Contains(gotQuery, want) {
		t.Errorf("query %q lacks %q", gotQuery, want)
	}

	if _, err := c.TrafficStats(context.Background(), "other.example.com", day(1), day(7)); !errors.Is(err, ErrDomainNotFound) {
		t.Errorf("unknown domain: err = %v, want ErrDomainNotFound", err)
	}
}
