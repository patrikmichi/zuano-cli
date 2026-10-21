package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

var (
	yankReasons = []string{"security", "broken", "legal", "policy"}
	reportKinds = []string{"broken", "security", "misleading", "abuse", "other"}
)

// marketDo sends one request of a marketplace command and prints the answer:
// the gateway's body with --json, otherwise what show writes from the decoded
// document. refusals gives the codes that are only meaningful on this route
// their exit statuses.
func marketDo[T any](cmd *cobra.Command, f approvalFlags, method, path string, body any, refusals map[string]int, show func(io.Writer, T)) error {
	c, err := approvalsClient(f.gatewayURL)
	if err != nil {
		return err
	}
	var doc T
	var raw []byte
	if method == http.MethodGet {
		raw, err = gatewayJSON(cmd.Context(), c, method, path, body, &doc)
	} else {
		raw, err = gatewayWrite(cmd, c, f.jsonOut, method, path, body, &doc)
	}
	if err != nil {
		return withRouteExit(err, refusals)
	}
	out := cmd.OutOrStdout()
	if f.jsonOut {
		printRaw(out, raw)
		return nil
	}
	show(out, doc)
	return nil
}

// listingVersionPath builds the route of one version of a listing from
// handle/slug@version.
func listingVersionPath(ref, verb string) (string, error) {
	listing, version := splitAtVersion(ref)
	if version == "" || strings.ContainsAny(version, "/?#") {
		return "", &ExitError{Code: exitUsage, Msg: fmt.Sprintf("%q is not <handle>/<slug>@<version>", textsafe.Clean(ref))}
	}
	return listingPath(listing, "/versions/"+url.PathEscape(version)+"/"+verb)
}

// marketLifecycleCmds returns the commands a publisher uses to withdraw and
// discuss what it listed.
func marketLifecycleCmds() []*cobra.Command {
	return []*cobra.Command{marketDeprecateCmd(), marketYankCmd(), marketEndOfLifeCmd(), marketReviewsCmd(), marketReportsCmd()}
}

