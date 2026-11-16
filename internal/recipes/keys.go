package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/patrikmichi/zuano-cli/internal/config"
	"github.com/patrikmichi/zuano-cli/internal/signing"
)

// KeysPath is where the gateway publishes the keys recipes are signed with.
const KeysPath = "/api/v1/recipes/keys"

// Algorithm is the only signature algorithm a recipe key may have.
const Algorithm = "ed25519"

// MaxKeyLifetime bounds how long a published key set is reused, whatever the
// gateway's answer allows.
const MaxKeyLifetime = time.Hour

const maxKeysBody = 64 << 10

// Key is one public key recipes are signed with, as the gateway publishes it.
type Key struct {
	KeyID     string `json:"keyId"`
	Alg       string `json:"alg"`
	PublicKey string `json:"publicKey"`
}

// KeySet is a published key set and how long the gateway lets it be reused.
type KeySet struct {
	Keys   []Key
	MaxAge time.Duration
}

// FetchKeys reads the keys the gateway at origin publishes. The request carries
// no credentials and follows no redirect.
func FetchKeys(ctx context.Context, origin string) (KeySet, error) {
	base, err := config.NormalizeGatewayURL(origin)
	if err != nil {
		return KeySet{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+KeysPath, nil)
	if err != nil {
		return KeySet{}, err
	}
	hc := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("refusing to follow a redirect") },
	}
	resp, err := hc.Do(req)
	if err != nil {
		return KeySet{}, fmt.Errorf("fetch recipe keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return KeySet{}, fmt.Errorf("fetch recipe keys: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeysBody+1))
	if err != nil {
		return KeySet{}, fmt.Errorf("fetch recipe keys: %w", err)
	}
	if len(body) > maxKeysBody {
		return KeySet{}, errors.New("fetch recipe keys: response is too large")
	}
	keys, err := ParseKeys(body)
	if err != nil {
		return KeySet{}, err
	}
	return KeySet{Keys: keys, MaxAge: maxAge(resp.Header.Get("Cache-Control"))}, nil
}

// ParseKeys reads a published key document. A document with a key that has no
// keyId, that still carries the older kid field, or that repeats an id is
// refused as a whole: a set that cannot be read exactly is not trusted in part.
func ParseKeys(raw []byte) ([]Key, error) {
	doc, err := signing.DecodeStrict(raw)
	if err != nil {
		return nil, fmt.Errorf("recipe keys: %w", err)
	}
	list, ok := field[[]any](doc, "keys")
	if !ok || len(list) == 0 {
		return nil, signing.ErrNoKeys
	}
	seen := map[string]bool{}
	keys := make([]Key, 0, len(list))
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("recipe keys: a key is not an object")
		}
		if _, legacy := obj["kid"]; legacy {
			return nil, errors.New("recipe keys: a key uses the older kid field")
		}
		id, _ := field[string](obj, "keyId")
		alg, _ := field[string](obj, "alg")
		pub, _ := field[string](obj, "publicKey")
		if id == "" || alg == "" || pub == "" {
			return nil, errors.New("recipe keys: a key lacks its keyId, alg or publicKey")
		}
		if seen[id] {
			return nil, errors.New("recipe keys: two keys share an id")
		}
		seen[id] = true
		keys = append(keys, Key{KeyID: id, Alg: alg, PublicKey: pub})
	}
	return keys, nil
}

// maxAge is how long a response with this Cache-Control may be reused; zero
// when it says not to, says nothing or cannot be read.
func maxAge(header string) time.Duration {
	var age time.Duration
	for _, directive := range strings.Split(header, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(strings.ToLower(directive)), "=")
		switch name {
		case "no-store", "no-cache", "private":
			return 0
		case "max-age":
			secs, err := strconv.ParseUint(strings.Trim(value, `"`), 10, 31)
			if err != nil {
				return 0
			}
			age = min(time.Duration(secs)*time.Second, MaxKeyLifetime)
		}
	}
	return age
}

// KeyCache keeps the published recipe keys on disk for as long as the gateway's
// answer allows, so showing recipes in a row does not ask for the keys each
// time. A key withdrawn from the published set stops verifying once that time
// has passed.
type KeyCache struct {
	// Path is the cache file.
	Path string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Fetch reads the keys published by the origin.
	Fetch func(ctx context.Context, origin string) (KeySet, error)
}

type cachedKeys struct {
	Origin    string    `json:"origin"`
	FetchedAt time.Time `json:"fetchedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Keys      []Key     `json:"keys"`
}

func (c KeyCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Keys returns the published keys. A cached set is used while it is fresh and
// holds wantKeyID; a set that lacks the key (a rotation) or has expired is
// fetched again.
func (c KeyCache) Keys(ctx context.Context, origin, wantKeyID string) ([]Key, error) {
	now := c.now()
	if raw, err := os.ReadFile(c.Path); err == nil {
		var cached cachedKeys
		// A set dated in the future, or kept longer than allowed, is not trusted.
		if json.Unmarshal(raw, &cached) == nil && cached.Origin == origin &&
			!cached.FetchedAt.After(now) && now.Before(cached.ExpiresAt) && cached.ExpiresAt.Sub(cached.FetchedAt) <= MaxKeyLifetime &&
			(wantKeyID == "" || slices.ContainsFunc(cached.Keys, func(k Key) bool { return k.KeyID == wantKeyID })) {
			return cached.Keys, nil
		}
	}
	set, err := c.Fetch(ctx, origin)
	if err != nil {
		return nil, err
	}
	if set.MaxAge > 0 {
		if raw, err := json.Marshal(cachedKeys{Origin: origin, FetchedAt: now, ExpiresAt: now.Add(set.MaxAge), Keys: set.Keys}); err == nil {
			if os.MkdirAll(filepath.Dir(c.Path), 0o700) == nil {
				// Not being able to cache costs a request next time, nothing more.
				_ = os.WriteFile(c.Path, raw, 0o600)
			}
		}
	} else {
		_ = os.Remove(c.Path)
	}
	return set.Keys, nil
}
