package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/agentsync"
	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// FirewallCmd returns `zuano firewall`, the client for the MCP firewall.
func FirewallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "firewall",
		Short: "See the MCP servers behind the firewall and review their changes",
		Long: `The firewall sits between your agents and MCP servers. It shows an agent only
the tool definitions your organisation approved, holds back a tool that
changed until it is reviewed, and checks each call and result.

Approving what a server offers, lifting a hold, adding a server and setting a
hosted package's variables need your passkey: zuano names the action, points to
the console and sends the same request again once you have confirmed it.
Without a person at the keyboard those commands stop with exit status 14.

Exit status
  10  a call was denied, or members may not request servers
  12  a call is held for approval
  14  a passkey confirmation is needed and nobody could give it
  43  a server changed a tool it was approved with
  44  the server is quarantined`,
	}
	cmd.AddCommand(firewallUpstreamsCmd(), firewallChangesCmd(), firewallDefinitionsCmd(), firewallShadowCmd(), firewallConnectCmd(), firewallImportLocalCmd())
	return cmd
}

func firewallUpstreamsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "upstreams", Short: "List, show, add, request and manage MCP servers", Long: `A <server> is a server's slug or id. The slugs local, oauth, mcp, new, changes
and shadow are reserved.`}
	list := listCommand("list", "List the MCP servers the firewall knows", firewallAPI+"/upstreams", "upstreams", "No MCP servers yet.",
		[]column{{"SLUG", "slug"}, {"NAME", "name"}, {"SOURCE", "source"}, {"TRANSPORT", "transport"}, {"STATUS", "status"}, {"TOOLS", "toolCount"}, {"PENDING", "pendingChanges"}}, func(cmd *cobra.Command) func() string {
			var status string
			cmd.Flags().StringVar(&status, "status", "", "Only servers in this state (pending, active, quarantined, disabled)")
			return func() string {
				if status == "" {
					return ""
				}
				return "?" + url.Values{"status": {status}}.Encode()
			}
		})
	show := showCommand("show <server>", "Show one server, its settings, endpoints and hosting", firewallAPI+"/upstreams", "upstream")
	cmd.AddCommand(list, show, firewallUpstreamAddCmd(false), firewallUpstreamAddCmd(true))
	cmd.AddCommand(firewallAdminCmds()...)
	return cmd
}

func firewallUpstreamAddCmd(request bool) *cobra.Command {
	var f approvalFlags
	var urlFlag, pkg, reason, name, auth string
	var tags []string
	var wait string
	use, short := "add <slug>", "Add a remote MCP server or a hosted package and approve it (administrators)"
	if request {
		use, short = "request <slug>", "Ask your administrators to add an MCP server"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: `With --package the gateway builds and runs the package itself (a stdio server
from npm or PyPI) and the build runs as a job; --wait follows it. Adding a
server needs your passkey. A member's request waits for an administrator.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (urlFlag == "") == (pkg == "") {
				return errors.New("give the server with either --url (a remote server) or --package (a hosted one, such as @acme/files-mcp)")
			}
			slug := upstreamSlug(args[0])
			if err := checkUpstreamSlug(slug); err != nil {
				return err
			}
			o, err := callOptsFor(cmd, wait)
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			body := map[string]any{"slug": slug, "name": name}
			if name == "" {
				body["name"] = args[0]
			}
			if urlFlag != "" {
				body["url"] = urlFlag
			} else {
				body["package"] = pkg
			}
			if auth != "" {
				body["authMode"] = auth
			}
			if len(tags) > 0 {
				body["tags"] = tags
			}
			path := firewallAPI + "/upstreams"
			if request {
				path = firewallAPI + "/upstream-requests"
				body["reason"] = reason
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, path, body, nil)
			if err != nil {
				return withFirewallHints(err)
			}
			var ans upstreamAnswer
			_ = json.Unmarshal(raw, &ans)
			if f.jsonOut && (!o.wait || ans.JobID == "") {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			if !f.jsonOut {
				if request {
					fmt.Fprintln(cmd.OutOrStdout(), "Requested. An administrator reviews it in the browser.")
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Added %s. Its tools wait for review: `%s`.\n", textsafe.Clean(slug), brand.Cmd("firewall definitions list "+slug+" --state pending"))
				}
			}
			return followBuild(cmd, c, ans.JobID, f.jsonOut, o)
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&urlFlag, "url", "", "Remote MCP server URL")
	cmd.Flags().StringVar(&pkg, "package", "", "Package the gateway hosts (npm or PyPI name, such as @acme/files-mcp)")
	cmd.Flags().StringVar(&name, "name", "", "Display name (default: the slug)")
	cmd.Flags().StringVar(&auth, "auth", "", "How the server is signed in to: none (default), stored (one credential) or per_user (each person connects their own)")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "Tag the server (repeatable; up to 10)")
	if request {
		cmd.Flags().StringVar(&reason, "reason", "", "Why you need it")
	} else {
		addWaitFlag(cmd, &wait)
	}
	return cmd
}

func firewallChangesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "changes", Short: "Review the changes servers made to what they offer", Long: `A change is decided per definition with ` + "`" + `zuano firewall definitions decide` + "`" + `, which asks for
your passkey; ` + "`" + `changes decide <id>` + "`" + ` opens the change's page instead.`}
	list := listCommand("list", "List the changes servers made to what they offer", firewallAPI+"/changes", "changes", "No changes.",
		[]column{{"ID", "id"}, {"SERVER", "upstream"}, {"NAME", "name"}, {"CHANGE", "change"}, {"SEVERITY", "severity"}, {"RISK", "risk"}, {"STATUS", "status"}}, func(cmd *cobra.Command) func() string {
			var status, upstream string
			var limit int
			cmd.Flags().StringVar(&status, "status", "", "Only changes in this state (open, approved, rejected, auto_resolved)")
			cmd.Flags().StringVar(&upstream, "server", "", "Only changes of this server")
			cmd.Flags().IntVar(&limit, "limit", 0, "Changes to list (1 to 200)")
			return func() string {
				q := url.Values{}
				if status != "" {
					q.Set("status", status)
				}
				if upstream != "" {
					q.Set("upstream", upstream)
				}
				if limit > 0 {
					q.Set("limit", strconv.Itoa(limit))
				}
				if len(q) == 0 {
					return ""
				}
				return "?" + q.Encode()
			}
		})
	show := showCommand("show <id>", "Show a change with its diff and what the screening found", firewallAPI+"/changes", "change")
	cmd.AddCommand(list, show, firewallChangeDecideCmd())
	return cmd
}

func firewallChangeDecideCmd() *cobra.Command {
	var f approvalFlags
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "decide <id>",
		Short: "Open the page where you approve or reject a change with your passkey",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("firewall changes decide")); err != nil {
				return err
			}
			return openDecisionPage(cmd, f.gatewayURL, "/firewall/changes/"+url.PathEscape(args[0]), noBrowser, "Review the change and decide there")
		},
	}
	f.bind(cmd, "Unused: the command only opens a page")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	return cmd
}

