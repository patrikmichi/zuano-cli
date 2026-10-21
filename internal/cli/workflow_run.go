package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// workflowPollDelay is how long to wait between checks of a run.
var workflowPollDelay = 2 * time.Second

// maxServerWaitSeconds is the longest the gateway holds a start request open.
const maxServerWaitSeconds = 55

type workflowStepDoc struct {
	Path       string `json:"path"`
	Seq        int    `json:"seq"`
	StepID     string `json:"step_id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Attempt    int    `json:"attempt"`
	ApprovalID string `json:"approval_id"`
	ErrorCode  string `json:"error_code"`
	Error      string `json:"error"`
}

type workflowRunDoc struct {
	ID             string          `json:"id"`
	InstallationID string          `json:"installation_id"`
	Status         string          `json:"status"`
	Mode           string          `json:"mode"`
	Outcome        string          `json:"outcome"`
	Output         json.RawMessage `json:"output"`
	ErrorCode      string          `json:"error_code"`
	Error          string          `json:"error"`
	FinishedAt     string          `json:"finished_at"`
}

type workflowRunView struct {
	Run       workflowRunDoc    `json:"run"`
	Steps     []workflowStepDoc `json:"steps"`
	NextAfter *int              `json:"next_after"`
}

func workflowRunTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "dead", "skipped":
		return true
	}
	return false
}

func fetchWorkflowRun(ctx context.Context, c *client.Client, runID string, after int) (workflowRunView, []byte, error) {
	var view workflowRunView
	path := workflowPath("runs", runID)
	if after >= 0 {
		path += fmt.Sprintf("?after=%d", after)
	}
	raw, err := gatewayJSON(ctx, c, http.MethodGet, path, nil, &view)
	return view, raw, err
}

func workflowStepLine(s workflowStepDoc) string {
	line := fmt.Sprintf("  %-24s %s", textsafe.Clean(s.Path), textsafe.Clean(s.Status))
	if s.Attempt > 1 {
		line += fmt.Sprintf(" (attempt %d)", s.Attempt)
	}
	if s.Error != "" {
		line += ": " + textsafe.Clean(s.Error)
	}
	return line
}

func printWorkflowStep(out io.Writer, c *client.Client, s workflowStepDoc) {
	fmt.Fprintln(out, workflowStepLine(s))
	if s.Status == "held" && s.ApprovalID != "" {
		if link, err := gatewayLink(c.GatewayURL, "/approvals/"+url.PathEscape(s.ApprovalID)); err == nil {
			fmt.Fprintf(out, "    waiting for approval: %s\n", textsafe.Clean(link))
		}
	}
}

// workflowRunExit maps a finished run to the process exit status.
func workflowRunExit(v workflowRunView) error {
	r := v.Run
	switch r.Status {
	case "succeeded":
		return nil
	case "cancelled", "skipped":
		return &ExitError{Code: 1, Msg: fmt.Sprintf("run %s %s", textsafe.Clean(r.ID), r.Status)}
	}
	code := exitRunFailed
	for _, s := range v.Steps {
		switch s.Status {
		case "denied":
			code = exitDenied
		case "expired":
			code = exitExpired
		}
	}
	msg := "run " + textsafe.Clean(r.ID) + " failed"
	if r.ErrorCode != "" {
		msg += " (" + textsafe.Clean(r.ErrorCode) + ")"
	}
	if r.Error != "" {
		msg += ": " + textsafe.Clean(r.Error)
	}
	return &ExitError{Code: code, Msg: msg}
}

func workflowHeld(v workflowRunView) bool {
	if v.Run.Status == "held" {
		return true
	}
	for _, s := range v.Steps {
		if s.Status == "held" {
			return true
		}
	}
	return false
}

// followWorkflowRun polls a run until it finishes, a step waits for an
// approval (exit 12) or the time ends (exit 17).
func followWorkflowRun(ctx context.Context, c *client.Client, runID string, total time.Duration, out io.Writer, jsonOut bool) error {
	deadline := time.Now().Add(total)
	for {
		v, raw, err := fetchWorkflowRun(ctx, c, runID, -1)
		if err != nil {
			return err
		}
		finished := workflowRunTerminal(v.Run.Status)
		held := !finished && workflowHeld(v)
		if finished || held || time.Now().After(deadline) {
			printWorkflowResult(out, c, v, raw, jsonOut)
			switch {
			case finished:
				return workflowRunExit(v)
			case held:
				return &ExitError{Code: exitPending}
			}
			return &ExitError{Code: exitWaitTimeout, Msg: fmt.Sprintf("run %s is still %s after %s; follow it with `%s`", textsafe.Clean(v.Run.ID), textsafe.Clean(v.Run.Status), total, brand.Cmd("workflow logs "+v.Run.ID+" --follow"))}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(workflowPollDelay):
		}
	}
}

// printWorkflowResult prints a run's typed output as JSON when it succeeded and
// the steps otherwise.
func printWorkflowResult(out io.Writer, c *client.Client, v workflowRunView, raw []byte, jsonOut bool) {
	if jsonOut {
		printRaw(out, raw)
		return
	}
	if v.Run.Status == "succeeded" && len(v.Run.Output) > 0 && v.Run.Mode != "dry_run" {
		printRaw(out, v.Run.Output)
		return
	}
	fmt.Fprintf(out, "Run %s: %s\n", textsafe.Clean(v.Run.ID), textsafe.Clean(v.Run.Status))
	for _, s := range v.Steps {
		printWorkflowStep(out, c, s)
	}
}

func newIdempotencyKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// workflowInput builds the run input from a JSON file and k=v pairs.
func workflowInput(inputFile string, sets []string) (any, error) {
	var in any = map[string]any{}
	if inputFile != "" {
		data, err := os.ReadFile(inputFile)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", textsafe.Clean(inputFile), err)
		}
		in = nil
		if err := json.Unmarshal(data, &in); err != nil {
			return nil, fmt.Errorf("%s is not JSON: %w", textsafe.Clean(inputFile), err)
		}
	}
	if len(sets) == 0 {
		return in, nil
	}
	obj, ok := in.(map[string]any)
	if !ok {
		return nil, errors.New("--set needs the input to be a JSON object")
	}
	for _, kv := range sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--set %q must be key=value", textsafe.Clean(kv))
		}
		var val any
		if json.Unmarshal([]byte(v), &val) != nil {
			val = v
		}
		obj[k] = val
	}
	return obj, nil
}

func workflowRunCmd() *cobra.Command {
	var f approvalFlags
	var dryRun, test, yes bool
	var inputFile, wait, idemKey string
	var sets []string
	cmd := &cobra.Command{
		Use:   "run <installation>",
		Short: "Run a workflow now, or dry-run it without changing anything",
		Long: `Prints the run's typed output as JSON when it succeeds.

--dry-run reads for real and shows what each step that would change something
would do, without doing it. --test runs it for real with the given input, under
the normal approvals, and needs --yes. Without either, an enabled workflow runs
live.

--wait follows the run (a duration such as 90s; plain --wait is 5m). It stops when
the run finishes, when a step waits for an approval (exit 12, with the link where a
person decides) or when the time ends (exit 17). A start repeated with the same
--idempotency-key does not start a second run.`,
		Example: `  zuano workflow run wfi_k3j2h1 --set domain=acme.com --wait
  zuano workflow run research_company --input in.json --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := workflowInstallationID(args[0])
			if err != nil {
				return err
			}
			if dryRun && test {
				return errors.New("--dry-run and --test cannot be used together")
			}
			total, err := parseRunWait(wait)
			if err != nil {
				return err
			}
			mode := "live"
			switch {
			case dryRun:
				mode = "dry_run"
			case test:
				mode = "test"
				if err := confirmOrYes(cmd, yes, "Run it for real with this input, under the normal approvals?"); err != nil {
					return err
				}
			}
			input, err := workflowInput(inputFile, sets)
			if err != nil {
				return err
			}
			if idemKey == "" {
				if idemKey, err = newIdempotencyKey(); err != nil {
					return err
				}
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			path := workflowPath(id, "runs")
			if secs := min(int(total/time.Second), maxServerWaitSeconds); secs > 0 {
				path += fmt.Sprintf("?wait=%d", secs)
			}
			status, raw, err := gatewayRequest(cmd.Context(), c, http.MethodPost, path, map[string]any{"input": input, "mode": mode}, map[string]string{"Idempotency-Key": idemKey})
			if err != nil {
				return err
			}
			if held := pendingFromREST(status, raw); held != nil {
				if f.jsonOut {
					printRaw(cmd.OutOrStdout(), raw)
				}
				return handlePending(cmd.Context(), c, held, f.jsonOut, callOpts{}, cmd.OutOrStdout())
			}
			if status != http.StatusOK && status != http.StatusAccepted {
				return gatewayStatusError(status, raw)
			}
			out := cmd.OutOrStdout()
			var started struct {
				Run   *workflowRunDoc `json:"run"`
				RunID string          `json:"run_id"`
			}
			if err := json.Unmarshal(raw, &started); err != nil {
				return fmt.Errorf("decode the run: %w", err)
			}
			if started.Run != nil {
				v, vraw, err := fetchWorkflowRun(cmd.Context(), c, started.Run.ID, -1)
				if err != nil {
					return err
				}
				printWorkflowResult(out, c, v, vraw, f.jsonOut)
				return workflowRunExit(v)
			}
			if started.RunID == "" {
				return errors.New("the gateway did not say which run started")
			}
			if total == 0 {
				if f.jsonOut {
					printRaw(out, raw)
					return nil
				}
				fmt.Fprintf(out, "Started %s. Follow it with `%s`.\n", textsafe.Clean(started.RunID), brand.Cmd("workflow logs "+started.RunID+" --follow"))
				return nil
			}
			return followWorkflowRun(cmd.Context(), c, started.RunID, total, out, f.jsonOut)
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not change anything; show what each changing step would do")
	cmd.Flags().BoolVar(&test, "test", false, "Run for real with this input, under the normal approvals")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Confirm --test without asking")
	cmd.Flags().StringVar(&inputFile, "input", "", "Input: a JSON file")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "Input field as key=value (a JSON value or text); repeatable")
	cmd.Flags().StringVar(&idemKey, "idempotency-key", "", "Key that makes a repeated start return the same run (default: a new one each time)")
	cmd.Flags().StringVar(&wait, "wait", "", "Follow the run for up to this long (default 5m when given without a value)")
	cmd.Flags().Lookup("wait").NoOptDefVal = "5m"
	return cmd
}

