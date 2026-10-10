// Package imports turns an uploaded CSV into checked contact rows: column
// mapping, per-row checks, the list-quality gate and quarantine.
package imports

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // timezone names in CSVs are checked against this
	"unicode"
)

// Field is a contact attribute a CSV column can be mapped to.
type Field string

const (
	FieldIgnore         Field = ""
	FieldEmail          Field = "email"
	FieldFirstName      Field = "first_name"
	FieldLastName       Field = "last_name"
	FieldFullName       Field = "full_name"
	FieldTimezone       Field = "timezone"
	FieldCountry        Field = "country"
	FieldTags           Field = "tags"
	FieldOptedInAt      Field = "opted_in_at"
	FieldConfirmedAt    Field = "confirmed_at"
	FieldLastActivityAt Field = "last_activity_at"
	FieldOptInIP        Field = "opt_in_ip"
	FieldRating         Field = "rating" // engagement stars from the old provider (Mailchimp MEMBER_RATING, 1–5)
)

var Fields = []Field{FieldEmail, FieldFirstName, FieldLastName, FieldFullName, FieldTimezone,
	FieldCountry, FieldTags, FieldOptedInAt, FieldConfirmedAt, FieldLastActivityAt, FieldOptInIP, FieldRating}

// Header names seen in Mailchimp, Klaviyo and hand-made exports, compared
// after lower-casing and dropping everything but letters and digits.
var headerAliases = map[string]Field{
	"email": FieldEmail, "emailaddress": FieldEmail, "mail": FieldEmail,
	"firstname": FieldFirstName, "fname": FieldFirstName, "givenname": FieldFirstName, "first": FieldFirstName,
	"lastname": FieldLastName, "lname": FieldLastName, "surname": FieldLastName, "familyname": FieldLastName, "last": FieldLastName,
	"name": FieldFullName, "fullname": FieldFullName,
	"timezone": FieldTimezone, "tz": FieldTimezone,
	"cc": FieldCountry, "country": FieldCountry, "countrycode": FieldCountry,
	"tags":      FieldTags,
	"optintime": FieldOptedInAt, "optindate": FieldOptedInAt, "subscribedat": FieldOptedInAt,
	"subscribedate": FieldOptedInAt, "signupdate": FieldOptedInAt, "dateadded": FieldOptedInAt,
	"consenttimestamp": FieldOptedInAt,
	"confirmtime":      FieldConfirmedAt, "confirmedat": FieldConfirmedAt,
	"lastactive": FieldLastActivityAt, "lastactivity": FieldLastActivityAt, "lastclick": FieldLastActivityAt,
	"lastclicked": FieldLastActivityAt, "lastclickdate": FieldLastActivityAt, "lastengaged": FieldLastActivityAt,
	"optinip": FieldOptInIP, "ipaddress": FieldOptInIP, "signupip": FieldOptInIP,
	"memberrating": FieldRating, "rating": FieldRating, "starrating": FieldRating,
}

func headerKey(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Mapping says which field each CSV column feeds, by column position.
type Mapping []Field

// SuggestMapping guesses a mapping from the header row. Each field is used
// at most once, by the first column that matches it.
func SuggestMapping(headers []string) Mapping {
	m := make(Mapping, len(headers))
	used := map[Field]bool{}
	for i, h := range headers {
		if f, ok := headerAliases[headerKey(h)]; ok && !used[f] {
			m[i] = f
			used[f] = true
		}
	}
	return m
}

// Validate checks the mapping fits the headers: exactly one email column and
// no field fed by two columns.
func (m Mapping) Validate(headers []string) error {
	if len(m) != len(headers) {
		return fmt.Errorf("mapping has %d columns, file has %d", len(m), len(headers))
	}
	seen := map[Field]int{}
	for i, f := range m {
		if f == FieldIgnore {
			continue
		}
		if !knownField(f) {
			return fmt.Errorf("column %q: unknown field %q", headers[i], f)
		}
		if j, dup := seen[f]; dup {
			return fmt.Errorf("columns %q and %q are both mapped to %s", headers[j], headers[i], f)
		}
		seen[f] = i
	}
	if _, ok := seen[FieldEmail]; !ok {
		return errors.New("no column is mapped to email")
	}
	return nil
}

func knownField(f Field) bool {
	for _, k := range Fields {
		if f == k {
			return true
		}
	}
	return false
}

// Reader reads an uploaded CSV. It copes with a UTF-8 BOM and with
// semicolon- or tab-separated files (Excel in many locales).
type Reader struct {
	csv     *csv.Reader
	headers []string
}

func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	first, _ := br.Peek(4096)
	cr := csv.NewReader(br)
	cr.Comma = sniffDelimiter(first)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = false

	headers, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("the file is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("read header row: %w", err)
	}
	for i := range headers {
		headers[i] = strings.TrimSpace(headers[i])
	}
	return &Reader{csv: cr, headers: headers}, nil
}

