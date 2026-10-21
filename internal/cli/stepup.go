package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/config"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// The prompt streams and the terminal check are variables so tests can
// replace them.
var (
	stepUpOut = io.Writer(os.Stderr)
	stepUpIn  = io.Reader(os.Stdin)
	// stepUpInteractive is true when a person can be asked to confirm in the
	// console: not an agent session and a terminal on stdin.
	stepUpInteractive = func() bool { return !config.AgentMode() && canPrompt(os.Stdin) }
)

func init() { client.SetDefaultStepUpHandler(completeStepUp) }

// completeStepUp asks the person to confirm the action with a passkey in the
// console. The passkey works only in a signed-in browser, so the CLI cannot do
// it: it names the action, points to the console and, once the person says it
// is done, lets the same request go out again. Without a person at the keyboard
// it stops with exit status 14.
func completeStepUp(_ context.Context, ch client.StepUpChallenge, _ *client.Client) error {
	if !stepUpInteractive() {
		return &client.StepUpRequiredError{Action: ch.Action, Digest: ch.Digest}
	}
	link, err := gatewayLink(ch.Gateway, "/")
	if err != nil {
		return err
	}
	what := "This action"
	if ch.Action != "" {
		what = fmt.Sprintf("The action %q", textsafe.Clean(ch.Action))
	}
	fmt.Fprintf(stepUpOut, "%s needs your confirmation with a passkey, which only a signed-in browser can give.\n", what)
	presentLink(stepUpOut, link, true, "Confirm it in the console")
	fmt.Fprint(stepUpOut, "Press Enter once you have confirmed it to try again, or Ctrl-C to cancel: ")
	if _, err := bufio.NewReader(stepUpIn).ReadString('\n'); err != nil {
		return &client.StepUpRequiredError{Action: ch.Action, Digest: ch.Digest}
	}
	return nil
}
