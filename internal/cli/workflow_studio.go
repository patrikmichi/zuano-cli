package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const (
	studioStateFile  = ".zuano-draft.json"
	studioSamplesDir = "samples"
)

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// studioState records the draft a directory was pulled from and the manifest at
// that revision, so a later push sends only what changed since.
type studioState struct {
	WorkflowID string      `json:"workflow_id"`
	Revision   int         `json:"revision"`
	Manifest   manifestDoc `json:"manifest"`
}

type studioDraftDoc struct {
	WorkflowID string          `json:"workflow_id"`
	Revision   int             `json:"revision"`
	Manifest   manifestDoc     `json:"manifest"`
	Validation json.RawMessage `json:"validation"`
}

type studioValidation struct {
	Valid    bool             `json:"valid"`
	Issues   []workflowIssue  `json:"issues"`
	Analysis workflowAnalysis `json:"analysis"`
}

func studioDraftPath(id string, rest ...string) string {
	return workflowPath(append([]string{"studio", "drafts", id}, rest...)...)
}

func studioID(arg string) (string, error) {
	if !workflowRefPattern.MatchString(arg) {
		return "", fmt.Errorf("%q is not a workflow draft id", textsafe.Clean(arg))
	}
	return arg, nil
}

func writeStudioFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func marshalManifest(m manifestDoc) []byte {
	raw, _ := json.MarshalIndent(m, "", "  ")
	return append(raw, '\n')
}

