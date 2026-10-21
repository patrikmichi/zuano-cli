package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/config"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const workflowsAPI = "/api/v1/workflows"

var (
	workflowIDPattern  = regexp.MustCompile(`^wfi_[A-Za-z0-9_-]{4,60}$`)
	workflowRunPattern = regexp.MustCompile(`^wfr_[A-Za-z0-9_-]{4,60}$`)
	workflowRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	listingPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}/[a-z0-9][a-z0-9-]{1,63}$`)
)

// WorkflowCmd returns `zuano workflow`, the client for workflows and the
// installations made from them.
func WorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Install, run and follow workflows",
		Long: `A workflow is a manifest (how it starts and its steps) installed for your
organisation with your parameters and connections. 'install' and 'draft' create
an installation that does nothing until a person turns it on in the browser with a
passkey, which is also where its standing permissions are decided.

An id that starts with wfi_ is an installation, one that starts with wfr_ is one
run of it. An installation can also be named by its MCP tool name. A template is
named by its id, with an optional @version.

An agent session may list, show, read logs, install or write a draft, dry-run and
run; enable, preapprove, uninstall and a major upgrade are refused (exit 14).

run and logs exit status
  0   the run succeeded
  10  a step was denied
  11  a step's approval expired
  12  the run waits for an approval
  17  --wait ran out
  18  the input does not match the workflow's input schema
  20  the run failed
  16  'validate', 'draft' and 'push': the manifest has issues, listed one per line
  25  'push': the draft changed since 'pull'; pull it again
  13  rate limited
  14  a person must do this in the browser
  30-33  the plan does not allow it, see 'zuano billing'`,
	}
	cmd.AddCommand(workflowListCmd(), workflowShowCmd(), workflowInstallCmd(), workflowRunCmd(), workflowRunsCmd(), workflowLogsCmd(), workflowCancelCmd(),
		workflowEnableCmd(), workflowPreapproveCmd(), workflowPauseCmd("pause"), workflowPauseCmd("disable"),
		workflowUpgradeCmd(), workflowUninstallCmd(), workflowValidateCmd(), workflowDraftCmd(), workflowSchemaCmd(),
		workflowPullCmd(), workflowPushCmd(), workflowDiffCmd(), workflowSimulateCmd(), workflowPublishCmd(),
		workflowQualityCmd(), workflowOutcomeCmd(), workflowCompareCmd(), workflowEvalCmd(), workflowReplayCmd(), workflowResumeCmd())
	return cmd
}

// refuseAgentDecision stops a command that creates a standing permission:
// that is decided by a person, in the browser.
func refuseAgentDecision(what string) error {
	if config.AgentMode() {
		return &ExitError{Code: client.ExitStepUp, Msg: fmt.Sprintf("%s gives standing permissions, which a person decides in the browser with a passkey; it is refused while ZUANO_AGENT is set. Ask the person who runs this machine to run it", what)}
	}
	return nil
}

type workflowInstallationDoc struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	PausedReason        string `json:"paused_reason"`
	ResourceID          string `json:"resource_id"`
	Version             string `json:"version"`
	CustomManifestID    string `json:"custom_manifest_id"`
	McpToolName         string `json:"mcp_tool_name"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	FailureThreshold    int    `json:"failure_threshold"`
	LastRunAt           string `json:"last_run_at"`
}

func (d workflowInstallationDoc) source() string {
	if d.ResourceID != "" {
		return d.ResourceID + "@" + d.Version
	}
	if d.CustomManifestID != "" {
		return "custom " + d.CustomManifestID
	}
	return "-"
}

func workflowInstallationID(arg string) (string, error) {
	if !workflowRefPattern.MatchString(arg) {
		return "", fmt.Errorf("%q is not a workflow installation (expected wfi_... or its tool name)", textsafe.Clean(arg))
	}
	return arg, nil
}

func workflowPath(parts ...string) string {
	esc := make([]string, len(parts))
	for i, p := range parts {
		esc[i] = url.PathEscape(p)
	}
	return workflowsAPI + "/" + strings.Join(esc, "/")
}

