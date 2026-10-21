package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/config"
	"github.com/patrikmichi/zuano-cli/internal/nofollow"
	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const (
	publisherKeyEnv      = "ZUANO_PUBLISHER_KEY_FILE"
	publisherKeyFileName = "publisher-signing-key.pem"
	maxListingBytes      = 1 << 20
	// maxScreenshotBytes keeps a screenshot, once base64 encoded, inside the gateway's field limit.
	maxScreenshotBytes = 1_000_000
	maxScreenshots     = 6
)

// publisherKeyPath is the private key the publisher signs with: the flag, then
// $ZUANO_PUBLISHER_KEY_FILE, then a file in the configuration directory.
func publisherKeyPath(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv(publisherKeyEnv); env != "" {
		return env, nil
	}
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, publisherKeyFileName), nil
}

// readRegularFile reads a file the person named, refusing a symlink, a FIFO or
// anything over max bytes.
func readRegularFile(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, nofollow.ReadFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", textsafe.Clean(path))
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", textsafe.Clean(path), max)
	}
	return raw, nil
}

func loadPublisherKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	// Windows reports no owner-only mode, so the permission check is for the other systems.
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return nil, fmt.Errorf("refusing to use %s: it must be a regular file only its owner can access", textsafe.Clean(path))
	}
	raw, err := readRegularFile(path, 8<<10)
	if err != nil {
		return nil, err
	}
	key, err := signing.ParsePublisherKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", textsafe.Clean(path), err)
	}
	return key, nil
}

const publisherAPI = marketAPI + "/publisher"

func marketPublisherCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publisher",
		Short: "The signing key a verified publisher signs its workflow versions with",
	}
	cmd.AddCommand(marketSigningKeyCmd())
	return cmd
}

func marketSigningKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "signing-key",
		Short: "Create, register or remove the Ed25519 key that signs your submissions",
		Long: `A verified publisher can register an Ed25519 public key. From then on the gateway
requires every submission to carry a signature made with the matching private
key, and stores it next to the platform's own signature. The private key stays on
this machine: 'generate' writes it to a file only you can read.`,
	}
	cmd.AddCommand(signingKeyGenerateCmd(), signingKeySetCmd(), signingKeyRemoveCmd())
	return cmd
}

func signingKeyGenerateCmd() *cobra.Command {
	var keyFile string
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Create a signing key on this machine and print its public half",
		Long: `Writes a new private key as PKCS#8 PEM, readable only by you. It does not
replace a key that exists. The file is $` + publisherKeyEnv + `, else --key-file, else
` + publisherKeyFileName + ` in the configuration directory.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := publisherKeyPath(keyFile)
			if err != nil {
				return err
			}
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			pemKey, err := signing.MarshalPublisherKey(key)
			if err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|nofollow.NoFollow, 0o600)
			if err != nil {
				if errors.Is(err, os.ErrExist) {
					return fmt.Errorf("%s already exists; remove it first if you mean to replace the key", textsafe.Clean(path))
				}
				return fmt.Errorf("store the key: %w", err)
			}
			if _, err := f.Write(pemKey); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return err
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Private key written to %s.\nPublic key: %s\nRegister it with `zuano market publisher signing-key set`.\n", textsafe.Clean(path), signing.PublisherPublicKey(key))
			return nil
		},
	}
	cmd.Flags().StringVar(&keyFile, "key-file", "", "Where to write the private key")
	return cmd
}

func signingKeySetCmd() *cobra.Command {
	var f approvalFlags
	var keyFile, publicKey string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Register or replace the publisher's signing public key",
		Long: `Registers the public key the gateway checks submissions against. Without
--public-key it is derived from the private key file. Only a verified publisher
can register a key, and only an admin of the organisation.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if publicKey != "" && keyFile != "" {
				return &ExitError{Code: exitUsage, Msg: "give --public-key or --key-file, not both"}
			}
			if publicKey == "" {
				path, err := publisherKeyPath(keyFile)
				if err != nil {
					return err
				}
				key, err := loadPublisherKey(path)
				if err != nil {
					return fmt.Errorf("%w\nCreate a key with `zuano market publisher signing-key generate`", err)
				}
				publicKey = signing.PublisherPublicKey(key)
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPut, publisherAPI+"/signing-key", map[string]any{"public_key": publicKey}, nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Registered signing key %s. Submissions must now be signed with it.\n", textsafe.Clean(publicKey))
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "Private key to derive the public key from (default: $"+publisherKeyEnv+", then the configuration directory)")
	cmd.Flags().StringVar(&publicKey, "public-key", "", "The raw 32-byte Ed25519 public key in base64url, instead of reading a private key")
	return cmd
}

func signingKeyRemoveCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove the registered signing key; submissions are then unsigned",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodDelete, publisherAPI+"/signing-key", nil, nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Signing key removed.")
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	return cmd
}

type submissionDoc struct {
	Submission struct {
		ID      string `json:"id"`
		Handle  string `json:"handle"`
		Slug    string `json:"slug"`
		Version string `json:"version"`
		Status  string `json:"status"`
		Gates   []struct {
			Gate   string `json:"gate"`
			Status string `json:"status"`
		} `json:"gates"`
		Problems        []string `json:"problems"`
		Elevated        bool     `json:"elevated"`
		ElevatedReasons []string `json:"elevated_reasons"`
	} `json:"submission"`
}

// listingScreenshots reads the screenshots a listing names, by the path the
// listing writes them with, from beside the listing file.
func listingScreenshots(dir string, listing any) (map[string]string, error) {
	doc, _ := listing.(map[string]any)
	names, _ := doc["screenshots"].([]any)
	if len(names) > maxScreenshots {
		return nil, fmt.Errorf("a listing has at most %d screenshots", maxScreenshots)
	}
	out := map[string]string{}
	for _, n := range names {
		rel, _ := n.(string)
		clean := filepath.Clean(filepath.FromSlash(rel))
		if rel == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("the screenshot %q is not a path beside the listing file", textsafe.Clean(rel))
		}
		raw, err := readRegularFile(filepath.Join(dir, clean), maxScreenshotBytes)
		if err != nil {
			return nil, fmt.Errorf("screenshot %q: %w", textsafe.Clean(rel), err)
		}
		out[rel] = base64.StdEncoding.EncodeToString(raw)
	}
	return out, nil
}

