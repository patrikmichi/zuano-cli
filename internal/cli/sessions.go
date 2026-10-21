package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const sessionsAPI = "/api/me/sessions"

// sessionDoc mirrors one entry of GET /api/me/sessions.
type sessionDoc struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	IPPrefix  string `json:"ipPrefix"`
	UserAgent string `json:"userAgent"`
	Current   bool   `json:"current"`
}

func sessionsRequest(cmd *cobra.Command, f approvalFlags, method, path string, out any) ([]byte, error) {
	c, err := approvalsClient(f.gatewayURL)
	if err != nil {
		return nil, err
	}
	return gatewayJSON(cmd.Context(), c, method, path, nil, out)
}

// SessionsCmd returns `zuano sessions`, the person's browser sessions on the
// gateway.
func SessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List and end your browser sessions on the gateway",
		Long: `The gateway keeps a server-side session for every browser sign-in. These commands
list them, end one, or end all of them. They work with your CLI login. Ending all
of them also ends your refresh tokens, OAuth families and the keys your CLI logins
created, in every organisation you belong to, so run ` + "`" + brand.Cmd("login") + "`" + ` afterwards.`,
	}
	cmd.AddCommand(sessionsListCmd(), sessionsEndCmd(), sessionsEndAllCmd())
	return cmd
}

func sessionsListCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list struct {
				Sessions []sessionDoc `json:"sessions"`
			}
			raw, err := sessionsRequest(cmd, f, http.MethodGet, sessionsAPI, &list)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			printSessions(out, list.Sessions)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func printSessions(out io.Writer, sessions []sessionDoc) {
	if len(sessions) == 0 {
		fmt.Fprintln(out, "No sessions")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tSIGNED IN\tEXPIRES\tFROM")
	for _, s := range sessions {
		id := textsafe.Clean(s.ID)
		if s.Current {
			id += " (this one)"
		}
		from := textsafe.Clean(s.UserAgent)
		if s.IPPrefix != "" {
			from += " " + textsafe.Clean(s.IPPrefix)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", id, textsafe.Clean(s.Kind), textsafe.Clean(s.CreatedAt), textsafe.Clean(s.ExpiresAt), from)
	}
	_ = tw.Flush()
}

func sessionsEndCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:     "end <id>",
		Aliases: []string{"revoke"},
		Short:   "End one session",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := sessionsRequest(cmd, f, http.MethodDelete, sessionsAPI+"?"+url.Values{"id": {args[0]}}.Encode(), nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Ended %s\n", textsafe.Clean(args[0]))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func sessionsEndAllCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:     "end-all",
		Aliases: []string{"revoke-all"},
		Short:   "Sign out everywhere: end every session, refresh token, OAuth family and CLI key, this one included",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var done struct {
				Sessions int `json:"sessions"`
				Families int `json:"families"`
				Keys     int `json:"keys"`
			}
			raw, err := sessionsRequest(cmd, f, http.MethodDelete, sessionsAPI+"?include=current", &done)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed out everywhere: %d sessions, %d refresh families and %d CLI keys ended. Run `%s` to sign in again.\n", done.Sessions, done.Families, done.Keys, brand.Cmd("login"))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}
