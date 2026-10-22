package mcpwrap

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

//go:embed schema/firewall-sections.json
var sectionsSchemaJSON []byte

var compiledSections = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(sectionsSchemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("mem://firewall-sections.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("mem://firewall-sections.json")
})

// SectionsSchema is the JSON Schema of the file: the `firewall:` and `dlp:`
// sections of the organisation's policy document, in the gateway's spelling.
func SectionsSchema() []byte { return sectionsSchemaJSON }

// Policy is the offline firewall policy: the `firewall:` and `dlp:` sections of
// the organisation's policy document, kept per server.
type Policy struct {
	Firewall struct {
		Upstreams map[string]UpstreamPolicy `json:"upstreams"`
	} `json:"firewall"`
	DLP struct {
		Profiles map[string]*DLPProfile `json:"profiles"`
	} `json:"dlp"`
}

// UpstreamPolicy is the policy of one server. Settings that act on a gateway's
// session, such as sampling, rate limits and token exchange, are validated
// with the rest of the file and have no effect without one.
type UpstreamPolicy struct {
	// OnDefinitionChange is block (hide a changed tool until reviewed) or warn
	// (keep serving the pinned definition).
	OnDefinitionChange string `json:"on_definition_change"`
	// NewTools is hidden (a new tool waits for approval) or auto_read_only (a
	// new tool classified read is pinned at once).
	NewTools string `json:"new_tools"`
	Tools    struct {
		Allow    []string          `json:"allow"`
		Deny     []string          `json:"deny"`
		Classify map[string]string `json:"classify"`
	} `json:"tools"`
	Approvals struct {
		PeopleRequireApproval []string `json:"people_require_approval"`
	} `json:"approvals"`
	// DLP names the profile that screens arguments and results.
	DLP *string `json:"dlp"`

	// Profile is the profile DLP names, or the default one.
	Profile *DLPProfile `json:"-"`
}

// LoadPolicy reads firewall.yaml; a missing file is the default policy. The
// file must match the schema of the policy document's sections, so a typo or
// a setting the gateway does not know is an error instead of being ignored.
func LoadPolicy(file string) (*Policy, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return &Policy{}, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := ParsePolicy(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return p, nil
}

// ParsePolicy validates and reads a policy document.
func ParsePolicy(raw []byte) (*Policy, error) {
	var generic any
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&generic); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if generic == nil {
		generic = map[string]any{}
	}
	asJSON, err := json.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("%w (only plain YAML is accepted)", err)
	}
	schema, err := compiledSections()
	if err != nil {
		return nil, fmt.Errorf("the embedded schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(inst); err != nil {
		return nil, errors.New(schemaProblems(err))
	}
	p := &Policy{}
	if err := json.Unmarshal(asJSON, p); err != nil {
		return nil, err
	}
	if err := p.check(); err != nil {
		return nil, err
	}
	return p, nil
}

func schemaProblems(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	var leaves []string
	var walk func(jsonschema.OutputUnit)
	walk = func(u jsonschema.OutputUnit) {
		if len(u.Errors) == 0 {
			if u.Error != nil {
				leaves = append(leaves, u.InstanceLocation+": "+u.Error.String())
			}
			return
		}
		for _, c := range u.Errors {
			walk(c)
		}
	}
	walk(*ve.DetailedOutput())
	sort.Strings(leaves)
	if len(leaves) > 5 {
		leaves = append(leaves[:5], fmt.Sprintf("and %d more", len(leaves)-5))
	}
	return strings.Join(leaves, "; ")
}

// check holds what the schema cannot say: the named profile exists, a custom
// pattern compiles, and nothing asks for what needs a gateway.
func (p *Policy) check() error {
	for _, name := range sortedKeys(p.DLP.Profiles) {
		if err := p.DLP.Profiles[name].prepare(); err != nil {
			return fmt.Errorf("dlp.profiles.%s: %w", name, err)
		}
	}
	for _, name := range sortedKeys(p.Firewall.Upstreams) {
		u := p.Firewall.Upstreams[name]
		if u.OnDefinitionChange == "auto" {
			return fmt.Errorf("firewall.upstreams.%s.on_definition_change: auto would accept changes nobody reviewed; offline it is block or warn", name)
		}
		if u.DLP != nil && p.DLP.Profiles[*u.DLP] == nil {
			return fmt.Errorf("firewall.upstreams.%s.dlp: profile %q is not defined", name, *u.DLP)
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// For returns the policy of one server.
func (p *Policy) For(name string) UpstreamPolicy {
	u := p.Firewall.Upstreams[name]
	if u.OnDefinitionChange == "" {
		u.OnDefinitionChange = "block"
	}
	if u.NewTools == "" {
		u.NewTools = "hidden"
	}
	u.Profile = defaultDLPProfile()
	if u.DLP != nil {
		u.Profile = p.DLP.Profiles[*u.DLP]
	}
	return u
}

var globCache sync.Map

// globMatches matches a tool name the way the gateway does: `*` is any run of
// characters, `?` one character, everything else is literal.
func globMatches(pattern, value string) bool {
	re, ok := globCache.Load(pattern)
	if !ok {
		quoted := strings.NewReplacer(`\*`, `.*`, `\?`, `.`).Replace(regexp.QuoteMeta(pattern))
		re = regexp.MustCompile(`(?s)^` + quoted + `$`)
		globCache.Store(pattern, re)
	}
	return re.(*regexp.Regexp).MatchString(value)
}

func matchesAny(patterns []string, value string) bool {
	return slices.ContainsFunc(patterns, func(g string) bool { return globMatches(g, value) })
}

// Denied reports whether the tool is kept out: a deny glob covers it, or an
// allow list exists and none of its globs do.
func (u UpstreamPolicy) Denied(tool string) bool {
	if matchesAny(u.Tools.Deny, tool) {
		return true
	}
	return len(u.Tools.Allow) > 0 && !matchesAny(u.Tools.Allow, tool)
}

// NeedsApproval reports whether a call of this risk class must wait for a
// person's approval, which offline mode cannot give.
func (u UpstreamPolicy) NeedsApproval(risk string) bool {
	return slices.Contains(u.Approvals.PeopleRequireApproval, risk)
}

var verbRisk = []struct {
	risk  string
	verbs []string
}{
	{"destructive", []string{"delete", "drop", "purge", "destroy", "terminate", "remove", "wipe", "truncate"}},
	{"outward", []string{"send", "post", "publish", "notify", "invite", "share", "transfer", "pay", "refund", "email", "tweet", "broadcast"}},
	{"write", []string{"create", "update", "set", "write", "upload", "merge", "close", "edit", "add", "put", "patch", "move", "rename", "run", "exec"}},
	{"read", []string{"get", "list", "search", "read", "describe", "find", "fetch", "show", "query", "lookup", "view"}},
}

// Classify gives a tool's risk class: the administrator's classification, or
// from its name, and write when nothing says otherwise. A read-only hint does
// not lower it.
func (u UpstreamPolicy) Classify(def Definition) string {
	name := nameOf(def)
	if r, ok := u.Tools.Classify[name]; ok {
		return r
	}
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '_' || r == '-' || r == '.' || r == ' ' })
	if len(words) == 0 {
		return "write"
	}
	for _, vr := range verbRisk {
		if slices.Contains(vr.verbs, words[0]) {
			return vr.risk
		}
	}
	if ann, _ := def["annotations"].(map[string]any); ann != nil {
		if d, _ := ann["destructiveHint"].(bool); d {
			return "destructive"
		}
	}
	return "write"
}
