package client

import (
	"fmt"
	"net/http"
	"regexp"
	"sync/atomic"
)

// ClientOrgHeader names the client organisation an agency person acts in. The
// gateway allows it only when the agency has an active link to that client and
// the person holds a delegated role there.
const ClientOrgHeader = "X-Zuano-Client-Org"

var (
	clientOrgPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,99}$`)
	actingClientOrg  atomic.Pointer[string]
)

// SetActingClient makes every request carry ClientOrgHeader for the client
// organisation; an empty id stops it.
func SetActingClient(orgID string) error {
	if orgID == "" {
		actingClientOrg.Store(nil)
		return nil
	}
	if !clientOrgPattern.MatchString(orgID) {
		return fmt.Errorf("%q is not a client organisation id", orgID)
	}
	actingClientOrg.Store(&orgID)
	return nil
}

// ActingClient is the client organisation requests are sent for, if any.
func ActingClient() string {
	if p := actingClientOrg.Load(); p != nil {
		return *p
	}
	return ""
}

func addActingClient(req *http.Request) {
	if org := ActingClient(); org != "" && req.Header.Get(ClientOrgHeader) == "" {
		req.Header.Set(ClientOrgHeader, org)
	}
}
