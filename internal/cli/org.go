package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const domainsAPI = builderAPI + "/domains"

// domainRefusals gives the domain refusal that has an exit status of its own:
// a verify that finds no DNS record yet is a failed proof, like a publisher's
// domain check.
var domainRefusals = map[string]int{"domain_unverified": exitVerificationFailed}

type orgDomain struct {
	ID           string `json:"id"`
	Domain       string `json:"domain"`
	Status       string `json:"status"`
	Verification struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"verification"`
	VerifiedAt *string `json:"verified_at"`
	CheckedAt  *string `json:"checked_at"`
	CreatedAt  string  `json:"created_at"`
}

// OrgCmd returns `zuano org`, the organisation's own settings.
func OrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Manage the organisation's settings",
	}
	cmd.AddCommand(orgDomainsCmd())
	return cmd
}

func orgDomainsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domains",
		Short: "Claim and verify the email domains of the organisation (owners and admins)",
		Long: `A claimed domain counts once the DNS TXT record the answer names is published and
verified. Only one organisation at a time can hold a domain verified.

Exit status: 70 when verify finds no DNS record yet (domain_unverified), 1 for
the other refusals (a domain already claimed, the limit, a domain another
organisation verified, a DNS lookup that could not complete), 10 for a denial,
30 to 33 for the plan.`,
	}
	cmd.AddCommand(domainsListCmd(), domainsAddCmd(), domainsVerifyCmd(), domainsRemoveCmd())
	return cmd
}

func domainsRequest(cmd *cobra.Command, c *client.Client, jsonOut bool, method, path string, body, out any) ([]byte, error) {
	raw, err := builderRequest(cmd, c, jsonOut, method, path, body, out)
	return raw, withRouteExit(err, domainRefusals)
}

func domainsList(cmd *cobra.Command, c *client.Client, jsonOut bool) ([]orgDomain, []byte, error) {
	var doc struct {
		Domains []orgDomain `json:"domains"`
	}
	raw, err := domainsRequest(cmd, c, jsonOut, http.MethodGet, domainsAPI, nil, &doc)
	return doc.Domains, raw, err
}

func printDomainRecord(cmd *cobra.Command, d orgDomain) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Domain:  %s (%s)\n", textsafe.Clean(d.Domain), textsafe.Clean(d.Status))
	if d.Status != "verified" {
		fmt.Fprintf(out, "Publish: %s record at %s with the value\n  %s\nthen run `zuano org domains verify %s`\n", textsafe.Clean(d.Verification.Type), textsafe.Clean(d.Verification.Name), textsafe.Clean(d.Verification.Value), textsafe.Clean(d.Domain))
	}
}

func domainsListCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the claimed domains and whether each is verified",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			domains, raw, err := domainsList(cmd, c, f.jsonOut)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(domains) == 0 {
				fmt.Fprintln(out, "No domains claimed")
				return nil
			}
			rows := make([][]string, 0, len(domains))
			for _, d := range domains {
				rows = append(rows, []string{d.ID, d.Domain, d.Status, orDash(deref(d.VerifiedAt)), orDash(deref(d.CheckedAt))})
			}
			printRows(out, []string{"ID", "DOMAIN", "STATUS", "VERIFIED", "LAST CHECK"}, rows)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func domainsAddCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "add <domain>",
		Short: "Claim a domain and print the DNS record that proves it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain := strings.ToLower(strings.TrimSpace(args[0]))
			if domain == "" || strings.ContainsAny(domain, "/ @:") {
				return fmt.Errorf("%q is not a domain name such as example.com", textsafe.Clean(args[0]))
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Domain orgDomain `json:"domain"`
			}
			raw, err := domainsRequest(cmd, c, f.jsonOut, http.MethodPost, domainsAPI, map[string]any{"domain": domain}, &doc)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			printDomainRecord(cmd, doc.Domain)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

// findDomain resolves a domain name or an id to the organisation's claim.
func findDomain(cmd *cobra.Command, c *client.Client, jsonOut bool, arg string) (orgDomain, error) {
	domains, _, err := domainsList(cmd, c, jsonOut)
	if err != nil {
		return orgDomain{}, err
	}
	for _, d := range domains {
		if d.ID == arg || strings.EqualFold(d.Domain, arg) {
			return d, nil
		}
	}
	return orgDomain{}, fmt.Errorf("%q is not one of your organisation's domains; see `zuano org domains list`", textsafe.Clean(arg))
}

func domainsVerifyCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "verify <domain>",
		Short: "Look up the DNS record and verify the domain when it is there",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			d, err := findDomain(cmd, c, f.jsonOut, args[0])
			if err != nil {
				return err
			}
			var doc struct {
				Domain orgDomain `json:"domain"`
			}
			raw, err := domainsRequest(cmd, c, f.jsonOut, http.MethodPost, domainsAPI+"/"+url.PathEscape(d.ID)+"/verify", nil, &doc)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			printDomainRecord(cmd, doc.Domain)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func domainsRemoveCmd() *cobra.Command {
	var f approvalFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <domain> --yes",
		Short: "Release a claimed domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return errors.New("this releases the domain and anything that relies on it being verified; confirm with --yes")
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			d, err := findDomain(cmd, c, f.jsonOut, args[0])
			if err != nil {
				return err
			}
			raw, err := domainsRequest(cmd, c, f.jsonOut, http.MethodDelete, domainsAPI+"/"+url.PathEscape(d.ID), nil, nil)
			if err != nil {
				return err
			}
			if f.jsonOut && len(raw) > 0 {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Released %s\n", textsafe.Clean(d.Domain))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the release")
	return cmd
}
