package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const recipeDiffDoc = `{"diff":{"recipeId":"rcp_1","against":"version","from":2,"to":3,"changed":true,` +
	`"title":{"from":"Send an email","to":"Send an email safely"},"useCase":null,` +
	`"difficulty":{"from":{"level":"easy","reason":"two calls"},"to":{"level":"medium","reason":"needs a draft first"}},` +
	`"primaryTools":{"added":["gmail.create_draft"],"removed":[]},"relatedTools":{"added":[],"removed":["gmail.list_labels"]},` +
	`"steps":[{"order":1,"change":"changed","before":{"order":1,"kind":"required","tool":"gmail.send","instruction":"Send it."},"after":{"order":1,"kind":"required","tool":"gmail.create_draft","instruction":"Draft it first."}},` +
	`{"order":3,"change":"added","before":null,"after":{"order":3,"kind":"optional","condition":"when asked","tool":"gmail.send","instruction":"Send the draft."}}],` +
	`"pitfalls":{"added":[{"tool":"gmail.send","errorCodes":["quota"],"text":"Daily quota.","fix":"Wait."}],"removed":[]}}}`

func TestRecipesDiffMatchesTheOpenAPI(t *testing.T) {
	doc := loadOpenAPI(t)
	op, _, ok := doc.operationFor("GET", "/api/v1/recipes/rcp_1/diff")
	if !ok {
		t.Fatal("recipes.diff is not in the OpenAPI document")
	}
	var v any
	if err := json.Unmarshal([]byte(recipeDiffDoc), &v); err != nil {
		t.Fatal(err)
	}
	for _, p := range schemaProblems(openAPIResponseSchema(t, op, "200"), v, "$") {
		t.Error(p)
	}
	var query url.Values
	gw := a2aGateway(t, routeFns{
		"GET /api/v1/recipes/rcp_1/diff": func(s *packServer, w http.ResponseWriter, r *http.Request) {
			query = r.URL.Query()
			jsonReply(200, recipeDiffDoc)(s, w, r)
		},
	})
	if _, err := runCommand(t, RecipesCmd(), "diff", "rcp_1", "--from", "2", "--to", "3"); err != nil {
		t.Fatal(err)
	}
	for _, p := range op.problems(query, nil) {
		t.Error(p)
	}
	if gw.count("GET /api/v1/recipes/rcp_1/diff") != 1 {
		t.Error("diff did not send exactly one request")
	}
}

func TestRecipesDiffPrintsWhatChanged(t *testing.T) {
	var query url.Values
	a2aGateway(t, routeFns{
		"GET /api/v1/recipes/rcp_1/diff": func(s *packServer, w http.ResponseWriter, r *http.Request) {
			query = r.URL.Query()
			jsonReply(200, recipeDiffDoc)(s, w, r)
		},
	})
	out, err := runCommand(t, RecipesCmd(), "diff", "rcp_1", "--from", "2", "--to", "3")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"v2 -> v3", "Title: Send an email -> Send an email safely", "Difficulty: easy -> medium",
		"Primary tools: +gmail.create_draft", "Related tools: -gmail.list_labels",
		"~ 1. gmail.send: Send it.", "-> 1. gmail.create_draft: Draft it first.", "+ 3. gmail.send (optional: when asked): Send the draft.",
		"+ gmail.send [quota]: Daily quota.", "not signature-checked",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the diff lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Use for") {
		t.Errorf("an unchanged field was printed:\n%s", out)
	}
	if query.Get("from") != "2" || query.Get("to") != "3" || query.Has("against") {
		t.Errorf("query = %v", query)
	}

	if out, err = runCommand(t, RecipesCmd(), "diff", "rcp_1", "--against", "platform"); err != nil || query.Get("against") != "platform" || query.Has("from") {
		t.Errorf("against platform: %v %v\n%s", query, err, out)
	}
	if out, err = runCommand(t, RecipesCmd(), "diff", "rcp_1", "--json"); err != nil || !strings.Contains(out, `"diff"`) {
		t.Errorf("--json: %v\n%s", err, out)
	}
}

