package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// pendingConnectionsPath lists the upstreams the signed-in person has not
// connected their own account to yet.
const pendingConnectionsPath = "/api/fw/oauth/pending"

// handleConnectElicitation sends a person to the page that connects their own
// account to an upstream server, waits until the gateway no longer lists the
// server as unconnected and runs the call once more. Without a person at the
// keyboard it opens nothing and waits only when --wait asks it to.
func handleConnectElicitation(parent context.Context, c *client.Client, e *urlElicitation, page *url.URL, sameOrigin bool, retry func() (*heldCall, error), jsonOut bool, o callOpts, out io.Writer) error {
	interactive := o.interactive && !jsonOut
	if !jsonOut {
		fmt.Fprintf(out, "Account not connected: %s\n", textsafe.Clean(firstNonEmptyString(e.Message, "connect your account to use this server")))
		fmt.Fprintf(out, "  %s\n  host: %s\n", highlightHost(page, colorEnabled(out)), textsafe.Clean(page.Host))
		if !sameOrigin {
			fmt.Fprintf(out, "This page is not on your gateway (%s); zuano will not open it. Check the host before you visit it.\n", textsafe.Clean(c.GatewayURL))
		}
	}
	if interactive && sameOrigin && !o.noBrowser && confirm(o.in, out, "Open it in your browser? [Y/n] ") {
		if err := openBrowserURL(page.String()); err != nil {
			fmt.Fprintf(out, "Could not open a browser (%v); open the link above.\n", err)
		}
	}

	total := o.waitFor
	switch {
	case o.wait:
	case interactive:
		total = time.Duration(defaultWaitSeconds) * time.Second
	default:
		if !jsonOut {
			fmt.Fprintln(out, "Connect it at the link, then repeat the call.")
		}
		return &ExitError{Code: exitAccountNotConnected}
	}
	if !jsonOut {
		fmt.Fprintf(out, "Waiting up to %s for the connection...\n", total)
	}
	connected, err := waitForConnection(parent, c, e.Upstream, total)
	if err != nil {
		return err
	}
	if !connected {
		return &ExitError{Code: exitAccountNotConnected, Msg: "the account was not connected in time"}
	}
	if !jsonOut {
		fmt.Fprintln(out, "Connected. Running the call again.")
	}
	again, err := retry()
	if err != nil {
		return err
	}
	if again != nil {
		return &ExitError{Code: exitPending, Msg: "the call was held again; nothing more was run"}
	}
	return nil
}

// waitForConnection polls the gateway until the upstream is no longer among
// the connections still to make, or the time is up.
func waitForConnection(parent context.Context, c *client.Client, upstream string, total time.Duration) (bool, error) {
	deadline := time.Now().Add(total)
	for {
		var doc struct {
			Connections []struct {
				Slug string `json:"slug"`
			} `json:"connections"`
		}
		status, raw, err := approvalsDo(parent, c, controlRequest, http.MethodGet, pendingConnectionsPath, nil)
		if err != nil {
			return false, err
		}
		if status != http.StatusOK {
			return false, gatewayStatusError(status, raw)
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return false, fmt.Errorf("decode %s: %w", pendingConnectionsPath, err)
		}
		slugs := make([]string, len(doc.Connections))
		for i, conn := range doc.Connections {
			slugs[i] = conn.Slug
		}
		if !slices.Contains(slugs, upstream) {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-parent.Done():
			return false, parent.Err()
		case <-time.After(approvalPollInterval):
		}
	}
}