func readStudioState(dir string) (*studioState, error) {
	raw, err := os.ReadFile(filepath.Join(dir, studioStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st studioState
	if err := json.Unmarshal(raw, &st); err != nil || st.WorkflowID == "" {
		return nil, fmt.Errorf("%s is damaged: pull the workflow again into an empty directory", textsafe.Clean(filepath.Join(dir, studioStateFile)))
	}
	return &st, nil
}

func writeStudioState(dir string, d studioDraftDoc) error {
	raw, _ := json.Marshal(studioState{WorkflowID: d.WorkflowID, Revision: d.Revision, Manifest: d.Manifest})
	return writeStudioFile(filepath.Join(dir, studioStateFile), raw)
}

func workflowPullCmd() *cobra.Command {
	var f approvalFlags
	var dir string
	var force bool
	cmd := &cobra.Command{
		Use:   "pull <workflow>",
		Short: "Write a workflow draft to a directory: workflow.json and its samples",
		Long: `Writes the draft's manifest to workflow.json and its saved samples to samples/,
so the workflow can be edited in your editor and saved with 'push'. The directory
remembers the revision it was pulled at. A directory with edits that were not
pushed is not overwritten unless --force is given.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := studioID(args[0])
			if err != nil {
				return err
			}
			if dir == "" {
				dir = id
			}
			if !force {
				if err := checkUnpushed(dir); err != nil {
					return err
				}
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var d studioDraftDoc
			if _, err := gatewayJSON(cmd.Context(), c, http.MethodGet, studioDraftPath(id), nil, &d); err != nil {
				return err
			}
			samples, err := fetchStudioSamples(cmd.Context(), c, id)
			if err != nil {
				return err
			}
			if err := writeStudioFile(filepath.Join(dir, workflowManifestFile), marshalManifest(d.Manifest)); err != nil {
				return err
			}
			if err := writeStudioSamples(dir, samples); err != nil {
				return err
			}
			if err := writeStudioState(dir, d); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pulled %s at revision %d into %s (%d sample(s)).\n", textsafe.Clean(id), d.Revision, textsafe.Clean(dir), len(samples))
			return nil
		},
	}
	f.bind(cmd, "Unused: the command writes files")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().StringVar(&dir, "dir", "", "Directory to write (default: the workflow id)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite edits that were not pushed")
	return cmd
}

// checkUnpushed refuses to overwrite a directory whose workflow.json differs
// from what it was pulled as.
func checkUnpushed(dir string) error {
	st, err := readStudioState(dir)
	if err != nil || st == nil {
		return err
	}
	local, err := readWorkflowManifest(dir)
	if err != nil {
		return nil
	}
	if canonicalJSON(local) != canonicalJSON(st.Manifest) {
		return fmt.Errorf("%s has edits that were not pushed; push them, or pull with --force", textsafe.Clean(dir))
	}
	return nil
}

type studioSample struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Invocation string          `json:"invocation"`
	Payload    json.RawMessage `json:"payload"`
}

func fetchStudioSamples(ctx context.Context, c *client.Client, id string) ([]studioSample, error) {
	var all []studioSample
	after := ""
	for page := 0; page < 50; page++ {
		path := studioDraftPath(id, "samples") + "?limit=100"
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var doc struct {
			Samples []studioSample `json:"samples"`
			Next    string         `json:"next"`
		}
		if _, err := gatewayJSON(ctx, c, http.MethodGet, path, nil, &doc); err != nil {
			return nil, err
		}
		all = append(all, doc.Samples...)
		if doc.Next == "" {
			break
		}
		after = doc.Next
	}
	return all, nil
}

func writeStudioSamples(dir string, samples []studioSample) error {
	if err := os.RemoveAll(filepath.Join(dir, studioSamplesDir)); err != nil {
		return err
	}
	used := map[string]bool{}
	for _, s := range samples {
		base := strings.Trim(unsafeFileChars.ReplaceAllString(s.Name, "_"), "._")
		if base == "" {
			base = "sample"
		}
		name := base
		for n := 2; used[name]; n++ {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		used[name] = true
		raw, _ := json.MarshalIndent(map[string]any{"name": s.Name, "invocation": s.Invocation, "payload": s.Payload}, "", "  ")
		if err := writeStudioFile(filepath.Join(dir, studioSamplesDir, name+".json"), append(raw, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func workflowPushCmd() *cobra.Command {
	var f approvalFlags
	var message string
	cmd := &cobra.Command{
		Use:   "push <dir>",
		Short: "Save the edits in workflow.json as a new draft revision",
		Long: `Turns the difference between workflow.json and the revision it was pulled at
into edit operations and saves them as one revision, then prints what the draft
needs. A directory that was not pulled creates a new draft. Nothing is published
or run.

--message names a checkpoint at the saved revision.

A draft that changed after the pull exits 25: pull it again and redo the edit.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			local, err := readWorkflowManifest(dir)
			if err != nil {
				return err
			}
			st, err := readStudioState(dir)
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var saved studioDraftDoc
			var raw []byte
			if st == nil {
				raw, err = gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, workflowPath("studio", "drafts"), map[string]any{"manifest": local, "source": "import"}, &saved)
			} else {
				ops, derr := opsFromManifestDiff(st.Manifest, local)
				if derr != nil {
					return derr
				}
				if len(ops) == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "No changes since revision %d.\n", st.Revision)
					return nil
				}
				var res struct {
					Draft studioDraftDoc `json:"draft"`
				}
				raw, err = gatewayWrite(cmd, c, f.jsonOut, http.MethodPut, studioDraftPath(st.WorkflowID), map[string]any{"baseRevision": st.Revision, "ops": ops, "source": "json"}, &res)
				saved = res.Draft
			}
			if err != nil {
				return err
			}
			if message != "" {
				if _, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, studioDraftPath(saved.WorkflowID, "checkpoints"), map[string]any{"name": message, "baseRevision": saved.Revision}, nil); err != nil {
					return err
				}
			}
			if err := writeStudioState(dir, saved); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Saved %s as revision %d.\n", textsafe.Clean(saved.WorkflowID), saved.Revision)
			var v studioValidation
			_ = json.Unmarshal(saved.Validation, &v)
			if !v.Valid {
				printWorkflowIssues(cmd.ErrOrStderr(), v.Issues)
				return &ExitError{Code: exitInvalidManifest, Msg: fmt.Sprintf("the draft is saved but has %d issue(s)", len(v.Issues))}
			}
			printWorkflowAnalysis(out, v.Analysis)
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&message, "message", "", "Name a checkpoint at the saved revision")
	return cmd
}

