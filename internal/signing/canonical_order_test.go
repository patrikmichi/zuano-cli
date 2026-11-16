package signing

import "testing"

// JavaScript sorts object keys by UTF-16 code unit, so a character above the
// basic plane (a surrogate pair, 0xD800 and up) sorts before U+E000 to U+FFFF,
// the reverse of byte order in UTF-8.
func TestCanonicalJSONOrdersKeysByUTF16CodeUnit(t *testing.T) {
	got, err := CanonicalJSON(map[string]any{"～": 1, "\U0001F600": 2, "z": 3})
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"z\":3,\"\U0001F600\":2,\"～\":1}"; string(got) != want {
		t.Errorf("canonical = %q, want %q", got, want)
	}
}
