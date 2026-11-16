// Package recipes holds the shape of a tool recipe (a playbook for using a
// few tools well) and checks its signature against the keys the gateway publishes.
package recipes

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/patrikmichi/zuano-cli/internal/signing"
)

// Platform and Org are the two scopes a recipe is signed for.
const (
	Platform = "platform"
	Org      = "org"
)

// Step is one step of a recipe.
type Step struct {
	Order       int    `json:"order"`
	Kind        string `json:"kind"`
	Condition   string `json:"condition,omitempty"`
	FallbackFor int    `json:"fallbackFor,omitempty"`
	Tool        string `json:"tool"`
	Instruction string `json:"instruction"`
}

// Pitfall is a way a step goes wrong and what to do instead.
type Pitfall struct {
	Tool       string   `json:"tool"`
	ErrorCodes []string `json:"errorCodes,omitempty"`
	Text       string   `json:"text"`
	Fix        string   `json:"fix"`
}

// Signature names the key that signed a recipe and the place it was signed for.
type Signature struct {
	KeyID     string  `json:"keyId"`
	Signature string  `json:"signature"`
	Scope     string  `json:"scope"`
	OrgID     *string `json:"orgId"`
}

// Recipe is a recipe as the gateway serves it.
type Recipe struct {
	ID             string     `json:"id"`
	Slug           string     `json:"slug"`
	Version        int        `json:"version"`
	CurrentVersion *int       `json:"currentVersion"`
	LatestVersion  int        `json:"latestVersion"`
	Services       []string   `json:"services"`
	Source         string     `json:"source"`
	Overrides      *string    `json:"overrides"`
	Status         string     `json:"status"`
	ContentHash    string     `json:"contentHash"`
	Signature      *Signature `json:"signature"`
	Editable       bool       `json:"editable"`
	Title          string     `json:"title"`
	UseCase        string     `json:"useCase"`
	Difficulty     struct {
		Level  string `json:"level"`
		Reason string `json:"reason"`
	} `json:"difficulty"`
	PrimaryTools []string  `json:"primaryTools"`
	RelatedTools []string  `json:"relatedTools"`
	Steps        []Step    `json:"steps"`
	Pitfalls     []Pitfall `json:"pitfalls"`
	CreatedAt    string    `json:"createdAt"`
	UpdatedAt    string    `json:"updatedAt"`
}

// Summary is one entry of a list of recipes.
type Summary struct {
	ID           string   `json:"id"`
	Slug         string   `json:"slug"`
	Title        string   `json:"title"`
	UseCase      string   `json:"useCase"`
	Services     []string `json:"services"`
	Source       string   `json:"source"`
	Status       string   `json:"status"`
	Version      int      `json:"version"`
	StepCount    int      `json:"stepCount"`
	PitfallCount int      `json:"pitfallCount"`
	UpdatedAt    string   `json:"updatedAt"`
}

// Expect is what the caller asked for; a recipe that answers something else is refused.
type Expect struct {
	// ID is the recipe id that was requested.
	ID string
	// Version is the version that was requested; 0 means the served one.
	Version int
	// OrgID is the organisation the caller reads recipes for. An organisation
	// recipe verifies only for this one; it may be empty when the caller reads
	// platform recipes only.
	OrgID string
}

// Verification is the outcome of checking a recipe's signature.
type Verification struct {
	Verified bool
	Scope    string
	KeyID    string
	Reason   string
}

// KeySource returns the published keys, given the id of the key a recipe names.
type KeySource func(keyID string) ([]Key, error)

// contentFields are the parts of a recipe the content hash covers.
var contentFields = []string{"title", "useCase", "difficulty", "primaryTools", "relatedTools", "steps", "pitfalls"}

