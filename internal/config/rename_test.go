package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/config"
)

func TestRelayConfigMigrationPreservesOtherFiles(t *testing.T) {
	home := withTempHome(t)
	legacy := filepath.Join(home, ".config", "relay")
	if err := os.MkdirAll(filepath.Join(legacy, "providers"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"config.json":             `{"email":"user@example.com","gatewayUrl":"https://example.com"}`,
		"agentport-manifest.json": `{"entries":[]}`,
		"providers/custom.yml":    "id: custom\n",
	} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load()
	if err != nil || cfg.Email != "user@example.com" {
		t.Fatalf("migration failed: %+v, %v", cfg, err)
	}
	for _, name := range []string{"agentport-manifest.json", "providers/custom.yml"} {
		if _, err := os.Stat(filepath.Join(home, ".config", "zuano", name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExistingZuanoConfigWinsOverRelay(t *testing.T) {
	home := withTempHome(t)
	for _, name := range []string{"relay", "zuano"} {
		dir := filepath.Join(home, ".config", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"email":"`+name+`@example.com"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load()
	if err != nil || cfg.Email != "zuano@example.com" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "relay", "config.json")); err != nil {
		t.Fatal(err)
	}
}

func TestZuanoEnvironmentPrecedence(t *testing.T) {
	withTempHome(t)
	t.Setenv("RELAY_EMAIL", "legacy@example.com")
	t.Setenv("ZUANO_EMAIL", "new@example.com")
	t.Setenv("GATEWAY_URL", "https://legacy.example.com")
	t.Setenv("ZUANO_URL", "https://new.example.com")
	if got, err := config.ResolveEmail(); err != nil || got != "new@example.com" {
		t.Fatalf("email %q, %v", got, err)
	}
	if got, err := config.GatewayURL(); err != nil || got != "https://new.example.com" {
		t.Fatalf("url %q, %v", got, err)
	}
	t.Setenv("ZUANO_EMAIL", "")
	if got, err := config.ResolveEmail(); err != nil || got != "legacy@example.com" {
		t.Fatalf("legacy email %q, %v", got, err)
	}
}