// sniffDelimiter picks whichever of , ; or tab is most common on the first line.
func sniffDelimiter(head []byte) rune {
	line, _, _ := bytes.Cut(head, []byte("\n"))
	best, bestN := ',', bytes.Count(line, []byte(","))
	for _, d := range []rune{';', '\t'} {
		if n := bytes.Count(line, []byte(string(d))); n > bestN {
			best, bestN = d, n
		}
	}
	return best
}

func (r *Reader) Headers() []string { return r.headers }

// Next returns the next non-blank record and the line it starts on (the
// header is line 1), or io.EOF.
func (r *Reader) Next() ([]string, int, error) {
	for {
		rec, err := r.csv.Read()
		if err != nil {
			return nil, 0, err
		}
		if !blank(rec) {
			line, _ := r.csv.FieldPos(0)
			return rec, line, nil
		}
	}
}

func blank(rec []string) bool {
	for _, v := range rec {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// Row is one CSV record after mapping. Email is normalized once checked.
type Row struct {
	Line           int // where the record starts in the file, for error messages
	Email          string
	FirstName      string
	LastName       string
	Timezone       string
	Country        string
	Tags           []string
	OptedInAt      *time.Time
	ConfirmedAt    *time.Time
	LastActivityAt *time.Time
	OptInIP        string
	Rating         int // 1–5 stars, 0 when unknown
	Result         Result
}

// EngagedRating is the lowest star rating that counts as a sign of life.
// Mailchimp gives 4–5 stars only to people who open and click often, so they
// are treated as if they had clicked on the day of the import: never
// quarantined, and Active until that "click" ages out like a real one.
const EngagedRating = 4

// Engaged reports whether the old provider rated this person as engaged.
func (r Row) Engaged() bool { return r.Rating >= EngagedRating }

// MapRecord applies a mapping to one record. Values that cannot be
// understood (an unknown timezone, a date in an odd format) are dropped
// rather than failing the row: only the email decides whether a row is valid.
func MapRecord(m Mapping, line int, rec []string) Row {
	row := Row{Line: line}
	for i, f := range m {
		if i >= len(rec) {
			break
		}
		v := strings.TrimSpace(rec[i])
		if v == "" {
			continue
		}
		switch f {
		case FieldEmail:
			row.Email = v
		case FieldFirstName:
			row.FirstName = clip(v, 100)
		case FieldLastName:
			row.LastName = clip(v, 100)
		case FieldFullName:
			first, last, _ := strings.Cut(v, " ")
			if row.FirstName == "" {
				row.FirstName = clip(first, 100)
			}
			if row.LastName == "" {
				row.LastName = clip(strings.TrimSpace(last), 100)
			}
		case FieldTimezone:
			if _, err := time.LoadLocation(v); err == nil && v != "Local" {
				row.Timezone = v
			}
		case FieldCountry:
			if len(v) == 2 && isLetters(v) {
				row.Country = strings.ToUpper(v)
			}
		case FieldTags:
			row.Tags = parseTags(v)
		case FieldOptedInAt:
			row.OptedInAt = parseTime(v)
		case FieldConfirmedAt:
			row.ConfirmedAt = parseTime(v)
		case FieldLastActivityAt:
			row.LastActivityAt = parseTime(v)
		case FieldOptInIP:
			row.OptInIP = clip(v, 64)
		case FieldRating:
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 5 {
				row.Rating = n
			}
		}
	}
	return row
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func isLetters(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) || r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// parseTags reads a cell like `"VIP","Spring sale"` (Mailchimp) or
// `VIP; Spring sale`.
func parseTags(v string) []string {
	sep := ","
	if !strings.Contains(v, ",") && strings.Contains(v, ";") {
		sep = ";"
	}
	var tags []string
	seen := map[string]bool{}
	for _, t := range strings.Split(v, sep) {
		t = strings.TrimSpace(strings.Trim(strings.TrimSpace(t), `"`))
		if t == "" || len(t) > 100 || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	return tags
}

// Day-first and month-first dates (01/02/2024) are ambiguous, so they are
// not accepted; every layout here is year-first.
var timeLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
}

// parseTime reads a timestamp; ones without a zone are taken as UTC.
func parseTime(v string) *time.Time {
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, v); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}