func parseRunWait(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --wait %q: use a duration such as 90s or 10m", textsafe.Clean(s))
	}
	return d, nil
}

func workflowRunsCmd() *cobra.Command {
	var f approvalFlags
	var status string
	cmd := &cobra.Command{
		Use:   "runs <installation>",
		Short: "List a workflow's runs, newest first",
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
			path := workflowPath(id, "runs")
			if status != "" {
				path += "?status=" + url.QueryEscape(status)
			}
			var doc struct {
				Runs []workflowRunDoc `json:"runs"`
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
			if len(doc.Runs) == 0 {
				fmt.Fprintln(out, "No runs.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "RUN\tSTATUS\tMODE\tOUTCOME\tFINISHED")
			for _, r := range doc.Runs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", textsafe.Clean(r.ID), textsafe.Clean(r.Status), textsafe.Clean(r.Mode), orDash(textsafe.Clean(r.Outcome)), orDash(textsafe.Clean(r.FinishedAt)))
			}
			return tw.Flush()
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&status, "status", "", "Only runs in this status (queued, running, held, succeeded, failed, cancelled, dead, skipped)")
	return cmd
}

func workflowLogsCmd() *cobra.Command {
	var f approvalFlags
	var follow bool
	var step string
	cmd := &cobra.Command{
		Use:   "logs <wfr_id|installation>",
		Short: "Show a run's steps; for a workflow, its latest run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			ctx, out := cmd.Context(), cmd.OutOrStdout()
			runID := args[0]
			if !workflowRunPattern.MatchString(runID) {
				id, err := workflowInstallationID(runID)
				if err != nil {
					return err
				}
				var list struct {
					Runs []workflowRunDoc `json:"runs"`
				}
				if _, err := gatewayJSON(ctx, c, http.MethodGet, workflowPath(id, "runs"), nil, &list); err != nil {
					return err
				}
				if len(list.Runs) == 0 {
					fmt.Fprintln(out, "No runs yet.")
					return nil
				}
				runID = list.Runs[0].ID
			}
			after, header := -1, false
			for {
				v, raw, err := fetchWorkflowRun(ctx, c, runID, after)
				if err != nil {
					return err
				}
				if f.jsonOut {
					printRaw(out, raw)
				} else {
					if !header {
						fmt.Fprintf(out, "Run %s (%s): %s\n", textsafe.Clean(v.Run.ID), textsafe.Clean(v.Run.Mode), textsafe.Clean(v.Run.Status))
						header = true
					}
					for _, s := range v.Steps {
						if step == "" || s.Path == step {
							printWorkflowStep(out, c, s)
						}
					}
				}
				for _, s := range v.Steps {
					after = max(after, s.Seq)
				}
				if workflowRunTerminal(v.Run.Status) {
					if follow {
						return workflowRunExit(v)
					}
					return nil
				}
				if !follow {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(workflowPollDelay):
				}
			}
		},
	}
	f.bind(cmd, "Print each response body unchanged")
	cmd.Flags().BoolVar(&follow, "follow", false, "Keep polling and print step changes until the run finishes")
	cmd.Flags().StringVar(&step, "step", "", "Only this step path")
	return cmd
}

func workflowCancelCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "cancel <wfr_id>",
		Short: "Cancel a run, with its branches and child runs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !workflowRunPattern.MatchString(args[0]) {
				return fmt.Errorf("%q is not a run id (expected wfr_...)", textsafe.Clean(args[0]))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var run workflowRunDoc
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, workflowPath("runs", args[0], "cancel"), map[string]any{}, &run)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s.\n", textsafe.Clean(run.ID), textsafe.Clean(run.Status))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}
