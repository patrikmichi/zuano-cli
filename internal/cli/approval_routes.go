package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

type approvalRoute struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Default  bool   `json:"isDefault"`
	Enabled  bool   `json:"enabled"`
	Match    struct {
		Services        []string `json:"services"`
		Tools           []string `json:"tools"`
		Risks           []string `json:"risks"`
		Sources         []string `json:"sources"`
		RouteHints      []string `json:"routeHints"`
		RequesterGroups []string `json:"requesterGroups"`
	} `json:"match"`
	Targets []struct {
		Kind         string `json:"kind"`
		GroupID      string `json:"groupId"`
		Installation string `json:"installationId"`
		Conversation string `json:"conversationId"`
	} `json:"targets"`
	RestrictDeciders bool      `json:"restrictDeciders"`
	FourEyes         bool      `json:"fourEyes"`
	Detail           *string   `json:"detail"`
	Reminders        []float64 `json:"reminders"`
}

func listText(v []string) string {
	if len(v) == 0 {
		return "any"
	}
	return textsafe.Clean(strings.Join(v, ","))
}

func (r approvalRoute) targetsText() string {
	var parts []string
	for _, t := range r.Targets {
		switch {
		case t.Kind == "channel" && t.Conversation != "":
			parts = append(parts, "channel "+t.Conversation+" ("+t.Installation+")")
		case t.GroupID != "":
			parts = append(parts, t.Kind+" "+t.GroupID)
		default:
			parts = append(parts, t.Kind)
		}
	}
	return textsafe.Clean(strings.Join(parts, ", "))
}

func approvalsRoutesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "routes",
		Short: "See where approvals are sent in chat (read-only)",
		Long: `Routes decide which chat channels and people an approval is sent to, by
service, tool, risk and who asked. These commands only read them and test what a
call would match; editing stays in the dashboard, because loosening a route
needs a passkey confirmation.`,
	}
	cmd.AddCommand(routesListCmd(), routesShowCmd(), routesTestCmd())
	return cmd
}

func routesListCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the routes in the order they are tried",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Routes []approvalRoute `json:"routes"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, "/api/admin/approval-routes", nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPRIORITY\tSERVICES\tRISKS\tSENT TO")
			for _, r := range doc.Routes {
				name := textsafe.Clean(r.Name)
				if r.Default {
					name += " (default)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n", textsafe.Clean(r.ID), name, r.Priority, listText(r.Match.Services), listText(r.Match.Risks), r.targetsText())
			}
			return tw.Flush()
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func printRoute(out io.Writer, r approvalRoute) {
	fmt.Fprintf(out, "Route:     %s (%s)\n", textsafe.Clean(r.Name), textsafe.Clean(r.ID))
	fmt.Fprintf(out, "Priority:  %d\n", r.Priority)
	fmt.Fprintf(out, "Matches:   services %s; tools %s; risks %s; sources %s\n", listText(r.Match.Services), listText(r.Match.Tools), listText(r.Match.Risks), listText(r.Match.Sources))
	if len(r.Match.RouteHints) > 0 {
		fmt.Fprintf(out, "Hints:     %s\n", listText(r.Match.RouteHints))
	}
	fmt.Fprintf(out, "Sent to:   %s\n", r.targetsText())
	fmt.Fprintf(out, "Detail:    %s\n", orDash(textsafe.Clean(deref(r.Detail))))
	fmt.Fprintf(out, "Deciders:  %s\n", map[bool]string{true: "only the route's groups", false: "any eligible approver"}[r.RestrictDeciders])
	fmt.Fprintf(out, "Four eyes: %s\n", map[bool]string{true: "the requester cannot decide their own call", false: "off"}[r.FourEyes])
}

func routesShowCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one route",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Route approvalRoute `json:"route"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, "/api/admin/approval-routes/"+url.PathEscape(args[0]), nil, &doc)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			printRoute(cmd.OutOrStdout(), doc.Route)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func routesTestCmd() *cobra.Command {
	var f approvalFlags
	var service, tool, risk, source, requester string
	cmd := &cobra.Command{
		Use:   "test --service S --tool T --risk R [--source agent|workflow|cli] [--requester EMAIL]",
		Short: "Show which route a call would take and who would be asked, without sending anything",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if service == "" || tool == "" || risk == "" {
				return errors.New("--service, --tool and --risk are required")
			}
			switch risk {
			case "read", "write", "destructive", "outward":
			default:
				return fmt.Errorf("invalid --risk %q: use read, write, destructive or outward", textsafe.Clean(risk))
			}
			switch source {
			case "", "agent", "workflow", "cli":
			default:
				return fmt.Errorf("invalid --source %q: use agent, workflow or cli", textsafe.Clean(source))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			body := map[string]any{"service": service, "tool": tool, "risk": risk}
			if source != "" {
				body["source"] = source
			}
			if requester != "" {
				body["requester"] = requester
			}
			var doc struct {
				Route struct {
					Name string `json:"name"`
				} `json:"route"`
				Detail     *string `json:"detail"`
				Recipients []struct {
					Email   string `json:"email"`
					Via     string `json:"via"`
					GroupID string `json:"groupId"`
					Status  string `json:"status"`
				} `json:"recipients"`
				Channels []struct {
					Installation string `json:"installationId"`
					Conversation string `json:"conversationId"`
				} `json:"channels"`
				DroppedGroups []string `json:"droppedGroups"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodPost, "/api/admin/approval-routes/test", body, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Matches route %q (detail %s)\n", textsafe.Clean(doc.Route.Name), orDash(textsafe.Clean(deref(doc.Detail))))
			for _, r := range doc.Recipients {
				who := textsafe.Clean(r.Email)
				if r.GroupID != "" {
					who += " (group " + textsafe.Clean(r.GroupID) + ")"
				}
				fmt.Fprintf(out, "  %s %s: %s\n", orDash(textsafe.Clean(r.Via)), who, orDash(textsafe.Clean(r.Status)))
			}
			for _, ch := range doc.Channels {
				fmt.Fprintf(out, "  channel %s (%s)\n", textsafe.Clean(ch.Conversation), textsafe.Clean(ch.Installation))
			}
			if len(doc.DroppedGroups) > 0 {
				fmt.Fprintf(out, "  groups without a reachable member: %s\n", textsafe.Clean(strings.Join(doc.DroppedGroups, ", ")))
			}
			fmt.Fprintln(out, "Nothing was sent.")
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&service, "service", "", "The call's service")
	cmd.Flags().StringVar(&tool, "tool", "", "The call's tool")
	cmd.Flags().StringVar(&risk, "risk", "", "The call's risk: read, write, destructive or outward")
	cmd.Flags().StringVar(&source, "source", "", "Where the call comes from: agent (default), workflow or cli")
	cmd.Flags().StringVar(&requester, "requester", "", "The member who asked (default: you)")
	return cmd
}
