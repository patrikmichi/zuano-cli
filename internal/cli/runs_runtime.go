package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// maxRunMessageBytes is the size the gateway accepts for one message to a run.
const maxRunMessageBytes = 4 * 1024

// runStopSentences say why an agent run stopped, by the gateway's stop reason.
// cost_budget is read the same way as every other failed stop.
var runStopSentences = map[string]string{
	"completed":        "the run finished",
	"max_steps":        "the run reached its step limit",
	"timeout":          "the run ran past its time limit",
	"token_budget":     "the run used its token budget",
	"cost_budget":      "the run used its cost budget",
	"cancelled":        "the run was cancelled",
	"pending_approval": "a tool call was still waiting for approval",
	"model_error":      "the model call failed",
	"approval_denied":  "a person denied a tool call",
	"approval_expired": "a tool call was not approved in time",
	"output_limit":     "the model reply was cut off at its output limit twice in a row",
}

// failedStops end an agent run as a failure whatever the run row says.
var failedStops = map[string]bool{
	"max_steps": true, "token_budget": true, "cost_budget": true, "model_error": true,
	"approval_denied": true, "approval_expired": true, "output_limit": true,
}

// runRefusalHints say what to do next for the refusals the run routes added.
var runRefusalHints = map[string]func(id string) string{
	"not_running":           func(id string) string { return "see `" + brand.Cmd("runs show "+id) + "` for how it ended" },
	"message_limit_reached": func(string) string { return "start a new run to give it more instructions" },
}

type runTranscript struct {
	Model string `json:"model"`
	Steps []struct {
		N      int  `json:"n"`
		CutOff bool `json:"cutOff"`
		Calls  []struct {
			Tool string `json:"tool"`
			Args string `json:"args"`
		} `json:"calls"`
	} `json:"steps"`
	Compactions []struct {
		BeforeStep int    `json:"beforeStep"`
		Summary    string `json:"summary"`
	} `json:"compactions"`
	Messages []struct {
		AfterStep int    `json:"afterStep"`
		Mode      string `json:"mode"`
		Author    string `json:"author"`
		Text      string `json:"text"`
		Delivered bool   `json:"delivered"`
	} `json:"messages"`
	Usage struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"usage"`
	Stop    *string `json:"stop"`
	Pending []struct {
		ApprovalID string `json:"approvalId"`
		Tool       string `json:"tool"`
	} `json:"pending"`
}

// runtimeRun mirrors a run of the gateway's /api/runtime/runs routes.
type runtimeRun struct {
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	Kind          string            `json:"kind"`
	Origin        string            `json:"origin"`
	UserEmail     string            `json:"userEmail"`
	DurationMs    *float64          `json:"durationMs"`
	CreatedAt     string            `json:"createdAt"`
	EndedAt       *string           `json:"endedAt"`
	Error         *string           `json:"error"`
	ContentHidden bool              `json:"contentHidden"`
	JobID         *string           `json:"job_id"`
	JobStatus     *string           `json:"job_status"`
	Transcript    json.RawMessage   `json:"transcript"`
	Calls         []json.RawMessage `json:"calls"`
}

// parseRuntimeRun reads {"run": {...}, "job_id", "job_status"}, the answer of
// GET /api/runtime/runs/{id}.
func parseRuntimeRun(raw []byte) (runtimeRun, bool) {
	var doc struct {
		Run       *runtimeRun `json:"run"`
		JobID     *string `json:"job_id"`
		JobStatus *string `json:"job_status"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Run == nil || doc.Run.Status == "" {
		return runtimeRun{}, false
	}
	r := *doc.Run
	if r.JobID == nil {
		r.JobID = doc.JobID
	}
	if r.JobStatus == nil {
		r.JobStatus = doc.JobStatus
	}
	return r, true
}

func (r runtimeRun) transcript() *runTranscript {
	var t runTranscript
	if len(r.Transcript) == 0 || json.Unmarshal(r.Transcript, &t) != nil || t.Model == "" {
		return nil
	}
	return &t
}

