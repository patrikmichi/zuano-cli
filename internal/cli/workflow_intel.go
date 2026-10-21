package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

type metricSummary struct {
	Runs             int `json:"runs"`
	Succeeded        int `json:"succeeded"`
	Failed           int `json:"failed"`
	Dead             int `json:"dead"`
	Cancelled        int `json:"cancelled"`
	ExecutionSuccess struct {
		Rate *float64 `json:"rate"`
		Low  *float64 `json:"low"`
		High *float64 `json:"high"`
		N    int      `json:"n"`
	} `json:"executionSuccess"`
	Outcome struct {
		Verified           int      `json:"verified"`
		FailedVerification int      `json:"failedVerification"`
		Unverified         int      `json:"unverified"`
		Pending            int      `json:"pending"`
		Success            *float64 `json:"success"`
		Coverage           *float64 `json:"coverage"`
	} `json:"outcome"`
	Cost struct {
		TotalNano int64    `json:"totalNano"`
		MeanNano  *float64 `json:"meanNano"`
		P95Nano   *float64 `json:"p95Nano"`
	} `json:"cost"`
	ActiveMs struct {
		P50 *float64 `json:"p50"`
		P95 *float64 `json:"p95"`
	} `json:"activeMs"`
	Intervention struct {
		Runs int      `json:"runs"`
		Rate *float64 `json:"rate"`
	} `json:"intervention"`
	Quality struct {
		Score *float64 `json:"score"`
	} `json:"quality"`
}

func percent(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v*100, 'f', 1, 64) + "%"
}

func nanoDollars(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("$%.4f", *v/1e9)
}

func seconds(ms *float64) string {
	if ms == nil {
		return "-"
	}
	return strconv.FormatFloat(*ms/1000, 'f', 1, 64) + "s"
}

func printMetricSummary(out io.Writer, indent string, s metricSummary) {
	es := s.ExecutionSuccess
	interval := ""
	if es.Low != nil && es.High != nil {
		interval = fmt.Sprintf(" (%s to %s)", percent(es.Low), percent(es.High))
	}
	fmt.Fprintf(out, "%sruns               %d (%d succeeded, %d failed, %d dead, %d cancelled)\n", indent, s.Runs, s.Succeeded, s.Failed, s.Dead, s.Cancelled)
	fmt.Fprintf(out, "%sexecution success  %s%s\n", indent, percent(es.Rate), interval)
	fmt.Fprintf(out, "%soutcome success    %s of %d decided; coverage %s (%d unverified, %d pending)\n", indent, percent(s.Outcome.Success), s.Outcome.Verified+s.Outcome.FailedVerification, percent(s.Outcome.Coverage), s.Outcome.Unverified, s.Outcome.Pending)
	fmt.Fprintf(out, "%squality            %s\n", indent, percent(s.Quality.Score))
	fmt.Fprintf(out, "%sintervention       %s (%d run(s))\n", indent, percent(s.Intervention.Rate), s.Intervention.Runs)
	fmt.Fprintf(out, "%scost               mean %s, p95 %s, total $%.4f\n", indent, nanoDollars(s.Cost.MeanNano), nanoDollars(s.Cost.P95Nano), float64(s.Cost.TotalNano)/1e9)
	fmt.Fprintf(out, "%sactive time        p50 %s, p95 %s\n", indent, seconds(s.ActiveMs.P50), seconds(s.ActiveMs.P95))
}

// parseWindowDays reads "7d" or "7" as a number of days.
func parseWindowDays(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid --window %q: use a number of days such as 7d", textsafe.Clean(s))
	}
	return n, nil
}

func workflowQualityCmd() *cobra.Command {
	var f approvalFlags
	var window, version, invocation, groupBy string
	cmd := &cobra.Command{
		Use:   "quality <installation>",
		Short: "Execution success, outcome success, quality, cost and time of a workflow",
		Long: `Reads the figures the gateway keeps for an installation over the last days
(default 7). Execution success is whether runs finished; outcome success is whether
what they did was verified to be right. --group-by splits the figures by version,
invocation or day. Reading them needs a plan with workflow metrics.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			days, err := parseWindowDays(window)
			if err != nil {
				return err
			}
			if groupBy != "" && groupBy != "version" && groupBy != "invocation" && groupBy != "day" {
				return errors.New("--group-by is version, invocation or day")
			}
			q := url.Values{"days": {strconv.Itoa(days)}}
			for k, v := range map[string]string{"version": version, "invocation": invocation, "group_by": groupBy} {
				if v != "" {
					q.Set(k, v)
				}
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Window struct {
					From string `json:"from"`
					To   string `json:"to"`
				} `json:"window"`
				Total  metricSummary `json:"total"`
				Groups []struct {
					Key     string        `json:"key"`
					Summary metricSummary `json:"summary"`
				} `json:"groups"`
				Truncated bool `json:"truncated"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, workflowPath(id, "metrics")+"?"+q.Encode(), nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "%s, %s to %s\n", textsafe.Clean(id), textsafe.Clean(doc.Window.From), textsafe.Clean(doc.Window.To))
			printMetricSummary(out, "  ", doc.Total)
			for _, g := range doc.Groups {
				fmt.Fprintf(out, "%s\n", textsafe.Clean(g.Key))
				printMetricSummary(out, "  ", g.Summary)
			}
			if doc.Truncated {
				fmt.Fprintln(out, "Some groups are not shown; narrow the window.")
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&window, "window", "7d", "Days to cover, such as 7d")
	cmd.Flags().StringVar(&version, "version", "", "Only runs of this version")
	cmd.Flags().StringVar(&invocation, "invocation", "", "Only runs started this way (mcp, api, manual, triggers)")
	cmd.Flags().StringVar(&groupBy, "group-by", "", "Split by version, invocation or day")
	return cmd
}

