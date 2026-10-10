// Package segments is the segment builder (S12): saved rules such as "tier
// is Active AND country is DE" that pick contacts when used.
//
// Rules are JSON in a fixed shape. Compile checks every field and operator
// against a whitelist and turns them into a WHERE clause with numbered
// parameters: no text from the rules ever becomes SQL.
//
//	{"match": "all", "groups": [
//	  {"match": "any", "conditions": [
//	    {"field": "tier", "op": "in", "value": ["active", "warm"]},
//	    {"field": "tag", "op": "has", "value": "vip"}]}]}
//
// People who mustn't be emailed (suppressed, unsubscribed, unconfirmed,
// sunset, frequency cap) are still removed at send time, whatever a segment says.
package segments

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kmrifat/klavio-alternative/api/internal/store"
)

type Rules struct {
	Match  string  `json:"match"` // all | any: how groups combine
	Groups []Group `json:"groups"`
}

type Group struct {
	Match      string      `json:"match"` // all | any: how conditions combine
	Conditions []Condition `json:"conditions"`
}

type Condition struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Limits keep rules (and their SQL) small.
const (
	MaxGroups     = 10
	MaxConditions = 20 // per group
	MaxList       = 50 // values in an "in" list
	MaxText       = 200
)

type kind string

const (
	kText    kind = "text"
	kEnum    kind = "enum"
	kTag     kind = "tag"
	kTopic   kind = "topic"
	kDate    kind = "date"
	kCountry kind = "country"
)

// Field is one thing rules can test, as the S12 editor shows it.
type Field struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Kind    kind     `json:"kind"`
	Ops     []string `json:"ops"`
	Options []string `json:"options,omitempty"` // enum values
	column  string
}

var (
	textOps    = []string{"equals", "not_equals", "contains", "starts_with", "ends_with", "is_empty", "is_not_empty"}
	enumOps    = []string{"is", "is_not", "in", "not_in"}
	dateOps    = []string{"within_days", "older_than_days", "before", "after", "is_empty", "is_not_empty"}
	countryOps = []string{"is", "is_not", "in", "not_in", "is_empty"}
)

// Fields is the whitelist, in the order the editor lists them.
var Fields = []Field{
	{Key: "tier", Label: "Engagement tier", Kind: kEnum, Ops: enumOps, Options: []string{"active", "warm", "at_risk", "sunset"}, column: "(" + store.TierSQL + ")"},
	{Key: "status", Label: "Status", Kind: kEnum, Ops: enumOps, Options: []string{"subscribed", "pending_confirmation", "quarantined", "unsubscribed"}, column: "c.status"},
	{Key: "consent_basis", Label: "Consent", Kind: kEnum, Ops: enumOps, Options: []string{"double_opt_in", "single_opt_in", "soft_opt_in"}, column: "c.consent_basis"},
	{Key: "tag", Label: "Tag", Kind: kTag, Ops: []string{"has", "has_not"}, column: "c.tags"},
	{Key: "topic", Label: "Topic", Kind: kTopic, Ops: []string{"subscribed", "not_subscribed"}},
	{Key: "email", Label: "Email", Kind: kText, Ops: textOps, column: "c.email"},
	{Key: "first_name", Label: "First name", Kind: kText, Ops: textOps, column: "c.first_name"},
	{Key: "last_name", Label: "Last name", Kind: kText, Ops: textOps, column: "c.last_name"},
	{Key: "country", Label: "Country", Kind: kCountry, Ops: countryOps, column: "c.country"},
	{Key: "timezone", Label: "Time zone", Kind: kText, Ops: textOps, column: "c.timezone"},
	{Key: "last_engaged_at", Label: "Last engaged", Kind: kDate, Ops: dateOps, column: "c.last_engaged_at"},
	{Key: "last_click_at", Label: "Last click", Kind: kDate, Ops: dateOps, column: "c.last_click_at"},
	{Key: "consented_at", Label: "Opted in", Kind: kDate, Ops: dateOps, column: "c.consented_at"},
	{Key: "created_at", Label: "Added", Kind: kDate, Ops: dateOps, column: "c.created_at"},
}

