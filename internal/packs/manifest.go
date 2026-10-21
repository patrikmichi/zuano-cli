// Package packs reads, checks and shows starter packs: versioned documents
// that set an organisation up for one kind of work in one step.
package packs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/patrikmichi/zuano-cli/internal/brand"
)

// Format is the manifest format this client understands.
const Format = brand.Name + ".pack/v1"

// Limits the gateway enforces too; checking them first saves a round trip.
const (
	MaxManifestBytes = 256 << 10
	MaxRules         = 50
	MaxRuleBytes     = 16 << 10
)

var (
	namePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	placeholderRe = regexp.MustCompile(`\{\{\s*([^}]*?)\s*\}\}`)
	paramKeyRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	workflowKeyRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	workflowRefRe = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{1,38}[a-z0-9]/)?[a-z0-9-]{3,64}@[\^~]?\d+(\.\d+){0,2}$`)
	paramTypes    = map[string]bool{"string": true, "number": true, "boolean": true, "enum": true, "group": true}
)

// Manifest is the authoring form (YAML) of a pack.
type Manifest struct {
	Format      string      `yaml:"format" json:"format"`
	Name        string      `yaml:"name" json:"name"`
	Version     string      `yaml:"version" json:"version"`
	DisplayName string      `yaml:"displayName" json:"displayName"`
	Summary     string      `yaml:"summary" json:"summary"`
	Category    string      `yaml:"category,omitempty" json:"category,omitempty"`
	Icon        string      `yaml:"icon,omitempty" json:"icon,omitempty"`
	Locales     []string    `yaml:"locales,omitempty" json:"locales,omitempty"`
	BasedOn     string      `yaml:"basedOn,omitempty" json:"basedOn,omitempty"`
	Params      []Param     `yaml:"params,omitempty" json:"params,omitempty"`
	Connectors  []Connector `yaml:"connectors,omitempty" json:"connectors,omitempty"`
	Groups      []Group     `yaml:"groups,omitempty" json:"groups,omitempty"`
	Skills      []Ref       `yaml:"skills,omitempty" json:"skills,omitempty"`
	Bundles     []Ref       `yaml:"bundles,omitempty" json:"bundles,omitempty"`
	Rules       []Rule      `yaml:"rules,omitempty" json:"rules,omitempty"`
	Risk        *Risk       `yaml:"risk,omitempty" json:"risk,omitempty"`
	Approvals   *Approvals  `yaml:"approvals,omitempty" json:"approvals,omitempty"`
	Workflows   []Workflow  `yaml:"workflows,omitempty" json:"workflows,omitempty"`
}

// Param is a value the installer supplies.
type Param struct {
	Key     string   `yaml:"key" json:"key"`
	Label   string   `yaml:"label" json:"label"`
	Type    string   `yaml:"type" json:"type"`
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
	Min     *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max     *float64 `yaml:"max,omitempty" json:"max,omitempty"`
	Default any      `yaml:"default,omitempty" json:"default,omitempty"`
}

// Connector is a service the pack uses.
type Connector struct {
	Slug            string   `yaml:"slug" json:"slug"`
	Required        bool     `yaml:"required" json:"required"`
	Alternatives    []string `yaml:"alternatives,omitempty" json:"alternatives,omitempty"`
	Scopes          []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	CredentialScope string   `yaml:"credentialScope,omitempty" json:"credentialScope,omitempty"`
	Why             string   `yaml:"why,omitempty" json:"why,omitempty"`
}

// Group maps a pack group key to a parameter.
type Group struct {
	Key  string `yaml:"key" json:"key"`
	From string `yaml:"from" json:"from"`
}

// Ref pins a catalog skill or plugin bundle and says who gets it.
type Ref struct {
	Ref    string   `yaml:"ref" json:"ref"`
	Assign []string `yaml:"assign,omitempty" json:"assign,omitempty"`
}

// Rule is text written into every member's coding agents.
type Rule struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	AlwaysApply bool     `yaml:"alwaysApply,omitempty" json:"alwaysApply,omitempty"`
	Globs       []string `yaml:"globs,omitempty" json:"globs,omitempty"`
	Agents      []string `yaml:"agents,omitempty" json:"agents,omitempty"`
	Body        string   `yaml:"body" json:"body"`
}

// Risk is the pack's risk-policy preset.
type Risk struct {
	Matrix        []map[string]any `yaml:"matrix,omitempty" json:"matrix,omitempty"`
	ToolOverrides []map[string]any `yaml:"toolOverrides,omitempty" json:"toolOverrides,omitempty"`
}

// Approvals designates approvers and caps time-boxed grants.
type Approvals struct {
	ApproverGroups  []map[string]any `yaml:"approverGroups,omitempty" json:"approverGroups,omitempty"`
	GrantMaxMinutes int              `yaml:"grantMaxMinutes,omitempty" json:"grantMaxMinutes,omitempty"`
}

// Workflow is a workflow installed as a draft; a pack never enables it or
// carries grants, pre-approvals or a run-as.
type Workflow struct {
	Key    string         `yaml:"key" json:"key"`
	Ref    string         `yaml:"ref" json:"ref"`
	Name   string         `yaml:"name,omitempty" json:"name,omitempty"`
	Params map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
}

// Parse decodes a pack.yaml. An unknown field is an error, so a typo is not
// silently dropped.
func Parse(data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("the pack is %d bytes; the limit is %d", len(data), MaxManifestBytes)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("read pack.yaml: %w", err)
	}
	return &m, nil
}

// JSON is the stored form: the manifest as compact JSON.
func (m *Manifest) JSON() ([]byte, error) { return json.Marshal(m) }

// Validate reports every problem the client can find without the gateway's
// catalog. Whether a referenced skill exists is the gateway's to check.
func (m *Manifest) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if m.Format != Format {
		add("format must be %s", Format)
	}
	if !namePattern.MatchString(m.Name) {
		add("name %q must be kebab-case", m.Name)
	}
	if !semverPattern.MatchString(m.Version) {
		add("version %q must be semver", m.Version)
	}
	if strings.TrimSpace(m.DisplayName) == "" || strings.TrimSpace(m.Summary) == "" {
		add("displayName and summary are required")
	}
	params := map[string]bool{}
	for _, p := range m.Params {
		switch {
		case !paramKeyRe.MatchString(p.Key):
			add("param key %q must be lower_snake_case", p.Key)
		case params[p.Key]:
			add("param %q is defined twice", p.Key)
		case !paramTypes[p.Type]:
			add("param %q has unknown type %q", p.Key, p.Type)
		case p.Type == "enum" && len(p.Options) == 0:
			add("enum param %q needs options", p.Key)
		}
		params[p.Key] = true
	}
	for _, g := range m.Groups {
		if k, ok := strings.CutPrefix(g.From, "params."); !ok || !params[k] {
			add("group %q takes its value from %q, which is not a param", g.Key, g.From)
		}
	}
	for _, r := range append(append([]Ref(nil), m.Skills...), m.Bundles...) {
		if name, ver, ok := strings.Cut(r.Ref, "@"); !ok || name == "" || !semverPattern.MatchString(ver) {
			add("ref %q must be name@version with an exact version", r.Ref)
		}
	}
	if len(m.Rules) > MaxRules {
		add("%d rules; the limit is %d", len(m.Rules), MaxRules)
	}
	ruleNames := map[string]bool{}
	for _, r := range m.Rules {
		switch {
		case !namePattern.MatchString(r.Name):
			add("rule name %q must be kebab-case", r.Name)
		case ruleNames[r.Name]:
			add("rule %q is defined twice", r.Name)
		case len(r.Body) > MaxRuleBytes:
			add("rule %q is %d bytes; the limit is %d", r.Name, len(r.Body), MaxRuleBytes)
		case strings.TrimSpace(r.Body) == "":
			add("rule %q has no body", r.Name)
		}
		ruleNames[r.Name] = true
		checkPlaceholders(r.Body, params, "rule "+r.Name, add)
	}
	workflowKeys := map[string]bool{}
	for _, w := range m.Workflows {
		switch {
		case !workflowKeyRe.MatchString(w.Key):
			add("workflow key %q must be kebab-case", w.Key)
		case workflowKeys[w.Key]:
			add("workflow %q is defined twice", w.Key)
		case !workflowRefRe.MatchString(w.Ref):
			add("workflow %q: ref %q must be handle/slug@range or slug@range", w.Key, w.Ref)
		}
		workflowKeys[w.Key] = true
		raw, _ := json.Marshal(w.Params)
		checkPlaceholders(string(raw), params, "workflow "+w.Key, add)
	}
	if m.Risk != nil {
		for _, c := range m.Risk.Matrix {
			if c["service"] == "*" && c["risk"] != "read" && c["risk"] != "write" {
				add("risk cell for every service may only cover read and write, not %v", c["risk"])
			}
		}
		raw, _ := json.Marshal(m.Risk.ToolOverrides)
		checkPlaceholders(string(raw), params, "risk overrides", add)
	}
	return errors.Join(errs...)
}

// checkPlaceholders accepts {{params.x}} for a declared param.
func checkPlaceholders(text string, params map[string]bool, where string, add func(string, ...any)) {
	for _, m := range placeholderRe.FindAllStringSubmatch(text, -1) {
		expr := m[1]
		if k, ok := strings.CutPrefix(expr, "params."); ok && params[k] {
			continue
		}
		add("%s: unknown placeholder {{%s}}", where, expr)
	}
}

// SplitRef splits name@version.
func SplitRef(ref string) (name, version string) {
	name, version, _ = strings.Cut(ref, "@")
	return name, version
}
