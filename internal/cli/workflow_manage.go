package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// openDecisionPage sends the person to a browser page of the gateway;
// everything that gives standing permissions is decided there.
func openDecisionPage(cmd *cobra.Command, gatewayURL, path string, noBrowser bool, hint string) error {
	c, err := approvalsClient(gatewayURL)
	if err != nil {
		return err
	}
	link, err := gatewayLink(c.GatewayURL, path)
	if err != nil {
		return err
	}
	showLink(cmd, link, noBrowser, hint)
	return nil
}

func workflowEnableCmd() *cobra.Command {
	var f approvalFlags
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "enable <wfi_id>",
		Short: "Open the page where you turn a workflow on with your passkey",
		Long:  `Turning a workflow on gives it standing permissions, so it is decided in the browser or on your phone with your passkey. zuano only opens the page.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("workflow enable")); err != nil {
				return err
			}
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			return openDecisionPage(cmd, f.gatewayURL, "/workflows/"+url.PathEscape(id), noBrowser, "Turn it on there")
		},
	}
	f.bind(cmd, "Unused: the command only opens a page")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	return cmd
}

func workflowPreapproveCmd() *cobra.Command {
	var f approvalFlags
	var noBrowser bool
	var stepPath string
	cmd := &cobra.Command{
		Use:   "preapprove <wfi_id> [--step id]",
		Short: "Open the page where you pre-approve an outward step with your passkey",
		Long: `A pre-approval lets one outward step (such as a Slack message) run without an
approval each time, for a pinned target, a daily cap and a limited time. It is
signed with your passkey in the browser; zuano only opens the page.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("workflow preapprove")); err != nil {
				return err
			}
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			path := "/workflows/" + url.PathEscape(id)
			if stepPath != "" {
				path += "?step=" + url.QueryEscape(stepPath)
			}
			return openDecisionPage(cmd, f.gatewayURL, path, noBrowser, "Pre-approve it there")
		},
	}
	f.bind(cmd, "Unused: the command only opens a page")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	cmd.Flags().StringVar(&stepPath, "step", "", "The step to pre-approve (default: all that can be)")
	return cmd
}

func workflowPauseCmd(verb string) *cobra.Command {
	var f approvalFlags
	short := map[string]string{
		"pause":   "Pause a workflow: its triggers stay, no runs start",
		"disable": "Turn a workflow off and disable its triggers",
	}[verb]
	cmd := &cobra.Command{
		Use:   verb + " <installation>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var w workflowInstallationDoc
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, workflowPath("installations", id, verb), map[string]any{}, &w)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s.\n", textsafe.Clean(id), textsafe.Clean(w.Status))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func workflowUpgradeCmd() *cobra.Command {
	var f approvalFlags
	var to string
	var major bool
	cmd := &cobra.Command{
		Use:   "upgrade <wfi_id>",
		Short: "Move a workflow to a newer published version",
		Long: `Moves the installation to the newest published version, or to --to. A patch or
minor upgrade is applied at once. A major upgrade can add permissions, so it needs
--acknowledge-major and a person's browser session: it is refused for an agent
session (exit 14). Pre-approvals are voided when the manifest changes; run
'workflow preapprove' to give them again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			if major {
				if err := refuseAgentDecision(brand.Cmd("workflow upgrade") + " to a major version"); err != nil {
					return err
				}
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if to != "" {
				body["version"] = to
			}
			if major {
				body["acknowledgeMajor"] = true
			}
			var res struct {
				Classification string `json:"classification"`
				From           string `json:"from"`
				To             string `json:"to"`
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, workflowPath("installations", id, "upgrade"), body, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if res.From == res.To {
				fmt.Fprintln(out, "Already on that version.")
				return nil
			}
			fmt.Fprintf(out, "Upgraded %s to %s (%s).\n", textsafe.Clean(res.From), textsafe.Clean(res.To), textsafe.Clean(res.Classification))
			fmt.Fprintf(out, "Outward steps ask for approval each run until re-approved: `%s`.\n", brand.Cmd("workflow preapprove "+id))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&to, "to", "", "Version to move to (default: the newest)")
	cmd.Flags().BoolVar(&major, "acknowledge-major", false, "Accept a major version change")
	return cmd
}

func workflowUninstallCmd() *cobra.Command {
	var f approvalFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall <wfi_id>",
		Short: "Remove a workflow, its triggers, grants and pre-approvals",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("workflow uninstall")); err != nil {
				return err
			}
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			if err := confirmOrYes(cmd, yes, fmt.Sprintf("Uninstall %s? Its triggers stop and its grants and pre-approvals end.", textsafe.Clean(id))); err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodDelete, workflowPath("installations", id), nil, nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Uninstalled %s.\n", textsafe.Clean(id))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Uninstall without asking")
	return cmd
}
