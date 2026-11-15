package cli

import (
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/config"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

var billingGroups = []string{"member", "service", "tool", "source"}

// billingStatus mirrors GET /api/v1/billing/status; amounts are nano-euros.
type billingStatus struct {
	Plan struct {
		Key         string `json:"key"`
		DisplayName string `json:"display_name"`
		Source      string `json:"source"`
		Status      string `json:"status"`
	} `json:"plan"`
	Period struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"period"`
	Trial struct {
		EndsAt *string `json:"ends_at"`
		Used   bool    `json:"used"`
	} `json:"trial"`
	CancelAtPeriodEnd bool    `json:"cancel_at_period_end"`
	ScheduledPlan     *string `json:"scheduled_plan"`
	Verified          bool    `json:"verified"`
	Suspended         bool    `json:"suspended"`
	PastDue           bool    `json:"past_due"`
	Dunning           *struct {
		StartedAt   string `json:"started_at"`
		Day         int    `json:"day"`
		GraceEndsAt string `json:"grace_ends_at"`
	} `json:"dunning"`
	PaymentActionURL *string `json:"payment_action_url"`
	OverageEnabled   bool    `json:"overage_enabled"`
	SpendCapNano     *int64  `json:"spend_cap_nano"`
	Credit           struct {
		GrantedNano int64   `json:"granted_nano"`
		Source      string  `json:"source"`
		ExpiresAt   *string `json:"expires_at"`
	} `json:"credit"`
	OverLimit map[string]struct {
		Since       string `json:"since"`
		GraceEndsAt string `json:"grace_ends_at"`
	} `json:"over_limit"`
	BillingConfigured bool `json:"billing_configured"`
}

// billingUsage mirrors GET /api/v1/billing/usage; amounts are nano-euros.
type billingUsage struct {
	Period struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"period"`
	Meters []struct {
		Meter             string   `json:"meter"`
		Unit              string   `json:"unit"`
		Used              float64  `json:"used"`
		Included          *float64 `json:"included"`
		Percent           *float64 `json:"percent"`
		Projected         float64  `json:"projected"`
		ProjectedCostNano float64  `json:"projected_cost_nano"`
		Overage           string   `json:"overage"`
	} `json:"meters"`
	Spend struct {
		BillableNano        float64 `json:"billable_nano"`
		CreditRemainingNano float64 `json:"credit_remaining_nano"`
		BeyondCreditNano    float64 `json:"beyond_credit_nano"`
		CapNano             *int64  `json:"cap_nano"`
	} `json:"spend"`
	Breakdown struct {
		By    string `json:"by"`
		Scope string `json:"scope"`
		Rows  []struct {
			Label    string  `json:"label"`
			Quantity float64 `json:"quantity"`
		} `json:"rows"`
	} `json:"breakdown"`
}

// BillingCmd returns `zuano billing`.
func BillingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "billing",
		Short: "Show the organisation's plan and usage, and open its billing page",
		Long: `Read-only views of the organisation's plan and metered usage, and a shortcut to
the billing page in the browser. Changing the plan, paying and the customer
portal are done there: the CLI holds no billing credential.

A request the plan does not allow ends with an exit status of 30 to 33 and the
link to upgrade, on every zuano command:
  30  a feature or limit the plan does not include
  31  a metered allowance or the spend cap is used up
  32  billing could not be read; try again
  33  the organisation or your seat is suspended`,
	}
	cmd.AddCommand(billingStatusCmd(), billingUsageCmd(), billingCreditsCmd(), billingOpenCmd())
	return cmd
}

func billingStatusCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the plan, subscription state, credit and spend cap",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var s billingStatus
			raw, err := gatewayJSON(cmd.Context(), c, "GET", "/api/v1/billing/status", nil, &s)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			printBillingStatus(cmd.OutOrStdout(), s)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

var planSources = map[string]string{
	"stripe":    "subscription",
	"default":   "default plan",
	"admin":     "assigned by an administrator",
	"simulated": "simulated",
}

