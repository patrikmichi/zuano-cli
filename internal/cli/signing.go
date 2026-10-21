package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/catalog"
	"github.com/patrikmichi/zuano-cli/internal/config"
	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// signingPolicy builds the verification policy for a gateway from the keys
// pinned for its origin.
func signingPolicy(gatewayURL string, require bool) (signing.Policy, error) {
	origin, err := config.NormalizeGatewayURL(gatewayURL)
	if err != nil {
		return signing.Policy{}, err
	}
	keys, err := config.PinnedSigningKeys(origin)
	if err != nil {
		return signing.Policy{}, fmt.Errorf("load config: %w", err)
	}
	return signing.Policy{Keys: keys, Require: require}, nil
}

func signingOption(gatewayURL string, require bool) (catalog.Option, error) {
	policy, err := signingPolicy(gatewayURL, require)
	if err != nil {
		return nil, err
	}
	return catalog.WithSigning(policy, os.Stderr), nil
}

// keyRing is one set of keys a gateway publishes and the client pins: the
// keys that sign downloads, or the ones that sign firewall documents.
type keyRing struct {
	// what names the ring in messages, such as "signing" or "firewall".
	what   string
	path   string
	pinned func(origin string) ([]signing.Key, error)
	pin    func(origin string, keys []signing.Key) error
	// refused says what is refused while the published keys differ.
	refused string
	// flags is the trust-key invocation that reviews the ring.
	flags string
}

var (
	downloadKeys = keyRing{
		what: "signing", path: signing.KeysPath, pinned: config.PinnedSigningKeys, pin: config.PinSigningKeys,
		refused: "Downloads signed with the new keys are refused", flags: "config trust-key",
	}
	firewallKeys = keyRing{
		what: "firewall", path: signing.PolicyKeysPath, pinned: config.PinnedFirewallKeys, pin: config.PinFirewallKeys,
		refused: "Firewall sessions and snapshots signed with the new keys are refused", flags: "config trust-key --firewall",
	}
)

func pinSigningKeys(ctx context.Context, origin string, out io.Writer) {
	downloadKeys.pinOnFirstUse(ctx, origin, out)
}

// pinOnFirstUse records the gateway's keys the first time they are seen (trust
// on first use). A gateway that publishes none yet leaves nothing pinned. Keys
// that differ from the pinned ones are never adopted here: that is a decision
// for `zuano config trust-key`.
func (r keyRing) pinOnFirstUse(ctx context.Context, origin string, out io.Writer) {
	keys, err := signing.FetchKeysAt(ctx, origin, r.path)
	if errors.Is(err, signing.ErrNoKeys) {
		return
	}
	if err != nil {
		fmt.Fprintf(out, "warning: could not read the gateway's %s keys (%v)\n", r.what, err)
		return
	}
	pinned, err := r.pinned(origin)
	if err != nil {
		fmt.Fprintf(out, "warning: could not read the pinned %s keys (%v)\n", r.what, err)
		return
	}
	switch {
	case len(pinned) == 0:
		if err := r.pin(origin, keys); err != nil {
			fmt.Fprintf(out, "warning: could not pin the gateway's %s keys (%v)\n", r.what, err)
			return
		}
		fmt.Fprintf(out, "Pinned %d %s key(s) for %s on first use:\n", len(keys), r.what, origin)
		printKeys(out, keys)
	case !signing.Same(pinned, keys):
		fmt.Fprintf(out, "warning: the %s keys %s publishes differ from the ones pinned. %s until you review them with `zuano %s`.\n", r.what, origin, r.refused, r.flags)
	}
}

func pinLoginKeys(ctx context.Context, gatewayURL string, out io.Writer) {
	if origin, err := config.NormalizeGatewayURL(gatewayURL); err == nil {
		pinSigningKeys(ctx, origin, out)
	}
}

func printKeys(out io.Writer, keys []signing.Key) {
	for _, k := range keys {
		fmt.Fprintf(out, "  %s  %s\n", textsafe.Clean(k.KID), signing.Fingerprint(k))
	}
}

func configTrustKeyCmd() *cobra.Command {
	var gatewayURL string
	var yes, firewall bool
	cmd := &cobra.Command{
		Use:   "trust-key",
		Short: "Review and pin the gateway's download-signing keys",
		Long: `Fetch the signing keys the gateway publishes at /.well-known/zuano-signing-keys,
show their fingerprints and pin them, replacing the keys pinned before.

zuano pins the keys the first time you log in. A later change is refused until
you run this, because a key that changed could mean the gateway was replaced.
Compare the fingerprints with the ones your gateway's operator publishes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseInAgentMode(brand.Cmd("config trust-key")); err != nil {
				return err
			}
			gURL, err := resolveGatewayURLOrFailClosed(gatewayURL)
			if err != nil {
				return err
			}
			origin, err := config.NormalizeGatewayURL(gURL)
			if err != nil {
				return err
			}
			ring := downloadKeys
			if firewall {
				ring = firewallKeys
			}
			keys, err := signing.FetchKeysAt(cmd.Context(), origin, ring.path)
			if errors.Is(err, signing.ErrNoKeys) {
				return fmt.Errorf("%s publishes no %s keys, so there is nothing to pin", origin, ring.what)
			}
			if err != nil {
				return err
			}
			pinned, err := ring.pinned(origin)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if signing.Same(pinned, keys) {
				fmt.Fprintf(out, "The keys pinned for %s already match:\n", origin)
				printKeys(out, keys)
				return nil
			}
			if len(pinned) > 0 {
				fmt.Fprintf(out, "Pinned now:\n")
				printKeys(out, pinned)
			}
			fmt.Fprintf(out, "%s publishes:\n", origin)
			printKeys(out, keys)
			if !yes {
				in := cmd.InOrStdin()
				if !canPrompt(in) {
					return errors.New("pinning signing keys needs an interactive terminal, or --yes after you compared the fingerprints")
				}
				if !askNo(bufio.NewReader(in), out, "Trust these keys?") {
					return errors.New("not confirmed; the pinned keys are unchanged")
				}
			}
			if err := ring.pin(origin, keys); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Fprintf(out, "Pinned %d key(s) for %s.\n", len(keys), origin)
			return nil
		},
	}
	cmd.Flags().StringVar(&gatewayURL, "gateway-url", "", "Gateway URL (default: $ZUANO_URL, then the config file)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Pin without asking (refused when ZUANO_AGENT is set)")
	cmd.Flags().BoolVar(&firewall, "firewall", false, "Pin the keys that sign firewall sessions and snapshots (/.well-known/zuano-policy-keys)")
	return cmd
}
