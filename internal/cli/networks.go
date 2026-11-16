package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const networksAPI = "/api/admin/networks"

type networkSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// NetworksCmd returns `zuano networks`, the admin side of private networks:
// where connector agents inside a customer network are enrolled and where
// connectors and MCP servers are routed through them.
func NetworksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "networks",
		Short: "Manage private networks, their connector agents and what is routed through them",
		Long: `A private network lets connectors and MCP servers reach systems that are not on
the internet, through a ` + brand.Name + `-connector agent that runs inside your network and
dials out. These commands are for organisation administrators: they create
networks, mint enrollment tokens, approve and revoke agents, show what each
agent allows and route a connector or MCP server through a network.

Changes that loosen access need a passkey; the command opens the page or waits
for the confirmation. They are refused while ZUANO_AGENT is set (exit 10). A
call that reaches an offline network exits 34.`,
	}
	cmd.AddCommand(networksListCmd(), networksShowCmd(), networksCreateCmd(), networksDeleteCmd(), networksTokenCmd(),
		networksAgentsCmd(), networksAgentActionCmd("approve"), networksAgentActionCmd("rotate"), networksAgentActionCmd("revoke"),
		networksAllowlistCmd(), networksEventsCmd(), networksLogsCmd(), networksBindCmd(false), networksBindCmd(true), networksTestCmd())
	return cmd
}

// resolveNetwork turns a name, slug or id into the network's id.
func resolveNetwork(ctx context.Context, c *client.Client, ref string) (networkSummary, error) {
	var doc struct {
		Networks []struct {
			Network networkSummary `json:"network"`
		} `json:"networks"`
	}
	if _, err := gatewayJSON(ctx, c, http.MethodGet, networksAPI, nil, &doc); err != nil {
		return networkSummary{}, err
	}
	for _, entry := range doc.Networks {
		if n := entry.Network; n.ID == ref || n.Slug == ref || n.Name == ref {
			return n, nil
		}
	}
	return networkSummary{}, fmt.Errorf("no network named %q; see `%s`", textsafe.Clean(ref), brand.Cmd("networks list"))
}

func networkPath(n networkSummary, rest ...string) string {
	return networksAPI + "/" + url.PathEscape(n.ID) + strings.Join(rest, "")
}

