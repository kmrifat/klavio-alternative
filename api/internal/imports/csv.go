// Package imports runs the safety-checked contact import (S4): upload a CSV,
// match columns, answer where the contacts came from, check every address,
// and only then import.
package imports

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxFileBytes = 20 << 20 // 20 MB
	MaxRows      = 200_000
)

// Mapping says which CSV column feeds each field ("" = not in the file).
type Mapping struct {
	Email         string `json:"email"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	Tags          string `json:"tags"`
	ConsentedAt   string `json:"consented_at"`    // when they signed up / confirmed
	LastEngagedAt string `json:"last_engaged_at"` // last click (or activity) in the old tool
	Rating        string `json:"rating"`          // engagement stars, e.g. Mailchimp MEMBER_RATING (1–5)
	Timezone      string `json:"timezone"`
	Country       string `json:"country"`
}

func (m Mapping) columns() map[string]string {
	return map[string]string{
		"email": m.Email, "first_name": m.FirstName, "last_name": m.LastName, "tags": m.Tags,
		"consented_at": m.ConsentedAt, "last_engaged_at": m.LastEngagedAt, "rating": m.Rating,
		"timezone": m.Timezone, "country": m.Country,
	}
}

// Table is a parsed CSV.
type Table struct {
	Headers []string
	Rows    [][]string
}

var errEmptyFile = errors.New("the file is empty")

// Parse reads a CSV export. It accepts a UTF-8 BOM and comma, semicolon or
// tab delimiters (Excel in many locales saves with semicolons).
func Parse(data []byte) (Table, error) {
	if len(data) > MaxFileBytes {
		return Table{}, fmt.Errorf("the file is larger than %d MB", MaxFileBytes>>20)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return Table{}, errors.New("the file isn't UTF-8 text; export it again as CSV UTF-8")
	}
	firstLine, _, _ := bytes.Cut(data, []byte("\n"))
	if len(bytes.TrimSpace(firstLine)) == 0 {
		return Table{}, errEmptyFile
	}

	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = guessDelimiter(firstLine)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	headers, err := r.Read()
	if err != nil {
		return Table{}, fmt.Errorf("reading the header row: %w", err)
	}
	for i, h := range headers {
		headers[i] = strings.TrimSpace(h)
	}
	seen := map[string]bool{}
	for _, h := range headers {
		if h != "" && seen[h] {
			return Table{}, fmt.Errorf("the column %q appears twice", h)
		}
		seen[h] = true
	}

	t := Table{Headers: headers}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Table{}, fmt.Errorf("line %d: %w", len(t.Rows)+2, err)
		}
		if blank(rec) {
			continue
		}
		if len(t.Rows) >= MaxRows {
			return Table{}, fmt.Errorf("the file has more than %d rows; split it into smaller files", MaxRows)
		}
		t.Rows = append(t.Rows, rec)
	}
	if len(t.Rows) == 0 {
		return Table{}, errors.New("the file has a header row but no contacts")
	}
	return t, nil
}

func guessDelimiter(line []byte) rune {
	best, bestN := ',', bytes.Count(line, []byte(","))
	for _, d := range []rune{';', '\t'} {
		if n := bytes.Count(line, []byte(string(d))); n > bestN {
			best, bestN = d, n
		}
	}
	return best
}

func blank(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// Get returns a row's value for a mapped column ("" if unmapped or missing).
func (t Table) Get(row []string, column string) string {
	if column == "" {
		return ""
	}
	for i, h := range t.Headers {
		if h == column && i < len(row) {
			return strings.TrimSpace(row[i])
		}
	}
	return ""
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func norm(h string) string { return nonAlnum.ReplaceAllString(strings.ToLower(h), "") }

// Candidate header names per field, best first. Covers Mailchimp, Klaviyo,
// Shopify and plain spreadsheets.
var known = []struct {
	field string
	names []string
}{
	{"email", []string{"emailaddress", "email", "email1", "emailaddr", "mail"}},
	{"first_name", []string{"firstname", "fname", "first", "givenname"}},
	{"last_name", []string{"lastname", "lname", "last", "surname", "familyname"}},
	{"tags", []string{"tags", "tag", "labels"}},
	{"consented_at", []string{"confirmtime", "optintime", "consentedat", "consenttimestamp", "subscribedat", "signupdate", "dateadded", "createdat", "created"}},
	{"last_engaged_at", []string{"lastclick", "lastclickdate", "lastclicked", "lastengaged", "lastengagement", "lastactive", "lastactivity"}},
	{"rating", []string{"memberrating", "rating", "starrating", "engagementrating"}},
	{"timezone", []string{"timezone", "tz"}},
	{"country", []string{"cc", "countrycode", "country"}},
}

// SuggestMapping guesses the mapping from the header row.
func SuggestMapping(headers []string) Mapping {
	byNorm := map[string]string{}
	for _, h := range headers {
		if _, ok := byNorm[norm(h)]; !ok {
			byNorm[norm(h)] = h
		}
	}
	pick := func(names []string) string {
		for _, n := range names {
			if h, ok := byNorm[n]; ok {
				return h
			}
		}
		return ""
	}
	var m Mapping
	for _, k := range known {
		v := pick(k.names)
		switch k.field {
		case "email":
			m.Email = v
		case "first_name":
			m.FirstName = v
		case "last_name":
			m.LastName = v
		case "tags":
			m.Tags = v
		case "consented_at":
			m.ConsentedAt = v
		case "last_engaged_at":
			m.LastEngagedAt = v
		case "rating":
			m.Rating = v
		case "timezone":
			m.Timezone = v
		case "country":
			m.Country = v
		}
	}
	return m
}

// Validate checks that the mapping names columns that exist.
func (m Mapping) Validate(headers []string) error {
	if m.Email == "" {
		return errors.New("choose the column that holds email addresses")
	}
	has := map[string]bool{}
	for _, h := range headers {
		has[h] = true
	}
	for field, col := range m.columns() {
		if col != "" && !has[col] {
			return fmt.Errorf("%s: the file has no column %q", field, col)
		}
	}
	return nil
}

var dateLayouts = []string{
	time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02",
	"2006/01/02", "01/02/2006 15:04:05", "01/02/2006 15:04", "01/02/2006", "1/2/2006", "Jan 2, 2006", "2 Jan 2006",
}

// ParseDate understands the formats common in email-tool exports. Unknown → nil.
func ParseDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

// ParseTags splits `"vip","customer"` or `vip, customer` into clean, unique tags.
func ParseTags(s string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '|' }) {
		p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"'`))
		if p == "" || len(p) > 64 || seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	return out
}
