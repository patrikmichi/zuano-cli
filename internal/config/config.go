// Package config provides persistent CLI configuration stored as JSON.
// Config file: ~/.config/zuano/config.json (auto-migrated from ~/.config/relay or ~/.config/gw)
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all persisted CLI settings.
type Config struct {
	GatewayURL string `json:"gatewayUrl,omitempty"`
	// Email is the identity of the last successful `zuano login` (or
	// `zuano login --device`), persisted so subsequent commands don't
	// require the ZUANO_EMAIL environment variable to find the right
	// keychain entry. ZUANO_EMAIL, when set, still takes precedence — see
	// ResolveEmail — so multi-account users can override without
	// re-running `zuano login`.
	Email string `json:"email,omitempty"`
}

// Dir returns ~/.config/zuano, creating it if necessary.
// Existing Relay or gw configuration is migrated only when Zuano has none.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "zuano")
	if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
		for _, name := range []string{"relay", "gw"} {
			legacy := filepath.Join(home, ".config", name)
			if _, err := os.Stat(legacy); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return "", fmt.Errorf("inspect legacy config: %w", err)
			}
			if err := os.Rename(legacy, dir); err != nil {
				return "", fmt.Errorf("migrate %s to %s: %w", legacy, dir, err)
			}
			break
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir %s: %w", dir, err)
	}
	return dir, nil
}

// configPath returns the path to the config file.
func configPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file. Returns a zero-value Config if the file does not exist.
func Load() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config file %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config to disk atomically (write temp file, rename).
func Save(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	raw = append(raw, '\n')

	// Write to a temp file alongside the target for atomic rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write config tmp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename config file: %w", err)
	}
	return nil
}

// GatewayURL returns the gateway URL from the GATEWAY_URL env var, else the
// config file. No gateway URL is compiled into the binary: with neither set
// it returns "" and callers must fail closed (see
// internal/cli/gateway.go's resolveGatewayURLOrFailClosed).
func GatewayURL() (string, error) {
	if v := os.Getenv("ZUANO_URL"); v != "" {
		return v, nil
	}
	if v := os.Getenv("GATEWAY_URL"); v != "" {
		return v, nil
	}
	cfg, err := Load()
	if err != nil {
		return "", nil // degrade gracefully: an unreadable config means no gateway
	}
	return cfg.GatewayURL, nil
}

// SetEmail persists the given email as the current logged-in identity,
// preserving any other existing config fields. Called by `zuano login` /
// `zuano login --device` on success so later commands don't require
// ZUANO_EMAIL to be set.
func SetEmail(email string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	cfg.Email = email
	return Save(cfg)
}

// ResolveEmail resolves the current CLI identity used to look up the
// keychain OAuth session.
//
// Resolution order:
//  1. ZUANO_EMAIL env var — explicit override (e.g. switching accounts
//     without re-running `zuano login`, or scripting against a specific
//     identity). Always wins when set.
//  2. The email persisted by the most recent successful `zuano login` /
//     `zuano login --device` (see SetEmail).
//
// Returns an error (mentioning `zuano login`) when neither is available.
func ResolveEmail() (string, error) {
	if v := os.Getenv("ZUANO_EMAIL"); v != "" {
		return v, nil
	}
	if v := os.Getenv("RELAY_EMAIL"); v != "" {
		return v, nil
	}
	cfg, err := Load()
	if err != nil {
		return "", err
	}
	if cfg.Email == "" {
		return "", errors.New("not logged in — run `zuano login` first, or set ZUANO_EMAIL")
	}
	return cfg.Email, nil
}
