package cli

import (
	"fmt"
	"io"
	"regexp"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

var usageDay = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// UsageCmd returns `zuano usage`.
func UsageCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show the organisation's usage against the plan's allowances",
		Long: `Prints each metered quantity with what the organisation used in the current
billing period, what the plan includes and when the period ends. A meter whose
plan blocks at the allowance refuses further use with exit status 31;
'zuano billing usage' shows the projected cost, spend and the breakdown.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var u billingUsage
			raw, err := gatewayJSON(cmd.Context(), c, "GET", "/api/v1/billing/usage", nil, &u)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			printOrgUsage(cmd.OutOrStdout(), u)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func printOrgUsage(out io.Writer, u billingUsage) {
	if len(u.Meters) == 0 {
		fmt.Fprintln(out, "No metered usage in this period")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "METRIC\tUNIT\tUSED\tINCLUDED\tRESETS")
	for _, m := range u.Meters {
		included := "unlimited"
		state := ""
		if m.Included != nil {
			included = fmt.Sprintf("%.0f", *m.Included)
			if m.Used >= *m.Included {
				state = "  over the allowance"
				if m.Overage == "none" {
					state = "  at the limit"
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%.0f\t%s\t%s%s\n", textsafe.Clean(m.Meter), textsafe.Clean(m.Unit), m.Used, included, textsafe.Clean(u.Period.End), state)
	}
	_ = tw.Flush()
}