func (r runtimeRun) stop() string {
	if t := r.transcript(); t != nil && t.Stop != nil {
		return *t.Stop
	}
	return ""
}

// status is the state a run is reported in. A run whose row still says
// running while its job died is as dead as the job; a stop that is a failure
// makes the run failed even if the row does not.
func (r runtimeRun) status() string {
	if failedStops[r.stop()] && (r.Status == "succeeded" || r.Status == "") {
		return "failed"
	}
	if r.Status == "running" && r.JobStatus != nil && jobTerminal(*r.JobStatus) {
		return *r.JobStatus
	}
	return r.Status
}

func (r runtimeRun) errorText() string {
	if r.Error != nil && *r.Error != "" {
		return *r.Error
	}
	if r.status() == "failed" {
		return runStopSentences[r.stop()]
	}
	return ""
}

func (r runtimeRun) asJob() jobDoc {
	j := jobDoc{ID: r.ID, Kind: r.Kind, Status: r.status(), CreatedAt: r.CreatedAt, FinishedAt: r.EndedAt}
	if e := r.errorText(); e != "" {
		j.Error = &e
	}
	return j
}

func clip(s string, n int) string {
	s = textsafe.Strict(strings.Join(strings.Fields(s), " "))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}

func printRun(out io.Writer, id string, r runtimeRun) {
	if r.ID == "" {
		r.ID = id
	}
	fmt.Fprintf(out, "Run:      %s (%s)\n", textsafe.Clean(r.ID), textsafe.Clean(r.status()))
	if r.Kind != "" {
		fmt.Fprintf(out, "Kind:     %s\n", textsafe.Clean(r.Kind))
	}
	if r.UserEmail != "" {
		fmt.Fprintf(out, "Started by: %s\n", textsafe.Clean(r.UserEmail))
	}
	if r.CreatedAt != "" {
		fmt.Fprintf(out, "Created:  %s\n", textsafe.Clean(r.CreatedAt))
	}
	if r.EndedAt != nil {
		fmt.Fprintf(out, "Ended:    %s\n", textsafe.Clean(*r.EndedAt))
	}
	if r.DurationMs != nil {
		fmt.Fprintf(out, "Duration: %.1f s\n", *r.DurationMs/1000)
	}
	if r.JobID != nil && *r.JobID != "" {
		status := ""
		if r.JobStatus != nil {
			status = " (" + textsafe.Clean(*r.JobStatus) + ")"
		}
		fmt.Fprintf(out, "Job:      %s%s\n", textsafe.Clean(*r.JobID), status)
	}
	if e := r.errorText(); e != "" {
		fmt.Fprintf(out, "Error:    %s\n", clip(e, 300))
	}
	if r.ContentHidden {
		fmt.Fprintln(out, "Output:   hidden, this run belongs to another member")
	}
	if t := r.transcript(); t != nil {
		printTranscript(out, t)
	}
}

