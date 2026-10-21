package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

var (
	modelSurfaces = []string{"studio_ai", "workflow_step", "agent", "eval_subject", "eval_judge", "internal"}
	marketSorts   = []string{"best_match", "outcome_success", "lowest_cost", "newest"}
	marketRisks   = []string{"read", "write", "outward", "destructive"}
)

func oneOf(flag, value string, allowed []string) error {
	if value == "" {
		return nil
	}
	for _, a := range allowed {
		if a == value {
			return nil
		}
	}
	return fmt.Errorf("invalid --%s %q: use one of %s", flag, textsafe.Clean(value), strings.Join(allowed, ", "))
}

// printIndented prints a gateway document that has no table form, indented.
func printIndented(cmd *cobra.Command, raw []byte) {
	var buf bytes.Buffer
	if json.Indent(&buf, bytes.TrimSpace(raw), "", "  ") != nil {
		printRaw(cmd.OutOrStdout(), raw)
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), buf.String())
}

// ModelsCmd returns `zuano models`, the models the organisation may run.
func ModelsCmd() *cobra.Command {
	var f approvalFlags
	var surface string
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the models you can run and which key or credit pays for each",
		Long: `Lists the models offered on a surface with the sources that are open for each:
'byo' (your own key) and 'platform' (Zuano credit). A model without a current price
is never offered on the platform.`,
		Example: `  zuano models
  zuano models --surface agent`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := oneOf("surface", surface, modelSurfaces); err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			path := "/api/v1/models"
			if surface != "" {
				path += "?" + url.Values{"surface": {surface}}.Encode()
			}
			var doc struct {
				Mode struct {
					Allowed []string `json:"allowed"`
					Default string   `json:"default"`
				} `json:"mode"`
				Defaults struct {
					Model  string `json:"model"`
					Source string `json:"source"`
				} `json:"defaults"`
				BalanceNano *float64 `json:"balanceNano"`
				Models      []struct {
					ID      string `json:"id"`
					Label   string `json:"label"`
					Status  string `json:"status"`
					Sources struct {
						Platform modelSource `json:"platform"`
						BYO      modelSource `json:"byo"`
					} `json:"sources"`
				} `json:"models"`
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
			fmt.Fprintf(out, "Default: %s on %s; sources open: %s\n", textsafe.Clean(doc.Defaults.Model), textsafe.Clean(doc.Defaults.Source), orDash(textsafe.Clean(strings.Join(doc.Mode.Allowed, ", "))))
			if doc.BalanceNano != nil {
				fmt.Fprintf(out, "Credit: %.2f\n", *doc.BalanceNano/1e9)
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "MODEL\tNAME\tSTATUS\tPLATFORM\tBYO")
			for _, m := range doc.Models {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", textsafe.Clean(m.ID), orDash(textsafe.Clean(m.Label)), textsafe.Clean(m.Status), m.Sources.Platform.summary(), m.Sources.BYO.summary())
			}
			return tw.Flush()
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&surface, "surface", "", "Where the model will run: "+strings.Join(modelSurfaces, ", ")+" (default: workflow_step)")
	cmd.AddCommand(modelsSettingsCmd(), modelsResumeCmd(), modelsDefaultsCmd())
	return cmd
}

type modelSource struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Account   string `json:"account"`
	Price     *struct {
		In  float64 `json:"inputPerMTokNano"`
		Out float64 `json:"outputPerMTokNano"`
	} `json:"price"`
}

func (s modelSource) summary() string {
	if !s.Available {
		return "no (" + orDash(textsafe.Clean(s.Reason)) + ")"
	}
	if s.Price != nil {
		return fmt.Sprintf("yes (%.2f / %.2f per M tokens)", s.Price.In/1e9, s.Price.Out/1e9)
	}
	if s.Account != "" {
		return "yes (" + textsafe.Clean(s.Account) + ")"
	}
	return "yes"
}

const marketAPI = "/api/v1/market"

// MarketCmd returns `zuano market`, the workflow marketplace.
func MarketCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "market",
		Short: "Find listed workflows and manage the updates of installed ones",
		Long: `Reading and searching are never metered. Listings are addressed as handle/slug.
Install one with 'zuano workflow install <handle>/<slug>'.`,
	}
	cmd.AddCommand(marketSearchCmd(), marketShowCmd(), marketCostCmd(), marketUpdateCmd(), marketUpdatePolicyCmd(), marketPublisherCmd(), marketSubmitCmd())
	return cmd
}