func marketSubmitCmd() *cobra.Command {
	var f approvalFlags
	var listingFile, versionID, keyFile string
	cmd := &cobra.Command{
		Use:   "submit <resource_id>@<version>",
		Short: "Offer a published version of your workflow to the marketplace review",
		Long: `Sends a published version, with the listing that describes it, to the review.
--version-id is the version id that 'zuano workflow publish' printed. The listing
file is JSON; the screenshots it names are read from beside it.

When a signing key exists (--key-file, $` + publisherKeyEnv + ` or the configuration
directory), the version is signed locally before it is sent. A publisher with a
registered key must sign: the gateway refuses an unsigned submission
(signature_required, exit ` + fmt.Sprint(exitSignatureRequired) + `) and one that does not match the key
(signature_invalid, exit 4). A signature without a registered key is refused as
publisher_key_missing (exit ` + fmt.Sprint(exitPublisherKeyMissing) + `); a publisher that is not verified yet as
publisher_not_verified (exit ` + fmt.Sprint(exitPublisherNotVerified) + `).`,
		Example: `  zuano market submit res_invoice@1.2.0 --version-id rev_9k2 --listing-file listing.json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource, version := splitAtVersion(args[0])
			if !workflowRefPattern.MatchString(resource) || version == "" {
				return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("%q is not <resource_id>@<version>", textsafe.Clean(args[0]))}
			}
			if versionID == "" || listingFile == "" {
				return &ExitError{Code: exitUsage, Msg: "--version-id and --listing-file are required"}
			}
			raw, err := readRegularFile(listingFile, maxListingBytes)
			if err != nil {
				return err
			}
			var listing any
			if err := json.Unmarshal(raw, &listing); err != nil {
				return fmt.Errorf("%s is not JSON: %w", textsafe.Clean(listingFile), err)
			}
			screenshots, err := listingScreenshots(filepath.Dir(listingFile), listing)
			if err != nil {
				return err
			}
			key, err := optionalPublisherKey(keyFile)
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			body := map[string]any{"resource_version_id": versionID, "listing": listing, "screenshots": screenshots}
			if key != nil {
				signed, err := signPublishedVersion(cmd, c, key, resource, version)
				if err != nil {
					return err
				}
				body["publisher_signature"] = signed
			}
			var doc submissionDoc
			rawDoc, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, marketAPI+"/submissions", body, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, rawDoc)
			} else {
				printSubmission(out, doc)
			}
			if doc.Submission.Status == "changes_requested" {
				return errors.New("the submission needs changes before it can be reviewed")
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	fl := cmd.Flags()
	fl.StringVar(&listingFile, "listing-file", "", "The listing as JSON; the screenshots it names are read from beside it")
	fl.StringVar(&versionID, "version-id", "", "The id of the published version (rev_...)")
	fl.StringVar(&keyFile, "key-file", "", "Private key to sign with (default: $"+publisherKeyEnv+", then the configuration directory; no key, no signature)")
	return cmd
}

// optionalPublisherKey loads the signing key when there is one. A key that was
// named on purpose must load; the default location may simply be empty.
func optionalPublisherKey(flag string) (ed25519.PrivateKey, error) {
	path, err := publisherKeyPath(flag)
	if err != nil {
		return nil, err
	}
	key, err := loadPublisherKey(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && flag == "" && os.Getenv(publisherKeyEnv) == "" {
			return nil, nil
		}
		return nil, err
	}
	return key, nil
}

// signPublishedVersion signs the version as the gateway stored it: the
// manifest and its digest come from the gateway, so both sides sign the same bytes.
func signPublishedVersion(cmd *cobra.Command, c *client.Client, key ed25519.PrivateKey, resource, version string) (string, error) {
	var pub struct {
		Publisher struct {
			Handle string `json:"handle"`
		} `json:"publisher"`
	}
	if _, err := gatewayJSON(cmd.Context(), c, http.MethodGet, publisherAPI, nil, &pub); err != nil {
		return "", err
	}
	var tpl struct {
		Slug     string `json:"slug"`
		Version  string `json:"version"`
		Manifest any    `json:"manifest"`
		Analysis struct {
			ManifestDigest string `json:"manifestDigest"`
		} `json:"analysis"`
	}
	path := workflowPath("templates", resource) + "?" + url.Values{"version": {version}}.Encode()
	if _, err := gatewayJSON(cmd.Context(), c, http.MethodGet, path, nil, &tpl); err != nil {
		return "", err
	}
	if tpl.Version != version || tpl.Slug == "" || tpl.Manifest == nil || tpl.Analysis.ManifestDigest == "" || pub.Publisher.Handle == "" {
		return "", fmt.Errorf("the gateway did not describe version %s of %s well enough to sign it", textsafe.Clean(version), textsafe.Clean(resource))
	}
	return signing.SignPublisherVersion(key, signing.PublisherVersion{Handle: pub.Publisher.Handle, Slug: tpl.Slug, Version: tpl.Version, ContentHash: tpl.Analysis.ManifestDigest, Manifest: tpl.Manifest})
}

func printSubmission(out io.Writer, doc submissionDoc) {
	s := doc.Submission
	fmt.Fprintf(out, "Submitted %s/%s@%s: %s (id %s)\n", textsafe.Clean(s.Handle), textsafe.Clean(s.Slug), textsafe.Clean(s.Version), textsafe.Clean(s.Status), textsafe.Clean(s.ID))
	if len(s.Gates) > 0 {
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "GATE\tSTATUS")
		for _, g := range s.Gates {
			fmt.Fprintf(tw, "%s\t%s\n", textsafe.Clean(g.Gate), textsafe.Clean(g.Status))
		}
		_ = tw.Flush()
	}
	if s.Elevated {
		fmt.Fprintf(out, "Needs a closer review: %s\n", orDash(textsafe.Clean(strings.Join(s.ElevatedReasons, ", "))))
	}
	for _, p := range s.Problems {
		fmt.Fprintf(out, "  - %s\n", textsafe.Clean(p))
	}
}
