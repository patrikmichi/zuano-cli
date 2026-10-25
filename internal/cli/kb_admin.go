package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// kbReq is one request of the knowledge base API. A page named by space and
// slug is looked up first when the route takes the page's id.
type kbReq struct {
	method, path string
	body         any
	page         *kbPageRef
	rest         string
}

// kbCmd builds a command that sends one request. bind registers the command's
// flags and returns what turns the arguments into the request; render prints
// the answer (the indented document when nil), and done is what a bodiless
// answer prints.
func kbCmd(use, short string, args cobra.PositionalArgs, bind func(*cobra.Command) func([]string) (kbReq, error), render func(io.Writer, []byte) error, done string) *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{Use: use, Short: short, Args: args}
	build := bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		req, err := build(args)
		if err != nil {
			return err
		}
		c, err := approvalsClient(f.gatewayURL)
		if err != nil {
			return err
		}
		path := req.path
		if req.page != nil {
			id := req.page.id
			if id == "" {
				var page kbPageDoc
				if _, err := kbRequest(cmd, c, f.jsonOut, http.MethodGet, req.page.path(""), nil, &page); err != nil {
					return err
				}
				id = page.ID
			}
			path = kbAPI + "/pages/" + url.PathEscape(id) + req.rest
		}
		raw, err := kbRequest(cmd, c, f.jsonOut, req.method, path, req.body, nil)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		switch {
		case f.jsonOut && len(raw) > 0:
			printRaw(out, raw)
		case len(raw) == 0:
			fmt.Fprintln(out, orDash(done))
		case render != nil:
			return render(out, raw)
		default:
			fmt.Fprintln(out, indentJSON(raw))
		}
		return nil
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}

func decodeKb(raw []byte, into any) error {
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decode the knowledge base answer: %w", err)
	}
	return nil
}

// kbPrincipal reads --user or --group, one of which names who a role is for.
type kbPrincipal struct{ user, group string }

func (p *kbPrincipal) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p.user, "user", "", "The member's email")
	cmd.Flags().StringVar(&p.group, "group", "", "The group's name")
}

func (p kbPrincipal) fields() (map[string]any, error) {
	switch {
	case p.user != "" && p.group == "":
		return map[string]any{"principal_kind": "user", "principal": p.user}, nil
	case p.group != "" && p.user == "":
		return map[string]any{"principal_kind": "group", "principal": p.group}, nil
	}
	return nil, errors.New("give exactly one of --user and --group")
}

func (p kbPrincipal) query() (string, error) {
	fields, err := p.fields()
	if err != nil {
		return "", err
	}
	return "?" + url.Values{"principal_kind": {fields["principal_kind"].(string)}, "principal": {fields["principal"].(string)}}.Encode(), nil
}

// kbChoice requires value to be one of allowed.
func kbChoice(flag, value string, allowed ...string) error {
	if value == "" {
		return fmt.Errorf("--%s is required: use one of %s", flag, strings.Join(allowed, ", "))
	}
	if !slices.Contains(allowed, value) {
		return fmt.Errorf("invalid --%s %q: use one of %s", flag, textsafe.Clean(value), strings.Join(allowed, ", "))
	}
	return nil
}

func renderKbItems(header []string, cells func(item map[string]any) []string) func(io.Writer, []byte) error {
	return func(out io.Writer, raw []byte) error {
		var doc struct {
			Items []map[string]any `json:"items"`
		}
		if err := decodeKb(raw, &doc); err != nil {
			return err
		}
		if len(doc.Items) == 0 {
			fmt.Fprintln(out, "Nothing here")
			return nil
		}
		rows := make([][]string, 0, len(doc.Items))
		for _, item := range doc.Items {
			rows = append(rows, cells(item))
		}
		printRows(out, header, rows)
		return nil
	}
}

