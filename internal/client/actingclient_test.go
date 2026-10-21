package client_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/client"
)

func TestActingClientHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(client.ClientOrgHeader)
	}))
	defer srv.Close()
	t.Cleanup(func() { _ = client.SetActingClient("") })

	send := func() {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := client.New(srv.URL, "tok").Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	send()
	if got != "" {
		t.Fatalf("header sent without an acting client: %q", got)
	}
	if err := client.SetActingClient("org_8f2c"); err != nil {
		t.Fatal(err)
	}
	send()
	if got != "org_8f2c" {
		t.Fatalf("header = %q", got)
	}
	if err := client.SetActingClient("bad id\r\nX: y"); err == nil {
		t.Fatal("accepted an invalid client id")
	}
}