// resolveNetworkAgent turns an agent's id or name into its id.
func resolveNetworkAgent(ctx context.Context, c *client.Client, n networkSummary, ref string) (string, error) {
	var doc struct {
		Agents []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"agents"`
	}
	if _, err := gatewayJSON(ctx, c, http.MethodGet, networkPath(n, "/agents"), nil, &doc); err != nil {
		return "", err
	}
	var byName []string
	for _, a := range doc.Agents {
		if a.ID == ref {
			return a.ID, nil
		}
		if a.Name == ref {
			byName = append(byName, a.ID)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return "", fmt.Errorf("no agent %q on %s; see `%s`", textsafe.Clean(ref), textsafe.Clean(n.Name), brand.Cmd("networks agents "+n.Name))
	}
	return "", fmt.Errorf("%d agents on %s are named %q; use an id from `%s`", len(byName), textsafe.Clean(n.Name), textsafe.Clean(ref), brand.Cmd("networks agents "+n.Name))
}

func networksListCmd() *cobra.Command {
	return listCommand("list", "List the private networks", networksAPI, "networks", "No private networks yet. Create one with `"+brand.Cmd("networks create <name>")+"`.",
		[]column{{"NAME", "network.name"}, {"STATUS", "network.status"}, {"HEALTH", "health"}, {"AGENTS ONLINE", "agents.online"}, {"PENDING", "agents.pending"}, {"BINDINGS", "bindings"}, {"LAST SEEN", "lastSeenAt"}}, nil)
}

func networksShowCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a network's status and settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			var doc map[string]json.RawMessage
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, networkPath(n), nil, &doc)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			obj := map[string]any{}
			_ = json.Unmarshal(doc["network"], &obj)
			var agents struct{ Total, Online, Pending int }
			_ = json.Unmarshal(doc["agents"], &agents)
			obj["agents"] = fmt.Sprintf("%d online, %d pending, %d in total", agents.Online, agents.Pending, agents.Total)
			for _, k := range []string{"health", "bindings", "lastSeenAt"} {
				var v any
				_ = json.Unmarshal(doc[k], &v)
				obj[k] = v
			}
			printObject(cmd.OutOrStdout(), obj)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func networksCreateCmd() *cobra.Command {
	var f approvalFlags
	var description, dataClass string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a private network",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentChange(brand.Cmd("networks create")); err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			body := map[string]any{"name": args[0]}
			if description != "" {
				body["description"] = description
			}
			if dataClass != "" {
				body["dataClass"] = dataClass
			}
			var res struct {
				Network networkSummary `json:"network"`
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, networksAPI, body, &res)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s. Enroll an agent with `%s`.\n", textsafe.Clean(res.Network.Name), brand.Cmd("networks token create "+args[0]))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&description, "description", "", "What the network is")
	cmd.Flags().StringVar(&dataClass, "data-class", "", "Label policies can match, such as internal or confidential")
	return cmd
}

func networksDeleteCmd() *cobra.Command {
	var f approvalFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a network: its bindings are removed and its agents revoked",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentChange(brand.Cmd("networks delete")); err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			if err := confirmOrYes(cmd, yes, fmt.Sprintf("Delete %s, remove its bindings and revoke its agents?", textsafe.Clean(n.Name))); err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodDelete, networkPath(n), nil, nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s.\n", textsafe.Clean(n.Name))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().BoolVar(&yes, "yes", false, "Delete without asking")
	return cmd
}

func networksTokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Enrollment tokens for connector agents"}
	var f approvalFlags
	var uses int
	var ttl time.Duration
	var requireApproval bool
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Mint an enrollment token and print it with the install commands",
		Long: `The token is shown once, here, and is useless after use or expiry. Treat it as
a secret: it is not exposed over MCP and this command is refused while
ZUANO_AGENT is set. A single-use token lasts an hour by default; a token with
--uses above 1 needs --require-approval unless you turn that off.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentChange(brand.Cmd("networks token create")); err != nil {
				return err
			}
			if uses < 1 || uses > 10 {
				return errors.New("--uses must be between 1 and 10")
			}
			if ttl < time.Minute || ttl > 24*time.Hour {
				return errors.New("--ttl must be between 1m and 24h")
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"uses": uses, "ttlSeconds": int(ttl.Seconds())}
			if cmd.Flags().Changed("require-approval") {
				body["requireApproval"] = requireApproval
			}
			var res struct {
				Token     string `json:"token"`
				ExpiresAt string `json:"expiresAt"`
				Install   map[string]string `json:"install"`
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, networkPath(n, "/enrollment-tokens"), body, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if !strings.HasPrefix(res.Token, "nxt_") {
				return errors.New("the gateway did not return an enrollment token")
			}
			fmt.Fprintf(out, "Enrollment token (shown once, expires %s):\n  %s\n", textsafe.Clean(orDash(res.ExpiresAt)), textsafe.Clean(res.Token))
			for _, k := range []struct{ key, title string }{{"linux", "Linux"}, {"debian", "Debian"}, {"docker", "Docker"}, {"kubernetes", "Kubernetes"}} {
				if text := res.Install[k.key]; text != "" {
					fmt.Fprintf(out, "\n%s\n%s\n", k.title, strings.TrimRight(textsafe.Clean(text), "\n"))
				}
			}
			return nil
		},
	}
	f.bind(create, "Print the response body unchanged")
	create.Flags().IntVar(&uses, "uses", 1, "How many agents may enroll with it (1 to 10)")
	create.Flags().DurationVar(&ttl, "ttl", time.Hour, "How long it stays valid (up to 24h)")
	create.Flags().BoolVar(&requireApproval, "require-approval", false, "New agents wait for approval (the default when --uses is above 1)")
	cmd.AddCommand(create, networksTokenListCmd(), networksTokenRevokeCmd())
	return cmd
}

