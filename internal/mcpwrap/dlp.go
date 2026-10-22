package mcpwrap

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/patrikmichi/zuano-cli/internal/redact"
)

// DLP actions; approval needs a second person, so offline it blocks.
const (
	dlpAllow    = "allow"
	dlpRedact   = "redact"
	dlpApproval = "approval"
	dlpBlock    = "block"
)

var dlpStrictness = map[string]int{dlpAllow: 0, dlpRedact: 1, dlpApproval: 2, dlpBlock: 3}

// builtinClasses maps a detect entry to the categories the built-in detectors
// report and to the redaction classes behind each.
var builtinClasses = map[string]struct {
	category string
	classes  []string
}{
	"secrets":   {"secret", []string{redact.ClassSecret, redact.ClassURL}},
	"pii":       {"pii", []string{redact.ClassEmail, redact.ClassPhone, redact.ClassNationalID}},
	"financial": {"financial", []string{redact.ClassCard, redact.ClassIBAN}},
}

// CustomRule is one pattern or term list of a profile.
type CustomRule struct {
	Name       string   `json:"name"`
	Regex      string   `json:"regex"`
	Dictionary []string `json:"dictionary"`
	Action     string   `json:"action"`
	Category   string   `json:"category"`

	re *regexp.Regexp
}

// DLPProfile is what an upstream's `dlp` setting names: which detectors run on
// the arguments that leave and the results that come back, and what happens to
// each finding.
type DLPProfile struct {
	Detect   []string          `json:"detect"`
	Outbound map[string]string `json:"outbound"`
	Inbound  map[string]string `json:"inbound"`
	Custom   []CustomRule      `json:"custom"`
	External *struct {
		URL     string `json:"url"`
		OnError string `json:"on_error"`
	} `json:"external"`
}

// defaultDLPProfile is what an upstream without a named profile gets: secrets
// never leave and are redacted on the way back.
func defaultDLPProfile() *DLPProfile {
	return &DLPProfile{Detect: []string{"secrets"}, Outbound: map[string]string{"secret": dlpBlock}, Inbound: map[string]string{"secret": dlpRedact}}
}

func (p *DLPProfile) prepare() error {
	if p.External != nil {
		return errors.New("external needs a network service; an offline profile runs the built-in and custom detectors only")
	}
	if p.Detect == nil {
		p.Detect = []string{"secrets"}
	}
	for i := range p.Custom {
		r := &p.Custom[i]
		if r.Category == "" {
			r.Category = "custom"
		}
		if r.Regex != "" {
			re, err := regexp.Compile(r.Regex)
			if err != nil {
				return fmt.Errorf("%s: %w", r.Name, err)
			}
			r.re = re
		}
		if len(r.Dictionary) > 0 {
			quoted := make([]string, len(r.Dictionary))
			for j, t := range r.Dictionary {
				quoted[j] = regexp.QuoteMeta(t)
			}
			r.re = regexp.MustCompile("(?i)" + strings.Join(quoted, "|"))
		}
	}
	return nil
}

// DLPVerdict is the outcome for one value.
type DLPVerdict struct {
	// Action is the strictest action any finding asked for.
	Action string
	// Value is the input with the redact findings replaced.
	Value any
	// Summary counts findings by "category:action"; it never holds the text.
	Summary map[string]int
}

// Evaluate screens a decoded JSON value. direction is "outbound" or "inbound".
func (p *DLPProfile) Evaluate(direction string, value any) DLPVerdict {
	rules := p.Outbound
	if direction == "inbound" {
		rules = p.Inbound
	}
	v := DLPVerdict{Action: dlpAllow, Summary: map[string]int{}}
	v.Value = p.walk(value, rules, &v)
	return v
}

func (p *DLPProfile) walk(value any, rules map[string]string, v *DLPVerdict) any {
	switch x := value.(type) {
	case string:
		return p.leaf(x, rules, v)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = p.walk(x[i], rules, v)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = p.walk(e, rules, v)
		}
		return out
	}
	return value
}

func (v *DLPVerdict) note(category, action string, n int) {
	if action == dlpAllow || n == 0 {
		return
	}
	v.Summary[category+":"+action] += n
	if dlpStrictness[action] > dlpStrictness[v.Action] {
		v.Action = action
	}
}

type span struct{ start, end int }

func (p *DLPProfile) leaf(s string, rules map[string]string, v *DLPVerdict) string {
	var spans []span
	for _, r := range p.Custom {
		for _, at := range r.find(s) {
			v.note(r.Category, r.Action, 1)
			if r.Action == dlpRedact {
				spans = append(spans, at)
			}
		}
	}
	out := s
	if len(spans) > 0 {
		out = redactSpans(s, spans)
	}
	if len(p.Detect) == 0 {
		return out
	}
	red := redact.New(nil)
	scrubbed := red.Text(out)
	counts := red.Counts()
	redactBuiltin := false
	for _, name := range p.Detect {
		b := builtinClasses[name]
		n := 0
		for _, class := range b.classes {
			n += counts[class]
		}
		action := rules[b.category]
		if action == "" {
			action = dlpAllow
		}
		v.note(b.category, action, n)
		if n > 0 && action == dlpRedact {
			redactBuiltin = true
		}
	}
	if redactBuiltin {
		return scrubbed
	}
	return out
}

func (r CustomRule) find(s string) []span {
	var out []span
	if r.re != nil {
		for _, m := range r.re.FindAllStringIndex(s, -1) {
			if m[1] > m[0] {
				out = append(out, span{m[0], m[1]})
			}
		}
	}
	return out
}

// redactSpans replaces the spans, merging those that overlap into one.
func redactSpans(s string, spans []span) string {
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	at := 0
	for i := 0; i < len(spans); {
		start, end := spans[i].start, spans[i].end
		for i++; i < len(spans) && spans[i].start <= end; i++ {
			end = max(end, spans[i].end)
		}
		b.WriteString(s[at:start])
		b.WriteString("[REDACTED]")
		at = end
	}
	b.WriteString(s[at:])
	return b.String()
}

// Describe names the findings of a verdict for a message, without the text.
func (v DLPVerdict) Describe() string {
	keys := sortedKeys(v.Summary)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s ×%d", k, v.Summary[k])
	}
	return strings.Join(parts, ", ")
}
