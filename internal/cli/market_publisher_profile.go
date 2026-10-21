package cli

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

type publisherDoc struct {
	Handle       string `json:"handle"`
	DisplayName  string `json:"display_name"`
	Tier         string `json:"tier"`
	SupportEmail string `json:"support_email"`
	Country      string `json:"country"`
	Suspended    bool   `json:"suspended"`
}

type publisherStanding struct {
	EmailVerified  bool    `json:"email_verified"`
	DomainVerified bool    `json:"domain_verified"`
	Domain         *string `json:"domain"`
	MaySubmit      bool    `json:"may_submit"`
}

func printStanding(out io.Writer, s publisherStanding) {
	domain := "no"
	if s.DomainVerified {
		domain = "yes"
		if s.Domain != nil {
			domain += " (" + textsafe.Clean(*s.Domain) + ")"
		}
	}
	fmt.Fprintf(out, "Email verified:  %s\nDomain verified: %s\nMay submit free listings: %s\n", yesNo(s.EmailVerified), domain, yesNo(s.MaySubmit))
}

func publisherInitCmd() *cobra.Command {
	var f approvalFlags
	var handle, name, email, country, website, supportURL, privacyURL, bio string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create your organisation's publisher profile",
		Long: `Creates the profile your listings appear under. It starts as a community
publisher; verify the support email and a domain with 'verify' before you submit.
The handle is 3 to 40 lowercase letters, digits and single hyphens. A handle that
is taken or reserved is refused (exit ` + strconv.Itoa(exitHandleUnavailable) + `). Only an admin of the organisation can
create it, and the plan must include publishing.`,
		Example: `  zuano publisher init --handle acme --name "Acme Finance" --support-email help@acme.example --country CZ`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if handle == "" || name == "" || email == "" || country == "" {
				return &ExitError{Code: exitUsage, Msg: "required: --handle, --name, --support-email and --country"}
			}
			body := map[string]any{"handle": handle, "display_name": name, "support_email": email, "country": country}
			for k, v := range map[string]string{"website": website, "support_url": supportURL, "privacy_url": privacyURL, "bio": bio} {
				if v != "" {
					body[k] = v
				}
			}
			return marketDo(cmd, f, http.MethodPost, publisherAPI, body, nil, func(out io.Writer, d struct {
				Publisher publisherDoc `json:"publisher"`
			}) {
				fmt.Fprintf(out, "Created publisher %s (%s).\nNext: `zuano publisher verify email`, then `zuano publisher verify domain <domain>`.\n", textsafe.Clean(d.Publisher.Handle), textsafe.Clean(d.Publisher.Tier))
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	fl := cmd.Flags()
	fl.StringVar(&handle, "handle", "", "The publisher handle, as in handle/slug")
	fl.StringVar(&name, "name", "", "The name shown on the listing")
	fl.StringVar(&email, "support-email", "", "Where customers and reviewers reach you; it is verified by mail")
	fl.StringVar(&country, "country", "", "Two-letter country code")
	fl.StringVar(&website, "website", "", "An https link to your site")
	fl.StringVar(&supportURL, "support-url", "", "An https link to your support page")
	fl.StringVar(&privacyURL, "privacy-url", "", "An https link to your privacy policy")
	fl.StringVar(&bio, "bio", "", "A short description, at most 1000 characters")
	return cmd
}

func publisherStatusCmd() *cobra.Command {
	var f approvalFlags
	var listings bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Your publisher profile and which proofs it has given",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listings {
				return marketDo(cmd, f, http.MethodGet, publisherAPI+"/listings", nil, nil, func(out io.Writer, d struct {
					Listings []map[string]any `json:"listings"`
				}) {
					if len(d.Listings) == 0 {
						fmt.Fprintln(out, "No listings.")
						return
					}
					for _, l := range d.Listings {
						fmt.Fprintf(out, "%s/%s\n", textsafe.Clean(fmt.Sprint(l["handle"])), textsafe.Clean(fmt.Sprint(l["slug"])))
					}
				})
			}
			return marketDo(cmd, f, http.MethodGet, publisherAPI, nil, nil, func(out io.Writer, d struct {
				Publisher publisherDoc      `json:"publisher"`
				Standing  publisherStanding `json:"standing"`
			}) {
				p := d.Publisher
				fmt.Fprintf(out, "Publisher: %s (%s), tier %s, %s\n", textsafe.Clean(p.Handle), textsafe.Clean(p.DisplayName), textsafe.Clean(p.Tier), textsafe.Clean(p.Country))
				fmt.Fprintf(out, "Support:   %s\n", textsafe.Clean(p.SupportEmail))
				if p.Suspended {
					fmt.Fprintln(out, "Suspended: yes; the platform has paused this publisher.")
				}
				printStanding(out, d.Standing)
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().BoolVar(&listings, "listings", false, "List the publisher's listings instead (admins)")
	return cmd
}

func publisherVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Prove you receive mail for the support address and control a domain",
		Long: `A publisher may submit free listings once both proofs are in. A wrong or
expired code, or a domain check that finds nothing yet, ends with exit ` + strconv.Itoa(exitVerificationFailed) + `.`,
	}
	cmd.AddCommand(verifyEmailCmd(), verifyDomainCmd())
	return cmd
}

func verifyEmailCmd() *cobra.Command {
	var f approvalFlags
	var code string
	cmd := &cobra.Command{
		Use:   "email",
		Short: "Mail a one-time code to the support address, or confirm the code with --code",
		Long: `Without --code, mails a code to the support address; an earlier unused code stops
working and one request a minute is allowed. The code works for 24 hours. With
--code, confirms it; ten wrong tries end the attempt.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if code == "" {
				return marketDo(cmd, f, http.MethodPost, publisherAPI+"/verifications/email", nil, nil, func(out io.Writer, d struct {
					SentTo string `json:"sent_to"`
				}) {
					fmt.Fprintf(out, "Sent a code to %s. Confirm it with `zuano publisher verify email --code <code>`.\n", textsafe.Clean(d.SentTo))
				})
			}
			return marketDo(cmd, f, http.MethodPost, publisherAPI+"/verifications/email/confirm", map[string]any{"token": code}, verificationRefusals, func(out io.Writer, d struct {
				Standing publisherStanding `json:"standing"`
			}) {
				fmt.Fprintln(out, "Email confirmed.")
				printStanding(out, d.Standing)
			})
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringVar(&code, "code", "", "The code from the mail")
	return cmd
}

func verifyDomainCmd() *cobra.Command {
	var f approvalFlags
	var check bool
	cmd := &cobra.Command{
		Use:   "domain [<domain>]",
		Short: "Start proving control of a domain, or look for the proof with --check",
		Long: `With a domain, prints the DNS TXT record or the well-known file to publish; an
earlier unfinished attempt stops working. With --check, looks for either and
confirms the domain when one is found. An attempt expires after 7 days.`,
		Example: `  zuano publisher verify domain acme.example
  zuano publisher verify domain --check`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if check == (len(args) == 1) {
				return &ExitError{Code: exitUsage, Msg: "give a domain to start, or --check to look for the proof"}
			}
			if !check {
				return marketDo(cmd, f, http.MethodPost, publisherAPI+"/verifications/domain", map[string]any{"domain": args[0]}, nil, func(out io.Writer, d struct {
					Domain string `json:"domain"`
					DNS    struct {
						Type  string `json:"type"`
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"dns"`
					WellKnown struct {
						URL  string `json:"url"`
						Body string `json:"body"`
					} `json:"well_known"`
				}) {
					fmt.Fprintf(out, "Prove you control %s with either of these, then run `zuano publisher verify domain --check`:\n", textsafe.Clean(d.Domain))
					fmt.Fprintf(out, "  DNS:  a %s record on %s with the value %s\n", textsafe.Clean(d.DNS.Type), textsafe.Clean(d.DNS.Name), textsafe.Clean(d.DNS.Value))
					fmt.Fprintf(out, "  File: %s containing exactly %s\n", textsafe.Clean(d.WellKnown.URL), textsafe.Clean(d.WellKnown.Body))
				})
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var d struct {
				Verified bool              `json:"verified"`
				Method   *string           `json:"method"`
				Standing publisherStanding `json:"standing"`
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, publisherAPI+"/verifications/domain/check", nil, &d)
			if err != nil {
				return withRouteExit(err, verificationRefusals)
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
			} else if d.Verified {
				method := "the " + strings.ReplaceAll(textsafe.Clean(derefString(d.Method)), "_", "-") + " proof"
				fmt.Fprintf(out, "Domain confirmed by %s.\n", method)
				printStanding(out, d.Standing)
			}
			if !d.Verified {
				return &ExitError{Code: exitVerificationFailed, Msg: "the record or file was not found yet; DNS can take a while, so run the check again"}
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().BoolVar(&check, "check", false, "Look for the published record or file")
	return cmd
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// PublisherCmd returns `zuano publisher`, the publisher profile, its proofs
// and its signing key.
func PublisherCmd() *cobra.Command {
	return marketPublisherCmd()
}
