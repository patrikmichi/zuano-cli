package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// gatewayJSON sends one control request and decodes a 200 response into out
// (when out is not nil). It returns the raw body so --json can print it
// unchanged. A refusal the gateway client recognises (billing, quota,
// step-up) comes back as its own typed error.
func gatewayJSON(ctx context.Context, c *client.Client, method, path string, body, out any) ([]byte, error) {
	status, raw, err := approvalsDo(ctx, c, controlRequest, method, path, body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted && status != http.StatusNoContent {
		return nil, gatewayStatusError(status, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return raw, nil
}

// gatewayWrite sends a change. An agent session's write may be held for
// approval; that is reported like a held call and exits 12. A refusal with a
// code is returned as the gateway's own sentence.
func gatewayWrite(cmd *cobra.Command, c *client.Client, jsonOut bool, method, path string, body, out any) ([]byte, error) {
	status, raw, err := approvalsDo(cmd.Context(), c, controlRequest, method, path, body)
	if err != nil {
		return nil, err
	}
	if held := pendingFromREST(status, raw); held != nil {
		if jsonOut {
			printRaw(cmd.OutOrStdout(), raw)
		}
		return nil, handlePending(cmd.Context(), c, held, jsonOut, callOpts{}, cmd.OutOrStdout())
	}
	if status < 200 || status > 299 {
		return nil, gatewayStatusError(status, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return raw, nil
}

// gatewayStatusError turns a failed response into an error that carries the
// gateway's own sentence.
func gatewayStatusError(status int, raw []byte) error {
	var doc struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Issues  []struct {
			Pointer string `json:"pointer"`
			Message string `json:"message"`
		} `json:"issues"`
	}
	_ = json.Unmarshal(raw, &doc)
	msg := doc.Message
	if msg == "" {
		msg = doc.Error
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	text := fmt.Sprintf("%s (HTTP %d)%s", textsafe.Clean(msg), status, client.RequestIDNote())
	for _, i := range doc.Issues {
		text += fmt.Sprintf("\n  %s: %s", textsafe.Clean(i.Pointer), textsafe.Clean(i.Message))
	}
	if code, ok := refusalExit[doc.Error]; ok {
		return &ExitError{Code: code, Msg: text}
	}
	if status == http.StatusUnauthorized {
		return errTokenRevoked
	}
	return errors.New(text)
}

// refusalExit maps the refusals that have a status of their own to their place
// in the exit-code registry.
var refusalExit = map[string]int{
	"manifest_invalid":       exitInvalidManifest,
	"input_invalid":          exitInputInvalid,
	"draft_conflict":         exitDraftConflict,
	"signature_invalid":      signing.ExitContentSignature,
	"signature_required":     exitSignatureRequired,
	"publisher_key_missing":  exitPublisherKeyMissing,
	"publisher_not_verified": exitPublisherNotVerified,
}

// gatewayRequest sends one control request with extra headers and returns the
// status and body.
func gatewayRequest(parent context.Context, c *client.Client, method, path string, body any, headers map[string]string) (int, []byte, error) {
	ctx, cancel, err := requestContext(parent, controlRequest)
	if err != nil {
		return 0, nil, err
	}
	defer cancel()
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request body: %w", err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.GatewayURL+path, payload)
	if err != nil {
		return 0, nil, fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, timeoutCause(ctx, fmt.Errorf("%s %s: %w", method, path, err))
	}
	defer resp.Body.Close()
	raw, err := readLimitedResponse(resp.Body, maxApprovalBody)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, raw, nil
}
