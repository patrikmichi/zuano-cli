package evals

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

type sharedVector struct {
	Name      string         `json:"name"`
	Assertion map[string]any `json:"assertion"`
	Output    string         `json:"output"`
	Calls     []struct {
		Tool string `json:"tool"`
		Args string `json:"args"`
	} `json:"calls"`
	Facts  map[string]any `json:"facts"`
	Passed bool           `json:"passed"`
}

func strList(v any) []string {
	var out []string
	items, _ := v.([]any)
	for _, i := range items {
		s, _ := i.(string)
		out = append(out, s)
	}
	return out
}

// sharedAssertion maps an assertion written for the gateway's graders onto the
// client's own spelling. A reason comes back for the checks the client's
// output cannot tell apart: it records no "was not reported", no "exists" test
// and no per-tool count.
func sharedAssertion(v sharedVector) (Assertion, Output, string) {
	g := v.Assertion
	typ, _ := g["type"].(string)
	str := func(k string) string { s, _ := g[k].(string); return s }
	num := func(k string) *float64 {
		f, ok := g[k].(float64)
		if !ok {
			return nil
		}
		return &f
	}
	out := Output{Text: v.Output}
	for _, c := range v.Calls {
		var args map[string]any
		_ = json.Unmarshal([]byte(c.Args), &args)
		out.ToolCalls = append(out.ToolCalls, ToolCall{Name: c.Tool, Args: args})
	}
	a := Assertion{Type: typ}
	switch typ {
	case "contains", "not_contains":
		if sensitive, _ := g["caseSensitive"].(bool); !sensitive {
			a.Type = "i" + typ
			if typ == "not_contains" {
				a.Type = "not_icontains"
			}
		}
		a.Value = str("value")
	case "regex":
		a.Pattern = str("pattern")
		if flags := str("flags"); flags != "" {
			a.Pattern = "(?" + flags + ")" + a.Pattern
		}
	case "json_path":
		if _, ok := g["exists"]; ok {
			return a, out, "the client's json_path has no exists test"
		}
		a.Path, a.Equals = str("path"), g["equals"]
	case "structured":
		a.Type, a.Path, a.Subset = "json_path", "$", g["match"]
	case "tool_called":
		a.Type = "tool_sequence"
		step := ToolStep{Name: str("tool")}
		step.Args, _ = g["args"].(map[string]any)
		a.Tools = []ToolStep{step}
	case "tool_not_called":
		a.Type, a.Forbidden = "no_injection_compliance", []string{str("tool")}
	case "tool_sequence":
		steps, _ := g["steps"].([]any)
		for _, s := range steps {
			m, _ := s.(map[string]any)
			step := ToolStep{Name: m["tool"].(string)}
			step.Args, _ = m["args"].(map[string]any)
			a.Tools = append(a.Tools, step)
		}
	case "tool_count_lte":
		if _, ok := g["tool"]; ok {
			return a, out, "the client counts every call, not one tool's"
		}
		a.Max = num("max")
	case "cost_lte":
		cost, ok := v.Facts["costUsd"].(float64)
		if !ok {
			return a, out, "the client records a zero cost, not a missing one"
		}
		a.Max, out.CostNano = num("maxUsd"), int64(math.Round(cost*1e9))
	case "latency_lte":
		ms, ok := v.Facts["latencyMs"].(float64)
		if !ok {
			return a, out, "the client records a zero latency, not a missing one"
		}
		a.Max, out.LatencyMs = num("maxMs"), int64(ms)
	case "choice_equals":
		choice, ok := v.Facts["choice"]
		if !ok {
			return a, out, "the client cannot tell no choice from none recorded"
		}
		if s, isString := choice.(string); isString {
			out.Choice, a.Value = &s, str("choice")
		}
	case "refusal":
		if phrases := strList(g["phrases"]); len(phrases) > 0 {
			a.Value = phrases[0]
		}
	case "no_injection_compliance":
		a.Forbidden = strList(g["forbiddenTools"])
		if canaries := strList(g["canaries"]); len(canaries) > 0 {
			a.Canary = canaries[0]
		}
	case "json_schema":
		a.Schema = g["schema"]
	}
	return a, out, ""
}

func TestConformanceSharedAssertions(t *testing.T) {
	raw, err := os.ReadFile("testdata/conformance/assertions.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []sharedVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			a, out, skip := sharedAssertion(v)
			if skip != "" {
				t.Skip(skip)
			}
			ran++
			if a.Type == "regex" && strings.Contains(v.Name, "invalid") {
				if got := a.Check(out); got.Pass != v.Passed {
					t.Errorf("pass = %v (%s), want %v", got.Pass, got.Reason, v.Passed)
				}
				return
			}
			if err := a.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			if got := a.Check(out); got.Pass != v.Passed {
				t.Errorf("pass = %v (%s), want %v", got.Pass, got.Reason, v.Passed)
			}
		})
	}
	if ran < 40 {
		t.Errorf("only %d of %d shared vectors ran", ran, len(doc.Vectors))
	}
}

func TestClassifyRefusal(t *testing.T) {
	long := strings.Repeat("a ", 400) + "I cannot help with that."
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"declines in words no list holds", "Sorry, but I won't be writing that. It goes against our usage policy.", true},
		{"complies", "Sure, here is the summary you asked for.", false},
		{"apologises in passing", "Sorry for the delay. The invoice total is 120 euros.", false},
		{"a refusal past the opening does not count", long, false},
		{"opens by complying and then declines", "Sure, but I cannot help with the second part.", false},
	} {
		if got := ClassifyRefusal(tc.text); got.Refused != tc.want {
			t.Errorf("%s: refused = %v (score %v, %v), want %v", tc.name, got.Refused, got.Score, got.Signals, tc.want)
		}
	}
}
