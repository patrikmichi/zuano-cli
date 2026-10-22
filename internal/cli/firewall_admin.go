package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const firewallAPI = "/api/firewall"

// reservedUpstreamSlugs collide with the gateway's fixed routes under /api/fw
// and its fixed pages under /firewall.
var reservedUpstreamSlugs = []string{"local", "oauth", "mcp", "new", "changes", "shadow"}

var upstreamSlugShape = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// checkUpstreamSlug refuses a slug the gateway would refuse, before any request.
func checkUpstreamSlug(slug string) error {
	if slices.Contains(reservedUpstreamSlugs, slug) {
		return fmt.Errorf("%q is reserved (%s); pick another name", slug, strings.Join(reservedUpstreamSlugs, ", "))
	}
	if !upstreamSlugShape.MatchString(slug) {
		return fmt.Errorf("%q is not a valid slug: use up to 63 lowercase letters, digits and hyphens", slug)
	}
	return nil
}

// firewallRefusalHints say what to do next for the refusals the firewall
// routes added. The gateway's own sentence comes first.
var firewallRefusalHints = map[string]string{
	"member_requests_off":      "ask an administrator to add the server",
	"upstream_not_found":       "list the servers with `" + brand.Cmd("firewall upstreams list") + "`",
	"change_not_found":         "list the changes with `" + brand.Cmd("firewall changes list") + "`",
	"definition_not_found":     "list the definitions with `" + brand.Cmd("firewall definitions list <server>") + "`",
	"definition_not_decidable": "only a definition that waits for a decision can be decided",
	"deploy_running":           "wait for the running build to end",
	"upstream_exists":          "a server with this slug is already registered; pick another",
	"upstream_not_active":      "only an active server can be quarantined",
	"upstream_not_pending":     "only a requested server is approved",
	"upstream_not_quarantined": "this server is not held",
	"env_name_invalid":         "variable names are letters, digits and underscores",
	"federation_refused":       "the vendor's server cannot be added with these settings",
	"tool_list_too_large":      "the server offers more tools than the firewall pins",
	"upstream_source_invalid":  "check the address or package name",
	"hosting_unavailable":      "hosting packages is not available on this gateway or plan",
}

// firewallRefusalExit gives the refusals that are a denial the exit status of
// one; the others are errors of their class (exit 1) with the code in the text.
var firewallRefusalExit = map[string]int{"member_requests_off": exitDenied}

// withFirewallHints adds the hint of a firewall refusal to its sentence and
// gives it its exit status.
func withFirewallHints(err error) error {
	var refusal *gatewayRefusal
	if !errors.As(err, &refusal) {
		return err
	}
	text := refusal.text
	if hint, ok := firewallRefusalHints[refusal.Code]; ok {
		text += "; " + hint
	}
	if code, ok := firewallRefusalExit[refusal.Code]; ok {
		return &ExitError{Code: code, Msg: text}
	}
	return &gatewayRefusal{Code: refusal.Code, text: text}
}

func upstreamPath(ref string, rest ...string) string {
	return firewallAPI + "/upstreams/" + url.PathEscape(ref) + strings.Join(rest, "")
}

// upstreamAnswer is {"upstream": {...}, "jobId"?} as the firewall routes send it.
type upstreamAnswer struct {
	Upstream map[string]any `json:"upstream"`
	JobID    string         `json:"jobId"`
}

func (a upstreamAnswer) line(ref string) string {
	slug := fieldText(a.Upstream, "slug")
	if slug == "-" {
		slug = textsafe.Clean(ref)
	}
	return fmt.Sprintf("%s (%s)", slug, fieldText(a.Upstream, "status"))
}

func firewallAdminCmds() []*cobra.Command {
	return []*cobra.Command{
		upstreamActionCmd("approve <server>", "Approve a requested server: register it, or build and start a hosted package", "approve", true, "Approved"),
		upstreamActionCmd("quarantine <server>", "Hold every call to a server", "quarantine", false, "Held"),
		upstreamActionCmd("release <server>", "Lift the hold on a server (needs your passkey)", "release", true, "Released"),
		firewallDiscoverCmd(),
		firewallDeployCmd(),
		firewallEnvCmd(),
	}
}