func networksTokenRevokeCmd() *cobra.Command {
	var f approvalFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "revoke <name> <token-id>",
		Short: "Revoke an enrollment token nobody has used up",
		Long:  "Takes the token's id from `networks token list`. A token already used up or revoked is not found.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentChange(brand.Cmd("networks token revoke")); err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			if err := confirmOrYes(cmd, yes, fmt.Sprintf("Revoke enrollment token %s of %s?", textsafe.Clean(args[1]), textsafe.Clean(n.Name))); err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodDelete, networkPath(n, "/enrollment-tokens/", url.PathEscape(args[1])), nil, nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked enrollment token %s.\n", textsafe.Clean(args[1]))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().BoolVar(&yes, "yes", false, "Revoke without asking")
	return cmd
}

func networksTokenListCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "list <name>",
		Short: "List the enrollment tokens that can still be used",
		Long:  "Shows each token's id, when it was made and expires and how many uses are left. The token itself is shown once, when it is created, and never again.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			var doc struct {
				Tokens []struct {
					ID              string `json:"id"`
					CreatedAt       string `json:"createdAt"`
					ExpiresAt       string `json:"expiresAt"`
					MaxUses         int    `json:"maxUses"`
					UsesLeft        int    `json:"usesLeft"`
					RequireApproval bool   `json:"requireApproval"`
				} `json:"tokens"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, networkPath(n, "/enrollment-tokens"), nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Tokens) == 0 {
				fmt.Fprintf(out, "No enrollment tokens can be used. Mint one with `%s`.\n", brand.Cmd("networks token create "+args[0]))
				return nil
			}
			var rows [][]string
			for _, t := range doc.Tokens {
				approval := "no"
				if t.RequireApproval {
					approval = "yes"
				}
				rows = append(rows, []string{t.ID, t.CreatedAt, t.ExpiresAt, fmt.Sprintf("%d of %d", t.UsesLeft, t.MaxUses), approval})
			}
			printRows(out, []string{"ID", "CREATED", "EXPIRES", "USES LEFT", "NEEDS APPROVAL"}, rows)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func networksAgentsCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "agents <name>",
		Short: "List a network's connector agents with their live status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			var doc map[string]json.RawMessage
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, networkPath(n, "/agents"), nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			var rows []map[string]any
			_ = json.Unmarshal(doc["agents"], &rows)
			if len(rows) == 0 {
				fmt.Fprintf(out, "No agents. Enroll one with `%s`.\n", brand.Cmd("networks token create "+args[0]))
				return nil
			}
			printTable(out, rows, []column{{"ID", "id"}, {"NAME", "name"}, {"STATUS", "status"}, {"CONNECTED", "connected"}, {"VERSION", "version"}, {"OS", "os"}, {"ARCH", "arch"}, {"LAST SEEN", "lastSeenAt"}, {"KEY EXPIRES", "keyNotAfter"}})
			var live bool
			if json.Unmarshal(doc["live"], &live) == nil && !live {
				fmt.Fprintln(out, "\nwarning: the broker did not answer; connection state is the last stored state.")
			}
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func networksAgentActionCmd(action string) *cobra.Command {
	var f approvalFlags
	var yes bool
	var reason string
	short := map[string]string{
		"approve": "Let a pending agent receive requests",
		"rotate":  "Make an agent rotate its keys now",
		"revoke":  "Cut an agent off: its socket closes within seconds and its keys are dropped",
	}[action]
	cmd := &cobra.Command{
		Use:   action + " <name> <agent>",
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentChange(brand.Cmd("networks " + action)); err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			agentID, err := resolveNetworkAgent(cmd.Context(), c, n, args[1])
			if err != nil {
				return err
			}
			if action == "revoke" {
				if err := confirmOrYes(cmd, yes, fmt.Sprintf("Revoke agent %s of %s?", textsafe.Clean(args[1]), textsafe.Clean(n.Name))); err != nil {
					return err
				}
			}
			body := map[string]any{}
			if reason != "" {
				body["reason"] = reason
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, networkPath(n, "/agents/", url.PathEscape(agentID), "/", action), body, nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: agent %s.\n", map[string]string{"approve": "Approved", "rotate": "Rotation requested", "revoke": "Revoked"}[action], textsafe.Clean(args[1]))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&reason, "reason", "", "Why, for the network's event log (up to 200 characters)")
	if action == "revoke" {
		cmd.Flags().BoolVar(&yes, "yes", false, "Revoke without asking")
	}
	return cmd
}

func networksAllowlistCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "allowlist <name>",
		Short: "Show what each agent advertises it will serve, and whether they differ",
		Long:  `The allowlist lives in each agent's own config file, on your side; this shows the target names, kinds, methods and path patterns the agents report. A difference between agents is configuration drift.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			var doc struct {
				Drift   bool `json:"drift"`
				Targets []struct {
					Name    string   `json:"name"`
					Kind    string   `json:"kind"`
					Methods []string `json:"methods"`
					Paths   []string `json:"pathGlobs"`
					Tools   []string `json:"mcpTools"`
					Agents  []string `json:"agents"`
					Bound   []string `json:"bindings"`
					Drift   bool     `json:"drift"`
				} `json:"targets"`
				Unserved []string `json:"unserved"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, networkPath(n, "/allowlist"), nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Targets) == 0 {
				fmt.Fprintln(out, "No agent advertises a target.")
			}
			for _, t := range doc.Targets {
				fmt.Fprintf(out, "%s (%s)\n  agents:  %s\n", textsafe.Clean(t.Name), textsafe.Clean(t.Kind), textsafe.Clean(orDash(strings.Join(t.Agents, ", "))))
				if t.Drift {
					fmt.Fprintln(out, "  drift:   the agents that advertise it disagree")
				}
				if len(t.Methods) > 0 {
					fmt.Fprintf(out, "  methods: %s\n", textsafe.Clean(strings.Join(t.Methods, ", ")))
				}
				if len(t.Paths) > 0 {
					fmt.Fprintf(out, "  paths:   %s\n", textsafe.Clean(strings.Join(t.Paths, ", ")))
				}
				if len(t.Tools) > 0 {
					fmt.Fprintf(out, "  tools:   %s\n", textsafe.Clean(strings.Join(t.Tools, ", ")))
				}
				if len(t.Bound) > 0 {
					fmt.Fprintf(out, "  bound:   %s\n", textsafe.Clean(strings.Join(t.Bound, ", ")))
				}
			}
			if doc.Drift {
				fmt.Fprintln(out, "\nwarning: the agents advertise different allowlists; give them the same config.")
			}
			for _, u := range doc.Unserved {
				fmt.Fprintf(out, "warning: %s is bound to a target no agent advertises.\n", textsafe.Clean(u))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func networksEventsCmd() *cobra.Command {
	var f approvalFlags
	var limit int
	var before string
	cmd := &cobra.Command{
		Use:   "events <name>",
		Short: "Show a network's events, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if cmd.Flags().Changed("limit") {
				if limit < 1 || limit > 200 {
					return errors.New("--limit must be between 1 and 200")
				}
				q.Set("limit", fmt.Sprint(limit))
			}
			if before != "" {
				q.Set("before", before)
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			path := networkPath(n, "/events")
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var doc struct {
				Events []map[string]any `json:"events"`
				Next   string           `json:"next"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, path, nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Events) == 0 {
				fmt.Fprintln(out, "No events.")
				return nil
			}
			printTable(out, doc.Events, []column{{"TIME", "ts"}, {"TYPE", "type"}, {"AGENT", "agentId"}, {"DETAILS", "details"}})
			if doc.Next != "" {
				fmt.Fprintf(out, "\nOlder events: --before %s\n", textsafe.Clean(doc.Next))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().IntVar(&limit, "limit", 0, "How many events (1 to 200)")
	cmd.Flags().StringVar(&before, "before", "", "Only events older than this timestamp (the cursor the previous page printed)")
	return cmd
}