func printTranscript(out io.Writer, t *runTranscript) {
	fmt.Fprintf(out, "Model:    %s\n", clip(t.Model, 120))
	cut := 0
	var reads []string
	for _, s := range t.Steps {
		if s.CutOff {
			cut++
		}
		for _, c := range s.Calls {
			if c.Tool == "agent.read_result" || c.Tool == "agent__read_result" || c.Tool == "read_result" {
				reads = append(reads, fmt.Sprintf("  step %d: %s", s.N, clip(c.Args, 120)))
			}
		}
	}
	steps := fmt.Sprintf("%d", len(t.Steps))
	if cut > 0 {
		steps += fmt.Sprintf(" (%d cut off at the output limit)", cut)
	}
	fmt.Fprintf(out, "Steps:    %s\n", steps)
	fmt.Fprintf(out, "Tokens:   %d in, %d out\n", t.Usage.InputTokens, t.Usage.OutputTokens)
	if t.Stop != nil {
		stop := textsafe.Clean(*t.Stop)
		if why, ok := runStopSentences[*t.Stop]; ok {
			stop += ": " + why
		}
		fmt.Fprintf(out, "Stopped:  %s\n", stop)
	}
	if len(t.Compactions) > 0 {
		fmt.Fprintf(out, "Compactions: %d (earlier work replaced by a summary)\n", len(t.Compactions))
		for _, c := range t.Compactions {
			fmt.Fprintf(out, "  before step %d: %s\n", c.BeforeStep, clip(c.Summary, 160))
		}
	}
	if len(reads) > 0 {
		fmt.Fprintf(out, "Stored results read: %d\n", len(reads))
		for _, line := range reads {
			fmt.Fprintln(out, line)
		}
	}
	if len(t.Messages) > 0 {
		fmt.Fprintf(out, "Messages: %d\n", len(t.Messages))
		for _, m := range t.Messages {
			state := "delivered"
			if !m.Delivered {
				state = "not read, the run ended first"
			}
			fmt.Fprintf(out, "  after step %d, %s, by %s (%s): %s\n", m.AfterStep, clip(m.Mode, 20), clip(m.Author, 80), state, clip(m.Text, 160))
		}
	}
	for _, p := range t.Pending {
		fmt.Fprintf(out, "Waiting:  approval %s for %s\n", clip(p.ApprovalID, 80), clip(p.Tool, 80))
	}
}

// withRunHints appends what to do next to the refusals the run routes added.
func withRunHints(err error, id string) error {
	if err == nil {
		return nil
	}
	var refusal *gatewayRefusal
	if errors.As(err, &refusal) {
		if hint, ok := runRefusalHints[refusal.Code]; ok {
			return &gatewayRefusal{Code: refusal.Code, text: refusal.text + "; " + hint(textsafe.Clean(id))}
		}
	}
	return err
}

func runsMessageCmd() *cobra.Command {
	var f approvalFlags
	var steer, followUp string
	cmd := &cobra.Command{
		Use:   `message <run-id> --steer|--follow-up "<text>"`,
		Short: "Send a note to an agent run that is going",
		Long: `A steering note is read before the run's next model call. A follow-up is read
when the run would otherwise finish, and the run goes on. Only the person who
started the run, or an administrator, may send one; an agent session may not.
A run takes 20 messages of at most 4 KB.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, text, err := runMessageParts(cmd, steer, followUp)
			if err != nil {
				return err
			}
			if strings.HasPrefix(args[0], jobIDPrefix) {
				return fmt.Errorf("message takes a run id, not a job id; find the run with `%s`", brand.Cmd("runs list --kind agent_run"))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodPost, "/api/runtime/runs/"+url.PathEscape(args[0])+"/messages", map[string]any{"mode": mode, "text": text}, nil)
			if err != nil {
				return withRunHints(err, args[0])
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			var sent struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			}
			_ = json.Unmarshal(raw, &sent)
			fmt.Fprintf(out, "Sent a %s message %s to %s.\n", strings.ReplaceAll(mode, "_", "-"), textsafe.Clean(sent.Message.ID), textsafe.Clean(args[0]))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&steer, "steer", "", "Text the run reads before its next model call")
	cmd.Flags().StringVar(&followUp, "follow-up", "", "Text the run reads when it would otherwise finish")
	return cmd
}

func runMessageParts(cmd *cobra.Command, steer, followUp string) (mode, text string, err error) {
	steering, following := cmd.Flags().Changed("steer"), cmd.Flags().Changed("follow-up")
	switch {
	case steering == following:
		return "", "", fmt.Errorf("give exactly one of --steer and --follow-up, each with the text to send")
	case steering:
		mode, text = "steer", steer
	default:
		mode, text = "follow_up", followUp
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", fmt.Errorf("the message text is empty")
	}
	if len(text) > maxRunMessageBytes {
		return "", "", fmt.Errorf("the message is %d bytes; a run takes at most %d", len(text), maxRunMessageBytes)
	}
	return mode, text, nil
}