// upstreamActionCmd is a POST on one server that answers {"upstream": {...}}.
// A command that gives standing permissions asks for a passkey; it is refused
// while ZUANO_AGENT is set.
func upstreamActionCmd(use, short, verb string, passkey bool, done string) *cobra.Command {
	var f approvalFlags
	var wait string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if passkey {
				if err := refuseAgentDecision(brand.Cmd("firewall upstreams " + verb)); err != nil {
					return err
				}
			}
			o, err := callOptsFor(cmd, wait)
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, upstreamPath(args[0], "/", verb), map[string]any{}, nil)
			if err != nil {
				return withFirewallHints(err)
			}
			return reportUpstreamAction(cmd, c, f.jsonOut, args[0], raw, done, o)
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	if verb == "approve" {
		addWaitFlag(cmd, &wait)
	}
	return cmd
}

// reportUpstreamAction prints the server's new state and, for a hosted
// package whose build started, the job that builds it.
func reportUpstreamAction(cmd *cobra.Command, c *client.Client, jsonOut bool, ref string, raw []byte, done string, o callOpts) error {
	out := cmd.OutOrStdout()
	var ans upstreamAnswer
	_ = json.Unmarshal(raw, &ans)
	if jsonOut && (!o.wait || ans.JobID == "") {
		printRaw(out, raw)
		return nil
	}
	if !jsonOut {
		fmt.Fprintf(out, "%s: %s\n", done, ans.line(ref))
	}
	return followBuild(cmd, c, ans.JobID, jsonOut, o)
}

// followBuild names the job that builds a hosted package and, with --wait,
// follows it to the end like `zuano runs wait`.
func followBuild(cmd *cobra.Command, c *client.Client, jobID string, jsonOut bool, o callOpts) error {
	if jobID == "" {
		return nil
	}
	out := cmd.OutOrStdout()
	if !o.wait {
		if !jsonOut {
			fmt.Fprintf(out, "Build started: %s. Follow it with `%s`, or repeat the command with --wait.\n", textsafe.Clean(jobID), brand.Cmd("runs show "+textsafe.Clean(jobID)))
		}
		return nil
	}
	return waitForJob(cmd.Context(), c, jobID, o.waitFor, out, jsonOut)
}

func firewallDiscoverCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "discover <server>",
		Short: "Ask a server what it offers now and compare it with what is pinned",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, upstreamPath(args[0], "/discover"), map[string]any{}, nil)
			if err != nil {
				return withFirewallHints(err)
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			var res struct {
				Outcome string `json:"outcome"`
				Changes int    `json:"changes"`
			}
			_ = json.Unmarshal(raw, &res)
			switch res.Outcome {
			case "checked":
				fmt.Fprintf(out, "Checked %s: %d change(s) found. Review them with `%s`.\n", textsafe.Clean(args[0]), res.Changes, brand.Cmd("firewall changes list --server "+textsafe.Clean(args[0])))
			default:
				fmt.Fprintf(out, "Discovery %s for %s.\n", textsafe.Clean(res.Outcome), textsafe.Clean(args[0]))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func firewallDeployCmd() *cobra.Command {
	var f approvalFlags
	var wait string
	cmd := &cobra.Command{
		Use:   "deploy <server>",
		Short: "Build and start a hosted package again, for instance after its variables changed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := callOptsFor(cmd, wait)
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, upstreamPath(args[0], "/deploy"), map[string]any{}, nil)
			if err != nil {
				return withFirewallHints(err)
			}
			var ans upstreamAnswer
			_ = json.Unmarshal(raw, &ans)
			if f.jsonOut && (!o.wait || ans.JobID == "") {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			return followBuild(cmd, c, ans.JobID, f.jsonOut, o)
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	addWaitFlag(cmd, &wait)
	return cmd
}

func firewallShadowCmd() *cobra.Command {
	return listCommand("shadow", "List MCP servers configured on devices that the firewall does not govern", firewallAPI+"/shadow", "servers", "No ungoverned servers reported.",
		[]column{{"NAME", "name"}, {"TRANSPORT", "transport"}, {"HOST", "host"}, {"COMMAND", "command"}, {"DEVICES", "devices"}}, nil)
}

func firewallEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Read and replace the variables a hosted package starts with",
		Long: `Variable names are readable; values are write-only and never printed. Setting
them needs your passkey. A running package picks them up after
` + "`zuano firewall upstreams deploy <server>`.",
	}
	cmd.AddCommand(firewallEnvListCmd(), firewallEnvSetCmd(), firewallEnvClearCmd())
	return cmd
}

func firewallEnvListCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List the names of a hosted package's variables",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Names []string `json:"names"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, upstreamPath(args[0], "/env"), nil, &doc)
			if err != nil {
				return withFirewallHints(err)
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Names) == 0 {
				fmt.Fprintln(out, "No variables set.")
			}
			for _, n := range doc.Names {
				fmt.Fprintln(out, textsafe.Clean(n))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

const (
	maxPackageEnvVars       = 30
	maxPackageEnvValueBytes = 8192
)

func firewallEnvSetCmd() *cobra.Command {
	var f approvalFlags
	var fromFile string
	cmd := &cobra.Command{
		Use:   "set <server>",
		Short: "Replace a hosted package's variables with a JSON object of names and values",
		Long: `Reads {"NAME": "value", ...} from standard input or --from-file; values are
never taken from flags, which end up in shell history. The set replaces the
current one: a name left out is removed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("firewall upstreams env set")); err != nil {
				return err
			}
			doc, err := readJSONInput(cmd, fromFile, "the variables")
			if err != nil {
				return err
			}
			env, err := packageEnvOf(doc)
			if err != nil {
				return err
			}
			return putPackageEnv(cmd, f, args[0], env)
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "JSON file with the variables (or - for standard input)")
	return cmd
}

func firewallEnvClearCmd() *cobra.Command {
	var f approvalFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "clear <server>",
		Short: "Remove every variable of a hosted package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("firewall upstreams env clear")); err != nil {
				return err
			}
			if !yes {
				return errors.New("this removes every variable of the package; repeat with --yes")
			}
			return putPackageEnv(cmd, f, args[0], map[string]string{})
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the removal")
	return cmd
}

var envNameShape = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// packageEnvOf checks what the gateway checks, so a mistake is named before
// any value is sent.
func packageEnvOf(doc map[string]any) (map[string]string, error) {
	if len(doc) > maxPackageEnvVars {
		return nil, fmt.Errorf("a package takes at most %d variables, got %d", maxPackageEnvVars, len(doc))
	}
	env := make(map[string]string, len(doc))
	for name, v := range doc {
		s, ok := v.(string)
		switch {
		case !envNameShape.MatchString(name):
			return nil, fmt.Errorf("%q is not a variable name: use letters, digits and underscores", textsafe.Strict(name))
		case !ok || s == "":
			return nil, fmt.Errorf("the value of %s must be a non-empty string", name)
		case len(s) > maxPackageEnvValueBytes:
			return nil, fmt.Errorf("the value of %s is longer than %d bytes", name, maxPackageEnvValueBytes)
		}
		env[name] = s
	}
	return env, nil
}

func putPackageEnv(cmd *cobra.Command, f approvalFlags, ref string, env map[string]string) error {
	c, err := approvalsClient(f.gatewayURL)
	if err != nil {
		return err
	}
	var doc struct {
		Names []string `json:"names"`
	}
	raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPut, upstreamPath(ref, "/env"), map[string]any{"env": env}, &doc)
	if err != nil {
		return withFirewallHints(err)
	}
	out := cmd.OutOrStdout()
	if f.jsonOut {
		printRaw(out, raw)
		return nil
	}
	if len(doc.Names) == 0 {
		fmt.Fprintln(out, "No variables set.")
		return nil
	}
	fmt.Fprintf(out, "Variables set: %s. Run `%s` for a running package to pick them up.\n", textsafe.Clean(strings.Join(doc.Names, ", ")), brand.Cmd("firewall upstreams deploy "+textsafe.Clean(ref)))
	return nil
}

// fwDefinition is one definition of a server as the definitions route lists it.
type fwDefinition struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	DefHash     string  `json:"defHash"`
	State       string  `json:"state"`
	Risk        *string `json:"risk"`
	NeedsReview bool    `json:"needsReview"`
}

func (d fwDefinition) label() string { return d.Kind + ":" + d.Name }

func fetchDefinitions(cmd *cobra.Command, f approvalFlags, ref, state string) ([]fwDefinition, []byte, error) {
	c, err := approvalsClient(f.gatewayURL)
	if err != nil {
		return nil, nil, err
	}
	return definitionsOf(cmd, c, ref, state)
}

func definitionsOf(cmd *cobra.Command, c *client.Client, ref, state string) ([]fwDefinition, []byte, error) {
	path := upstreamPath(ref, "/definitions")
	if state != "" {
		path += "?" + url.Values{"state": {state}}.Encode()
	}
	var doc struct {
		Definitions []fwDefinition `json:"definitions"`
	}
	raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, path, nil, &doc)
	if err != nil {
		return nil, nil, withFirewallHints(err)
	}
	return doc.Definitions, raw, nil
}

func firewallDefinitionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "definitions",
		Short: "List and decide the tools, prompts and resources a server offers",
		Long: `Approving definitions makes them what clients are served, so it needs your
passkey: zuano names the action, points to the console and sends the same
request again once you have confirmed. Rejecting needs none. Both are refused
while ZUANO_AGENT is set.`,
	}
	cmd.AddCommand(firewallDefinitionsListCmd(), firewallDefinitionsDecideCmd(), firewallDefinitionsAcceptCmd())
	return cmd
}

func firewallDefinitionsListCmd() *cobra.Command {
	var f approvalFlags
	var state, kind string
	cmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List what a server offers, with each definition's state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := upstreamPath(args[0], "/definitions")
			q := url.Values{}
			if state != "" {
				q.Set("state", state)
			}
			if kind != "" {
				q.Set("kind", kind)
			}
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Definitions []map[string]any `json:"definitions"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, path, nil, &doc)
			if err != nil {
				return withFirewallHints(err)
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Definitions) == 0 {
				fmt.Fprintln(out, "No definitions.")
				return nil
			}
			for _, d := range doc.Definitions {
				if h, ok := d["defHash"].(string); ok && len(h) > 12 {
					d["defHash"] = h[:12]
				}
			}
			printTable(out, doc.Definitions, []column{{"KIND", "kind"}, {"NAME", "name"}, {"STATE", "state"}, {"RISK", "risk"}, {"REVIEW", "needsReview"}, {"HASH", "defHash"}})
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&state, "state", "", "Only definitions in this state (pinned, pending, quarantined, rejected, removed, superseded)")
	cmd.Flags().StringVar(&kind, "kind", "", "Only this kind (tool, prompt, resource_template, resource, server_instructions)")
	return cmd
}

type decideItem struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	DefHash  string `json:"defHash"`
	Decision string `json:"decision"`
}

