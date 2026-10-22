package cli

import (
	"net/http"
	"strings"
	"testing"
)

const agentRunOutputLimit = `{"run":{"id":"crn_7","status":"failed","kind":"agent","language":"agent","origin":"mcp","userEmail":"dev@example.com","durationMs":12400,"createdAt":"2026-10-22T08:00:00.000Z","endedAt":"2026-10-22T08:00:12.400Z","codeBytes":0,"stdout":"","stderr":"","result":null,"error":"The model reply was cut off at its output limit twice in a row.","exitCode":null,"outputTruncated":false,"skillId":null,"contentHidden":false,"job_id":"job_5","job_status":"succeeded","calls":[],
"transcript":{"v":1,"model":"anthropic/claude-sonnet-5.5","instruction":"summarise","steps":[
 {"n":1,"text":"","inputTokens":900,"outputTokens":300,"calls":[{"tool":"clockify.list_time_entries","args":"{}","outcome":"ok","result":"[... read the rest with read_result(id=\"res_1\", offset=2000, limit=2000)]"}]},
 {"n":2,"text":"","inputTokens":1200,"outputTokens":200,"calls":[{"tool":"agent.read_result","args":"{\"id\":\"res_1\",\"offset\":2000}","outcome":"ok","result":"more lines"}]},
 {"n":3,"text":"cut","inputTokens":1300,"outputTokens":4096,"calls":[],"cutOff":true}],
 "compactions":[{"beforeStep":3,"summary":"Listed the time entries\nand read the rest.","usage":{"inputTokens":500,"outputTokens":100}}],
 "messages":[{"afterStep":1,"mode":"steer","author":"dev@example.com","text":"focus on October","delivered":true},{"afterStep":3,"mode":"follow_up","author":"dev@example.com","text":"then email it","delivered":false}],
 "usage":{"inputTokens":3900,"outputTokens":4696},"stop":"output_limit","pending":[]}},"job_id":"job_5","job_status":"succeeded"}`

func TestRuns_ShowAnAgentRunPrintsItsTranscriptNotes(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/runtime/runs/crn_7": reply(200, agentRunOutputLimit),
	})
	out, err := runCommand(t, RunsCmd(), "show", "crn_7")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"crn_7 (failed)", "Steps:    3 (1 cut off at the output limit)", "Tokens:   3900 in, 4696 out",
		"Stopped:  output_limit: the model reply was cut off at its output limit twice in a row",
		"Error:    The model reply was cut off", "Job:      job_5 (succeeded)",
		"Compactions: 1", "before step 3: Listed the time entries and read the rest.",
		"Stored results read: 1", `step 2: {"id":"res_1","offset":2000}`,
		"Messages: 2", "after step 1, steer, by dev@example.com (delivered): focus on October",
		"after step 3, follow_up, by dev@example.com (not read, the run ended first)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRuns_AStopThatIsAFailureFailsTheRunWhateverTheRowSays(t *testing.T) {
	fastJobPolls(t)
	for _, stop := range []string{"output_limit", "cost_budget"} {
		body := `{"run":{"id":"crn_8","status":"succeeded","kind":"agent","createdAt":"x","error":null,"contentHidden":false,"calls":[],"transcript":{"v":1,"model":"m","instruction":"i","steps":[],"usage":{"inputTokens":1,"outputTokens":1},"stop":"` + stop + `","pending":[]}}}`
		approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){"GET /api/runtime/runs/crn_8": reply(200, body)})
		out, err := runCommand(t, RunsCmd(), "wait", "crn_8", "--wait-timeout", "5")
		if got := exitCodeOf(err); got != 1 {
			t.Fatalf("%s: exit = %d (%v), want 1", stop, got, err)
		}
		if !strings.Contains(out, "crn_8 (failed)") || !strings.Contains(out, "Stopped:  "+stop) {
			t.Errorf("%s: output:\n%s", stop, out)
		}
	}
}