func marketDeprecateCmd() *cobra.Command {
	var f approvalFlags
	var note string
	cmd := &cobra.Command{
		Use:   "deprecate <handle>/<slug>@<version>",
		Short: "Hide a version from new installs; installations keep running and are offered the update",
		Long: `Only an admin of the publisher organisation can deprecate a published version.
Installations of it keep running and are told about the newer version. A listing
with no installable version left is delisted. A version that is not published is
refused (exit ` + strconv.Itoa(exitVersionNotWithdrawn) + `).`,
		Example: `  zuano market deprecate acme/invoice-check@1.2.0 --note "Use 1.3.0, it fixes the totals"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := listingVersionPath(args[0], "deprecate")
			if err != nil {
				return err
			}
			var body map[string]any
			if note != "" {
				body = map[string]any{"note": note}
			}
			return marketDo(cmd, f, http.MethodPost, path, body, withdrawRefusals, func(out io.Writer, d struct {
				Listing       string `json:"listing"`
				Version       string `json:"version"`
				Installations int    `json:"installations"`
			}) {
				fmt.Fprintf(out, "Deprecated %s@%s; %d installations keep running and are offered the update.\n", textsafe.Clean(d.Listing), textsafe.Clean(d.Version), d.Installations)
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&note, "note", "", "Why, shown to the people who installed it")
	return cmd
}

func marketYankCmd() *cobra.Command {
	var f approvalFlags
	var reason, note string
	cmd := &cobra.Command{
		Use:   "yank <handle>/<slug>@<version>",
		Short: "Withdraw a version: its installations pause and are offered the newest other version",
		Long: `Withdraws a published or deprecated version for good. Every enabled installation
of it pauses; with the reason "security" its standing grants are revoked too. The
version cannot be enabled or run live again. A version that is already yanked or
is not published is refused (exit ` + strconv.Itoa(exitVersionNotWithdrawn) + `).`,
		Example: `  zuano market yank acme/invoice-check@1.2.0 --reason security --note "Leaks the invoice number"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reason == "" {
				return &ExitError{Code: exitUsage, Msg: "--reason is required: " + strings.Join(yankReasons, ", ")}
			}
			if err := oneOf("reason", reason, yankReasons); err != nil {
				return &ExitError{Code: exitUsage, Msg: err.Error()}
			}
			path, err := listingVersionPath(args[0], "yank")
			if err != nil {
				return err
			}
			body := map[string]any{"reason": reason}
			if note != "" {
				body["note"] = note
			}
			return marketDo(cmd, f, http.MethodPost, path, body, withdrawRefusals, func(out io.Writer, d struct {
				Listing       string  `json:"listing"`
				Version       string  `json:"version"`
				Reason        string  `json:"reason"`
				Installations int     `json:"installations"`
				UpgradeTo     *string `json:"upgrade_to"`
			}) {
				fmt.Fprintf(out, "Yanked %s@%s (%s); %d installations paused.\n", textsafe.Clean(d.Listing), textsafe.Clean(d.Version), textsafe.Clean(d.Reason), d.Installations)
				if d.UpgradeTo != nil {
					fmt.Fprintf(out, "Installers are offered %s.\n", textsafe.Clean(*d.UpgradeTo))
				}
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&reason, "reason", "", "Why: "+strings.Join(yankReasons, ", "))
	cmd.Flags().StringVar(&note, "note", "", "Details, kept with the withdrawal")
	return cmd
}

// parseEndDate reads the end date of a listing: an RFC 3339 time or a plain
// date, which means the start of that day in UTC.
func parseEndDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	return time.Time{}, &ExitError{Code: exitUsage, Msg: fmt.Sprintf("--at %q is not a date such as 2027-03-31 or 2027-03-31T12:00:00Z", textsafe.Clean(s))}
}

func marketEndOfLifeCmd() *cobra.Command {
	var f approvalFlags
	var at string
	cmd := &cobra.Command{
		Use:   "end-of-life <handle>/<slug>",
		Short: "Announce the date a listing ends; installers are told and it is archived that day",
		Long: `The notice must be at least 7 days for a free listing and 90 days for a paid one;
a shorter notice is refused (exit ` + strconv.Itoa(exitNoticeTooShort) + `). Installations pause when the day comes.`,
		Example: `  zuano market end-of-life acme/invoice-check --at 2027-03-31`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if at == "" {
				return &ExitError{Code: exitUsage, Msg: "--at is required"}
			}
			end, err := parseEndDate(at)
			if err != nil {
				return err
			}
			path, err := listingPath(args[0], "/end-of-life")
			if err != nil {
				return err
			}
			return marketDo(cmd, f, http.MethodPost, path, map[string]any{"ends_at": end.Format("2006-01-02T15:04:05Z")}, nil, func(out io.Writer, d struct {
				Listing string `json:"listing"`
				EndsAt  string `json:"ends_at"`
			}) {
				fmt.Fprintf(out, "%s ends on %s. Installers have been told.\n", textsafe.Clean(d.Listing), textsafe.Clean(d.EndsAt))
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&at, "at", "", "The day it ends: 2027-03-31 or an RFC 3339 time")
	return cmd
}

type marketReview struct {
	ID             string  `json:"id"`
	Rating         int     `json:"rating"`
	Body           *string `json:"body"`
	Version        *string `json:"version"`
	LiveRuns       int     `json:"live_runs"`
	PublisherReply *string `json:"publisher_reply"`
	CreatedAt      string  `json:"created_at"`
	Mine           bool    `json:"mine"`
}

func printReview(out io.Writer, verb string, r marketReview) {
	fmt.Fprintf(out, "%s review %s: %d of 5.\n", verb, textsafe.Clean(r.ID), r.Rating)
}

func marketReviewsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reviews",
		Short: "Read the reviews of a listing, review one you have run, or answer a review of yours",
		Long: `Only an organisation that ran the workflow successfully in live mode three times
can review it, and only an admin or the person who installed it; any other
attempt is refused (exit ` + strconv.Itoa(exitReviewNotEligible) + `). A review that holds an email address or a phone
number is refused. The publisher answers a review once.`,
	}
	cmd.AddCommand(marketReviewsListCmd(), reviewsPostCmd(), reviewsEditCmd(), reviewsReplyCmd())
	return cmd
}

func marketReviewsListCmd() *cobra.Command {
	var f approvalFlags
	var cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "list <handle>/<slug>",
		Short: "The visible reviews of a listing, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := listingPath(args[0], "/reviews")
			if err != nil {
				return err
			}
			q := url.Values{}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			return marketDo(cmd, f, http.MethodGet, path, nil, nil, func(out io.Writer, d struct {
				Reviews []marketReview `json:"reviews"`
				Summary struct {
					Count int      `json:"count"`
					Mean  *float64 `json:"mean"`
				} `json:"summary"`
				Eligible bool    `json:"eligible"`
				LiveRuns int     `json:"live_runs"`
				Next     *string `json:"next"`
			}) {
				if d.Summary.Mean != nil {
					fmt.Fprintf(out, "%d reviews, %.1f of 5 on average.\n", d.Summary.Count, *d.Summary.Mean)
				} else {
					fmt.Fprintf(out, "%d reviews.\n", d.Summary.Count)
				}
				if d.Eligible {
					fmt.Fprintln(out, "Your organisation can review this listing.")
				} else {
					fmt.Fprintf(out, "Your organisation has %d live runs; three are needed to review.\n", d.LiveRuns)
				}
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				for _, r := range d.Reviews {
					body := ""
					if r.Body != nil {
						body = *r.Body
					}
					who := ""
					if r.Mine {
						who = " (yours)"
					}
					fmt.Fprintf(tw, "%s\t%d/5\t%s\t%s%s\n", textsafe.Clean(r.ID), r.Rating, textsafe.Clean(r.CreatedAt), orDash(textsafe.Clean(body)), who)
					if r.PublisherReply != nil {
						fmt.Fprintf(tw, "\t\t\tpublisher: %s\n", textsafe.Clean(*r.PublisherReply))
					}
				}
				_ = tw.Flush()
				if d.Next != nil && *d.Next != "" {
					fmt.Fprintf(out, "More follow; use --cursor %s\n", textsafe.Clean(*d.Next))
				}
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue after a previous page")
	cmd.Flags().IntVar(&limit, "limit", 0, "Reviews per page (at most 50)")
	return cmd
}

func checkRating(n int) error {
	if n < 1 || n > 5 {
		return &ExitError{Code: exitUsage, Msg: "--rating is a whole number from 1 to 5"}
	}
	return nil
}

func reviewsPostCmd() *cobra.Command {
	var f approvalFlags
	var rating int
	var text string
	cmd := &cobra.Command{
		Use:     "post <handle>/<slug>",
		Short:   "Review a listing your organisation has run",
		Example: `  zuano market reviews post acme/invoice-check --rating 5 --text "Caught three wrong totals"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkRating(rating); err != nil {
				return err
			}
			path, err := listingPath(args[0], "/reviews")
			if err != nil {
				return err
			}
			body := map[string]any{"rating": rating}
			if text != "" {
				body["body"] = text
			}
			return marketDo(cmd, f, http.MethodPost, path, body, nil, func(out io.Writer, d struct {
				Review marketReview `json:"review"`
			}) {
				printReview(out, "Recorded", d.Review)
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().IntVar(&rating, "rating", 0, "From 1 to 5")
	cmd.Flags().StringVar(&text, "text", "", "What you found, at most 2000 characters; no email addresses or phone numbers")
	return cmd
}

func reviewsEditCmd() *cobra.Command {
	var f approvalFlags
	var rating int
	var text string
	cmd := &cobra.Command{
		Use:   "edit <handle>/<slug>",
		Short: "Change your organisation's review of a listing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("rating") {
				if err := checkRating(rating); err != nil {
					return err
				}
				body["rating"] = rating
			}
			if cmd.Flags().Changed("text") {
				if text == "" {
					body["body"] = nil
				} else {
					body["body"] = text
				}
			}
			if len(body) == 0 {
				return &ExitError{Code: exitUsage, Msg: "give --rating or --text"}
			}
			path, err := listingPath(args[0], "/reviews")
			if err != nil {
				return err
			}
			return marketDo(cmd, f, http.MethodPatch, path, body, nil, func(out io.Writer, d struct {
				Review marketReview `json:"review"`
			}) {
				printReview(out, "Updated", d.Review)
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().IntVar(&rating, "rating", 0, "From 1 to 5")
	cmd.Flags().StringVar(&text, "text", "", "The new text; empty removes it")
	return cmd
}

func reviewsReplyCmd() *cobra.Command {
	var f approvalFlags
	var reply string
	cmd := &cobra.Command{
		Use:   "reply <review-id>",
		Short: "Answer a review of your listing, once (publisher admins)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !workflowRefPattern.MatchString(args[0]) {
				return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("%q is not a review id", textsafe.Clean(args[0]))}
			}
			if reply == "" {
				return &ExitError{Code: exitUsage, Msg: "--reply is required"}
			}
			return marketDo(cmd, f, http.MethodPost, marketAPI+"/reviews/"+url.PathEscape(args[0])+"/reply", map[string]any{"reply": reply}, nil, func(out io.Writer, d struct {
				Review marketReview `json:"review"`
			}) {
				printReview(out, "Answered", d.Review)
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&reply, "reply", "", "Your answer, at most 1000 characters")
	return cmd
}

type marketReport struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	Status          string  `json:"status"`
	Listing         string  `json:"listing"`
	Body            string  `json:"body"`
	Response        *string `json:"response"`
	CreatedAt       string  `json:"created_at"`
	FirstResponseAt *string `json:"first_response_at"`
}

func marketReportsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reports",
		Short: "Report a problem with a listing, and answer the problems reported on yours",
		Long: `A report goes to the publisher and to the platform. The publisher's first answer
ends the response clock; the reporter is never named to the publisher.`,
	}
	cmd.AddCommand(reportsListCmd(), reportsFileCmd(), reportsRespondCmd())
	return cmd
}

func reportsListCmd() *cobra.Command {
	var f approvalFlags
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "The problems reported on your listings, newest first (publisher admins)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := oneOf("status", status, []string{"open", "responded"}); err != nil {
				return &ExitError{Code: exitUsage, Msg: err.Error()}
			}
			path := publisherAPI + "/reports"
			if status != "" {
				path += "?" + url.Values{"status": {status}}.Encode()
			}
			return marketDo(cmd, f, http.MethodGet, path, nil, nil, func(out io.Writer, d struct {
				Reports []marketReport `json:"reports"`
			}) {
				if len(d.Reports) == 0 {
					fmt.Fprintln(out, "No reports.")
					return
				}
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tLISTING\tKIND\tSTATUS\tREPORTED\tPROBLEM")
				for _, r := range d.Reports {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", textsafe.Clean(r.ID), textsafe.Clean(r.Listing), textsafe.Clean(r.Kind), textsafe.Clean(r.Status), textsafe.Clean(r.CreatedAt), textsafe.Clean(r.Body))
				}
				_ = tw.Flush()
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&status, "status", "", "Only open or responded reports")
	return cmd
}

func reportsFileCmd() *cobra.Command {
	var f approvalFlags
	var kind, text string
	cmd := &cobra.Command{
		Use:     "file <handle>/<slug>",
		Short:   "Report a problem with a listing to its publisher and the platform",
		Example: `  zuano market reports file acme/invoice-check --kind broken --text "It stops at step 3 with a 500"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if kind == "" || text == "" {
				return &ExitError{Code: exitUsage, Msg: "--kind and --text are required"}
			}
			if err := oneOf("kind", kind, reportKinds); err != nil {
				return &ExitError{Code: exitUsage, Msg: err.Error()}
			}
			path, err := listingPath(args[0], "/reports")
			if err != nil {
				return err
			}
			return marketDo(cmd, f, http.MethodPost, path, map[string]any{"kind": kind, "body": text}, nil, func(out io.Writer, d struct {
				Report marketReport `json:"report"`
			}) {
				fmt.Fprintf(out, "Reported as %s (%s).\n", textsafe.Clean(d.Report.ID), textsafe.Clean(d.Report.Kind))
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&kind, "kind", "", "What is wrong: "+strings.Join(reportKinds, ", "))
	cmd.Flags().StringVar(&text, "text", "", "The problem, 10 to 2000 characters")
	return cmd
}

func reportsRespondCmd() *cobra.Command {
	var f approvalFlags
	var message string
	cmd := &cobra.Command{
		Use:   "respond <report-id>",
		Short: "Answer a reported problem; the first answer ends the response clock (publisher admins)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !workflowRefPattern.MatchString(args[0]) {
				return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("%q is not a report id", textsafe.Clean(args[0]))}
			}
			if message == "" {
				return &ExitError{Code: exitUsage, Msg: "--message is required"}
			}
			return marketDo(cmd, f, http.MethodPost, marketAPI+"/reports/"+url.PathEscape(args[0])+"/respond", map[string]any{"message": message}, nil, func(out io.Writer, d struct {
				Report marketReport `json:"report"`
			}) {
				fmt.Fprintf(out, "Answered report %s (%s).\n", textsafe.Clean(d.Report.ID), textsafe.Clean(d.Report.Status))
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&message, "message", "", "Your answer, at most 2000 characters")
	return cmd
}
