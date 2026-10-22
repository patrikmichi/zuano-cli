package cli

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// creditsAPI is the route of the organisation's prepaid model credits.
const creditsAPI = "/api/v1/billing/credits"

// creditOverview mirrors GET /api/v1/billing/credits; amounts are nano-euros.
type creditOverview struct {
	BalanceNano int64 `json:"balance_nano"`
	HeldNano    int64 `json:"held_nano"`
	Buckets     []struct {
		ID            string  `json:"id"`
		Kind          string  `json:"kind"`
		RemainingNano int64   `json:"remaining_nano"`
		GrantedNano   int64   `json:"granted_nano"`
		ExpiresAt     *string `json:"expires_at"`
	} `json:"buckets"`
	Entries []struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		AmountNano int64  `json:"amount_nano"`
		CreatedAt  string `json:"created_at"`
	} `json:"entries"`
}

func billingCreditsCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "credits",
		Short: "Show the prepaid model credit balance, what each bucket holds and the latest entries",
		Long: `Read-only view of the organisation's prepaid model credits: the balance that can
be spent, what running calls hold back, each top-up or grant that still has
credit with its expiry, and the latest top-ups, grants, refunds and expiries.
Buying credits and the auto top-up are done in the billing page. Owners and
administrators only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var o creditOverview
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, creditsAPI, nil, &o)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			printCredits(cmd.OutOrStdout(), o)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

// formatNanoEuros renders billionths of a euro as euros with the cents and
// as many further digits as the amount needs, so a fraction of a cent left
// after many small calls is not shown as zero.
func formatNanoEuros(nano int64) string {
	sign := ""
	if nano < 0 {
		sign, nano = "-", -nano
	}
	frac := strings.TrimRight(fmt.Sprintf("%09d", nano%1_000_000_000), "0")
	for len(frac) < 2 {
		frac += "0"
	}
	return fmt.Sprintf("%s%d.%s EUR", sign, nano/1_000_000_000, frac)
}

func printCredits(out io.Writer, o creditOverview) {
	fmt.Fprintf(out, "Balance:  %s\n", formatNanoEuros(o.BalanceNano))
	if o.HeldNano > 0 {
		fmt.Fprintf(out, "Held:     %s (reserved by calls that are running)\n", formatNanoEuros(o.HeldNano))
	}
	if len(o.Buckets) == 0 {
		fmt.Fprintln(out, "No credit left in any top-up or grant")
	} else {
		fmt.Fprintln(out)
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "BUCKET\tKIND\tREMAINING\tGRANTED\tEXPIRES")
		for _, b := range o.Buckets {
			expires := "never"
			if b.ExpiresAt != nil {
				expires = textsafe.Clean(*b.ExpiresAt)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", textsafe.Clean(b.ID), textsafe.Clean(b.Kind), formatNanoEuros(b.RemainingNano), formatNanoEuros(b.GrantedNano), expires)
		}
		_ = tw.Flush()
	}
	if len(o.Entries) == 0 {
		return
	}
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ENTRY\tKIND\tAMOUNT\tWHEN")
	for _, e := range o.Entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", textsafe.Clean(e.ID), textsafe.Clean(e.Kind), formatNanoEuros(e.AmountNano), textsafe.Clean(e.CreatedAt))
	}
	_ = tw.Flush()
}