func workflowOutcomeCmd() *cobra.Command {
	var f approvalFlags
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "outcome <run>",
		Short: "Whether a finished run was verified, with its checks",
		Long: `Prints the run's outcome (verified, failed_verification, unverified or pending)
and each check that decides it. With --wait the command keeps checking until the
outcome is decided; delayed checks can take hours.

exit status
  0   verified
  20  failed_verification
  17  --wait ran out before the outcome was decided
  1   unverified (no check could decide)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !workflowRunPattern.MatchString(args[0]) {
				return fmt.Errorf("%q is not a workflow run (expected wfr_...)", textsafe.Clean(args[0]))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			deadline := time.Now().Add(wait)
			for {
				var doc struct {
					Run struct {
						ID           string `json:"id"`
						Status       string `json:"status"`
						Outcome      string `json:"outcome"`
						Verification *struct {
							Required int `json:"required"`
							Passed   int `json:"passed"`
							Failed   int `json:"failed"`
							Pending  int `json:"pending"`
							Skipped  int `json:"skipped"`
							Expired  int `json:"expired"`
							Checks   []struct {
								ID       string   `json:"id"`
								Kind     string   `json:"kind"`
								Required bool     `json:"required"`
								Status   string   `json:"status"`
								Score    *float64 `json:"score"`
							} `json:"checks"`
						} `json:"verification"`
					} `json:"run"`
				}
				raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, workflowPath("runs", args[0]), nil, &doc)
				if err != nil {
					return err
				}
				decided := workflowRunTerminal(doc.Run.Status) && doc.Run.Outcome != "pending"
				if decided || wait == 0 || time.Now().After(deadline) {
					return printOutcome(cmd, raw, doc.Run.ID, doc.Run.Status, doc.Run.Outcome, decided, wait, f.jsonOut, func(out io.Writer) {
						if v := doc.Run.Verification; v != nil {
							fmt.Fprintf(out, "  required checks: %d passed, %d failed, %d pending, %d skipped, %d expired of %d\n", v.Passed, v.Failed, v.Pending, v.Skipped, v.Expired, v.Required)
							for _, ch := range v.Checks {
								score := ""
								if ch.Score != nil {
									score = " score " + percent(ch.Score)
								}
								req := ""
								if !ch.Required {
									req = " (optional)"
								}
								fmt.Fprintf(out, "  %-10s %-12s %s%s%s\n", textsafe.Clean(ch.Kind), textsafe.Clean(ch.Status), textsafe.Clean(ch.ID), req, score)
							}
						}
					})
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(workflowPollDelay):
				}
			}
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().DurationVar(&wait, "wait", 0, "Keep checking until the outcome is decided, up to this long")
	return cmd
}

func printOutcome(cmd *cobra.Command, raw []byte, id, status, outcome string, decided bool, wait time.Duration, jsonOut bool, details func(io.Writer)) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		printRaw(out, raw)
	} else {
		fmt.Fprintf(out, "Run %s: %s, outcome %s\n", textsafe.Clean(id), textsafe.Clean(status), orDash(textsafe.Clean(outcome)))
		details(out)
	}
	switch {
	case !decided && wait > 0:
		return &ExitError{Code: exitWaitTimeout, Msg: fmt.Sprintf("the outcome of %s is still undecided after %s", textsafe.Clean(id), wait)}
	case outcome == "failed_verification":
		return &ExitError{Code: exitRunFailed, Msg: fmt.Sprintf("run %s failed verification", textsafe.Clean(id))}
	case decided && outcome == "unverified":
		return &ExitError{Code: 1, Msg: fmt.Sprintf("run %s could not be verified", textsafe.Clean(id))}
	}
	return nil
}

func workflowCompareCmd() *cobra.Command {
	var f approvalFlags
	var baseline, candidate, invocation, window string
	var failOnBreach bool
	cmd := &cobra.Command{
		Use:   "compare <installation> --baseline 1.2.0 --candidate 1.3.0",
		Short: "Judge a version against another on live runs",
		Long: `Compares the candidate version with the baseline on the installation's live runs
over the window and reports whether it is better, worse or no different, and which
guardrails (execution and outcome success, latency, cost) it breaches. With
--fail-on-breach a breached guardrail exits 21.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			days, err := parseWindowDays(window)
			if err != nil {
				return err
			}
			q := url.Values{"baseline": {baseline}, "candidate": {candidate}, "days": {strconv.Itoa(days)}}
			if invocation != "" {
				q.Set("invocation", invocation)
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			type guardrail struct {
				Metric         string  `json:"metric"`
				MaxDegradation float64 `json:"maxDegradation"`
			}
			var doc struct {
				Breached     []guardrail `json:"breached"`
				Satisfied    []guardrail `json:"satisfied"`
				Insufficient []guardrail `json:"insufficient"`
				Comparison   struct {
					Verdict string `json:"verdict"`
					Metrics map[string]struct {
						Baseline  *float64   `json:"baseline"`
						Candidate *float64   `json:"candidate"`
						Delta     *float64   `json:"delta"`
						Samples   [2]float64 `json:"samples"`
					} `json:"metrics"`
				} `json:"comparison"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, workflowPath(id, "compare")+"?"+q.Encode(), nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
			} else {
				fmt.Fprintf(out, "%s vs %s: %s\n", textsafe.Clean(candidate), textsafe.Clean(baseline), textsafe.Clean(doc.Comparison.Verdict))
				keys := make([]string, 0, len(doc.Comparison.Metrics))
				for k := range doc.Comparison.Metrics {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					m := doc.Comparison.Metrics[k]
					fmt.Fprintf(out, "  %-18s baseline %-8s candidate %-8s change %-8s runs %d/%d\n", textsafe.Clean(k), ratio(m.Baseline), ratio(m.Candidate), ratio(m.Delta), int(m.Samples[0]), int(m.Samples[1]))
				}
				for _, g := range doc.Breached {
					fmt.Fprintf(out, "  breached: %s (allowed %s)\n", textsafe.Clean(g.Metric), ratio(&g.MaxDegradation))
				}
				for _, g := range doc.Insufficient {
					fmt.Fprintf(out, "  too few runs to judge: %s\n", textsafe.Clean(g.Metric))
				}
			}
			if failOnBreach && len(doc.Breached) > 0 {
				return &ExitError{Code: exitEvalRegression, Msg: fmt.Sprintf("%d guardrail(s) breached", len(doc.Breached))}
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&baseline, "baseline", "", "The version to compare against")
	cmd.Flags().StringVar(&candidate, "candidate", "", "The version being judged")
	cmd.Flags().StringVar(&invocation, "invocation", "", "Only runs started this way")
	cmd.Flags().StringVar(&window, "window", "7d", "Days to cover, such as 7d")
	cmd.Flags().BoolVar(&failOnBreach, "fail-on-breach", false, "Exit 21 when a guardrail is breached")
	_ = cmd.MarkFlagRequired("baseline")
	_ = cmd.MarkFlagRequired("candidate")
	return cmd
}

func ratio(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', 3, 64)
}

func workflowEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run a workflow's evaluation gate",
	}
	cmd.AddCommand(workflowEvalCICmd())
	return cmd
}

func workflowEvalCICmd() *cobra.Command {
	var f approvalFlags
	var manifest string
	var suites []string
	cmd := &cobra.Command{
		Use:   "ci <installation> [--manifest file|dir]",
		Short: "Run the evaluation gate on a manifest from a change under review",
		Long: `Measures the manifest (default ./workflow.json) on the installation's gating
suites and compares it with the newest published version, as publishing would. Use
it in CI before a workflow file is merged.

exit status
  0   the gate passes, or nothing gates
  20  a suite fails or could not be measured
  21  the change regresses against the published version`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			m, err := readWorkflowManifest(orDefault(manifest, "."))
			if err != nil {
				return err
			}
			body := map[string]any{"manifest": m}
			if len(suites) > 0 {
				body["suite_ids"] = suites
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var res struct {
				Status   string  `json:"status"`
				ExitCode int     `json:"exit_code"`
				Baseline *string `json:"baseline"`
				Suites   []struct {
					ID       string   `json:"id"`
					Name     string   `json:"name"`
					Verdict  string   `json:"verdict"`
					PassRate *float64 `json:"pass_rate"`
				} `json:"suites"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodPost, workflowPath(id, "eval-gate"), body, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
			} else {
				base := "no published version"
				if res.Baseline != nil {
					base = "version " + *res.Baseline
				}
				fmt.Fprintf(out, "Gate %s against %s.\n", textsafe.Clean(res.Status), textsafe.Clean(base))
				for _, s := range res.Suites {
					fmt.Fprintf(out, "  %-24s %-10s pass rate %s\n", textsafe.Clean(s.Name), textsafe.Clean(s.Verdict), percent(s.PassRate))
				}
			}
			switch res.ExitCode {
			case 0:
				return nil
			case exitRunFailed, exitEvalRegression:
				return &ExitError{Code: res.ExitCode, Msg: "the evaluation gate " + textsafe.Clean(res.Status)}
			}
			return fmt.Errorf("the gateway answered an exit code this client does not know: %d", res.ExitCode)
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&manifest, "manifest", "", "The manifest file, or a directory holding workflow.json (default .)")
	cmd.Flags().StringArrayVar(&suites, "suite", nil, "Only this suite (repeatable)")
	return cmd
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func workflowReplayCmd() *cobra.Command {
	var f approvalFlags
	var fromStep, version, draft string
	var reuseModel, liveReads bool
	var models []string
	cmd := &cobra.Command{
		Use:   "replay <run> [--from-step path [--version v | --draft id]]",
		Short: "Play a finished run again from its recording, in eval mode",
		Long: `Plays a finished run again from its recording. Nothing outside the gateway
changes: tool calls are answered from the recording. With --from-step the steps
before it are taken from the recording and the rest runs on another published
version (--version) or a draft (--draft). A version that cannot continue the run
is refused and the steps that differ are listed. Only an administrator or the person
who installed the workflow may replay a run.

--reuse-model-outputs returns each model step's recorded answer, which reproduces
the recorded control flow. --live-reads lets reads the run never made go through.
--model old=new swaps a model.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !workflowRunPattern.MatchString(args[0]) {
				return fmt.Errorf("%q is not a workflow run (expected wfr_...)", textsafe.Clean(args[0]))
			}
			if fromStep == "" && (version != "" || draft != "") {
				return errors.New("--version and --draft need --from-step")
			}
			if version != "" && draft != "" {
				return errors.New("--version and --draft cannot be used together")
			}
			body := map[string]any{}
			if reuseModel {
				body["reuse_model_outputs"] = true
			}
			if liveReads {
				body["live_reads"] = true
			}
			if len(models) > 0 {
				mm := map[string]string{}
				for _, kv := range models {
					from, to, ok := strings.Cut(kv, "=")
					if !ok || from == "" || to == "" {
						return fmt.Errorf("--model %q must be old=new", textsafe.Clean(kv))
					}
					mm[from] = to
				}
				body["model_map"] = mm
			}
			action := "replay"
			if fromStep != "" {
				action = "rerun-from-step"
				body["from_step_path"] = fromStep
				if version != "" {
					body["version"] = version
				}
				if draft != "" {
					body["draft"] = draft
				}
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var res struct {
				ID     string `json:"id"`
				RunID  string `json:"run_id"`
				Status string `json:"status"`
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, workflowPath("runs", args[0], action), body, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Replay %s started as run %s: %s.\n", textsafe.Clean(res.ID), textsafe.Clean(res.RunID), textsafe.Clean(res.Status))
			fmt.Fprintf(out, "Follow it with `%s`.\n", brand.Cmd("workflow logs "+res.RunID+" --follow"))
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&fromStep, "from-step", "", "Continue from this step path on another version")
	cmd.Flags().StringVar(&version, "version", "", "Published version to continue on")
	cmd.Flags().StringVar(&draft, "draft", "", "Draft to continue on")
	cmd.Flags().BoolVar(&reuseModel, "reuse-model-outputs", false, "Return each model step's recorded answer")
	cmd.Flags().BoolVar(&liveReads, "live-reads", false, "Let reads the run never made go through")
	cmd.Flags().StringArrayVar(&models, "model", nil, "Swap a model: old=new (repeatable)")
	return cmd
}

func workflowResumeCmd() *cobra.Command {
	var f approvalFlags
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "resume <run>",
		Short: "Open the page where an administrator resumes a failed run for real",
		Long: `Resuming runs the rest of a failed run live, under the workflow's permissions, and
never repeats a write that completed. It is confirmed with a passkey in the browser, so
zuano only opens the run's page; choose the step there.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseAgentDecision(brand.Cmd("workflow resume")); err != nil {
				return err
			}
			if !workflowRunPattern.MatchString(args[0]) {
				return fmt.Errorf("%q is not a workflow run (expected wfr_...)", textsafe.Clean(args[0]))
			}
			return openDecisionPage(cmd, f.gatewayURL, "/workflows/runs/"+url.PathEscape(args[0]), noBrowser, "Choose the step and resume it there")
		},
	}
	f.bind(cmd, "Unused: the command only opens a page")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	return cmd
}
