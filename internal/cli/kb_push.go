package cli

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"

	"github.com/spf13/cobra"
)

var kbRepoName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

// kbGitCmd keeps a space in step with a folder of a repository the Zuano
// GitHub App can reach.
func kbGitCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "git", Short: "Keep a space in step with a Git repository (admins)", Long: `A space synced with Git takes its pages from a folder of a repository the Zuano
GitHub App can reach. Each commit becomes a page version by its author, who
must be a member who can edit the space. The space syncs hourly; "push enable"
syncs it the moment the branch is pushed to.`}
	var repo, branch, path string
	spaceArg := func(args []string) (string, error) { return kbSpaceArg(args[0]) }
	gitPath := func(space, rest string) string { return kbAPI + "/spaces/" + space + "/git" + rest }
	push := &cobra.Command{Use: "push", Short: "Sync the space the moment its branch is pushed to"}
	push.AddCommand(
		kbCmd("enable <space>", "Sync on every push; needs access to manage the repository's webhooks", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := spaceArg(args)
					return kbReq{method: http.MethodPut, path: gitPath(space, "/push")}, err
				}
			}, nil, "Syncing on push"),
		kbCmd("disable <space>", "Stop syncing on pushes; the hourly sync continues", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := spaceArg(args)
					return kbReq{method: http.MethodDelete, path: gitPath(space, "/push")}, err
				}
			}, nil, "No longer syncing on push"),
	)
	cmd.AddCommand(
		kbCmd("show <space>", "Show the repository a space is kept in step with and how the last sync went", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := spaceArg(args)
					return kbReq{method: http.MethodGet, path: gitPath(space, "")}, err
				}
			}, nil, ""),
		kbCmd("set <space> --repo OWNER/NAME", "Keep a space in step with a folder of a repository", cobra.ExactArgs(1),
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				cmd.Flags().StringVar(&repo, "repo", "", "The repository, owner/name")
				cmd.Flags().StringVar(&branch, "branch", "", "The branch (the repository's default when omitted)")
				cmd.Flags().StringVar(&path, "path", "", "The folder of the repository (its root when omitted)")
				return func(args []string) (kbReq, error) {
					space, err := spaceArg(args)
					if err != nil {
						return kbReq{}, err
					}
					if !kbRepoName.MatchString(repo) {
						return kbReq{}, errors.New("--repo is required: the repository as owner/name")
					}
					body := map[string]any{"repo": repo}
					if branch != "" {
						body["branch"] = branch
					}
					if cmd.Flags().Changed("path") {
						body["path"] = path
					}
					return kbReq{method: http.MethodPut, path: gitPath(space, ""), body: body}, nil
				}
			}, nil, ""),
		kbCmd("remove <space>", "Stop syncing a space with Git; its pages stay", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := spaceArg(args)
					return kbReq{method: http.MethodDelete, path: gitPath(space, "")}, err
				}
			}, nil, "Git sync removed"),
		kbCmd("sync <space>", "Sync a space with its repository now", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := spaceArg(args)
					return kbReq{method: http.MethodPost, path: gitPath(space, "/sync")}, err
				}
			}, nil, "Queued a sync"),
		push,
	)
	return cmd
}

// kbSourcePushCmd syncs a Google Drive or GitHub source when the provider
// reports a change.
func kbSourcePushCmd() *cobra.Command {
	byID := func(method string) func(*cobra.Command) func([]string) (kbReq, error) {
		return func(*cobra.Command) func([]string) (kbReq, error) {
			return func(args []string) (kbReq, error) {
				return kbReq{method: method, path: kbAPI + "/sources/" + url.PathEscape(args[0]) + "/push"}, nil
			}
		}
	}
	cmd := &cobra.Command{Use: "push", Short: "Sync a Google Drive or GitHub source the moment the provider reports a change"}
	cmd.AddCommand(
		kbCmd("enable <id>", "Sync on provider changes; the scheduled sync continues", cobra.ExactArgs(1), byID(http.MethodPut), nil, "Syncing on provider changes"),
		kbCmd("disable <id>", "Stop syncing on provider changes; the scheduled sync continues", cobra.ExactArgs(1), byID(http.MethodDelete), nil, "No longer syncing on provider changes"),
	)
	return cmd
}