func firewallConnectCmd() *cobra.Command {
	var f approvalFlags
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "connect <slug>",
		Short: "Open the page that signs the gateway in to a remote server for you",
		Long:  `A remote server's credentials stay with the gateway, never in an agent's config. This opens the server's sign-in page; zuano never sees the token.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("firewall connect")); err != nil {
				return err
			}
			return openDecisionPage(cmd, f.gatewayURL, "/api/fw/oauth/start?upstream="+url.QueryEscape(upstreamSlug(args[0])), noBrowser, "Sign in to the server there")
		},
	}
	f.bind(cmd, "Unused: the command only opens a page")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	return cmd
}

func firewallImportLocalCmd() *cobra.Command {
	var f mcpInstallFlags
	var from string
	cmd := &cobra.Command{
		Use:   "import-local <name>",
		Short: "Bring an MCP server you configured in an agent under the firewall",
		Long: `Reads the server named <name> from an agent's config, keeps it in zuano's own
server list and syncs it from there, routed through the firewall. The entry you
wrote is replaced; the original command stays in zuano's list, so
'zuano sync --firewall=off' puts the server back as it was. Only user-scope
servers of the agents given with --from (default: every detected agent) are
read.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !f.dryRun {
				if err := refuseInAgentMode(brand.Cmd("firewall import-local")); err != nil {
					return err
				}
			}
			return runImportLocal(cmd, args[0], from, f)
		},
	}
	addMcpSyncFlags(cmd, &f)
	_ = cmd.Flags().MarkHidden("scope")
	cmd.Flags().StringVar(&from, "from", "", "Agent whose config holds the server (default: the first detected agent that has it)")
	cmd.Flags().BoolVar(&f.envToKeychain, "env-to-keychain", false, "Keep literal secret-looking environment values in the system keychain instead of the agents' config")
	return cmd
}

func runImportLocal(cmd *cobra.Command, name, from string, f mcpInstallFlags) error {
	env, err := syncEnv()
	if err != nil {
		return err
	}
	var agents []agentsync.AgentID
	if from != "" {
		if agents, err = agentsync.ParseAgents([]string{from}); err != nil {
			return err
		}
	} else {
		agents = agentsync.Detected(env)
	}
	var server *agentsync.MCPServer
	for _, a := range agents {
		b, _, err := agentsync.ReadAgent(env, a, agentsync.ScopeUser, []agentsync.Kind{agentsync.KindMCP})
		if err != nil {
			return err
		}
		for i := range b.MCP {
			if b.MCP[i].Name == name {
				server = &b.MCP[i]
				break
			}
		}
		if server != nil {
			break
		}
	}
	if server == nil {
		return fmt.Errorf("no MCP server named %q in the user-scope config of %s", textsafe.Clean(name), agentList(agents))
	}
	if name == gatewayServerName {
		return fmt.Errorf("%q is the gateway's own MCP server", gatewayServerName)
	}
	src, err := agentsync.LoadMCPSource(env, agentsync.ScopeUser)
	if err != nil {
		return err
	}
	for _, s := range src.Servers {
		if s.Name == name {
			return fmt.Errorf("%q is already one of the servers zuano syncs; run `%s`", textsafe.Clean(name), brand.Cmd("sync --firewall"))
		}
	}
	if err := server.Validate(); err != nil {
		return err
	}
	src.Servers = append(src.Servers, *server)
	f.scope, f.firewall = "user", true
	f.forceNames = map[string]bool{name: true}
	return syncMCPSource(cmd, env, agentsync.ScopeUser, src, f)
}

func agentList(agents []agentsync.AgentID) string {
	if len(agents) == 0 {
		return "any agent (none detected)"
	}
	s := ""
	for i, a := range agents {
		if i > 0 {
			s += ", "
		}
		s += string(a)
	}
	return s
}