// kbField reads one value of a decoded document as text, cleaned for a terminal.
func kbField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case nil:
		return ""
	case string:
		return textsafe.Clean(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func kbSpacesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "spaces", Short: "List, create and change spaces, and who may use them"}
	cmd.AddCommand(
		kbCmd("list", "List the spaces you may read", cobra.NoArgs,
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func([]string) (kbReq, error) { return kbReq{method: http.MethodGet, path: kbAPI + "/spaces"}, nil }
			},
			renderKbItems([]string{"SPACE", "NAME", "KIND", "DEFAULT ACCESS", "YOUR ROLE"}, func(s map[string]any) []string {
				return []string{kbField(s, "slug"), kbField(s, "name"), kbField(s, "kind"), kbField(s, "defaultAccess"), kbField(s, "role")}
			}), ""),
		kbCmd("show <space>", "Show one space", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := kbSpaceArg(args[0])
					return kbReq{method: http.MethodGet, path: kbAPI + "/spaces/" + space}, err
				}
			}, nil, ""),
		kbSpaceCreateCmd(),
		kbSpaceUpdateCmd(),
		kbCmd("graph <space>", "Show the readable pages of a space and the wikilinks among them", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := kbSpaceArg(args[0])
					return kbReq{method: http.MethodGet, path: kbAPI + "/spaces/" + space + "/graph"}, err
				}
			}, nil, ""),
		kbCmd("routines <space>", "Show the workflows and schedules the pages of a space link to", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := kbSpaceArg(args[0])
					return kbReq{method: http.MethodGet, path: kbAPI + "/spaces/" + space + "/routines"}, err
				}
			}, nil, ""),
		kbMembersCmd(),
	)
	return cmd
}

func kbSpaceCreateCmd() *cobra.Command {
	var name, access string
	var reviewDays int
	return kbCmd("create <slug> --name N", "Create an organisation space", cobra.ExactArgs(1),
		func(cmd *cobra.Command) func([]string) (kbReq, error) {
			cmd.Flags().StringVar(&name, "name", "", "The space's name")
			cmd.Flags().StringVar(&access, "default-access", "", "What members with no role get: none, reader or editor")
			cmd.Flags().IntVar(&reviewDays, "review-days", 0, "Days before a page is due for review (1 to 3650)")
			return func(args []string) (kbReq, error) {
				slug, err := kbSpaceArg(args[0])
				if err != nil {
					return kbReq{}, err
				}
				if name == "" {
					return kbReq{}, errors.New("--name is required")
				}
				body := map[string]any{"slug": slug, "name": name}
				if err := spaceSettings(body, access, reviewDays); err != nil {
					return kbReq{}, err
				}
				return kbReq{method: http.MethodPost, path: kbAPI + "/spaces", body: body}, nil
			}
		}, nil, "")
}

func spaceSettings(body map[string]any, access string, reviewDays int) error {
	if access != "" {
		if err := kbChoice("default-access", access, "none", "reader", "editor"); err != nil {
			return err
		}
		body["default_access"] = access
	}
	if reviewDays != 0 {
		if reviewDays < 1 || reviewDays > 3650 {
			return errors.New("--review-days is between 1 and 3650")
		}
		body["review_days"] = reviewDays
	}
	return nil
}

func kbSpaceUpdateCmd() *cobra.Command {
	var name, access string
	var reviewDays int
	return kbCmd("update <space>", "Change a space's name, default access or review period", cobra.ExactArgs(1),
		func(cmd *cobra.Command) func([]string) (kbReq, error) {
			cmd.Flags().StringVar(&name, "name", "", "The space's name")
			cmd.Flags().StringVar(&access, "default-access", "", "What members with no role get: none, reader or editor")
			cmd.Flags().IntVar(&reviewDays, "review-days", 0, "Days before a page is due for review (1 to 3650)")
			return func(args []string) (kbReq, error) {
				space, err := kbSpaceArg(args[0])
				if err != nil {
					return kbReq{}, err
				}
				body := map[string]any{}
				if name != "" {
					body["name"] = name
				}
				if err := spaceSettings(body, access, reviewDays); err != nil {
					return kbReq{}, err
				}
				if len(body) == 0 {
					return kbReq{}, errors.New("give something to change: --name, --default-access or --review-days")
				}
				return kbReq{method: http.MethodPatch, path: kbAPI + "/spaces/" + space, body: body}, nil
			}
		}, nil, "")
}

func kbMembersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "members", Short: "See and change who has a role in a space"}
	var setWho, removeWho kbPrincipal
	var role string
	cmd.AddCommand(
		kbCmd("list <space>", "List who has a role in a space", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := kbSpaceArg(args[0])
					return kbReq{method: http.MethodGet, path: kbAPI + "/spaces/" + space + "/members"}, err
				}
			},
			renderKbItems([]string{"KIND", "WHO", "ROLE"}, func(m map[string]any) []string {
				return []string{kbField(m, "principalKind"), kbField(m, "principal"), kbField(m, "role")}
			}), ""),
		kbCmd("set <space> --role R", "Give a member or a group a role in a space", cobra.ExactArgs(1),
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				setWho.bind(cmd)
				cmd.Flags().StringVar(&role, "role", "", "reader, editor or admin")
				return func(args []string) (kbReq, error) {
					space, err := kbSpaceArg(args[0])
					if err != nil {
						return kbReq{}, err
					}
					if err := kbChoice("role", role, "reader", "editor", "admin"); err != nil {
						return kbReq{}, err
					}
					body, err := setWho.fields()
					if err != nil {
						return kbReq{}, err
					}
					body["role"] = role
					return kbReq{method: http.MethodPut, path: kbAPI + "/spaces/" + space + "/members", body: body}, nil
				}
			}, nil, "Role set"),
		kbCmd("remove <space>", "Remove a member's or a group's role in a space", cobra.ExactArgs(1),
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				removeWho.bind(cmd)
				return func(args []string) (kbReq, error) {
					space, err := kbSpaceArg(args[0])
					if err != nil {
						return kbReq{}, err
					}
					q, err := removeWho.query()
					return kbReq{method: http.MethodDelete, path: kbAPI + "/spaces/" + space + "/members" + q}, err
				}
			}, nil, "Role removed"),
	)
	return cmd
}

func kbPagesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pages", Short: "List pages and manage their history, review, status and grants"}
	pageArg := func(args []string) (kbPageRef, error) { return parseKbPage(args[0]) }
	byPage := func(method, rest string) func(*cobra.Command) func([]string) (kbReq, error) {
		return func(*cobra.Command) func([]string) (kbReq, error) {
			return func(args []string) (kbReq, error) {
				ref, err := pageArg(args)
				return kbReq{method: method, page: &ref, rest: rest}, err
			}
		}
	}
	var toSpace string
	var grantWho, ungrantWho kbPrincipal
	var grantRole string
	cmd.AddCommand(
		kbCmd("list <space>", "List the pages of a space that you may read", cobra.ExactArgs(1),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					space, err := kbSpaceArg(args[0])
					return kbReq{method: http.MethodGet, path: kbAPI + "/spaces/" + space + "/pages"}, err
				}
			},
			renderKbItems([]string{"PAGE", "TITLE", "STATUS", "VERSION", "UPDATED", ""}, func(p map[string]any) []string {
				return []string{kbField(p, "spaceSlug") + "/" + kbField(p, "slug"), kbField(p, "title"), kbField(p, "status"), kbField(p, "version"), kbField(p, "updatedAt"), staleMark(kbField(p, "stale") == "true")}
			}), ""),
		kbCmd("versions <page>", "List a page's versions, newest first", cobra.ExactArgs(1), byPage(http.MethodGet, "/versions"),
			renderKbItems([]string{"VERSION", "AUTHOR", "CREATED", "HASH"}, func(v map[string]any) []string {
				return []string{kbField(v, "version"), kbField(v, "authorUserId"), kbField(v, "createdAt"), kbField(v, "contentHash")}
			}), ""),
		kbCmd("backlinks <page>", "List the readable pages that link to a page", cobra.ExactArgs(1), byPage(http.MethodGet, "/backlinks"),
			renderKbItems([]string{"PAGE", "TITLE", "KIND"}, func(b map[string]any) []string {
				return []string{kbField(b, "spaceSlug") + "/" + kbField(b, "slug"), kbField(b, "title"), kbField(b, "kind")}
			}), ""),
		kbCmd("runs <page>", "Show the skills, workflows and schedules a page links to, with their last run", cobra.ExactArgs(1), byPage(http.MethodGet, "/runs"), nil, ""),
		kbCmd("verify <page>", "Mark a page as checked by you and start its next review period (editors)", cobra.ExactArgs(1), byPage(http.MethodPost, "/verify"), nil, ""),
		kbCmd("status <page> draft|published|archived", "Publish, archive or return a page to draft", cobra.ExactArgs(2),
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func(args []string) (kbReq, error) {
					ref, err := pageArg(args)
					if err != nil {
						return kbReq{}, err
					}
					if err := kbChoice("status", args[1], "draft", "published", "archived"); err != nil {
						return kbReq{}, err
					}
					return kbReq{method: http.MethodPatch, page: &ref, body: map[string]any{"status": args[1]}}, nil
				}
			}, nil, ""),
		kbCmd("promote <page> --space S", "Copy a personal page into an organisation space as a draft for its editors", cobra.ExactArgs(1),
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				cmd.Flags().StringVar(&toSpace, "space", "", "The organisation space to copy it into")
				return func(args []string) (kbReq, error) {
					ref, err := pageArg(args)
					if err != nil {
						return kbReq{}, err
					}
					space, err := kbSpaceArg(toSpace)
					return kbReq{method: http.MethodPost, page: &ref, rest: "/promote", body: map[string]any{"space": space}}, err
				}
			}, nil, ""),
		kbCmd("grants <page>", "List who has a role on one page (admins)", cobra.ExactArgs(1), byPage(http.MethodGet, "/grants"),
			renderKbItems([]string{"KIND", "WHO", "ROLE"}, func(g map[string]any) []string {
				return []string{kbField(g, "principalKind"), kbField(g, "principal"), kbField(g, "role")}
			}), ""),
		kbCmd("grant <page> --role R", "Give a member or a group a role on one page, over the space's role; none removes access", cobra.ExactArgs(1),
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				grantWho.bind(cmd)
				cmd.Flags().StringVar(&grantRole, "role", "", "none, reader, editor or admin")
				return func(args []string) (kbReq, error) {
					ref, err := pageArg(args)
					if err != nil {
						return kbReq{}, err
					}
					if err := kbChoice("role", grantRole, "none", "reader", "editor", "admin"); err != nil {
						return kbReq{}, err
					}
					body, err := grantWho.fields()
					if err != nil {
						return kbReq{}, err
					}
					body["role"] = grantRole
					return kbReq{method: http.MethodPut, page: &ref, rest: "/grants", body: body}, nil
				}
			}, nil, "Grant set"),
		kbCmd("ungrant <page>", "Remove a page grant", cobra.ExactArgs(1),
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				ungrantWho.bind(cmd)
				return func(args []string) (kbReq, error) {
					ref, err := pageArg(args)
					if err != nil {
						return kbReq{}, err
					}
					q, err := ungrantWho.query()
					return kbReq{method: http.MethodDelete, page: &ref, rest: "/grants" + q}, err
				}
			}, nil, "Grant removed"),
	)
	return cmd
}