func field(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Error is a rule problem, pointing at the group and condition (0-based).
type Error struct {
	Group, Condition int
	Msg              string
}

func (e *Error) Error() string {
	if e.Group < 0 {
		return e.Msg
	}
	if e.Condition < 0 {
		return fmt.Sprintf("group %d: %s", e.Group+1, e.Msg)
	}
	return fmt.Sprintf("group %d, condition %d: %s", e.Group+1, e.Condition+1, e.Msg)
}

var countryRe = regexp.MustCompile(`^[A-Za-z]{2}$`)

// Compiled is a WHERE fragment over contacts c (joined to suppressions s).
// Its parameters are numbered from first.
type Compiled struct {
	SQL  string
	Args []any
}

// Compile validates the rules and builds SQL. Parameters start at $first
// (the caller's own come before, e.g. $1 = brand id). Empty rules match
// everyone.
func Compile(r Rules, first int) (Compiled, error) {
	b := &builder{next: first}
	if len(r.Groups) == 0 {
		return Compiled{SQL: "true"}, nil
	}
	if len(r.Groups) > MaxGroups {
		return Compiled{}, &Error{-1, -1, fmt.Sprintf("at most %d groups", MaxGroups)}
	}
	top, err := joiner(r.Match)
	if err != nil {
		return Compiled{}, &Error{-1, -1, err.Error()}
	}
	var groups []string
	for gi, g := range r.Groups {
		if len(g.Conditions) == 0 {
			return Compiled{}, &Error{gi, -1, "add at least one condition"}
		}
		if len(g.Conditions) > MaxConditions {
			return Compiled{}, &Error{gi, -1, fmt.Sprintf("at most %d conditions", MaxConditions)}
		}
		j, err := joiner(g.Match)
		if err != nil {
			return Compiled{}, &Error{gi, -1, err.Error()}
		}
		var conds []string
		for ci, c := range g.Conditions {
			sql, err := b.condition(c)
			if err != nil {
				return Compiled{}, &Error{gi, ci, err.Error()}
			}
			conds = append(conds, sql)
		}
		groups = append(groups, "("+strings.Join(conds, j)+")")
	}
	return Compiled{SQL: "(" + strings.Join(groups, top) + ")", Args: b.args}, nil
}

func joiner(m string) (string, error) {
	switch m {
	case "all", "":
		return " AND ", nil
	case "any":
		return " OR ", nil
	}
	return "", fmt.Errorf("match must be all or any")
}

type builder struct {
	next int
	args []any
}

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	p := fmt.Sprintf("$%d", b.next)
	b.next++
	return p
}

func (b *builder) condition(c Condition) (string, error) {
	f, ok := field(c.Field)
	if !ok {
		return "", fmt.Errorf("unknown field %q", c.Field)
	}
	if !contains(f.Ops, c.Op) {
		return "", fmt.Errorf("%s can't use %q", f.Label, c.Op)
	}
	switch f.Kind {
	case kText, kCountry:
		return b.text(f, c)
	case kEnum:
		return b.enum(f, c)
	case kTag:
		tag, err := str(c.Value, MaxText)
		if err != nil || tag == "" {
			return "", fmt.Errorf("%s needs a tag", f.Label)
		}
		if c.Op == "has" {
			return b.arg(tag) + " = ANY(c.tags)", nil
		}
		return "NOT (" + b.arg(tag) + " = ANY(c.tags))", nil
	case kTopic:
		s, err := str(c.Value, 64)
		id, perr := uuid.Parse(s)
		if err != nil || perr != nil {
			return "", fmt.Errorf("pick a topic")
		}
		in := "EXISTS (SELECT 1 FROM contact_topics ct WHERE ct.contact_id = c.id AND ct.topic_id = " + b.arg(id) + ")"
		if c.Op == "subscribed" {
			return in, nil
		}
		return "NOT " + in, nil
	case kDate:
		return b.date(f, c)
	}
	return "", fmt.Errorf("unsupported field")
}