// Open reads the gateway's answer to a recipe request and verifies it. The
// recipe comes back only when every check passed: the keys come from keys,
// never from the answer; the content hash is recomputed from the content as
// received; the signature covers that hash with the recipe's id, version, scope
// and organisation; and those match what was asked for.
func Open(raw []byte, want Expect, keys KeySource) (Recipe, Verification) {
	var v Verification
	fail := func(format string, a ...any) (Recipe, Verification) {
		v.Verified = false
		v.Reason = fmt.Sprintf(format, a...)
		return Recipe{}, v
	}
	doc, err := signing.DecodeStrict(raw)
	if err != nil {
		return fail("the answer is not a clean JSON document: %v", err)
	}
	rec, ok := field[map[string]any](doc, "recipe")
	if !ok {
		return fail("the answer holds no recipe")
	}
	id, _ := field[string](rec, "id")
	if id == "" || id != want.ID {
		return fail("the answer is not the recipe that was asked for")
	}
	version, ok := intField(rec, "version")
	if !ok || version < 1 {
		return fail("the recipe has no valid version")
	}
	if want.Version > 0 && version != want.Version {
		return fail("the answer is version %d, not the version asked for", version)
	}
	sigObj, ok := field[map[string]any](rec, "signature")
	if !ok {
		return fail("the recipe is not signed")
	}
	keyID, _ := field[string](sigObj, "keyId")
	sigText, _ := field[string](sigObj, "signature")
	scope, _ := field[string](sigObj, "scope")
	if keyID == "" || sigText == "" {
		return fail("the recipe is not signed")
	}
	v.KeyID, v.Scope = keyID, scope
	var orgID any
	rawOrg, present := sigObj["orgId"]
	switch o := rawOrg.(type) {
	case nil:
		if !present {
			return fail("the signature names no organisation field")
		}
	case string:
		orgID = o
	default:
		return fail("the signature names no valid organisation")
	}

	published, err := keys(keyID)
	if err != nil {
		return fail("the published keys could not be read: %v", err)
	}
	pub, err := publicKeyFor(published, keyID)
	if err != nil {
		return fail("%v", err)
	}

	hash, err := contentHash(rec)
	if err != nil {
		return fail("%v", err)
	}
	if shown, _ := field[string](rec, "contentHash"); shown != hash {
		return fail("the content does not match its hash")
	}

	envelope, err := signing.CanonicalJSON(map[string]any{
		"contentHash": hash, "orgId": orgID, "recipeId": id, "scope": scope, "version": json.Number(strconv.Itoa(version)),
	})
	if err != nil {
		return fail("the signed envelope cannot be built: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigText)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, envelope, sig) {
		return fail("the signature does not match the content")
	}

	switch scope {
	case Platform:
		if orgID != nil {
			return fail("a platform recipe is signed for no organisation")
		}
	case Org:
		org, _ := orgID.(string)
		switch {
		case org == "":
			return fail("an organisation recipe names no organisation")
		case want.OrgID == "":
			return fail("this is an organisation recipe and your organisation is not known; pass --org to say which")
		case org != want.OrgID:
			return fail("the recipe is signed for another organisation")
		}
	default:
		return fail("the signature has an unknown scope")
	}

	var parsed struct {
		Recipe Recipe `json:"recipe"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fail("the recipe cannot be read: %v", err)
	}
	v.Verified = true
	return parsed.Recipe, v
}

// contentHash is the SHA-256 of the canonical JSON of the content fields of rec,
// taken from the document as received so that nothing in them is dropped or added.
func contentHash(rec map[string]any) (string, error) {
	content := make(map[string]any, len(contentFields))
	for _, name := range contentFields {
		value, ok := rec[name]
		if !ok || value == nil {
			return "", fmt.Errorf("the recipe has no %s", name)
		}
		content[name] = value
	}
	canon, err := signing.CanonicalJSON(content)
	if err != nil {
		return "", fmt.Errorf("the content cannot be hashed: %v", err)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

func publicKeyFor(keys []Key, keyID string) (ed25519.PublicKey, error) {
	for _, k := range keys {
		if k.KeyID != keyID {
			continue
		}
		if k.Alg != Algorithm {
			return nil, fmt.Errorf("the key %q uses the algorithm %q, which is not accepted", keyID, k.Alg)
		}
		raw, err := base64.StdEncoding.DecodeString(k.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("the key %q is not a valid Ed25519 public key", keyID)
		}
		return ed25519.PublicKey(raw), nil
	}
	return nil, ErrNoKey
}

// ErrNoKey is returned when a key set holds no key with the signature's id.
var ErrNoKey = errors.New("no published key has the id the recipe is signed with")

func field[T any](m any, name string) (T, bool) {
	obj, ok := m.(map[string]any)
	if !ok {
		var zero T
		return zero, false
	}
	v, ok := obj[name].(T)
	return v, ok
}

func intField(m map[string]any, name string) (int, bool) {
	n, ok := field[json.Number](m, name)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(n.String())
	return i, err == nil && strconv.Itoa(i) == n.String()
}