var kbSourceKinds = []string{"google_drive", "notion", "confluence", "slack", "github", "jira"}

func kbSourcesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sources", Short: "Index other tools into the knowledge base (admins)", Long: `A source indexes a tool into a space. Each document keeps the readers it has in
the tool, so search never shows it to anyone who could not open it there.`}
	byID := func(method, rest string) func(*cobra.Command) func([]string) (kbReq, error) {
		return func(*cobra.Command) func([]string) (kbReq, error) {
			return func(args []string) (kbReq, error) {
				return kbReq{method: method, path: kbAPI + "/sources/" + url.PathEscape(args[0]) + rest}, nil
			}
		}
	}
	var kind, name, space, schedule, configFile string
	var updName, updSchedule, updStatus, updConfig string
	var yes bool
	cmd.AddCommand(
		kbCmd("list", "List the indexed tools", cobra.NoArgs,
			func(*cobra.Command) func([]string) (kbReq, error) {
				return func([]string) (kbReq, error) { return kbReq{method: http.MethodGet, path: kbAPI + "/sources"}, nil }
			},
			renderKbItems([]string{"ID", "KIND", "NAME", "SPACE", "SCHEDULE", "STATUS", "DOCUMENTS", "LAST SYNC"}, func(s map[string]any) []string {
				return []string{kbField(s, "id"), kbField(s, "kind"), kbField(s, "name"), kbField(s, "space"), kbField(s, "schedule"), kbField(s, "status"), kbField(s, "documents"), orDash(kbField(s, "lastSyncedAt"))}
			}), ""),
		kbCmd("show <id>", "Show one source and its last sync", cobra.ExactArgs(1), byID(http.MethodGet, ""), nil, ""),
		kbCmd("create --kind K --name N --space S --config-file F", "Index a tool into a space", cobra.NoArgs,
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				cmd.Flags().StringVar(&kind, "kind", "", "The tool: "+strings.Join(kbSourceKinds, ", "))
				cmd.Flags().StringVar(&name, "name", "", "A name for the source")
				cmd.Flags().StringVar(&space, "space", "", "The organisation space the documents go into")
				cmd.Flags().StringVar(&schedule, "schedule", "", "hourly, daily or manual")
				cmd.Flags().StringVar(&configFile, "config-file", "", "JSON file with what to index, or - for standard input")
				return func([]string) (kbReq, error) {
					if err := kbChoice("kind", kind, kbSourceKinds...); err != nil {
						return kbReq{}, err
					}
					if name == "" {
						return kbReq{}, errors.New("--name is required")
					}
					slug, err := kbSpaceArg(space)
					if err != nil {
						return kbReq{}, err
					}
					body := map[string]any{"kind": kind, "name": name, "space": slug}
					if schedule != "" {
						if err := kbChoice("schedule", schedule, "hourly", "daily", "manual"); err != nil {
							return kbReq{}, err
						}
						body["schedule"] = schedule
					}
					config, err := readSourceConfig(cmd, configFile)
					if err != nil {
						return kbReq{}, err
					}
					body["config"] = config
					return kbReq{method: http.MethodPost, path: kbAPI + "/sources", body: body}, nil
				}
			}, nil, ""),
		kbCmd("update <id>", "Rename a source, change its schedule or what it indexes, or pause and resume it", cobra.ExactArgs(1),
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				cmd.Flags().StringVar(&updName, "name", "", "A new name")
				cmd.Flags().StringVar(&updSchedule, "schedule", "", "hourly, daily or manual")
				cmd.Flags().StringVar(&updStatus, "status", "", "active or paused")
				cmd.Flags().StringVar(&updConfig, "config-file", "", "JSON file with what to index, or - for standard input")
				return func(args []string) (kbReq, error) {
					body := map[string]any{}
					if updName != "" {
						body["name"] = updName
					}
					if updSchedule != "" {
						if err := kbChoice("schedule", updSchedule, "hourly", "daily", "manual"); err != nil {
							return kbReq{}, err
						}
						body["schedule"] = updSchedule
					}
					if updStatus != "" {
						if err := kbChoice("status", updStatus, "active", "paused"); err != nil {
							return kbReq{}, err
						}
						body["status"] = updStatus
					}
					if updConfig != "" {
						config, err := readSourceConfig(cmd, updConfig)
						if err != nil {
							return kbReq{}, err
						}
						body["config"] = config
					}
					if len(body) == 0 {
						return kbReq{}, errors.New("give something to change: --name, --schedule, --status or --config-file")
					}
					return kbReq{method: http.MethodPatch, path: kbAPI + "/sources/" + url.PathEscape(args[0]), body: body}, nil
				}
			}, nil, ""),
		kbCmd("delete <id> --yes", "Stop indexing a tool and remove its documents, unless they are under legal hold", cobra.ExactArgs(1),
			func(cmd *cobra.Command) func([]string) (kbReq, error) {
				cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the removal")
				return func(args []string) (kbReq, error) {
					if !yes {
						return kbReq{}, errors.New("this removes the source's documents and passages; confirm with --yes")
					}
					return kbReq{method: http.MethodDelete, path: kbAPI + "/sources/" + url.PathEscape(args[0])}, nil
				}
			}, nil, "Source removed"),
		kbCmd("sync <id>", "Sync a source now", cobra.ExactArgs(1), byID(http.MethodPost, "/sync"), nil, "Queued a sync"),
		kbCmd("documents <id>", "List a source's documents with how many readers each has", cobra.ExactArgs(1), byID(http.MethodGet, "/documents"),
			renderKbItems([]string{"ID", "TITLE", "READERS", "UNMATCHED", "ADMINS ONLY", "SYNCED"}, func(d map[string]any) []string {
				readers := kbField(d, "readerCount")
				if kbField(d, "orgWide") == "true" {
					readers = "everyone"
				}
				return []string{kbField(d, "id"), kbField(d, "title"), readers, kbField(d, "unmappedReaders"), kbField(d, "adminOnly"), kbField(d, "syncedAt")}
			}), ""),
	)
	return cmd
}

// readSourceConfig reads what a source indexes: a JSON object, from a file or
// standard input, as no credential is ever taken from a flag.
func readSourceConfig(cmd *cobra.Command, file string) (map[string]any, error) {
	if file == "" {
		return nil, errors.New("--config-file is required: a JSON object saying what to index")
	}
	return readJSONInput(cmd, file, "the source config")
}
