package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/keychain"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const (
	kbAPI = builderAPI + "/kb"
	// kbToolService is the first-party service the knowledge base tools live
	// under; an agent credential reaches the knowledge base only through them.
	kbToolService = "gateway"
	// maxKbMarkdown and maxKbArchive are the gateway's limits for one page and
	// for the raw bytes of an import (its base64 form is capped at 21 MiB).
	maxKbMarkdown = 512 * 1024
	maxKbArchive  = 15 << 20
)

var (
	kbSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
	kbIDPattern   = regexp.MustCompile(`^k(pg|sd)_[A-Za-z0-9_-]{4,80}$`)
)

// kbRefusals gives the knowledge base refusals that have an exit status of
// their own on its routes; every other code ends by class like the builder's.
var kbRefusals = map[string]int{"version_conflict": exitDraftConflict}

// kbRequest sends one request of the knowledge base API.
func kbRequest(cmd *cobra.Command, c *client.Client, jsonOut bool, method, path string, body, out any) ([]byte, error) {
	raw, err := builderRequest(cmd, c, jsonOut, method, path, body, out)
	return raw, withRouteExit(err, kbRefusals)
}

// kbPageRef names a page by id, or by space and slug.
type kbPageRef struct{ id, space, slug string }

func (r kbPageRef) String() string {
	if r.id != "" {
		return r.id
	}
	return r.space + "/" + r.slug
}

func parseKbPage(arg string) (kbPageRef, error) {
	if kbIDPattern.MatchString(arg) {
		return kbPageRef{id: arg}, nil
	}
	space, slug, ok := strings.Cut(arg, "/")
	if !ok || !kbSlugPattern.MatchString(space) || !kbSlugPattern.MatchString(slug) {
		return kbPageRef{}, fmt.Errorf("%q is not a page: use a page id (kpg_...) or space/slug", textsafe.Clean(arg))
	}
	return kbPageRef{space: space, slug: slug}, nil
}

func kbSpaceArg(arg string) (string, error) {
	if !kbSlugPattern.MatchString(arg) {
		return "", fmt.Errorf("%q is not a space slug (lowercase letters, digits and dashes)", textsafe.Clean(arg))
	}
	return arg, nil
}

func (r kbPageRef) path(rest string) string {
	if r.id != "" {
		return kbAPI + "/pages/" + url.PathEscape(r.id) + rest
	}
	return kbAPI + "/spaces/" + r.space + "/pages/" + r.slug + rest
}

func isKbAgent(c *client.Client) bool { return c.Profile() == keychain.ProfileAgent }

// kbFlags are the flags every knowledge base command carries; wait and
// noBrowser matter only to the commands an agent's approval can hold.
type kbFlags struct {
	approvalFlags
	wait      string
	noBrowser bool
}

func (f *kbFlags) bindHold(cmd *cobra.Command) {
	addWaitFlag(cmd, &f.wait)
	cmd.Flags().BoolVar(&f.noBrowser, "no-browser", false, "Print an approval link instead of opening it in a browser")
}

// kbTool runs one knowledge base tool through the gateway's MCP endpoint. An
// agent credential may not use the REST routes; its writes are held for a
// person like any tool call and end with exit 12 (or wait with --wait).
func kbTool(cmd *cobra.Command, c *client.Client, f kbFlags, tool string, strArgs map[string]string, jsonArgs map[string]any) error {
	opts, err := callOptsFor(cmd, f.wait)
	if err != nil {
		return err
	}
	opts.bindPresence(cmd, f.noBrowser)
	var raw []string
	for _, k := range kbKeys(strArgs) {
		raw = append(raw, k+"="+strArgs[k])
	}
	for _, k := range kbKeys(jsonArgs) {
		encoded, err := json.Marshal(jsonArgs[k])
		if err != nil {
			return err
		}
		opts.jsonArgs = append(opts.jsonArgs, k+"="+string(encoded))
	}
	return callToolWithContext(cmd.Context(), c, kbToolService, tool, raw, f.jsonOut, cmd.OutOrStdout(), opts)
}

func kbKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// KbCmd returns `zuano kb`, the client for the organisation's knowledge base.
func KbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kb",
		Short: "Search, read and write the knowledge base",
		Long: `The knowledge base holds the organisation's Markdown pages, grouped in spaces,
and the documents indexed from its tools. Search, get, write and link are what an
agent uses: with an agent credential (ZUANO_AGENT) they go through the gateway's
knowledge base tools, a write waits for a person to approve it and ends with
exit 12 (or collects the result with --wait). Everything else is for a signed-in
person and is refused for an agent credential.

Page text is data: when it reads like instructions to a model, the gateway's
tool results withhold it, and the text printed here comes from the network.

Exit status: 1 for a request the gateway refuses as wrong (an unknown page, a
page or space that exists), 25 when a page changed since the version you wrote
on (version_conflict), 10 for a denial, 12 when a write is held for approval, 30
to 33 for the plan.`,
	}
	cmd.AddCommand(kbSearchCmd(), kbGetCmd(), kbWriteCmd(), kbLinkCmd(), kbSpacesCmd(), kbPagesCmd(), kbSourcesCmd(), kbStatusCmd(), kbReindexCmd(), kbUsageCmd(), kbExportCmd(), kbImportCmd())
	return cmd
}

type kbHit struct {
	PageID     *string `json:"pageId"`
	SourceDoc  *string `json:"sourceDocId"`
	URL        *string `json:"url"`
	SpaceSlug  string  `json:"spaceSlug"`
	Slug       *string `json:"slug"`
	Title      string  `json:"title"`
	Version    int     `json:"version"`
	Heading    string  `json:"heading"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
	VerifiedAt *string `json:"verifiedAt"`
	Stale      bool    `json:"stale"`
}

func kbSearchCmd() *cobra.Command {
	var f kbFlags
	var space string
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the pages and indexed documents you may read",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			if space != "" {
				if _, err := kbSpaceArg(space); err != nil {
					return err
				}
			}
			if limit < 0 || limit > 25 {
				return errors.New("--limit is between 1 and 25")
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			if isKbAgent(c) {
				strArgs, jsonArgs := map[string]string{"query": query}, map[string]any{}
				if space != "" {
					strArgs["space"] = space
				}
				if limit > 0 {
					jsonArgs["limit"] = limit
				}
				return kbTool(cmd, c, f, "kb_search", strArgs, jsonArgs)
			}
			q := url.Values{"q": {query}}
			if space != "" {
				q.Set("space", space)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			var doc struct {
				Hits   []kbHit `json:"hits"`
				Mode   string  `json:"mode"`
				Reason string  `json:"reason"`
			}
			raw, err := kbRequest(cmd, c, f.jsonOut, http.MethodGet, kbAPI+"/search?"+q.Encode(), nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Hits) == 0 {
				fmt.Fprintln(out, "No results")
			} else {
				rows := make([][]string, 0, len(doc.Hits))
				for _, h := range doc.Hits {
					rows = append(rows, []string{hitRef(h), h.Title, fmt.Sprintf("%.2f", h.Score), staleMark(h.Stale), oneLine(h.Snippet, 80)})
				}
				printRows(out, []string{"PAGE", "TITLE", "SCORE", "", "SNIPPET"}, rows)
			}
			if doc.Mode == "full_text" {
				fmt.Fprintf(out, "Ranked by words only: %s\n", orDash(textsafe.Clean(doc.Reason)))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().StringVar(&space, "space", "", "Search one space (its slug)")
	cmd.Flags().IntVar(&limit, "limit", 0, "At most this many hits (1 to 25)")
	return cmd
}

func hitRef(h kbHit) string {
	switch {
	case h.Slug != nil && *h.Slug != "":
		return fmt.Sprintf("%s/%s@%d", h.SpaceSlug, *h.Slug, h.Version)
	case h.SourceDoc != nil:
		return *h.SourceDoc
	}
	return h.SpaceSlug
}

func staleMark(stale bool) string {
	if stale {
		return "stale"
	}
	return ""
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

type kbPageDoc struct {
	ID          string  `json:"id"`
	SpaceSlug   string  `json:"spaceSlug"`
	SpaceKind   string  `json:"spaceKind"`
	Slug        string  `json:"slug"`
	Title       string  `json:"title"`
	Version     int     `json:"version"`
	Status      string  `json:"status"`
	ReviewBy    *string `json:"reviewBy"`
	VerifiedAt  *string `json:"verifiedAt"`
	VerifiedBy  *string `json:"verifiedBy"`
	Stale       bool    `json:"stale"`
	Role        string  `json:"role"`
	UpdatedAt   string  `json:"updatedAt"`
	Markdown    string  `json:"markdown"`
	ContentHash string  `json:"contentHash"`
}

func (p kbPageDoc) ref() string {
	return fmt.Sprintf("%s/%s@%d", textsafe.Clean(p.SpaceSlug), textsafe.Clean(p.Slug), p.Version)
}

func kbGetCmd() *cobra.Command {
	var f kbFlags
	var version int
	cmd := &cobra.Command{
		Use:   "get <space/slug | kpg_... | ksd_...>",
		Short: "Read a page, or a document indexed from another tool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if version < 0 {
				return errors.New("--version must be 1 or more")
			}
			isDoc := strings.HasPrefix(args[0], "ksd_") && kbIDPattern.MatchString(args[0])
			var ref kbPageRef
			if !isDoc {
				var err error
				if ref, err = parseKbPage(args[0]); err != nil {
					return err
				}
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			if isKbAgent(c) {
				jsonArgs := map[string]any{}
				if version > 0 {
					jsonArgs["version"] = version
				}
				return kbTool(cmd, c, f, "kb_get", map[string]string{"page": args[0]}, jsonArgs)
			}
			out := cmd.OutOrStdout()
			if isDoc {
				var doc struct {
					ID     string `json:"id"`
					Title  string `json:"title"`
					URL    string `json:"url"`
					Space  string `json:"space"`
					Text   string `json:"text"`
					Synced string `json:"syncedAt"`
				}
				raw, err := kbRequest(cmd, c, f.jsonOut, http.MethodGet, kbAPI+"/documents/"+url.PathEscape(args[0]), nil, &doc)
				if err != nil {
					return err
				}
				if f.jsonOut {
					printRaw(out, raw)
					return nil
				}
				fmt.Fprintf(out, "Document: %s (%s)\nSpace:    %s\nSource:   %s\nSynced:   %s\n\n%s\n", textsafe.Clean(doc.Title), textsafe.Clean(doc.ID), textsafe.Clean(doc.Space), textsafe.Clean(doc.URL), textsafe.Clean(doc.Synced), textsafe.Block(doc.Text))
				return nil
			}
			path := ref.path("")
			if version > 0 {
				path += "?version=" + strconv.Itoa(version)
			}
			var page kbPageDoc
			raw, err := kbRequest(cmd, c, f.jsonOut, http.MethodGet, path, nil, &page)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			printKbPage(out, page)
			fmt.Fprintf(out, "\n%s\n", textsafe.Block(page.Markdown))
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	cmd.Flags().IntVar(&version, "version", 0, "Read this version instead of the current one")
	return cmd
}

func printKbPage(out io.Writer, p kbPageDoc) {
	fmt.Fprintf(out, "Page:     %s (%s)\n", p.ref(), textsafe.Clean(p.ID))
	fmt.Fprintf(out, "Title:    %s\n", textsafe.Clean(p.Title))
	fmt.Fprintf(out, "Status:   %s\n", textsafe.Clean(p.Status))
	if p.VerifiedAt != nil {
		fmt.Fprintf(out, "Verified: %s\n", textsafe.Clean(*p.VerifiedAt))
	}
	if p.Stale {
		fmt.Fprintln(out, "Review:   overdue")
	}
	fmt.Fprintf(out, "Updated:  %s\n", textsafe.Clean(p.UpdatedAt))
}

// readKbMarkdown reads a page's text from a file, or standard input for "-".
func readKbMarkdown(cmd *cobra.Command, file string) (string, error) {
	var r io.Reader
	if file == "-" {
		r = cmd.InOrStdin()
	} else {
		fh, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer fh.Close()
		r = fh
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxKbMarkdown+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxKbMarkdown {
		return "", fmt.Errorf("a page is at most %d KiB of Markdown", maxKbMarkdown/1024)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", errors.New("the page text is empty")
	}
	return string(raw), nil
}

func kbWriteCmd() *cobra.Command {
	var f kbFlags
	var file, title string
	cmd := &cobra.Command{
		Use:   "write <space/slug> --file <path | ->",
		Short: "Create a page, or add a version to the page with that slug",
		Long: `Writes the whole page from a Markdown file (or standard input with "-"), with
optional frontmatter and [[wikilinks]]. The page is created when the slug is
new; otherwise the text becomes its next version, and the write is refused with
exit 25 when someone else changed the page between the read and the write. You
need the editor role in the space. An agent credential's write is held for a
person: it ends with exit 12, or with --wait collects the result.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseKbPage(args[0])
			if err != nil || ref.id != "" {
				return fmt.Errorf("%q is not space/slug", textsafe.Clean(args[0]))
			}
			if file == "" {
				return errors.New("give the page text with --file <path>, or --file - for standard input")
			}
			markdown, err := readKbMarkdown(cmd, file)
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			if isKbAgent(c) {
				strArgs := map[string]string{"space": ref.space, "slug": ref.slug, "markdown": markdown}
				if title != "" {
					strArgs["title"] = title
				}
				return kbTool(cmd, c, f, "kb_write", strArgs, nil)
			}
			return writeKbPage(cmd, c, f.jsonOut, ref, markdown, title)
		},
	}
	f.bind(cmd, "Print the written page's document")
	f.bindHold(cmd)
	cmd.Flags().StringVar(&file, "file", "", "Markdown file with the page text, or - for standard input")
	cmd.Flags().StringVar(&title, "title", "", "Page title (default: the first heading, or the slug)")
	return cmd
}