func TestRuns_WaitEndsOnEveryRunStatus(t *testing.T) {
	fastJobPolls(t)
	for _, tc := range []struct {
		status string
		code   int
	}{
		{"succeeded", 0}, {"failed", 1}, {"timed_out", 1}, {"killed", 1}, {"cancelled", 1}, {"pending_approval", exitPending},
	} {
		body := `{"run":{"id":"crn_9","status":"` + tc.status + `","kind":"script","createdAt":"x","error":null,"contentHidden":false,"calls":[],"transcript":null}}`
		approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){"GET /api/runtime/runs/crn_9": reply(200, body)})
		if _, err := runCommand(t, RunsCmd(), "wait", "crn_9", "--wait-timeout", "5"); exitCodeOf(err) != tc.code {
			t.Errorf("%s: exit = %d (%v), want %d", tc.status, exitCodeOf(err), err, tc.code)
		}
	}
}

func TestRuns_MessageSendsASteerOrFollowUpNote(t *testing.T) {
	_, bodies := approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"POST /api/runtime/runs/crn_7/messages": reply(201, `{"message":{"id":"msg_1","mode":"steer"}}`),
	})
	out, err := runCommand(t, RunsCmd(), "message", "crn_7", "--steer", " focus on October ")
	if err != nil || !strings.Contains(out, "Sent a steer message msg_1 to crn_7.") {
		t.Fatalf("%q, %v", out, err)
	}
	if _, err := runCommand(t, RunsCmd(), "message", "crn_7", "--follow-up", "then email it"); err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 2 || (*bodies)[0]["mode"] != "steer" || (*bodies)[0]["text"] != "focus on October" || (*bodies)[1]["mode"] != "follow_up" {
		t.Errorf("bodies = %v", *bodies)
	}
}

func TestRuns_MessageRefusesBadInputBeforeAnyRequest(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"message", "crn_7"}, "exactly one of"},
		{[]string{"message", "crn_7", "--steer", "a", "--follow-up", "b"}, "exactly one of"},
		{[]string{"message", "crn_7", "--steer", "  "}, "empty"},
		{[]string{"message", "crn_7", "--steer", strings.Repeat("x", maxRunMessageBytes+1)}, "at most 4096"},
		{[]string{"message", "job_01", "--steer", "a"}, "run id"},
	} {
		if _, err := runCommand(t, RunsCmd(), tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args[:3], err, tc.want)
		}
	}
}

func TestRuns_TheRunRefusalsCarryTheGatewaysSentenceAndAHint(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"POST /api/runtime/runs/crn_7/messages": reply(409, `{"error":"not_running","message":"The run is not going, so it cannot read a message."}`),
		"POST /api/runtime/runs/crn_8/messages": reply(409, `{"error":"message_limit_reached","message":"This run has reached its limit of messages."}`),
		"POST /api/runtime/runs/crn_7/cancel":   reply(409, `{"error":"not_running","message":"The run has already ended."}`),
		"POST /api/runtime/runs/crn_9/messages": reply(403, `{"error":"forbidden","message":"Agent credentials may not message a run."}`),
	})
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"message", "crn_7", "--steer", "a"}, []string{"The run is not going, so it cannot read a message. (HTTP 409)", "zuano runs show crn_7"}},
		{[]string{"message", "crn_8", "--follow-up", "a"}, []string{"limit of messages", "start a new run"}},
		{[]string{"cancel", "crn_7"}, []string{"The run has already ended. (HTTP 409)", "zuano runs show crn_7"}},
		{[]string{"message", "crn_9", "--steer", "a"}, []string{"Agent credentials may not message a run. (HTTP 403)"}},
	} {
		_, err := runCommand(t, RunsCmd(), tc.args...)
		if err == nil {
			t.Fatalf("%v: no error", tc.args)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: %q lacks %q", tc.args, err, want)
			}
		}
	}
}