func marketSearchCmd() *cobra.Command {
	var f approvalFlags
	var category, connectors, invocation, risk, badge, sort string
	var ready bool
	var limit, offset int
	cmd := &cobra.Command{
		Use:     "search [query]",
		Short:   "Search listed workflows",
		Example: `  zuano market search invoice --connectors gmail,slack --ready --sort lowest_cost`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := oneOf("sort", sort, marketSorts); err != nil {
				return err
			}
			if err := oneOf("max-risk", risk, marketRisks); err != nil {
				return err
			}
			q := url.Values{}
			set := func(k, v string) {
				if v != "" {
					q.Set(k, v)
				}
			}
			if len(args) == 1 {
				set("q", args[0])
			}
			set("category", category)
			set("connectors", connectors)
			set("invocation", invocation)
			set("max_risk", risk)
			set("badge", badge)
			set("sort", sort)
			if ready {
				q.Set("ready", "true")
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if offset > 0 {
				q.Set("offset", strconv.Itoa(offset))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			path := marketAPI + "/listings"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var doc struct {
				Listings []struct {
					Handle    string   `json:"handle"`
					Slug      string   `json:"slug"`
					Title     string   `json:"title"`
					Publisher string   `json:"publisherName"`
					MaxRisk   string   `json:"maxRisk"`
					Badges    []string `json:"badges"`
					Missing   []string `json:"missing"`
				} `json:"listings"`
				Total int `json:"total"`
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
			if len(doc.Listings) == 0 {
				fmt.Fprintln(out, "No listings match.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "LISTING\tTITLE\tPUBLISHER\tRISK\tBADGES\tMISSING APPS")
			for _, l := range doc.Listings {
				fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%s\t%s\n", textsafe.Clean(l.Handle), textsafe.Clean(l.Slug), textsafe.Clean(l.Title), orDash(textsafe.Clean(l.Publisher)), textsafe.Clean(l.MaxRisk), orDash(textsafe.Clean(strings.Join(l.Badges, ","))), orDash(textsafe.Clean(strings.Join(l.Missing, ","))))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if shown := offset + len(doc.Listings); shown < doc.Total {
				fmt.Fprintf(out, "%d of %d shown; use --offset %d for more.\n", shown, doc.Total, shown)
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	fl := cmd.Flags()
	fl.StringVar(&category, "category", "", "Only this category")
	fl.StringVar(&connectors, "connectors", "", "Comma-separated apps, all required")
	fl.StringVar(&invocation, "invocation", "", "mcp, api, manual, schedule, platform_event or event")
	fl.StringVar(&risk, "max-risk", "", "Highest risk allowed: "+strings.Join(marketRisks, ", "))
	fl.StringVar(&badge, "badge", "", "Only listings with this badge")
	fl.StringVar(&sort, "sort", "", "Order: "+strings.Join(marketSorts, ", "))
	fl.BoolVar(&ready, "ready", false, "Only workflows whose apps you have connected")
	fl.IntVar(&limit, "limit", 0, "Results per page (at most 50)")
	fl.IntVar(&offset, "offset", 0, "Results to skip (at most 1000)")
	return cmd
}

// listingPath builds the route of one listing from handle/slug.
func listingPath(ref, suffix string) (string, error) {
	handle, slug, ok := strings.Cut(ref, "/")
	if !ok || handle == "" || slug == "" || strings.Contains(slug, "/") {
		return "", fmt.Errorf("%q is not a listing: use handle/slug", textsafe.Clean(ref))
	}
	return marketAPI + "/listings/" + url.PathEscape(handle) + "/" + url.PathEscape(slug) + suffix, nil
}

func marketReadCmd(use, short, suffix string) *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   use + " <handle>/<slug>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := listingPath(args[0], suffix)
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			printIndented(cmd, raw)
			return nil
		},
	}
	cmd.Flags().StringVar(&f.gatewayURL, "gateway-url", "", "Gateway URL (default: $ZUANO_URL, then the config file)")
	return cmd
}

func marketShowCmd() *cobra.Command {
	return marketReadCmd("show", "One listing: fit, permissions, risk, inputs, versions, quality and cost", "")
}

func marketCostCmd() *cobra.Command {
	return marketReadCmd("cost", "The cost per run of a listing and its breakdown by meter", "/cost-estimate")
}

func marketUpdateCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "update <installation>",
		Short: "The newer version of an installed listing and whether it needs consent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !workflowRefPattern.MatchString(args[0]) {
				return fmt.Errorf("%q is not an installation id", textsafe.Clean(args[0]))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, marketAPI+"/installations/"+url.PathEscape(args[0])+"/update", nil, nil)
			if err != nil {
				return err
			}
			printIndented(cmd, raw)
			return nil
		},
	}
	cmd.Flags().StringVar(&f.gatewayURL, "gateway-url", "", "Gateway URL (default: $ZUANO_URL, then the config file)")
	return cmd
}