func workflowDiffCmd() *cobra.Command {
	var f approvalFlags
	var from, to string
	cmd := &cobra.Command{
		Use:   "diff <workflow> --from v:1.2.0 [--to draft]",
		Short: "What changed between two versions of a workflow draft",
		Long:  `A version is draft, rev:<n> or v:<published version>. The summary names the steps, permissions and schemas that changed and the version bump the change calls for.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := studioID(args[0])
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var d struct {
				From string `json:"from"`
				To   string `json:"to"`
				Diff struct {
					StepsAdded         []string             `json:"stepsAdded"`
					StepsRemoved       []string             `json:"stepsRemoved"`
					StepsChanged       []string             `json:"stepsChanged"`
					StepsMoved         []string             `json:"stepsMoved"`
					PermissionsAdded   []workflowPermission `json:"permissionsAdded"`
					PermissionsRemoved []workflowPermission `json:"permissionsRemoved"`
					ConnectorsAdded    []string             `json:"connectorsAdded"`
					InvocationsChanged bool                 `json:"invocationsChanged"`
				} `json:"diff"`
				InputChange  string   `json:"input_change"`
				OutputChange string   `json:"output_change"`
				UpgradeClass string   `json:"upgrade_class"`
				Reasons      []string `json:"reasons"`
			}
			q := url.Values{"from": {from}, "to": {to}}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, studioDraftPath(id, "diff")+"?"+q.Encode(), nil, &d)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "%s -> %s\n", textsafe.Clean(d.From), textsafe.Clean(d.To))
			line := func(label string, items []string) {
				if len(items) > 0 {
					fmt.Fprintf(out, "  %-20s %s\n", label, textsafe.Clean(strings.Join(items, ", ")))
				}
			}
			line("steps added", d.Diff.StepsAdded)
			line("steps removed", d.Diff.StepsRemoved)
			line("steps changed", d.Diff.StepsChanged)
			line("steps moved", d.Diff.StepsMoved)
			line("connections added", d.Diff.ConnectorsAdded)
			for _, p := range d.Diff.PermissionsAdded {
				fmt.Fprintf(out, "  permission added     %s %s.%s\n", textsafe.Clean(p.Risk), textsafe.Clean(p.Service), textsafe.Clean(p.Tool))
			}
			for _, p := range d.Diff.PermissionsRemoved {
				fmt.Fprintf(out, "  permission removed   %s %s.%s\n", textsafe.Clean(p.Risk), textsafe.Clean(p.Service), textsafe.Clean(p.Tool))
			}
			fmt.Fprintf(out, "  input schema         %s\n  output schema        %s\n", orDash(textsafe.Clean(d.InputChange)), orDash(textsafe.Clean(d.OutputChange)))
			if d.Diff.InvocationsChanged {
				fmt.Fprintln(out, "  how it starts        changed")
			}
			fmt.Fprintf(out, "  version bump         %s\n", orDash(textsafe.Clean(d.UpgradeClass)))
			for _, r := range d.Reasons {
				fmt.Fprintf(out, "    %s\n", textsafe.Clean(r))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&from, "from", "", "Version to compare from: draft, rev:<n> or v:<version>")
	cmd.Flags().StringVar(&to, "to", "draft", "Version to compare to")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}

type simulatedRun struct {
	Name  string  `json:"name"`
	RunID *string `json:"run_id"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type simulationReport struct {
	Runs []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Steps  int    `json:"steps"`
	} `json:"runs"`
	Coverage *struct {
		Edges []struct {
			StepID    string `json:"step_id"`
			Label     string `json:"label"`
			Exercised bool   `json:"exercised"`
		} `json:"edges"`
		Exercised int `json:"exercised"`
		Total     int `json:"total"`
	} `json:"coverage"`
}

func workflowSimulateCmd() *cobra.Command {
	var f approvalFlags
	var inputs, samples []string
	var fixtures bool
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "simulate <workflow> (--input file.json | --sample name | --fixtures)",
		Short: "Dry-run the draft against inputs and report which branches they took",
		Long: `Starts one dry run of the draft per input: a JSON file (--input, repeatable), a
saved sample (--sample) or, with --fixtures, every sample the draft keeps. A dry
run reads real data but changes nothing. The command waits for the runs and
reports each one and the branches the whole set covered.

exit status
  0   every run succeeded
  18  an input does not match the workflow's input schema
  20  a run failed
  17  --wait ran out`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := studioID(args[0])
			if err != nil {
				return err
			}
			if len(inputs) == 0 && len(samples) == 0 && !fixtures {
				return errors.New("name an input: --input <file>, --sample <name> or --fixtures")
			}
			body := map[string]any{"samples": append([]string{}, samples...), "inputs": []map[string]any{}}
			var typed []map[string]any
			for _, path := range inputs {
				in, err := workflowInput(path, nil)
				if err != nil {
					return err
				}
				typed = append(typed, map[string]any{"name": strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), "input": in})
			}
			if typed != nil {
				body["inputs"] = typed
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var started struct {
				Runs []simulatedRun `json:"runs"`
			}
			if _, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, studioDraftPath(id, "simulate"), body, &started); err != nil {
				return err
			}
			return reportSimulation(cmd, c, id, started.Runs, wait, f.jsonOut)
		},
	}
	f.bind(cmd, "Print the final report as JSON")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "A JSON input file; its name is the file name (repeatable)")
	cmd.Flags().StringArrayVar(&samples, "sample", nil, "A saved sample by name (repeatable)")
	cmd.Flags().BoolVar(&fixtures, "fixtures", false, "Try every sample the draft keeps")
	cmd.Flags().DurationVar(&wait, "wait", 2*time.Minute, "How long to wait for the runs")
	return cmd
}