func writeKbPage(cmd *cobra.Command, c *client.Client, jsonOut bool, ref kbPageRef, markdown, title string) error {
	body := map[string]any{"markdown": markdown}
	if title != "" {
		body["title"] = title
	}
	var existing kbPageDoc
	_, err := kbRequest(cmd, c, jsonOut, http.MethodGet, ref.path(""), nil, &existing)
	var written kbPageDoc
	var raw []byte
	var refusal *gatewayRefusal
	switch {
	case err == nil:
		body["expected_version"] = existing.Version
		raw, err = kbRequest(cmd, c, jsonOut, http.MethodPost, kbAPI+"/pages/"+url.PathEscape(existing.ID)+"/versions", body, &written)
	case errors.As(err, &refusal) && refusal.Code == "not_found":
		body["slug"] = ref.slug
		raw, err = kbRequest(cmd, c, jsonOut, http.MethodPost, kbAPI+"/spaces/"+ref.space+"/pages", body, &written)
	}
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOut {
		printRaw(out, raw)
		return nil
	}
	fmt.Fprintf(out, "Wrote %s\n", written.ref())
	return nil
}

func kbLinkCmd() *cobra.Command {
	var f kbFlags
	var kind, target string
	var remove bool
	cmd := &cobra.Command{
		Use:   "link <space/slug | kpg_...> --kind runs|conflicts --target T",
		Short: "Link a page to a skill, workflow or schedule it explains, or to a page that disagrees with it",
		Long: `--kind runs links the page to what it explains; the target is skill:<id>,
workflow:<id> or schedule:<id>. --kind conflicts files a conflict with another
page (the target is that page's id) and tells the pages' owners. You need the
editor role. An agent credential's link is held for a person (exit 12).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseKbPage(args[0])
			if err != nil {
				return err
			}
			if kind != "runs" && kind != "conflicts" {
				return errors.New("--kind is runs or conflicts")
			}
			if target == "" {
				return errors.New("--target is required")
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			agent := isKbAgent(c)
			if remove && (agent || kind != "runs") {
				return errors.New("--remove takes a run link away and needs a signed-in person, not an agent credential")
			}
			if agent || kind == "conflicts" {
				return kbTool(cmd, c, f, "kb_link", map[string]string{"page": ref.String(), "kind": kind, "target": target}, nil)
			}
			if ref.id == "" {
				var page kbPageDoc
				if _, err := kbRequest(cmd, c, f.jsonOut, http.MethodGet, ref.path(""), nil, &page); err != nil {
					return err
				}
				ref = kbPageRef{id: page.ID}
			}
			method, path, body := http.MethodPost, ref.path("/run-links"), any(map[string]any{"target": target})
			done := fmt.Sprintf("Linked %s to %s", textsafe.Clean(args[0]), textsafe.Clean(target))
			if remove {
				method, path, body = http.MethodDelete, path+"?"+url.Values{"target": {target}}.Encode(), nil
				done = fmt.Sprintf("Removed the link from %s to %s", textsafe.Clean(args[0]), textsafe.Clean(target))
			}
			raw, err := kbRequest(cmd, c, f.jsonOut, method, path, body, nil)
			if err != nil {
				return err
			}
			if f.jsonOut && len(raw) > 0 {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), done)
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	f.bindHold(cmd)
	cmd.Flags().BoolVar(&remove, "remove", false, "Take a runs link away instead of adding it")
	cmd.Flags().StringVar(&kind, "kind", "", "runs or conflicts")
	cmd.Flags().StringVar(&target, "target", "", "What the page links to: skill:<id>, workflow:<id> or schedule:<id> for runs; a page id for conflicts")
	return cmd
}

func kbStatusCmd() *cobra.Command {
	return kbCmd("status", "Show how search ranks: the embedding model, or why it is words-only", cobra.NoArgs,
		func(*cobra.Command) func([]string) (kbReq, error) {
			return func([]string) (kbReq, error) {
				return kbReq{method: http.MethodGet, path: kbAPI + "/search/status"}, nil
			}
		},
		func(out io.Writer, raw []byte) error {
			var doc struct {
				Mode    string  `json:"mode"`
				Status  string  `json:"status"`
				Active  *string `json:"activeModel"`
				Target  *string `json:"targetModel"`
				Reason  *string `json:"reason"`
				Pending int     `json:"pendingPassages"`
			}
			if err := decodeKb(raw, &doc); err != nil {
				return err
			}
			fmt.Fprintf(out, "Search:    %s (%s)\n", textsafe.Clean(doc.Mode), textsafe.Clean(doc.Status))
			fmt.Fprintf(out, "Model:     %s\n", orDash(textsafe.Clean(deref(doc.Active))))
			if doc.Target != nil && *doc.Target != deref(doc.Active) {
				fmt.Fprintf(out, "Switching: %s (%d passages left to embed)\n", textsafe.Clean(*doc.Target), doc.Pending)
			}
			if doc.Reason != nil {
				fmt.Fprintf(out, "Reason:    %s\n", textsafe.Clean(*doc.Reason))
			}
			return nil
		}, "")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func kbReindexCmd() *cobra.Command {
	return kbCmd("reindex", "Try embedding the pages again, after fixing the key or switching the model (admins)", cobra.NoArgs,
		func(*cobra.Command) func([]string) (kbReq, error) {
			return func([]string) (kbReq, error) {
				return kbReq{method: http.MethodPost, path: kbAPI + "/search/reindex"}, nil
			}
		}, nil, "Queued a reindex")
}

func kbUsageCmd() *cobra.Command {
	return kbCmd("usage", "Show what the knowledge base holds per space and this month's embedding tokens (admins)", cobra.NoArgs,
		func(*cobra.Command) func([]string) (kbReq, error) {
			return func([]string) (kbReq, error) { return kbReq{method: http.MethodGet, path: kbAPI + "/usage"}, nil }
		},
		func(out io.Writer, raw []byte) error {
			var doc struct {
				Spaces []struct {
					Space    string `json:"space"`
					Name     string `json:"name"`
					Pages    int    `json:"pages"`
					Passages int    `json:"passages"`
					Bytes    int64  `json:"storageBytes"`
				} `json:"spaces"`
				Personal struct {
					Spaces int   `json:"spaces"`
					Pages  int   `json:"pages"`
					Bytes  int64 `json:"storageBytes"`
				} `json:"personal"`
				Tokens int64 `json:"embeddingTokensThisMonth"`
			}
			if err := decodeKb(raw, &doc); err != nil {
				return err
			}
			rows := make([][]string, 0, len(doc.Spaces))
			for _, s := range doc.Spaces {
				rows = append(rows, []string{s.Space, s.Name, strconv.Itoa(s.Pages), strconv.Itoa(s.Passages), strconv.FormatInt(s.Bytes, 10)})
			}
			printRows(out, []string{"SPACE", "NAME", "PAGES", "PASSAGES", "BYTES"}, rows)
			fmt.Fprintf(out, "Personal: %d spaces, %d pages, %d bytes\nEmbedding tokens this month: %d\n", doc.Personal.Spaces, doc.Personal.Pages, doc.Personal.Bytes, doc.Tokens)
			return nil
		}, "")
}

func kbExportCmd() *cobra.Command {
	var f approvalFlags
	var outPath string
	var force bool
	cmd := &cobra.Command{
		Use:   "export <space>",
		Short: "Save the pages of a space you may read as a ZIP of Markdown files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := kbSpaceArg(args[0])
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Filename string `json:"filename"`
				Content  string `json:"contentBase64"`
			}
			if _, err := kbRequest(cmd, c, false, http.MethodGet, kbAPI+"/spaces/"+space+"/export", nil, &doc); err != nil {
				return err
			}
			data, err := base64.StdEncoding.DecodeString(doc.Content)
			if err != nil {
				return errors.New("the gateway's export is not valid base64")
			}
			dest := outPath
			if dest == "" {
				dest = filepath.Base(strings.ReplaceAll(doc.Filename, "\\", "/"))
				if dest == "." || dest == ".." || dest == "/" {
					dest = space + ".zip"
				}
			}
			flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
			if force {
				flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			}
			fh, err := os.OpenFile(dest, flags, 0o600)
			if err != nil {
				if errors.Is(err, os.ErrExist) {
					return fmt.Errorf("%s already exists; use --force to replace it", textsafe.Clean(dest))
				}
				return err
			}
			_, werr := fh.Write(data)
			if cerr := fh.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				return werr
			}
			if f.jsonOut {
				fmt.Fprintf(cmd.OutOrStdout(), "{\"file\":%q,\"bytes\":%d}\n", dest, len(data))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved %s (%d bytes)\n", textsafe.Clean(dest), len(data))
			return nil
		},
	}
	f.bind(cmd, "Print the saved file and its size as JSON")
	cmd.Flags().StringVar(&outPath, "out", "", "Where to save the ZIP (default: the name the gateway gives, in the current directory)")
	cmd.Flags().BoolVar(&force, "force", false, "Replace the file if it exists")
	return cmd
}

func kbImportCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "import <space> <zip>",
		Short: "Add the Markdown files of a ZIP to a space as pages or new versions (editors)",
		Long: `Each Markdown file becomes a page, or the next version of the page with the same
slug. The answer counts the pages created, updated and unchanged, and names the
files it skipped. The ZIP is at most 15 MiB.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := kbSpaceArg(args[0])
			if err != nil {
				return err
			}
			fh, err := os.Open(args[1])
			if err != nil {
				return err
			}
			defer fh.Close()
			data, err := io.ReadAll(io.LimitReader(fh, maxKbArchive+1))
			if err != nil {
				return err
			}
			if len(data) > maxKbArchive {
				return fmt.Errorf("%s is larger than %d MiB", textsafe.Clean(args[1]), maxKbArchive>>20)
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var res struct {
				Created   int      `json:"created"`
				Updated   int      `json:"updated"`
				Unchanged int      `json:"unchanged"`
				Skipped   []string `json:"skipped"`
			}
			raw, err := kbRequest(cmd, c, f.jsonOut, http.MethodPost, kbAPI+"/spaces/"+space+"/import", map[string]any{"contentBase64": base64.StdEncoding.EncodeToString(data)}, &res)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Imported into %s: %d created, %d updated, %d unchanged\n", space, res.Created, res.Updated, res.Unchanged)
			for _, name := range res.Skipped {
				fmt.Fprintf(out, "  skipped %s\n", textsafe.Clean(name))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the response body unchanged")
	return cmd
}
