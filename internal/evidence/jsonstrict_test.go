package evidence

import "testing"

func TestParseJSONRefusesWhatTwoReadersCouldSeeDifferently(t *testing.T) {
	for name, doc := range map[string]string{
		"a repeated key":        `{"a":1,"a":2}`,
		"a nested repeated key": `{"a":{"b":1,"b":1}}`,
		"data after":            `{"a":1} {"b":2}`,
		"a truncated document":  `{"a":`,
		"too deep":              deep(maxJSONDepth + 2),
	} {
		if _, err := parseJSON([]byte(doc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	v, err := parseJSON([]byte(`{"a":[1,2.50,"x",null,true],"b":{"c":12345678901234567890}}`))
	if err != nil {
		t.Fatal(err)
	}
	obj := v.(map[string]any)
	if got := obj["a"].([]any)[1].(interface{ String() string }).String(); got != "2.50" {
		t.Errorf("a number was changed to %q", got)
	}
}

func deep(n int) string {
	open, closing := make([]byte, n), make([]byte, n)
	for i := range open {
		open[i], closing[i] = '[', ']'
	}
	return string(open) + string(closing)
}

func TestSafeName(t *testing.T) {
	for name, ok := range map[string]bool{
		"manifest.json": true, "audit/events.part-001.jsonl": true, "customer-evidence/cev_1-a_b.pdf": true,
		"": false, "/abs": false, "../x": false, "a/../b": false, "a//b": false, "a/": false, "./a": false, "a/./b": false, "..": false, ".": false,
		`a\b`: false, "c:x": false, "a\x00b": false, "a\nb": false, "a\x7fb": false, "\xff\xfe": false,
	} {
		if got := safeName(name); got != ok {
			t.Errorf("safeName(%q) = %v, want %v", name, got, ok)
		}
	}
}
