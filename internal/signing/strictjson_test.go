package signing

import (
	"encoding/json"
	"testing"
)

func TestDecodeStrictRefusesRepeatedKeysAndTrailingContent(t *testing.T) {
	for name, doc := range map[string]string{
		"top level":     `{"a":1,"a":2}`,
		"nested":        `{"a":{"b":1,"b":1}}`,
		"in an array":   `{"a":[{"b":1,"b":2}]}`,
		"escaped twin":  `{"a":1,"a":2}`,
		"trailing":      `{"a":1} {"b":2}`,
		"trailing text": `{"a":1}x`,
		"truncated":     `{"a":`,
		"empty":         ``,
	} {
		if _, err := DecodeStrict([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDecodeStrictKeepsNumbersAndNulls(t *testing.T) {
	v, err := DecodeStrict([]byte(` {"n":1.50,"z":null,"l":[1,"x",true],"o":{}} `))
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["n"] != json.Number("1.50") || m["z"] != nil || len(m["l"].([]any)) != 3 || len(m["o"].(map[string]any)) != 0 {
		t.Errorf("decoded = %#v", m)
	}
	out, err := CanonicalJSON(m)
	if err != nil || string(out) != `{"l":[1,"x",true],"n":1.50,"o":{},"z":null}` {
		t.Errorf("canonical = %s, %v", out, err)
	}
}