func marketUpdatePolicyCmd() *cobra.Command {
	var f approvalFlags
	var channel string
	cmd := &cobra.Command{
		Use:   "update-policy <installation> <auto_compatible|manual>",
		Short: "Choose whether updates that ask for nothing new are taken automatically",
		Long: `Automatic updates need the workflows.auto_update plan feature. --channel picks
the stable or beta channel the installation follows.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !workflowRefPattern.MatchString(args[0]) {
				return fmt.Errorf("%q is not an installation id", textsafe.Clean(args[0]))
			}
			if err := oneOf("policy", args[1], []string{"auto_compatible", "manual"}); err != nil {
				return err
			}
			if err := oneOf("channel", channel, []string{"stable", "beta"}); err != nil {
				return err
			}
			body := map[string]any{"policy": args[1]}
			if channel != "" {
				body["channel"] = channel
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodPut, marketAPI+"/installations/"+url.PathEscape(args[0])+"/update-policy", body, nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s now follows %s updates.\n", textsafe.Clean(args[0]), textsafe.Clean(args[1]))
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&channel, "channel", "", "stable or beta")
	return cmd
}

// AppsCmd returns `zuano apps`, the board of apps members ask for.
func AppsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Ask for an app that is not in the catalog and vote on requests",
		Long:  `One request per app; your organisation's vote counts once. An app that is already available is refused.`,
	}
	cmd.AddCommand(appsRequestCmd(), appsRequestsCmd(), appsVoteCmd())
	return cmd
}

type appRequest struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Votes  int    `json:"votes"`
}

func appsRequestCmd() *cobra.Command {
	var f approvalFlags
	var link string
	cmd := &cobra.Command{
		Use:   "request <name>",
		Short: "Ask for an app, or add your organisation's vote when it was already asked for",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"name": args[0]}
			if link != "" {
				body["url"] = link
			}
			return appsPost(cmd, f, "/api/v1/apps/requests", body)
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&link, "url", "", "An https link to the app")
	return cmd
}

func appsVoteCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "vote <id>",
		Short: "Vote for a request once for your organisation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !workflowRefPattern.MatchString(args[0]) {
				return fmt.Errorf("%q is not a request id", textsafe.Clean(args[0]))
			}
			return appsPost(cmd, f, "/api/v1/apps/requests/"+url.PathEscape(args[0])+"/vote", nil)
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	return cmd
}

func appsPost(cmd *cobra.Command, f approvalFlags, path string, body any) error {
	c, err := approvalsClient(f.gatewayURL)
	if err != nil {
		return err
	}
	var doc struct {
		Request appRequest `json:"request"`
		Voted   bool       `json:"voted"`
	}
	raw, err := gatewayJSON(cmd.Context(), c, http.MethodPost, path, body, &doc)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if f.jsonOut {
		printRaw(out, raw)
		return nil
	}
	verb := "Your organisation had already voted for"
	if doc.Voted {
		verb = "Counted your organisation's vote for"
	}
	fmt.Fprintf(out, "%s %s (%s, %d votes), id %s.\n", verb, textsafe.Clean(doc.Request.Name), textsafe.Clean(doc.Request.Status), doc.Request.Votes, textsafe.Clean(doc.Request.ID))
	return nil
}

func appsRequestsCmd() *cobra.Command {
	var f approvalFlags
	var status, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "requests",
		Short: "The public board, most voted first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			path := "/api/v1/apps/requests"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var doc struct {
				Requests   []appRequest `json:"requests"`
				NextCursor string       `json:"next_cursor"`
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
			if len(doc.Requests) == 0 {
				fmt.Fprintln(out, "No requests.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tAPP\tSTATUS\tVOTES")
			for _, r := range doc.Requests {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", textsafe.Clean(r.ID), textsafe.Clean(r.Name), textsafe.Clean(r.Status), r.Votes)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if doc.NextCursor != "" {
				fmt.Fprintf(out, "More follow; use --cursor %s\n", textsafe.Clean(doc.NextCursor))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&status, "status", "", "Only requests in this status (default: open, planned and in review)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue after a previous page")
	cmd.Flags().IntVar(&limit, "limit", 0, "Rows per page (at most 50)")
	return cmd
}