func TestRecipesDiffUnchangedAndRefusedInput(t *testing.T) {
	gw := a2aGateway(t, routeFns{
		"GET /api/v1/recipes/rcp_1/diff": jsonReply(200, `{"diff":{"recipeId":"rcp_1","against":"version","from":1,"to":2,"changed":false,"title":null,"useCase":null,"difficulty":null,"primaryTools":{"added":[],"removed":[]},"relatedTools":{"added":[],"removed":[]},"steps":[],"pitfalls":{"added":[],"removed":[]}}}`),
	})
	if out, err := runCommand(t, RecipesCmd(), "diff", "rcp_1"); err != nil || !strings.Contains(out, "No difference between v1 and v2") {
		t.Errorf("unchanged: %v\n%s", err, out)
	}
	for name, args := range map[string][]string{
		"bad ref":                    {"diff", "a/b"},
		"zero from":                  {"diff", "rcp_1", "--from", "0"},
		"negative to":                {"diff", "rcp_1", "--to", "-1"},
		"unknown base":               {"diff", "rcp_1", "--against", "main"},
		"platform with a version":    {"diff", "rcp_1", "--against", "platform", "--from", "1"},
		"platform with the other to": {"diff", "rcp_1", "--against", "platform", "--to", "2"},
	} {
		before := gw.count("GET /api/v1/recipes/rcp_1/diff")
		if _, err := runCommand(t, RecipesCmd(), args...); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if gw.count("GET /api/v1/recipes/rcp_1/diff") != before {
			t.Errorf("%s reached the gateway", name)
		}
	}
	a2aGateway(t, routeFns{
		"GET /api/v1/recipes/rcp_1/diff": jsonReply(404, `{"error":"not_found","message":"Recipe not found."}`),
	})
	if _, err := runCommand(t, RecipesCmd(), "diff", "rcp_1", "--against", "platform"); err == nil || !strings.Contains(err.Error(), "Recipe not found.") {
		t.Errorf("a refusal lost its sentence: %v", err)
	}
}

func TestRecipesPromoteSaysItMustBeDoneInTheDashboard(t *testing.T) {
	gw := a2aGateway(t, routeFns{
		"POST /api/v1/recipes/rcp_1:promote": jsonReply(403, `{"error":"browser_session_required","message":"Sign in to the dashboard to do this."}`),
	})
	_, err := runCommand(t, RecipesCmd(), "promote", "rcp_1")
	if err == nil || exitCodeOf(err) != exitDenied {
		t.Fatalf("exit %d, %v", exitCodeOf(err), err)
	}
	for _, want := range []string{"Sign in to the dashboard to do this.", "in the dashboard", "will not change that"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	if n := gw.count("POST /api/v1/recipes/rcp_1:promote"); n != 1 {
		t.Errorf("the refused promote was sent %d times", n)
	}
}

func TestRecipesPromoteVersionAndPlanNote(t *testing.T) {
	gw := a2aGateway(t, routeFns{
		"POST /api/v1/recipes/rcp_1:promote": jsonReply(201, `{"skill":{"id":"res_9","slug":"send-email","url":"/catalog/res_9"},"versionId":"ver_1","semver":"0.1.0","casesCopied":0,"casesSkipped":"The plan does not include evaluations."}`),
	})
	out, err := runCommand(t, RecipesCmd(), "promote", "rcp_1", "--version", "2")
	if err != nil || !strings.Contains(out, "No eval cases were copied: The plan does not include evaluations.") {
		t.Errorf("promote: %v\n%s", err, out)
	}
	if b := gw.body("POST /api/v1/recipes/rcp_1:promote", 0); b["version"] != float64(2) || len(b) != 1 {
		t.Errorf("promote body = %v", b)
	}
	if _, err := runCommand(t, RecipesCmd(), "promote", "rcp_1", "--version", "0"); err == nil {
		t.Error("version 0 was accepted")
	}
	if _, err := runCommand(t, RecipesCmd(), "promote", "rcp_1"); err != nil {
		t.Fatal(err)
	}
	if b := gw.body("POST /api/v1/recipes/rcp_1:promote", 1); len(b) != 0 {
		t.Errorf("a promote without --version sent %v", b)
	}
}

func TestRecipesShowTakesTheOrganisationFromWhoami(t *testing.T) {
	_, keys := gmailFixture(t)
	body := resignedRecipe(t, "org", "org_a", nil)
	whoamiOrg := "org_a"
	gw := a2aGateway(t, routeFns{
		"GET /api/v1/recipes/" + gmailRecipeID: jsonReply(200, body),
		"GET /api/v1/recipes/keys":             keysReply(keys, nil),
		"GET /api/cli/whoami": func(s *packServer, w http.ResponseWriter, r *http.Request) {
			jsonReply(200, `{"email":"a@example.com","googleSub":null,"orgId":"`+whoamiOrg+`","services":[],"issuedAt":"2026-10-16T00:00:00Z"}`)(s, w, r)
		},
	})
	out, err := runCommand(t, RecipesCmd(), "show", gmailRecipeID)
	if err != nil || !strings.Contains(out, "signature: verified (org recipe") {
		t.Fatalf("the credential's own organisation: %v\n%s", err, out)
	}
	whoamiOrg = "org_b"
	if out, err = runCommand(t, RecipesCmd(), "show", gmailRecipeID); err == nil || !strings.Contains(out, "another organisation") {
		t.Errorf("another organisation: %v\n%s", err, out)
	}
	before := gw.count("GET /api/cli/whoami")
	if out, err = runCommand(t, RecipesCmd(), "show", gmailRecipeID, "--org", "org_a"); err != nil || gw.count("GET /api/cli/whoami") != before {
		t.Errorf("--org did not take precedence: %v\n%s", err, out)
	}
}
