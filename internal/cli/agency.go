package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const agencyAPI = "/api/v1/agency"

// ClientsCmd returns `zuano clients`, the organisations an agency is linked to.
func ClientsCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "clients",
		Short: "List the client organisations your agency is linked to",
		Long: `Lists the agency's links to client organisations with their status and the
scopes each grants. To act inside one client, pass its organisation id to any
command with --client (or ZUANO_CLIENT): the gateway allows it only while the link
is active, you hold a delegated role there and the link grants what the command
needs. Every such request is recorded in both organisations' audit logs.`,
		Example: `  zuano clients
  zuano --client org_8f2c workflow list`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Clients []struct {
					ID          string   `json:"id"`
					ClientOrgID string   `json:"client_org_id"`
					ClientName  string   `json:"client_name"`
					Status      string   `json:"status"`
					Scopes      []string `json:"scopes"`
					BillingMode string   `json:"billing_mode"`
					ConsentedAt string   `json:"consented_at"`
					SuspendedAt string   `json:"suspended_at"`
					EndedAt     string   `json:"ended_at"`
					Offboarding string   `json:"offboarding"`
				} `json:"clients"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, agencyAPI+"/clients", nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Clients) == 0 {
				fmt.Fprintln(out, "No clients are linked.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "CLIENT\tNAME\tLINK\tSTATUS\tBILLING\tSCOPES")
			for _, l := range doc.Clients {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", textsafe.Clean(l.ClientOrgID), orDash(textsafe.Clean(l.ClientName)), textsafe.Clean(l.ID), textsafe.Clean(l.Status), orDash(textsafe.Clean(l.BillingMode)), len(l.Scopes))
			}
			return tw.Flush()
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	return cmd
}

type fleetFlags struct {
	approvalFlags
	days, limit int
	all         bool
}

func (f *fleetFlags) bind(cmd *cobra.Command) {
	f.approvalFlags.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().IntVar(&f.days, "days", 0, "Days to cover (default: the gateway's, 7)")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "Rows per page")
	cmd.Flags().BoolVar(&f.all, "all", false, "Follow every page")
}

// fleetPages reads a paged fleet route and hands each page's rows to show;
// with --json it prints each page's answer unchanged.
func fleetPages(cmd *cobra.Command, f fleetFlags, path, rowsKey string, show func(out io.Writer, rows []map[string]any)) error {
	c, err := approvalsClient(f.gatewayURL)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	cursor := ""
	for page := 0; page < 200; page++ {
		q := url.Values{}
		if f.days > 0 {
			q.Set("days", strconv.Itoa(f.days))
		}
		if f.limit > 0 {
			q.Set("limit", strconv.Itoa(f.limit))
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		full := path
		if len(q) > 0 {
			full += "?" + q.Encode()
		}
		var doc map[string]any
		raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, full, nil, &doc)
		if err != nil {
			return err
		}
		if f.jsonOut {
			printRaw(out, raw)
		} else {
			var rows []map[string]any
			if list, ok := doc[rowsKey].([]any); ok {
				for _, r := range list {
					if m, ok := r.(map[string]any); ok {
						rows = append(rows, m)
					}
				}
			}
			show(out, rows)
		}
		next, _ := doc["next_cursor"].(string)
		if next == "" {
			return nil
		}
		if !f.all {
			if !f.jsonOut {
				fmt.Fprintf(out, "More rows follow; add --all to read every page.\n")
			}
			return nil
		}
		cursor = next
	}
	return nil
}

func field(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return textsafe.Clean(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case nil:
		return "-"
	}
	return "-"
}

func cost(m map[string]any) string {
	if v, ok := m["costNano"].(float64); ok {
		return fmt.Sprintf("$%.4f", v/1e9)
	}
	return "-"
}

// FleetCmd returns `zuano fleet`, the agency's view across its clients.
func FleetCmd() *cobra.Command {
	var f fleetFlags
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Runs, outcomes, interventions and cost across your clients",
		Long: `Shows each client with its recent runs, outcomes, interventions and cost, from
daily totals; it never reads run content. A client appears only while its link lets
your agency read run metadata, and cost only if the link grants usage. 'client',
'products', 'failures' and 'alerts' narrow it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fleetPages(cmd, f, agencyAPI+"/fleet", "clients", func(out io.Writer, rows []map[string]any) {
				if len(rows) == 0 {
					fmt.Fprintln(out, "No clients report run metadata.")
					return
				}
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "CLIENT\tNAME\tWORKFLOWS\tSETUP\tRUNS\tFAILED\tVERIFIED\tNOT VERIFIED\tHELP\tCOST")
				for _, r := range rows {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", field(r, "clientOrgId"), field(r, "clientName"), field(r, "installations"), field(r, "setupPending"), field(r, "runs"), field(r, "runsFailed"), field(r, "outcomeSuccess"), field(r, "outcomeFailed"), field(r, "interventions"), cost(r))
				}
				_ = tw.Flush()
			})
		},
	}
	f.bind(cmd)
	cmd.AddCommand(fleetClientCmd(), fleetProductsCmd(), fleetFailuresCmd(), fleetAlertsCmd())
	return cmd
}

