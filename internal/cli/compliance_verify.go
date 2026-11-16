package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/evidence"
	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// Exit statuses of `zuano compliance verify`.
const (
	exitBundleInvalid = 45
	exitKeysMissing   = 46
)

func complianceVerifyCmd() *cobra.Command {
	var gatewayURL, keysFile string
	var maxBytes int64
	var requireCheckpoints bool
	cmd := &cobra.Command{
		Use:   "verify <volume.zip>... [--keys keys.json]",
		Short: "Check an export bundle offline: signature, file hashes and the audit checkpoints",
		Long: `Needs no login. Give every volume of the bundle (a bundle over 2 GiB is split
into numbered archives; the manifest is in the last one). Checks the manifest
signature, that every file matches the size, hash and volume the signed manifest
lists and that nothing else is in the archives, then recomputes the Merkle root
of every audit hour from the exported rows and checks it and its signature
against the hour's checkpoint. Archives are read in place and never extracted.
A bundle that does not check out exits 45 and names each file or audit hour that
failed; it never prints the bundle's content.

The manifest is signed with an evidence key, and that key must come from a
source you trust, never from the bundle: --keys (a file you obtained from the
gateway's operator by another route, the stricter way) or the keys the
configured gateway publishes over https. --keys holds {"keys": [...]} or
{"evidence": [...], "audit": [...]}. The audit checkpoint keys are not published
by the gateway; they are read from the bundle's key file, which the signed
manifest covers by hash, unless --keys gives "audit" keys.

An audit hour with rows and no signed checkpoint is not verified. It is listed as
a warning, and --require-checkpoints makes it a failure.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, source, err := verifyKeys(cmd, gatewayURL, keysFile)
			if err != nil {
				return &ExitError{Code: exitKeysMissing, Msg: fmt.Sprintf("the public keys could not be obtained: %v", err)}
			}
			res, err := evidence.Verify(args, keys, evidence.Options{MaxBytes: maxBytes, RequireCheckpoints: requireCheckpoints})
			if err != nil {
				return &ExitError{Code: exitBundleInvalid, Msg: textsafe.Clean(err.Error())}
			}
			out := cmd.OutOrStdout()
			if !res.OK() {
				for _, f := range res.Failures {
					fmt.Fprintf(out, "  %s\n", textsafe.Clean(f.String()))
				}
				if res.Dropped > 0 {
					fmt.Fprintf(out, "  and %d more\n", res.Dropped)
				}
				return &ExitError{Code: exitBundleInvalid, Msg: fmt.Sprintf("the bundle is not valid (%d problem(s))", len(res.Failures)+res.Dropped)}
			}
			if n := len(res.UnsignedHours); n > 0 {
				shown := res.UnsignedHours
				if len(shown) > 5 {
					shown = shown[:5]
				}
				fmt.Fprintf(out, "warning: %d audit hour(s) have rows and no signed checkpoint, so they are not verified (%s)\n", n, textsafe.Clean(strings.Join(shown, ", ")))
			}
			m := res.Manifest
			fmt.Fprintf(out, "Valid: %s pack for %s, %s to %s: %d files in %d volume(s), %d audit rows in %d hours, %d hours checked against signed checkpoints.\n",
				textsafe.Clean(m.Pack.ID), textsafe.Clean(m.Org.Slug), textsafe.Clean(m.Range.From), textsafe.Clean(m.Range.To), res.Files, res.Volumes, res.Events, res.Hours, res.VerifiedHours)
			fmt.Fprintf(out, "The manifest signature was checked with the evidence keys %s.\n", source)
			return nil
		},
	}
	cmd.Flags().StringVar(&keysFile, "keys", "", "A file of public keys to check against, instead of the gateway's published ones")
	cmd.Flags().StringVar(&gatewayURL, "gateway-url", "", "Gateway URL for its published keys (default: $ZUANO_URL, then the config file)")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", evidence.DefaultMaxBytes, "Refuse a bundle whose files add up to more than this many bytes")
	cmd.Flags().BoolVar(&requireCheckpoints, "require-checkpoints", false, "Fail when an audit hour with rows has no signed checkpoint")
	return cmd
}

// verifyKeys returns the keys to check a bundle with and where the evidence
// keys came from. The gateway publishes the evidence keys only; the audit keys
// stay empty so the verifier reads them from the bundle's signed key file.
func verifyKeys(cmd *cobra.Command, gatewayURL, keysFile string) (evidence.Keys, string, error) {
	if keysFile != "" {
		raw, err := os.ReadFile(keysFile)
		if err != nil {
			return evidence.Keys{}, "", err
		}
		var doc struct {
			Keys     []signing.Key `json:"keys"`
			Evidence []signing.Key `json:"evidence"`
			Audit    []signing.Key `json:"audit"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return evidence.Keys{}, "", fmt.Errorf("%s is not a key document: %w", textsafe.Clean(keysFile), err)
		}
		k := evidence.Keys{Evidence: append(doc.Evidence, doc.Keys...), Audit: doc.Audit}
		if len(k.Evidence) == 0 {
			return k, "", fmt.Errorf("%s holds no evidence keys", textsafe.Clean(keysFile))
		}
		return k, "in " + textsafe.Clean(keysFile), nil
	}
	gw, err := resolveGatewayURLOrFailClosed(gatewayURL)
	if err != nil {
		return evidence.Keys{}, "", fmt.Errorf("%v; pass --keys to check offline", err)
	}
	ev, err := signing.FetchKeysAt(cmd.Context(), gw, "/.well-known/"+brand.Name+"-evidence-keys")
	if err != nil {
		return evidence.Keys{}, "", err
	}
	return evidence.Keys{Evidence: ev}, "published by " + textsafe.Clean(gw), nil
}