func workflowListCmd() *cobra.Command {
	var f approvalFlags
	var gallery, ready bool
	var category, service, query string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your organisation's workflows, or the templates you can install",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if gallery {
				return listWorkflowTemplates(cmd.Context(), c, out, f.jsonOut, url.Values{"category": {category}, "service": {service}, "q": {query}}, ready)
			}
			var doc struct {
				Installations []workflowInstallationDoc `json:"installations"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, workflowsAPI+"/installations", nil, &doc)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Installations) == 0 {
				fmt.Fprintf(out, "No workflows installed. Browse templates with `%s`.\n", brand.Cmd("workflow list --gallery"))
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSOURCE\tSTATUS\tLAST RUN\tNOTE")
			for _, w := range doc.Installations {
				note := ""
				if w.PausedReason != "" {
					note = "paused: " + textsafe.Clean(w.PausedReason)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", textsafe.Clean(w.ID), textsafe.Clean(w.Name), textsafe.Clean(w.source()), textsafe.Clean(w.Status), orDash(textsafe.Clean(w.LastRunAt)), note)
			}
			return tw.Flush()
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().BoolVar(&gallery, "gallery", false, "List templates you can install instead of installed workflows")
	cmd.Flags().StringVar(&category, "category", "", "With --gallery: only this category")
	cmd.Flags().StringVar(&service, "service", "", "With --gallery: only templates that use this service")
	cmd.Flags().StringVar(&query, "query", "", "With --gallery: only templates whose name or summary contains this")
	cmd.Flags().BoolVar(&ready, "ready", false, "With --gallery: only templates whose connections you have")
	return cmd
}

type workflowTemplateDoc struct {
	ResourceID         string   `json:"resource_id"`
	Slug               string   `json:"slug"`
	Name               string   `json:"name"`
	Summary            string   `json:"summary"`
	Category           string   `json:"category"`
	Version            string   `json:"version"`
	Services           []string `json:"services"`
	MaxRisk            string   `json:"max_risk"`
	MissingConnections []string `json:"missing_connections"`
}

func listWorkflowTemplates(ctx context.Context, c *client.Client, out io.Writer, jsonOut bool, filters url.Values, ready bool) error {
	q := url.Values{}
	for k, v := range filters {
		if len(v) > 0 && v[0] != "" {
			q.Set(k, v[0])
		}
	}
	if ready {
		q.Set("ready", "true")
	}
	path := workflowsAPI + "/templates"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var doc struct {
		Templates []workflowTemplateDoc `json:"templates"`
	}
	raw, err := gatewayJSON(ctx, c, http.MethodGet, path, nil, &doc)
	if err != nil {
		return err
	}
	if jsonOut {
		printRaw(out, raw)
		return nil
	}
	if len(doc.Templates) == 0 {
		fmt.Fprintln(out, "No templates.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TEMPLATE\tNAME\tVERSION\tCATEGORY\tRISK\tSUMMARY")
	for _, t := range doc.Templates {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", textsafe.Clean(t.ResourceID), textsafe.Clean(t.Name), textsafe.Clean(t.Version), orDash(textsafe.Clean(t.Category)), orDash(textsafe.Clean(t.MaxRisk)), textsafe.Clean(t.Summary))
	}
	return tw.Flush()
}

func splitAtVersion(ref string) (string, string) {
	name, version, _ := strings.Cut(ref, "@")
	return name, version
}

type workflowPermission struct {
	Service string `json:"service"`
	Tool    string `json:"tool"`
	Risk    string `json:"risk"`
}

func workflowShowCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "show <wfi_id|template[@version]>",
		Short: "Show a workflow with its pre-approvals, or a template with the permissions it needs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			if workflowIDPattern.MatchString(args[0]) {
				return showWorkflowInstallation(cmd, c, args[0], f.jsonOut)
			}
			ref, version := splitAtVersion(args[0])
			if !workflowRefPattern.MatchString(ref) {
				return fmt.Errorf("%q is neither a workflow installation (wfi_...) nor a template", textsafe.Clean(args[0]))
			}
			path := workflowPath("templates", ref)
			if version != "" {
				path += "?version=" + url.QueryEscape(version)
			}
			var doc struct {
				workflowTemplateDoc
				Analysis struct {
					Permissions []workflowPermission `json:"permissions"`
					Preapprove  []string             `json:"preapprovableSteps"`
				} `json:"analysis"`
				Required []struct {
					Service  string `json:"service"`
					Optional bool   `json:"optional"`
				} `json:"required_connectors"`
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
			fmt.Fprintf(out, "%s %s (%s)\n%s\n", textsafe.Clean(doc.Name), textsafe.Clean(doc.Version), textsafe.Clean(doc.ResourceID), textsafe.Clean(doc.Summary))
			var names []string
			for _, r := range doc.Required {
				n := r.Service
				if r.Optional {
					n += " (optional)"
				}
				names = append(names, n)
			}
			if len(names) > 0 {
				fmt.Fprintf(out, "Connections: %s\n", textsafe.Clean(strings.Join(names, ", ")))
			}
			for _, p := range doc.Analysis.Permissions {
				fmt.Fprintf(out, "  %-12s %s.%s\n", textsafe.Clean(p.Risk), textsafe.Clean(p.Service), textsafe.Clean(p.Tool))
			}
			if len(doc.Analysis.Preapprove) > 0 {
				fmt.Fprintf(out, "Steps you can pre-approve: %s\n", textsafe.Clean(strings.Join(doc.Analysis.Preapprove, ", ")))
			}
			fmt.Fprintf(out, "\nInstall it with `%s`.\n", brand.Cmd("workflow install "+doc.ResourceID))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func showWorkflowInstallation(cmd *cobra.Command, c *client.Client, id string, jsonOut bool) error {
	var w workflowInstallationDoc
	raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, workflowPath("installations", id), nil, &w)
	if err != nil {
		return err
	}
	var pre struct {
		Preapprovals []struct {
			ID        string `json:"id"`
			StepPath  string `json:"step_path"`
			Service   string `json:"service"`
			Tool      string `json:"tool"`
			PerDay    int    `json:"per_day_limit"`
			ExpiresAt string `json:"expires_at"`
		} `json:"preapprovals"`
	}
	praw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, workflowPath("installations", id, "preapprovals"), nil, &pre)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOut {
		printRaw(out, raw)
		printRaw(out, praw)
		return nil
	}
	fmt.Fprintf(out, "%s %s (%s)\n  source %s\n", textsafe.Clean(w.ID), textsafe.Clean(w.Name), textsafe.Clean(w.Status), textsafe.Clean(w.source()))
	if w.McpToolName != "" {
		fmt.Fprintf(out, "  tool name: %s\n", textsafe.Clean(w.McpToolName))
	}
	if w.PausedReason != "" {
		fmt.Fprintf(out, "  paused: %s\n", textsafe.Clean(w.PausedReason))
	}
	if w.ConsecutiveFailures > 0 {
		fmt.Fprintf(out, "  %d consecutive failure(s) of %d allowed\n", w.ConsecutiveFailures, w.FailureThreshold)
	}
	if len(pre.Preapprovals) == 0 {
		fmt.Fprintln(out, "  no pre-approvals")
	}
	for _, p := range pre.Preapprovals {
		fmt.Fprintf(out, "  pre-approval %s step %s (%s.%s): up to %d a day, expires %s\n", textsafe.Clean(p.ID), textsafe.Clean(p.StepPath), textsafe.Clean(p.Service), textsafe.Clean(p.Tool), p.PerDay, textsafe.Clean(p.ExpiresAt))
	}
	return nil
}

func workflowInstallCmd() *cobra.Command {
	var f planFlags
	var name, runAs, channel string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "install <template>[@version] | <handle>/<slug>",
		Short: "Install a template as a draft and open the page where you turn it on",
		Long: `Creates an installation from a template or a marketplace listing
(<handle>/<slug>). It does nothing until a person turns it on in the browser: that
page also asks for the passkey that gives it its standing permissions and for the
pre-approvals of its outward steps. zuano opens the page, or prints the link and a
QR code for your phone when there is no browser.`,
		Example: `  zuano workflow install res_invoice_check --name "Invoice check" --param channel=C0123`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := parseParams(f.params, f.paramsFile)
			if err != nil {
				return err
			}
			body := map[string]any{}
			ref, version := splitAtVersion(args[0])
			switch {
			case listingPattern.MatchString(args[0]):
				body["listing"] = args[0]
				if channel != "" {
					body["channel"] = channel
				}
				ref = path0(args[0])
			case workflowRefPattern.MatchString(ref):
				body["resourceId"] = ref
				if version != "" {
					body["version"] = version
				}
			default:
				return fmt.Errorf("%q is not a template or a marketplace listing", textsafe.Clean(args[0]))
			}
			if name == "" {
				name = ref
			}
			body["name"] = name
			if len(params) > 0 {
				body["params"] = params
			}
			if runAs == "caller" {
				body["runAs"] = "caller"
			} else if runAs != "" {
				body["runAs"] = "fixed"
				body["runAsUserId"] = runAs
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var w workflowInstallationDoc
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, workflowsAPI+"/installations", body, &w)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if w.ID == "" {
				return errors.New("the gateway did not return the installation")
			}
			fmt.Fprintf(out, "Created draft %s (%s).\n", textsafe.Clean(w.ID), textsafe.Clean(w.Name))
			link, err := gatewayLink(c.GatewayURL, "/workflows/"+url.PathEscape(w.ID))
			if err != nil {
				return err
			}
			showLink(cmd, link, noBrowser, "Test it and turn it on there")
			fmt.Fprintf(out, "Dry-run it meanwhile with `%s`.\n", brand.Cmd("workflow run "+w.ID+" --dry-run"))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&name, "name", "", "Name of the workflow (default: the template's id)")
	cmd.Flags().StringVar(&runAs, "run-as", "", "Run as 'caller' or as this member id (an administrator's choice; default: you)")
	cmd.Flags().StringVar(&channel, "channel", "", "With a listing: follow this release channel")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	return cmd
}

func path0(listing string) string {
	_, slug, _ := strings.Cut(listing, "/")
	return slug
}

// doctorWorkflows reports your installations that are paused; a gateway
// without workflows is not a fault.
func doctorWorkflows(ctx context.Context, c *client.Client, add func(string, bool, string)) {
	var doc struct {
		Installations []workflowInstallationDoc `json:"installations"`
	}
	if _, err := gatewayJSON(ctx, c, http.MethodGet, workflowsAPI+"/installations", nil, &doc); err != nil {
		add("workflows", true, "workflows are not available from this gateway")
		return
	}
	var paused []string
	first := ""
	for _, w := range doc.Installations {
		if w.Status == "paused" {
			if first == "" {
				first = w.ID
			}
			paused = append(paused, fmt.Sprintf("%s (%s)", w.ID, w.PausedReason))
		}
	}
	if len(paused) > 0 {
		add("workflows", false, fmt.Sprintf("paused: %s; see `%s`", textsafe.Clean(strings.Join(paused, ", ")), brand.Cmd("workflow show "+first)))
		return
	}
	add("workflows", true, fmt.Sprintf("%d workflow(s), none paused", len(doc.Installations)))
}
