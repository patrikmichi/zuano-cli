package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/client"
)

func TestWhoami_ReadsTheOrganisationTheCredentialActsIn(t *testing.T) {
	withTempHome(t)
	for name, tc := range map[string]struct{ body, want string }{
		"an organisation": {`{"email":"a@example.com","googleSub":null,"orgId":"org_1","services":[],"issuedAt":"t"}`, "org_1"},
		"none":            {`{"email":"a@example.com","googleSub":null,"orgId":null,"services":[],"issuedAt":"t"}`, ""},
		"not sent":        {`{"email":"a@example.com","services":[],"issuedAt":"t"}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			resp, err := Whoami(context.Background(), client.New(srv.URL, "static-key"), false)
			if err != nil {
				t.Fatal(err)
			}
			if resp.OrgID != tc.want {
				t.Errorf("OrgID = %q, want %q", resp.OrgID, tc.want)
			}
		})
	}
}
