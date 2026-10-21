package cli

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/client"
)

const (
	pausedSettingsJSON = `{"allowed_sources":["byo","platform"],"default_source":"platform","default_models":{"workflow_step":"anthropic/claude-sonnet-5.5"},"platform_members":"listed","listed_members":["usr_1","usr_2"],"platform_pause":{"reason":"runaway","since":"2026-10-21T09:00:00.000Z"},"platform_models_available":true}`
	freeSettingsJSON   = `{"allowed_sources":["byo"],"default_source":"byo","default_models":{},"platform_members":"all","listed_members":[],"platform_pause":null,"platform_models_available":false}`
)

func actingClient(t *testing.T, org string) {
	t.Helper()
	if err := client.SetActingClient(org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.SetActingClient("") })
}

func TestModelsSettings_ShowsThePauseAndWhoMayResume(t *testing.T) {
	workflowGateway(t, routeMap{
		"GET /api/v1/models/settings": jsonReply(200, pausedSettingsJSON),
	})
	out, err := runCommand(t, ModelsCmd(), "settings")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"byo, platform; default platform", "listed (2 listed)", "workflow_step", "claude-sonnet-5.5", "PAUSED since 2026-10-21T09:00:00.000Z (runaway)", "zuano models resume"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings lacks %q:\n%s", want, out)
		}
	}
	if out, err := runCommand(t, ModelsCmd(), "settings", "--json"); err != nil || strings.TrimSpace(out) != pausedSettingsJSON {
		t.Errorf("--json = %q, %v", out, err)
	}
}

func TestModelsSettings_AnOrganisationWithoutCreditSaysSo(t *testing.T) {
	workflowGateway(t, routeMap{
		"GET /api/v1/models/settings": jsonReply(200, freeSettingsJSON),
	})
	out, err := runCommand(t, ModelsCmd(), "settings")
	if err != nil || !strings.Contains(out, "not offered on this gateway") || strings.Contains(out, "PAUSED") || !strings.Contains(out, "No default models are set") {
		t.Fatalf("settings = %q, %v", out, err)
	}
}

func TestModelsResume_PostsAndExplainsAnUnpausedOrganisation(t *testing.T) {
	var paused atomic.Bool
	paused.Store(true)
	gw := workflowGateway(t, routeMap{
		"POST /api/v1/models/settings/resume": func(_ *packServer, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if paused.Swap(false) {
				_, _ = io.WriteString(w, `{"platform_pause":null}`)
				return
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"not_paused","message":"Platform models are not paused for this organization."}`)
		},
	})
	out, err := runCommand(t, ModelsCmd(), "resume")
	if err != nil || !strings.Contains(out, "Platform models run again.") {
		t.Fatalf("resume = %q, %v", out, err)
	}
	if body := gw.body("POST /api/v1/models/settings/resume", 0); body == nil || len(body) != 0 {
		t.Errorf("body = %v, want an empty object", body)
	}
	_, err = runCommand(t, ModelsCmd(), "resume")
	if err == nil || !strings.Contains(err.Error(), "not paused") || !strings.Contains(err.Error(), "409") {
		t.Fatalf("second resume err = %v", err)
	}
}

func TestModelsResume_AskForAPasskeyConfirmationAndRetries(t *testing.T) {
	var confirmed atomic.Bool
	var posts atomic.Int32
	workflowGateway(t, routeMap{
		"POST /api/v1/models/settings/resume": func(_ *packServer, w http.ResponseWriter, _ *http.Request) {
			posts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if confirmed.Load() {
				_, _ = io.WriteString(w, `{"platform_pause":null}`)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"step_up_required","step_up":{"action":"billing_change","digest":"d1"}}`)
		},
	})
	stepUpEnv(t, true, personConfirms(func() { confirmed.Store(true) }))
	out, err := runCommand(t, ModelsCmd(), "resume")
	if err != nil || !strings.Contains(out, "run again") || posts.Load() != 2 {
		t.Fatalf("resume = %q, %v (posts %d)", out, err, posts.Load())
	}
}

func TestModelsResume_WithNobodyToConfirmExits14(t *testing.T) {
	workflowGateway(t, routeMap{
		"POST /api/v1/models/settings/resume": jsonReply(401, `{"error":"step_up_required","step_up":{"action":"billing_change","digest":"d1"}}`),
	})
	stepUpEnv(t, false, nil)
	_, err := runCommand(t, ModelsCmd(), "resume")
	if got := exitCodeOf(err); got != client.ExitStepUp {
		t.Fatalf("exit %d, err %v", got, err)
	}
}

