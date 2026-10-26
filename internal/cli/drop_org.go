package cli

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// exitScanPending is the status of a release whose data protection scan has
// not finished, so it cannot go live or public yet: retry shortly.
const exitScanPending = 47

// artifactRefusals gives the refusals of the artifact routes the status they
// have there: a revision that moved on (the sharing or the content changed
// since the one the request names) ends like any other stale write, and a
// pending scan has its own.
var artifactRefusals = map[string]int{
	"revision_conflict": exitDraftConflict,
	"scan_pending":      exitScanPending,
}

var orgLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,58}[a-z0-9])?$`)

// orgSitesDoc mirrors the organisation's settings for sites.
type orgSitesDoc struct {
	Enabled       bool    `json:"enabled"`
	Label         *string `json:"label"`
	PublicSharing bool    `json:"public_sharing"`
	AllowRemote   bool    `json:"allow_remote"`
	StorageBytes  int64   `json:"storage_bytes"`
	StorageLimit  *int64  `json:"storage_limit"`
}

func dropOrgCmd() *cobra.Command {
	var f approvalFlags
	var enabled, publicSharing, allowRemote bool
	var label string
	var yes bool
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Show or change the organisation's settings for sites (admins)",
		Long: `Without flags, prints whether sites are on, the label in every site address, whether
sites may be made public, whether sites may load remote content, and the storage in use.

--label sets the label that ends up in every site address
(<site>--<label>.<domain>). It is set once and final, and is confirmed with a
passkey in the console: in a session without a terminal the command stops with
exit 14 and points there. Turning public sharing on or off and the other
settings are changed with the flags; each flag takes true or false.`,
		Example: `  zuano drop org
  zuano drop org --enabled=true --label acme
  zuano drop org --public-sharing=false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			body := map[string]any{}
			flags := cmd.Flags()
			if flags.Changed("enabled") {
				body["enabled"] = enabled
			}
			if flags.Changed("public-sharing") {
				body["public_sharing"] = publicSharing
			}
			if flags.Changed("allow-remote") {
				body["allow_remote"] = allowRemote
			}
			if flags.Changed("label") {
				label = strings.ToLower(strings.TrimSpace(label))
				if !orgLabelPattern.MatchString(label) {
					return fmt.Errorf("invalid --label %q: use 1 to 60 lower-case letters, digits and single hyphens", textsafe.Clean(label))
				}
				body["label"] = label
			}
			var doc orgSitesDoc
			var raw []byte
			if len(body) == 0 {
				raw, err = gatewayJSON(cmd.Context(), c, http.MethodGet, "/api/artifacts/org", nil, &doc)
			} else {
				if _, ok := body["label"]; ok {
					if err := confirmOrYes(cmd, yes, fmt.Sprintf("Set the label to %q? It appears in every site address and cannot be changed afterwards.", label)); err != nil {
						return err
					}
				}
				raw, err = gatewayWrite(cmd, c, f.jsonOut, http.MethodPut, "/api/artifacts/org", body, &doc)
			}
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			printOrgSites(cmd, doc)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "Turn sites on or off for the organisation")
	cmd.Flags().StringVar(&label, "label", "", "The label in every site address (set once)")
	cmd.Flags().BoolVar(&publicSharing, "public-sharing", false, "Let sites be made public")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false, "Let sites load remote content")
	cmd.Flags().BoolVar(&yes, "yes", false, "Do not ask before setting the label")
	return cmd
}

func printOrgSites(cmd *cobra.Command, d orgSitesDoc) {
	out := cmd.OutOrStdout()
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	label := "not set"
	if d.Label != nil && *d.Label != "" {
		label = textsafe.Clean(*d.Label)
	}
	fmt.Fprintf(out, "Sites:          %s\nLabel:          %s\nPublic sharing: %s\nRemote content: %s\n", onOff(d.Enabled), label, onOff(d.PublicSharing), onOff(d.AllowRemote))
	storage := fmt.Sprintf("%d bytes", d.StorageBytes)
	if d.StorageLimit != nil {
		storage += fmt.Sprintf(" of %d", *d.StorageLimit)
	}
	fmt.Fprintf(out, "Storage:        %s\n", storage)
}