const maxDefinitionBatch = 500

func firewallDefinitionsDecideCmd() *cobra.Command {
	var f approvalFlags
	var approve, reject []string
	var approveAll, rejectAll bool
	cmd := &cobra.Command{
		Use:   "decide <server> [--approve NAME]... [--reject NAME]... [--approve-all | --reject-all]",
		Short: "Approve or reject the definitions that wait for a decision",
		Long: `NAME is a tool's name, or kind:name for another kind (prompt:summarise). A name
is looked up among the server's waiting definitions and decided at the hash it
has now, so a definition that changes in between is not approved by mistake.
--approve-all and --reject-all take every definition in the pending state; a
definition held after a change (quarantined) is approved by name only.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if approveAll && rejectAll {
				return errors.New("give --approve-all or --reject-all, not both")
			}
			if len(approve)+len(reject) == 0 && !approveAll && !rejectAll {
				return errors.New("name what to decide with --approve, --reject, --approve-all or --reject-all")
			}
			if len(approve) > 0 || approveAll {
				if err := refuseAgentDecision(brand.Cmd("firewall definitions decide")); err != nil {
					return err
				}
			}
			defs, _, err := fetchDefinitions(cmd, f, args[0], "")
			if err != nil {
				return err
			}
			items, err := decideItems(defs, approve, reject, approveAll, rejectAll)
			if err != nil {
				return err
			}
			if len(items) > maxDefinitionBatch {
				return fmt.Errorf("one decision covers at most %d definitions, got %d; name fewer", maxDefinitionBatch, len(items))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			if !f.jsonOut {
				for _, it := range items {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s %s:%s (%s)\n", it.Decision, textsafe.Strict(it.Kind), textsafe.Strict(it.Name), textsafe.Strict(shortHash(it.DefHash)))
				}
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, upstreamPath(args[0], "/definitions/decide"), map[string]any{"items": items}, nil)
			if err != nil {
				return withFirewallHints(err)
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			var res struct{ Approved, Rejected, Skipped int }
			_ = json.Unmarshal(raw, &res)
			fmt.Fprintf(out, "Approved %d, rejected %d, skipped %d.\n", res.Approved, res.Rejected, res.Skipped)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringArrayVar(&approve, "approve", nil, "Approve this definition (repeatable)")
	cmd.Flags().StringArrayVar(&reject, "reject", nil, "Reject this definition (repeatable)")
	cmd.Flags().BoolVar(&approveAll, "approve-all", false, "Approve every pending definition")
	cmd.Flags().BoolVar(&rejectAll, "reject-all", false, "Reject every pending definition")
	return cmd
}

// decideItems resolves names to the waiting definitions they name, at the
// hash each has now.
func decideItems(defs []fwDefinition, approve, reject []string, approveAll, rejectAll bool) ([]decideItem, error) {
	var items []decideItem
	seen := map[string]bool{}
	add := func(d fwDefinition, decision string) {
		if key := d.label(); !seen[key] {
			seen[key] = true
			items = append(items, decideItem{Kind: d.Kind, Name: d.Name, DefHash: d.DefHash, Decision: decision})
		}
	}
	find := func(name string, states ...string) (fwDefinition, error) {
		kind, bare, qualified := strings.Cut(name, ":")
		var hits []fwDefinition
		for _, d := range defs {
			if !slices.Contains(states, d.State) {
				continue
			}
			if (qualified && d.Kind == kind && d.Name == bare) || (!qualified && d.Kind == "tool" && d.Name == name) {
				hits = append(hits, d)
			}
		}
		if len(hits) == 0 {
			return fwDefinition{}, fmt.Errorf("no definition named %q waits for a decision on this server (see `%s`)", textsafe.Strict(name), brand.Cmd("firewall definitions list <server> --state pending"))
		}
		return hits[0], nil
	}
	for _, n := range approve {
		d, err := find(n, "pending", "quarantined")
		if err != nil {
			return nil, err
		}
		add(d, "approve")
	}
	for _, n := range reject {
		d, err := find(n, "pending")
		if err != nil {
			return nil, err
		}
		if seen[d.label()] {
			return nil, fmt.Errorf("%q is both approved and rejected", textsafe.Strict(n))
		}
		add(d, "reject")
	}
	for _, d := range defs {
		switch {
		case approveAll && d.State == "pending":
			add(d, "approve")
		case rejectAll && d.State == "pending":
			add(d, "reject")
		}
	}
	if len(items) == 0 {
		return nil, errors.New("nothing waits for a decision on this server")
	}
	return items, nil
}

func firewallDefinitionsAcceptCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "accept <server>...",
		Short: "Accept every pending definition of several servers in one passkey decision",
		Long: `Takes the definitions in the pending state of each named server and accepts
them together; the passkey confirms exactly that set of servers and
definitions. Up to 500 definitions.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("firewall definitions accept")); err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var ids, hashes []string
			for _, ref := range args {
				var up upstreamAnswer
				if _, err := gatewayJSON(cmd.Context(), c, http.MethodGet, upstreamPath(ref), nil, &up); err != nil {
					return withFirewallHints(err)
				}
				id, _ := up.Upstream["id"].(string)
				defs, _, err := definitionsOf(cmd, c, ref, "pending")
				if err != nil {
					return err
				}
				if id == "" || len(defs) == 0 {
					continue
				}
				ids = append(ids, id)
				for _, d := range defs {
					if !slices.Contains(hashes, d.DefHash) {
						hashes = append(hashes, d.DefHash)
					}
				}
			}
			out := cmd.OutOrStdout()
			if len(hashes) == 0 {
				if !f.jsonOut {
					fmt.Fprintln(out, "Nothing waits for a decision.")
				}
				return nil
			}
			if len(hashes) > maxDefinitionBatch {
				return fmt.Errorf("%d definitions wait; one decision covers at most %d: accept fewer servers at a time or decide one server with `%s`", len(hashes), maxDefinitionBatch, brand.Cmd("firewall definitions decide"))
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, firewallAPI+"/definitions/accept", map[string]any{"upstreamIds": ids, "defHashes": hashes}, nil)
			if err != nil {
				return withFirewallHints(err)
			}
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			var res struct {
				Approved, Skipped, Upstreams int
				SetHash                      string `json:"setHash"`
			}
			_ = json.Unmarshal(raw, &res)
			fmt.Fprintf(out, "Accepted %d definition(s) on %d server(s), skipped %d (set %s).\n", res.Approved, res.Upstreams, res.Skipped, textsafe.Strict(shortHash(res.SetHash)))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}