func TestModelsDefaults_NeedAClient(t *testing.T) {
	workflowGateway(t, routeMap{})
	for _, args := range [][]string{{"defaults", "get"}, {"defaults", "set", "agent=openai/gpt-5.2"}} {
		_, err := runCommand(t, ModelsCmd(), args...)
		if got := exitCodeOf(err); got != exitUsage || err == nil || !strings.Contains(err.Error(), "--client") {
			t.Errorf("%v: exit %d, err %v", args, got, err)
		}
	}
}

func TestModelsDefaults_GetSendsTheClientHeader(t *testing.T) {
	var header string
	workflowGateway(t, routeMap{
		"GET /api/v1/models/defaults": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			header = r.Header.Get(client.ClientOrgHeader)
			_, _ = io.WriteString(w, `{"default_source":"platform","default_models":{"agent":"openai/gpt-5.2","workflow_step":"anthropic/claude-sonnet-5.5"}}`)
		},
	})
	actingClient(t, "org_8f2c")
	out, err := runCommand(t, ModelsCmd(), "defaults", "get")
	if err != nil || header != "org_8f2c" {
		t.Fatalf("get = %q, %v (header %q)", out, err, header)
	}
	for _, want := range []string{"Default source: platform", "openai/gpt-5.2", "claude-sonnet-5.5"} {
		if !strings.Contains(out, want) {
			t.Errorf("get lacks %q:\n%s", want, out)
		}
	}
}

func TestModelsDefaults_SetMergesWithTheCurrentDefaultsUnlessToldOtherwise(t *testing.T) {
	gw := workflowGateway(t, routeMap{
		"GET /api/v1/models/defaults": jsonReply(200, `{"default_source":"platform","default_models":{"agent":"openai/gpt-5.2","eval_judge":"openai/gpt-5.2"}}`),
		"PUT /api/v1/models/defaults": jsonReply(200, `{"default_source":"platform","default_models":{"agent":"openai/gpt-5.2","workflow_step":"anthropic/claude-sonnet-5.5"}}`),
	})
	actingClient(t, "org_8f2c")

	out, err := runCommand(t, ModelsCmd(), "defaults", "set", "workflow_step=anthropic/claude-sonnet-5.5", "--unset", "eval_judge")
	if err != nil || !strings.Contains(out, "claude-sonnet-5.5") {
		t.Fatalf("set = %q, %v", out, err)
	}
	want := map[string]any{"defaultModels": map[string]any{"agent": "openai/gpt-5.2", "workflow_step": "anthropic/claude-sonnet-5.5"}}
	if got := gw.body("PUT /api/v1/models/defaults", 0); !reflect.DeepEqual(got, want) {
		t.Errorf("merged body = %v, want %v", got, want)
	}

	if _, err := runCommand(t, ModelsCmd(), "defaults", "set", "--replace", "agent=openai/gpt-5.2"); err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"defaultModels": map[string]any{"agent": "openai/gpt-5.2"}}
	if got := gw.body("PUT /api/v1/models/defaults", 1); !reflect.DeepEqual(got, want) {
		t.Errorf("replace body = %v, want %v", got, want)
	}
	if gw.count("GET /api/v1/models/defaults") != 1 {
		t.Errorf("--replace read the current defaults")
	}
}

func TestModelsDefaults_SetRefusesBadInputBeforeSendingAnything(t *testing.T) {
	workflowGateway(t, routeMap{})
	actingClient(t, "org_8f2c")
	for name, args := range map[string][]string{
		"nothing":         {"defaults", "set"},
		"no equals":       {"defaults", "set", "agent"},
		"unknown surface": {"defaults", "set", "chat=openai/gpt-5.2"},
		"unknown unset":   {"defaults", "set", "--unset", "chat"},
		"set and unset":   {"defaults", "set", "agent=openai/gpt-5.2", "--unset", "agent"},
	} {
		if _, err := runCommand(t, ModelsCmd(), args...); exitCodeOf(err) != exitUsage {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestModelsDefaults_AnAgencyWithoutTheScopeGetsTheGatewaysSentence(t *testing.T) {
	workflowGateway(t, routeMap{
		"GET /api/v1/models/defaults": jsonReply(403, `{"error":"agency_scope_required","message":"This is for an agency acting in a client; send the client organization header."}`),
	})
	actingClient(t, "org_8f2c")
	_, err := runCommand(t, ModelsCmd(), "defaults", "get")
	if err == nil || !strings.Contains(err.Error(), "agency acting in a client") {
		t.Fatalf("err = %v", err)
	}
}