func networksLogsCmd() *cobra.Command {
	var f approvalFlags
	var agent, target, outcome, since, before string
	var limit int
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show the exchange logs the agents uploaded (metadata only), newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if cmd.Flags().Changed("limit") {
				if limit < 1 || limit > 200 {
					return errors.New("--limit must be between 1 and 200")
				}
				q.Set("limit", fmt.Sprint(limit))
			}
			if before != "" {
				t, err := time.Parse(time.RFC3339Nano, before)
				if err != nil {
					return fmt.Errorf("invalid --before %q: use the timestamp the previous page printed", textsafe.Clean(before))
				}
				q.Set("before", t.UTC().Format(time.RFC3339Nano))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			for k, v := range map[string]string{"agent": agent, "target": target, "outcome": outcome} {
				if v != "" {
					q.Set(k, v)
				}
			}
			if since != "" {
				t, err := parseSince(since)
				if err != nil {
					return err
				}
				q.Set("since", t.UTC().Format(time.RFC3339))
			}
			path := networkPath(n, "/logs")
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var doc struct {
				Logs []map[string]any `json:"logs"`
				Next string           `json:"next"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, path, nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			rows := doc.Logs
			if len(rows) == 0 {
				fmt.Fprintln(out, "No log lines match. Agents upload what log.upload allows (errors by default).")
				return nil
			}
			printTable(out, rows, []column{{"TIME", "ts"}, {"AGENT", "agent"}, {"TARGET", "target"}, {"METHOD", "method"}, {"STATUS", "status"}, {"OUTCOME", "outcome"}, {"MS", "duration_ms"}, {"AUDIT", "audit_ref"}})
			if doc.Next != "" {
				fmt.Fprintf(out, "\nOlder lines: --before %s\n", textsafe.Clean(doc.Next))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().IntVar(&limit, "limit", 0, "How many lines (1 to 200)")
	cmd.Flags().StringVar(&before, "before", "", "Only lines older than this timestamp (the cursor the previous page printed)")
	cmd.Flags().StringVar(&agent, "agent", "", "Only this agent")
	cmd.Flags().StringVar(&target, "target", "", "Only this target")
	cmd.Flags().StringVar(&outcome, "outcome", "", "Only this outcome (ok, offline, refused, upstream_error, timeout, protocol_error)")
	cmd.Flags().StringVar(&since, "since", "", "Only newer than this (a duration such as 24h or 7d)")
	return cmd
}

// bindingTarget splits "mcp:<server>" from a connector name.
func bindingTarget(arg string) (kind, service string, err error) {
	if server, ok := strings.CutPrefix(arg, "mcp:"); ok {
		if server == "" {
			return "", "", errors.New("name the MCP server after the mcp: prefix")
		}
		return "mcp_server", server, nil
	}
	if arg == "" {
		return "", "", errors.New("name a connector, or mcp:<server>")
	}
	return "connector", arg, nil
}

// findBinding returns the id of the network's binding for a connector or MCP
// server; with several for the same service the target tells them apart.
func findBinding(ctx context.Context, c *client.Client, n networkSummary, kind, service, target string) (string, error) {
	var doc struct {
		Bindings []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Service string `json:"service"`
			Target  string `json:"target"`
		} `json:"bindings"`
	}
	if _, err := gatewayJSON(ctx, c, http.MethodGet, networkPath(n, "/bindings"), nil, &doc); err != nil {
		return "", err
	}
	var ids []string
	for _, b := range doc.Bindings {
		if b.Kind == kind && b.Service == service && (target == "" || b.Target == target) {
			ids = append(ids, b.ID)
		}
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return "", fmt.Errorf("%s is not routed through %s", textsafe.Clean(service), textsafe.Clean(n.Name))
	}
	return "", fmt.Errorf("%s is routed through %s to %d targets; name one with --target", textsafe.Clean(service), textsafe.Clean(n.Name), len(ids))
}

func networksBindCmd(unbind bool) *cobra.Command {
	var f approvalFlags
	var network, target, credentials string
	use, short := "bind <connector|mcp:server>", "Route a connector or an MCP server through a network target"
	if unbind {
		use, short = "unbind <connector|mcp:server>", "Stop routing a connector or an MCP server through a network"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: `Adding a binding sends that connector's traffic into your network, which loosens
access and needs a passkey. A bound connector never falls back to the public
internet: when the network cannot serve a call, the call fails. Built-in
services such as Google or Slack cannot be bound.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentChange(brand.Cmd("networks " + strings.Fields(use)[0])); err != nil {
				return err
			}
			kind, service, err := bindingTarget(args[0])
			if err != nil {
				return err
			}
			if network == "" {
				return errors.New("--network is required")
			}
			if !unbind {
				if target == "" {
					return errors.New("--target is required: the target name from the agent's config")
				}
				if credentials != "agent" && credentials != "gateway" {
					return fmt.Errorf("invalid --credentials %q: use agent or gateway", credentials)
				}
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, network)
			if err != nil {
				return err
			}
			var raw []byte
			if unbind {
				var id string
				if id, err = findBinding(cmd.Context(), c, n, kind, service, target); err != nil {
					return err
				}
				raw, err = gatewayWrite(cmd, c, f.jsonOut, http.MethodDelete, networkPath(n, "/bindings/", url.PathEscape(id)), nil, nil)
			} else {
				raw, err = gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, networkPath(n, "/bindings"), map[string]any{"kind": kind, "service": service, "target": target, "credentialsMode": credentials}, nil)
			}
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			verb := "Bound"
			if unbind {
				verb = "Unbound"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s on %s.\n", verb, textsafe.Clean(args[0]), textsafe.Clean(n.Name))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&network, "network", "", "Network name")
	if unbind {
		cmd.Flags().StringVar(&target, "target", "", "Only the binding to this target, when the service has several")
	} else {
		cmd.Flags().StringVar(&target, "target", "", "Target name from the agent's config")
		cmd.Flags().StringVar(&credentials, "credentials", "gateway", "gateway injects the stored credentials; agent uses the ones held on the agent")
	}
	return cmd
}

func networksTestCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "test <name>",
		Short: "Send a ping through each online agent and show the round-trip time",
		Long: `The gateway asks its broker to ping every connected agent, so the time shown is
the round trip from the gateway to the broker and back, not from this machine.
Exits 34 when no agent is online. A broker that does not answer in time is a
refusal with its own sentence and exit 1.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			n, err := resolveNetwork(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			var res struct {
				Results []struct {
					Agent string `json:"agent"`
					OK    bool   `json:"ok"`
					RTT   int    `json:"rtt_ms"`
					Error string `json:"error"`
				} `json:"results"`
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, networkPath(n, "/test"), map[string]any{}, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
			} else {
				for _, r := range res.Results {
					if r.OK {
						fmt.Fprintf(out, "%s: ok, %d ms\n", textsafe.Clean(r.Agent), r.RTT)
					} else {
						fmt.Fprintf(out, "%s: failed: %s\n", textsafe.Clean(r.Agent), textsafe.Clean(r.Error))
					}
				}
				if len(res.Results) > 0 {
					fmt.Fprintln(out, "Times are the round trip from the gateway to the broker and back, not from this machine.")
				}
			}
			if len(res.Results) == 0 {
				return &ExitError{Code: exitNetworkOffline, Msg: textsafe.Clean(n.Name) + " is offline: no connector agent is connected"}
			}
			for _, r := range res.Results {
				if r.OK {
					return nil
				}
			}
			return &ExitError{Code: 1, Msg: "no agent answered the ping"}
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}
