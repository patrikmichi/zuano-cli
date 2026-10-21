package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const (
	workflowManifestFile = "workflow.json"
	maxWorkflowManifest  = 1 << 20
)

type workflowIssue struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
	StepID  string `json:"stepId"`
}

type workflowAnalysis struct {
	Permissions []workflowPermission `json:"permissions"`
	MaxRisk     string               `json:"maxRisk"`
	Preapprove  []string             `json:"preapprovableSteps"`
	Features    []string             `json:"requiredFeatures"`
}

// readWorkflowManifest reads a manifest from a file, or workflow.json in a
// directory.
func readWorkflowManifest(path string) (map[string]any, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, workflowManifestFile)
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", textsafe.Clean(path), err)
	}
	defer fh.Close()
	raw, err := io.ReadAll(io.LimitReader(fh, maxWorkflowManifest+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxWorkflowManifest {
		return nil, fmt.Errorf("%s is larger than %d bytes", textsafe.Clean(path), maxWorkflowManifest)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, fmt.Errorf("%s is not a JSON object: a manifest is one object with steps", textsafe.Clean(path))
	}
	return m, nil
}

func printWorkflowIssues(w io.Writer, issues []workflowIssue) {
	for _, i := range issues {
		where := textsafe.Clean(i.Pointer)
		if where == "" {
			where = "manifest"
		}
		fmt.Fprintf(w, "%s: %s\n", where, textsafe.Clean(i.Message))
	}
}

func printWorkflowAnalysis(w io.Writer, a workflowAnalysis) {
	fmt.Fprintf(w, "  highest risk %s\n", orDash(textsafe.Clean(a.MaxRisk)))
	for _, p := range a.Permissions {
		fmt.Fprintf(w, "  %-12s %s.%s\n", textsafe.Clean(p.Risk), textsafe.Clean(p.Service), textsafe.Clean(p.Tool))
	}
	if len(a.Features) > 0 {
		fmt.Fprintf(w, "  needs: %s\n", textsafe.Clean(strings.Join(a.Features, ", ")))
	}
	if len(a.Preapprove) > 0 {
		fmt.Fprintf(w, "  steps that can be pre-approved: %s\n", textsafe.Clean(strings.Join(a.Preapprove, ", ")))
	}
}

func workflowValidateCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "validate <file|dir>",
		Short: "Check a workflow manifest with the gateway, without saving anything",
		Long: `Checks the manifest (a file, or workflow.json in a directory) and prints what it
would need: permissions with their risk, plan features and the steps that can be
pre-approved. Every issue is listed on its own line and exits 16.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := readWorkflowManifest(args[0])
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var res struct {
				Valid          bool             `json:"valid"`
				ManifestDigest string           `json:"manifest_digest"`
				Analysis       workflowAnalysis `json:"analysis"`
				Issues         []workflowIssue  `json:"issues"`
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodPost, workflowsAPI+"/validate", map[string]any{"manifest": m}, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
			}
			if !res.Valid {
				if !f.jsonOut {
					printWorkflowIssues(cmd.ErrOrStderr(), res.Issues)
				}
				return &ExitError{Code: exitInvalidManifest, Msg: fmt.Sprintf("the manifest has %d issue(s)", len(res.Issues))}
			}
			if !f.jsonOut {
				fmt.Fprintf(out, "%s is valid.\n", textsafe.Clean(args[0]))
				printWorkflowAnalysis(out, res.Analysis)
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	return cmd
}

func workflowDraftCmd() *cobra.Command {
	var f planFlags
	var name string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "draft <file|dir>",
		Short: "Save a manifest as a private workflow and, with --name, a draft installation of it",
		Long: `Saves the manifest as your private workflow. With --name it also creates a draft
installation. Nothing is enabled: a person opens the page this prints, sees the
permissions it needs and a dry run, and turns it on with a passkey. A draft never
gets standing permissions from this command, so an agent session may use it (as
any write, it can be held for approval).

A manifest that is not valid prints every issue on its own line and exits 16.`,
		Example: `  zuano workflow draft triage.json --name "Issue triage" --param channel=C0123`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := readWorkflowManifest(args[0])
			if err != nil {
				return err
			}
			params, err := parseParams(f.params, f.paramsFile)
			if err != nil {
				return err
			}
			if name == "" && len(params) > 0 {
				return errors.New("--param needs --name: parameters belong to an installation")
			}
			body := map[string]any{"manifest": m}
			if name != "" {
				install := map[string]any{"name": name}
				if len(params) > 0 {
					install["params"] = params
				}
				body["install"] = install
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var res struct {
				CustomManifestID string           `json:"custom_manifest_id"`
				Analysis         workflowAnalysis `json:"analysis"`
				Installation     *struct {
					ID string `json:"id"`
				} `json:"installation"`
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, workflowsAPI+"/draft", body, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Saved workflow %s.\n", textsafe.Clean(res.CustomManifestID))
			printWorkflowAnalysis(out, res.Analysis)
			if res.Installation == nil {
				fmt.Fprintf(out, "Install it with `%s`.\n", brand.Cmd("workflow draft "+args[0]+" --name <name>"))
				return nil
			}
			fmt.Fprintf(out, "Draft installation %s.\n", textsafe.Clean(res.Installation.ID))
			link, err := gatewayLink(c.GatewayURL, "/workflows/"+url.PathEscape(res.Installation.ID))
			if err != nil {
				return err
			}
			showLink(cmd, link, noBrowser, "Test it and turn it on there")
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer as JSON")
	cmd.Flags().StringVar(&name, "name", "", "Also create a draft installation with this name")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the link instead of opening a browser")
	return cmd
}

func workflowSchemaCmd() *cobra.Command {
	var f approvalFlags
	var input, output bool
	cmd := &cobra.Command{
		Use:   "schema <installation|template[@version]>",
		Short: "Print a workflow's input and output JSON Schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if input && output {
				return errors.New("--input and --output cannot be used together")
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			ref, version := splitAtVersion(args[0])
			if workflowIDPattern.MatchString(ref) {
				ref, version, err = workflowTemplateOf(cmd, c, ref)
				if err != nil {
					return err
				}
			}
			if !workflowRefPattern.MatchString(ref) {
				return fmt.Errorf("%q is neither a workflow installation nor a template", textsafe.Clean(args[0]))
			}
			path := workflowPath("templates", ref)
			if version != "" {
				path += "?version=" + url.QueryEscape(version)
			}
			var doc struct {
				InputSchema  json.RawMessage `json:"input_schema"`
				OutputSchema json.RawMessage `json:"output_schema"`
			}
			if _, err := gatewayJSON(cmd.Context(), c, http.MethodGet, path, nil, &doc); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case output:
				printRaw(out, orNull(doc.OutputSchema))
			case input:
				printRaw(out, orNull(doc.InputSchema))
			default:
				printRaw(out, []byte(fmt.Sprintf(`{"input":%s,"output":%s}`, orNull(doc.InputSchema), orNull(doc.OutputSchema))))
			}
			return nil
		},
	}
	f.bind(cmd, "Unused: the schema is always JSON")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().BoolVar(&input, "input", false, "Only the input schema")
	cmd.Flags().BoolVar(&output, "output", false, "Only the output schema")
	return cmd
}

func orNull(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("null")
	}
	return raw
}

// workflowTemplateOf names the published template an installation runs.
func workflowTemplateOf(cmd *cobra.Command, c *client.Client, id string) (string, string, error) {
	var w workflowInstallationDoc
	if _, err := gatewayJSON(cmd.Context(), c, http.MethodGet, workflowPath("installations", id), nil, &w); err != nil {
		return "", "", err
	}
	if w.ResourceID == "" {
		return "", "", fmt.Errorf("%s runs a private custom workflow; read its manifest with the studio commands", textsafe.Clean(id))
	}
	return w.ResourceID, w.Version, nil
}
