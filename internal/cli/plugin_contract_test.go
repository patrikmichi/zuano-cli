package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/plugins"
)

func decodeFixture(t *testing.T, name string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(contractFixture(t, name)), into); err != nil {
		t.Fatalf("%s does not decode: %v", name, err)
	}
}

func TestContract_PluginFeed(t *testing.T) {
	var f pluginFeed
	decodeFixture(t, "plugin-feed.json", &f)
	if f.Slug != "acme" || f.NPMScope != "acme" || f.GitMirror || !strings.HasPrefix(f.BaseURL, "https://") || !strings.Contains(f.BaseURL, "/m/") || len(f.Channels) != 2 {
		t.Errorf("feed = %+v", f)
	}
}

func TestContract_PluginMarketplacesPointAtThePackagesTheyListed(t *testing.T) {
	var pk struct {
		Packages []struct {
			Platform string `json:"platform"`
			State    string `json:"state"`
			SHA256   string `json:"sha256"`
			URL      string `json:"url"`
		} `json:"packages"`
	}
	decodeFixture(t, "plugin-packages.json", &pk)
	byPlatform := map[string][2]string{}
	for _, p := range pk.Packages {
		if p.State != "ready" || len(p.SHA256) != 64 {
			t.Errorf("package %+v", p)
		}
		byPlatform[p.Platform] = [2]string{p.URL, p.SHA256}
	}
	for file, platform := range map[string]string{"plugin-claude-marketplace.json": "claude", "plugin-agent-plugins-marketplace.json": "agent_plugins"} {
		m, err := plugins.ParseMarketplace([]byte(contractFixture(t, file)))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		e, err := m.Find("sales-kit", "1.2.0")
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if want := byPlatform[platform]; e.Source.URL != want[0] || e.Source.SHA256 != want[1] || e.Ext() != ".zip" {
			t.Errorf("%s: entry %+v, package %v", file, e, want)
		}
	}
}

func TestContract_PluginSignatureAndInstallDocuments(t *testing.T) {
	var sig plugins.Signature
	decodeFixture(t, "plugin-signature.json", &sig)
	if sig.Schema != "plugin-signature/1" || sig.Alg != "ed25519" || sig.KID == "" || sig.Signature == "" || sig.Plugin != "sales-kit" || sig.Version != "1.2.0" || sig.Platform != "claude" || len(sig.Files) != 2 {
		t.Errorf("signature = %+v", sig)
	}
	var report struct {
		Install struct {
			ID string `json:"id"`
		} `json:"install"`
	}
	decodeFixture(t, "plugin-install-report.json", &report)
	if report.Install.ID != "pli_1" {
		t.Errorf("install report = %+v", report)
	}
	var installs struct {
		CurrentVersion string         `json:"current_version"`
		Total          int            `json:"total"`
		Outdated       int            `json:"outdated"`
		ByAgent        map[string]int `json:"by_agent"`
	}
	decodeFixture(t, "plugin-installs.json", &installs)
	if installs.CurrentVersion != "1.2.0" || installs.Total != 1 || installs.ByAgent["claude"] != 1 {
		t.Errorf("installs = %+v", installs)
	}
}

func TestContract_PluginDescribeAndPackument(t *testing.T) {
	var d struct {
		Plugin struct {
			Slug string `json:"slug"`
		} `json:"plugin"`
		Version struct {
			Semver  string `json:"semver"`
			Channel string `json:"channel"`
		} `json:"version"`
		Packages []struct {
			Platform string `json:"platform"`
			State    string `json:"state"`
		} `json:"packages"`
	}
	decodeFixture(t, "plugin-describe.json", &d)
	if d.Plugin.Slug != "sales-kit" || d.Version.Semver != "1.2.0" || d.Version.Channel != "stable" || len(d.Packages) != 5 {
		t.Errorf("describe = %+v", d)
	}
	var p struct {
		Name     string            `json:"name"`
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct {
				Tarball   string `json:"tarball"`
				Integrity string `json:"integrity"`
				Shasum    string `json:"shasum"`
			} `json:"dist"`
		} `json:"versions"`
	}
	decodeFixture(t, "plugin-npm-packument.json", &p)
	v := p.Versions[p.DistTags["latest"]]
	if p.Name != "@acme/sales-kit" || !strings.HasSuffix(v.Dist.Tarball, ".tgz") || !strings.HasPrefix(v.Dist.Integrity, "sha512-") || len(v.Dist.Shasum) != 40 {
		t.Errorf("packument = %+v", p)
	}
}