func printBillingStatus(out io.Writer, s billingStatus) {
	clean := textsafe.Clean
	plan := clean(s.Plan.DisplayName)
	if src, ok := planSources[s.Plan.Source]; ok {
		plan += " — " + src
	}
	fmt.Fprintf(out, "Plan:      %s\n", plan)
	switch {
	case s.Suspended:
		fmt.Fprintln(out, "Status:    suspended")
	case s.PastDue && s.Dunning != nil:
		fmt.Fprintf(out, "Status:    payment failing since %s (day %d); the organisation moves to Free on %s unless it is paid\n", clean(s.Dunning.StartedAt), s.Dunning.Day, clean(s.Dunning.GraceEndsAt))
	case s.Plan.Status != "" && s.Plan.Status != "none":
		status := clean(s.Plan.Status)
		if s.Plan.Status == "trialing" && s.Trial.EndsAt != nil {
			status += ", trial ends " + clean(*s.Trial.EndsAt)
		}
		fmt.Fprintf(out, "Status:    %s\n", status)
	}
	if s.Period.End != "" {
		fmt.Fprintf(out, "Period:    %s to %s\n", clean(s.Period.Start), clean(s.Period.End))
	}
	if s.CancelAtPeriodEnd {
		fmt.Fprintln(out, "Cancels:   at the end of the period")
	}
	if s.ScheduledPlan != nil {
		fmt.Fprintf(out, "Moves to:  %s at the end of the period\n", clean(*s.ScheduledPlan))
	}
	fmt.Fprintf(out, "Verified:  %s\n", yesNo(s.Verified))
	overage := "off"
	if s.OverageEnabled {
		overage = "on, no spend cap"
		if s.SpendCapNano != nil {
			overage = "on, spend cap " + formatNanoEuros(*s.SpendCapNano)
		}
	}
	fmt.Fprintf(out, "Overage:   %s\n", overage)
	if s.Credit.GrantedNano > 0 {
		credit := formatNanoEuros(s.Credit.GrantedNano) + " (" + clean(s.Credit.Source) + ")"
		if s.Credit.ExpiresAt != nil {
			credit += ", until " + clean(*s.Credit.ExpiresAt)
		}
		fmt.Fprintf(out, "Credit:    %s\n", credit)
	}
	for _, key := range slices.Sorted(maps.Keys(s.OverLimit)) {
		o := s.OverLimit[key]
		fmt.Fprintf(out, "Over limit: %s since %s, until %s\n", clean(key), clean(o.Since), clean(o.GraceEndsAt))
	}
	if s.PaymentActionURL != nil && *s.PaymentActionURL != "" {
		fmt.Fprintf(out, "Pay:       %s\n", clean(*s.PaymentActionURL))
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func billingUsageCmd() *cobra.Command {
	var f approvalFlags
	var meter, by string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show this period's metered usage against the plan's allowances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if by != "" && !slices.Contains(billingGroups, by) {
				return fmt.Errorf("invalid --by %q: use one of %s", by, strings.Join(billingGroups, ", "))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			q := url.Values{}
			if meter != "" {
				q.Set("meter", meter)
			}
			if by != "" {
				q.Set("by", by)
			}
			path := "/api/v1/billing/usage"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var u billingUsage
			raw, err := gatewayJSON(cmd.Context(), c, "GET", path, nil, &u)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			printBillingUsage(cmd.OutOrStdout(), u, meter)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&meter, "meter", "", "Only this meter, such as tool_calls")
	cmd.Flags().StringVar(&by, "by", "", "Break the tool calls down by "+strings.Join(billingGroups, ", ")+" (default source)")
	return cmd
}

func printBillingUsage(out io.Writer, u billingUsage, only string) {
	fmt.Fprintf(out, "Period: %s to %s\n", textsafe.Clean(u.Period.Start), textsafe.Clean(u.Period.End))
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	shown := 0
	for _, m := range u.Meters {
		if only != "" && m.Meter != only {
			continue
		}
		if shown == 0 {
			fmt.Fprintln(tw, "METER\tUSED\tINCLUDED\tPERCENT\tPROJECTED\tPROJECTED COST")
		}
		shown++
		included, percent := "unlimited", "-"
		if m.Included != nil {
			included = fmt.Sprintf("%.0f", *m.Included)
		}
		if m.Percent != nil {
			percent = fmt.Sprintf("%.0f%%", *m.Percent)
		}
		cost := "-"
		if m.Overage == "bill" {
			cost = formatNanoEuros(int64(m.ProjectedCostNano))
		}
		fmt.Fprintf(tw, "%s\t%.0f\t%s\t%s\t%.0f\t%s\n", textsafe.Clean(m.Meter), m.Used, included, percent, m.Projected, cost)
	}
	if shown == 0 {
		fmt.Fprintln(out, "No metered usage in this period")
		return
	}
	_ = tw.Flush()
	spend := "Spend:  " + formatNanoEuros(int64(u.Spend.BillableNano)) + " (credit left " + formatNanoEuros(int64(u.Spend.CreditRemainingNano))
	if u.Spend.BeyondCreditNano > 0 {
		spend += ", beyond credit " + formatNanoEuros(int64(u.Spend.BeyondCreditNano))
	}
	if u.Spend.CapNano != nil {
		spend += ", cap " + formatNanoEuros(*u.Spend.CapNano)
	}
	fmt.Fprintln(out, spend+")")
	if len(u.Breakdown.Rows) > 0 {
		fmt.Fprintf(out, "Tool calls by %s (%s):\n", textsafe.Clean(u.Breakdown.By), textsafe.Clean(u.Breakdown.Scope))
		for _, r := range u.Breakdown.Rows {
			fmt.Fprintf(out, "  %s  %.0f\n", textsafe.Clean(r.Label), r.Quantity)
		}
	}
}

func billingOpenCmd() *cobra.Command {
	var gatewayURL string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open the billing page in the browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gURL, err := resolveGatewayURLOrFailClosed(gatewayURL)
			if err != nil {
				return err
			}
			origin, err := config.NormalizeGatewayURL(gURL)
			if err != nil {
				return err
			}
			page := origin + "/admin/billing"
			out := cmd.OutOrStdout()
			if canOpenBrowser(cmd, noBrowser) {
				if err := openBrowserURL(page); err == nil {
					fmt.Fprintf(out, "Opened %s\n", page)
					return nil
				}
			}
			fmt.Fprintln(out, page)
			return nil
		},
	}
	cmd.Flags().StringVar(&gatewayURL, "gateway-url", "", "Gateway URL (default: $ZUANO_URL, then the config file)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	return cmd
}