func (b *builder) text(f Field, c Condition) (string, error) {
	switch c.Op {
	case "is_empty":
		return f.column + " = ''", nil
	case "is_not_empty":
		return f.column + " <> ''", nil
	case "in", "not_in":
		list, err := strList(c.Value)
		if err != nil {
			return "", err
		}
		for i, v := range list {
			if f.Kind == kCountry && !countryRe.MatchString(v) {
				return "", fmt.Errorf("%q isn't a two-letter country code", v)
			}
			list[i] = strings.ToUpper(v)
		}
		sql := "upper(" + f.column + ") = ANY(" + b.arg(list) + ")"
		if c.Op == "not_in" {
			sql = "NOT (" + sql + ")"
		}
		return sql, nil
	}
	v, err := str(c.Value, MaxText)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("%s needs a value", f.Label)
	}
	if f.Kind == kCountry {
		if !countryRe.MatchString(v) {
			return "", fmt.Errorf("%q isn't a two-letter country code", v)
		}
		if c.Op == "is" {
			return "upper(" + f.column + ") = " + b.arg(strings.ToUpper(v)), nil
		}
		return "upper(" + f.column + ") <> " + b.arg(strings.ToUpper(v)), nil
	}
	lower := "lower(" + f.column + ")"
	v = strings.ToLower(v)
	switch c.Op {
	case "equals":
		return lower + " = " + b.arg(v), nil
	case "not_equals":
		return lower + " <> " + b.arg(v), nil
	case "contains":
		return lower + " LIKE " + b.arg("%"+escapeLike(v)+"%"), nil
	case "starts_with":
		return lower + " LIKE " + b.arg(escapeLike(v)+"%"), nil
	case "ends_with":
		return lower + " LIKE " + b.arg("%"+escapeLike(v)), nil
	}
	return "", fmt.Errorf("unsupported operator")
}

func (b *builder) enum(f Field, c Condition) (string, error) {
	var vals []string
	if c.Op == "in" || c.Op == "not_in" {
		list, err := strList(c.Value)
		if err != nil {
			return "", err
		}
		vals = list
	} else {
		v, err := str(c.Value, MaxText)
		if err != nil {
			return "", err
		}
		vals = []string{v}
	}
	if len(vals) == 0 {
		return "", fmt.Errorf("%s needs a value", f.Label)
	}
	for _, v := range vals {
		if !contains(f.Options, v) {
			return "", fmt.Errorf("%q isn't a %s", v, strings.ToLower(f.Label))
		}
	}
	sql := f.column + " = ANY(" + b.arg(vals) + ")"
	if c.Op == "is_not" || c.Op == "not_in" {
		sql = "NOT (" + sql + ")"
	}
	return sql, nil
}

func (b *builder) date(f Field, c Condition) (string, error) {
	switch c.Op {
	case "is_empty":
		return f.column + " IS NULL", nil
	case "is_not_empty":
		return f.column + " IS NOT NULL", nil
	case "within_days", "older_than_days":
		var n int
		if err := json.Unmarshal(c.Value, &n); err != nil || n < 1 || n > 3650 {
			return "", fmt.Errorf("days must be a whole number from 1 to 3650")
		}
		p := "make_interval(days => " + b.arg(n) + ")"
		if c.Op == "within_days" {
			return f.column + " >= now() - " + p, nil
		}
		// "Older than" includes never: someone who never clicked hasn't clicked in 90 days.
		return "(" + f.column + " < now() - " + p + " OR " + f.column + " IS NULL)", nil
	}
	s, err := str(c.Value, 10)
	if err != nil {
		return "", err
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return "", fmt.Errorf("dates look like 2026-01-31")
	}
	if c.Op == "before" {
		return f.column + " < " + b.arg(d), nil
	}
	return f.column + " >= " + b.arg(d.AddDate(0, 0, 1)), nil // after = from the next day
}

func str(raw json.RawMessage, max int) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("value must be text")
	}
	s = strings.TrimSpace(s)
	if len([]rune(s)) > max {
		return "", fmt.Errorf("value is too long")
	}
	return s, nil
}

func strList(raw json.RawMessage) ([]string, error) {
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("value must be a list")
	}
	if len(list) == 0 || len(list) > MaxList {
		return nil, fmt.Errorf("pick between 1 and %d values", MaxList)
	}
	for i, v := range list {
		v = strings.TrimSpace(v)
		if v == "" || len([]rune(v)) > MaxText {
			return nil, fmt.Errorf("a value is empty or too long")
		}
		list[i] = v
	}
	return list, nil
}

func contains(list []string, v string) bool { return slices.Contains(list, v) }

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
