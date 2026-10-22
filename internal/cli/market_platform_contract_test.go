package cli

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The platform moderation routes are staff-only, so the CLI calls none of
// them; the document still lists them and a refusal they write must reach the
// user with the gateway's sentence and the generic exit status.
func TestContract_PlatformMarketOperationsAreNotCalledByTheCLI(t *testing.T) {
	doc := loadOpenAPI(t)
	platform := 0
	for path, methods := range doc.Paths {
		if !strings.HasPrefix(path, "/platform/market") {
			continue
		}
		for _, op := range methods {
			if strings.HasPrefix(op.ID, "platform.market.") {
				platform++
			}
		}
	}
	if platform != 12 {
		t.Errorf("the OpenAPI document lists %d platform market operations, want 12", platform)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "/platform/market") {
			t.Errorf("%s calls a platform market route; give it a contract test against its operation", f)
		}
	}
}

func TestContract_SubmissionRefusalsKeepTheirCodeAndExitGenerically(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{404, "submission_not_found"},
		{409, "submission_not_decidable"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := submitGateway(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":"`+tc.code+`","message":"the submission cannot be used"}`)
			})
			_, err := runCommand(t, MarketCmd(), "submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", f.listing, "--key-file", f.keyFile)
			if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "the submission cannot be used") {
				t.Fatalf("exit %d (%v), want 1 with the gateway's sentence", exitCodeOf(err), err)
			}
			if _, mapped := refusalExit[tc.code]; mapped {
				t.Errorf("%s has an exit status of its own; the registry lists none", tc.code)
			}
		})
	}
}

func TestContract_PlatformDecideDocumentsTheSubmissionRefusals(t *testing.T) {
	raw, err := os.ReadFile(vendoredOpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"submission_not_found", "submission_not_decidable"} {
		if !strings.Contains(string(raw), `"`+code+`"`) {
			t.Errorf("the vendored OpenAPI document lacks %s", code)
		}
	}
}