func reportSimulation(cmd *cobra.Command, c *client.Client, id string, started []simulatedRun, wait time.Duration, jsonOut bool) error {
	out := cmd.OutOrStdout()
	names := map[string]string{}
	var ids []string
	refused := false
	for _, r := range started {
		if r.Error != nil {
			if !jsonOut {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: refused (%s) %s\n", textsafe.Clean(r.Name), textsafe.Clean(r.Error.Code), textsafe.Clean(r.Error.Message))
			}
			refused = refused || r.Error.Code == "input_invalid"
			continue
		}
		if r.RunID != nil {
			ids = append(ids, *r.RunID)
			names[*r.RunID] = r.Name
		}
	}
	if len(ids) == 0 {
		if refused {
			return &ExitError{Code: exitInputInvalid, Msg: "no input was accepted"}
		}
		return errors.New("no run started")
	}
	deadline := time.Now().Add(wait)
	var rep simulationReport
	var raw []byte
	for {
		var err error
		raw, err = gatewayJSON(cmd.Context(), c, http.MethodGet, studioDraftPath(id, "simulate")+"?runs="+url.QueryEscape(strings.Join(ids, ",")), nil, &rep)
		if err != nil {
			return err
		}
		done := true
		for _, r := range rep.Runs {
			done = done && workflowRunTerminal(r.Status)
		}
		if done || time.Now().After(deadline) {
			break
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(workflowPollDelay):
		}
	}
	failed, pending := false, false
	for _, r := range rep.Runs {
		failed = failed || (workflowRunTerminal(r.Status) && r.Status != "succeeded")
		pending = pending || !workflowRunTerminal(r.Status)
	}
	if jsonOut {
		printRaw(out, raw)
	} else {
		for _, r := range rep.Runs {
			fmt.Fprintf(out, "%-24s %s  %s  (%d step(s))\n", textsafe.Clean(names[r.ID]), textsafe.Clean(r.ID), textsafe.Clean(r.Status), r.Steps)
		}
		if rep.Coverage != nil {
			fmt.Fprintf(out, "Branches covered: %d of %d\n", rep.Coverage.Exercised, rep.Coverage.Total)
			for _, e := range rep.Coverage.Edges {
				if !e.Exercised {
					fmt.Fprintf(out, "  not taken: %s %s\n", textsafe.Clean(e.StepID), textsafe.Clean(e.Label))
				}
			}
		}
	}
	switch {
	case pending:
		return &ExitError{Code: exitWaitTimeout, Msg: fmt.Sprintf("the runs are still going after %s", wait)}
	case failed:
		return &ExitError{Code: exitRunFailed, Msg: "a simulated run did not succeed"}
	case refused:
		return &ExitError{Code: exitInputInvalid, Msg: "an input does not match the workflow's input schema"}
	}
	return nil
}

func workflowPublishCmd() *cobra.Command {
	var f approvalFlags
	var version, channel, notes string
	var revision int
	var override bool
	cmd := &cobra.Command{
		Use:   "publish <workflow> --version 1.3.0",
		Short: "Publish a draft revision as a new version",
		Long: `Publishes a revision of the draft (default: the latest) as an immutable version.
It never changes an installation: upgrading one is a separate step. The gateway
runs its publish checks and names the ones that did not pass.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := studioID(args[0])
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("revision") {
				var d studioDraftDoc
				if _, err := gatewayJSON(cmd.Context(), c, http.MethodGet, studioDraftPath(id), nil, &d); err != nil {
					return err
				}
				revision = d.Revision
			}
			body := map[string]any{"revision": revision, "version": version, "channel": channel}
			if notes != "" {
				body["notes"] = notes
			}
			if override {
				body["override_eval_gate"] = true
			}
			var res struct {
				Version      string `json:"version"`
				UpgradeClass string `json:"upgrade_class"`
				Gates        []struct {
					Gate   string `json:"gate"`
					Status string `json:"status"`
				} `json:"gates"`
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, studioDraftPath(id, "publish"), body, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Published %s %s from revision %d.\n", textsafe.Clean(id), textsafe.Clean(res.Version), revision)
			for _, g := range res.Gates {
				fmt.Fprintf(out, "  %-14s %s\n", textsafe.Clean(g.Gate), textsafe.Clean(g.Status))
			}
			fmt.Fprintf(out, "Upgrade installations with `%s`.\n", brand.Cmd("workflow upgrade <wfi_id>"))
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer as JSON")
	cmd.Flags().StringVar(&version, "version", "", "Version to publish, major.minor.patch")
	cmd.Flags().StringVar(&channel, "channel", "stable", "stable or beta")
	cmd.Flags().StringVar(&notes, "notes", "", "Release notes")
	cmd.Flags().IntVar(&revision, "revision", 0, "Revision to publish (default: the latest)")
	cmd.Flags().BoolVar(&override, "override-eval-gate", false, "Publish past a failed evaluation gate (administrators; audited)")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}