func fleetClientCmd() *cobra.Command {
	var f fleetFlags
	cmd := &cobra.Command{
		Use:   "client <client-org>",
		Short: "One client's managed workflows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.SetActingClient(""); err != nil {
				return err
			}
			if _, err := fleetOrgID(args[0]); err != nil {
				return err
			}
			return fleetPages(cmd, f, agencyAPI+"/fleet/clients/"+url.PathEscape(args[0]), "installations", func(out io.Writer, rows []map[string]any) {
				if len(rows) == 0 {
					fmt.Fprintln(out, "No managed workflows.")
					return
				}
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "WORKFLOW\tNAME\tSTATUS\tRUNS\tFAILED\tVERIFIED\tNOT VERIFIED\tP95 MS\tCOST")
				for _, r := range rows {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", field(r, "installationId"), field(r, "name"), field(r, "status"), field(r, "runs"), field(r, "runsFailed"), field(r, "outcomeSuccess"), field(r, "outcomeFailed"), field(r, "p95Ms"), cost(r))
				}
				_ = tw.Flush()
			})
		},
	}
	f.bind(cmd)
	return cmd
}

func fleetOrgID(s string) (string, error) {
	if !workflowRefPattern.MatchString(s) {
		return "", fmt.Errorf("%q is not a client organisation id", textsafe.Clean(s))
	}
	return s, nil
}

func fleetProductsCmd() *cobra.Command {
	var f fleetFlags
	cmd := &cobra.Command{
		Use:   "products",
		Short: "Versions of each product in use and the clients that fail most",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fleetPages(cmd, f, agencyAPI+"/fleet/products", "products", func(out io.Writer, rows []map[string]any) {
				if len(rows) == 0 {
					fmt.Fprintln(out, "No products.")
					return
				}
				for _, r := range rows {
					fmt.Fprintf(out, "%s (%s) %s\n", field(r, "title"), field(r, "slug"), field(r, "status"))
					versions, _ := r["versions"].([]any)
					for _, v := range versions {
						m, _ := v.(map[string]any)
						fmt.Fprintf(out, "  %-12s %s installation(s), %s enabled, %s pending\n", field(m, "version"), field(m, "installations"), field(m, "enabled"), field(m, "pending"))
					}
					worst, _ := r["worstClients"].([]any)
					var names []string
					for _, w := range worst {
						m, _ := w.(map[string]any)
						names = append(names, fmt.Sprintf("%s (%s of %s runs failed)", field(m, "clientOrgId"), field(m, "runsFailed"), field(m, "runs")))
					}
					if len(names) > 0 {
						fmt.Fprintf(out, "  failing most: %s\n", strings.Join(names, ", "))
					}
				}
			})
		},
	}
	f.bind(cmd)
	return cmd
}

func fleetFailuresCmd() *cobra.Command {
	var f fleetFlags
	cmd := &cobra.Command{
		Use:   "failures",
		Short: "Workflows with failed runs, most failures first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fleetPages(cmd, f, agencyAPI+"/fleet/failures", "failures", func(out io.Writer, rows []map[string]any) {
				if len(rows) == 0 {
					fmt.Fprintln(out, "No failed runs.")
					return
				}
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "CLIENT\tNAME\tWORKFLOW\tFAILED\tRUNS")
				for _, r := range rows {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", field(r, "clientOrgId"), field(r, "clientName"), field(r, "installationId"), field(r, "runsFailed"), field(r, "runs"))
				}
				_ = tw.Flush()
			})
		},
	}
	f.bind(cmd)
	return cmd
}

func fleetAlertsCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "Open alerts across the fleet",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listAlerts(cmd.Context(), cmd, f)
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	return cmd
}

func listAlerts(ctx context.Context, cmd *cobra.Command, f approvalFlags) error {
	c, err := approvalsClient(f.gatewayURL)
	if err != nil {
		return err
	}
	var doc struct {
		Alerts []map[string]any `json:"alerts"`
	}
	raw, err := gatewayJSON(ctx, c, http.MethodGet, agencyAPI+"/alerts", nil, &doc)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if f.jsonOut {
		printRaw(out, raw)
		return nil
	}
	if len(doc.Alerts) == 0 {
		fmt.Fprintln(out, "No open alerts.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ALERT\tKIND\tCLIENT\tSINCE")
	for _, a := range doc.Alerts {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", field(a, "id"), field(a, "kind"), field(a, "clientOrgId"), field(a, "openedAt"))
	}
	return tw.Flush()
}
