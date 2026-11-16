package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/brand"
	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/config"
	"github.com/patrikmichi/zuano-cli/internal/recipes"
	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// RecipesCmd returns `zuano recipes`: the playbooks the gateway serves with
// tool search, which tools to call in what order and what goes wrong.
func RecipesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recipes",
		Short: "Read the recipes agents are served and make one into a skill",
		Long: `A recipe is a playbook for using a few tools well: the steps in order, which are
optional and the pitfalls and how to avoid them. The gateway serves the best
matching ones next to tool search results. Recipes come from the platform, from
your organisation or are mined from its usage.

'show' checks the recipe's signature against the keys the gateway publishes and
exits 4, printing none of the recipe, when it does not verify. Everything printed
from a recipe has control and invisible characters removed.`,
	}
	cmd.AddCommand(recipesListCmd(), recipesShowCmd(), recipesVersionsCmd(), recipesDiffCmd(), recipesPromoteCmd(), recipesSettingsCmd())
	return cmd
}

func recipePath(id string, rest ...string) string {
	return builderAPI + "/recipes/" + url.PathEscape(id) + strings.Join(rest, "")
}

func recipeRef(arg string) (string, error) {
	if arg == "" || strings.ContainsAny(arg, "/?#%: \t\n") {
		return "", fmt.Errorf("%q is not a recipe id", textsafe.Clean(arg))
	}
	return arg, nil
}

func recipesListCmd() *cobra.Command {
	var f approvalFlags
	var service, source, status, query, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recipes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			q := map[string]string{"service": service, "source": source, "status": status, "q": query, "cursor": cursor}
			if limit > 0 {
				q["limit"] = strconv.Itoa(limit)
			}
			var doc struct {
				Items      []recipes.Summary `json:"items"`
				NextCursor string            `json:"nextCursor"`
			}
			raw, err := builderRequest(cmd, c, f.jsonOut, http.MethodGet, withQuery(builderAPI+"/recipes", q), nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			if len(doc.Items) == 0 {
				fmt.Fprintln(out, "No recipes match.")
				return nil
			}
			var rows [][]string
			for _, r := range doc.Items {
				rows = append(rows, []string{r.ID, r.Title, strings.Join(r.Services, ","), strconv.Itoa(r.StepCount), strconv.Itoa(r.PitfallCount), r.Source, r.Status, "v" + strconv.Itoa(r.Version)})
			}
			printRows(out, []string{"ID", "TITLE", "SERVICES", "STEPS", "PITFALLS", "SOURCE", "STATUS", "VERSION"}, rows)
			if doc.NextCursor != "" {
				fmt.Fprintf(out, "\nMore: --cursor %s\n", textsafe.Clean(doc.NextCursor))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the list as JSON")
	cmd.Flags().StringVar(&service, "service", "", "Only recipes that use this service")
	cmd.Flags().StringVar(&source, "source", "", "platform, mined or org")
	cmd.Flags().StringVar(&status, "status", "", "draft, active, suspended or retired")
	cmd.Flags().StringVar(&query, "query", "", "Words in the title or use case")
	cmd.Flags().IntVar(&limit, "limit", 0, "Page size, at most 100")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue from a previous page")
	return cmd
}

// recipeKeys returns the keys the gateway publishes for recipes, reused only as
// long as its answer allows.
func recipeKeys(ctx context.Context, gatewayURL, keyID string) ([]recipes.Key, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	cache := recipes.KeyCache{Path: filepath.Join(dir, "recipes", "keys.json"), Fetch: recipes.FetchKeys}
	return cache.Keys(ctx, gatewayURL, keyID)
}

func recipesShowCmd() *cobra.Command {
	var f approvalFlags
	var version int
	var org string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a recipe and check its signature",
		Long: `Reads a recipe by id and prints it only when its signature verifies: the key
comes from the gateway's published recipe keys, the content must hash to what the
signature covers, and the recipe, version, scope and organisation must be the ones
asked for. Otherwise nothing of the recipe is printed and the exit code is 4.

A platform recipe verifies for anyone. An organisation recipe verifies only for
the organisation named by --org, else the client organisation you act for, else
the one the gateway reports for your credential (GET /api/cli/whoami). When none
is known, an organisation recipe is refused and --org says which.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := recipeRef(args[0])
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var q map[string]string
			if version > 0 {
				q = map[string]string{"version": strconv.Itoa(version)}
			}
			raw, err := builderRequest(cmd, c, f.jsonOut, http.MethodGet, withQuery(recipePath(ref), q), nil, nil)
			if err != nil {
				return err
			}
			asOrg := org
			if asOrg == "" {
				asOrg = client.ActingClient()
			}
			if asOrg == "" {
				asOrg = credentialOrgID(cmd.Context(), c)
			}
			r, v := recipes.Open(raw, recipes.Expect{ID: ref, Version: version, OrgID: asOrg}, func(keyID string) ([]recipes.Key, error) {
				return recipeKeys(cmd.Context(), c.GatewayURL, keyID)
			})
			out := cmd.OutOrStdout()
			if !v.Verified {
				fmt.Fprintf(out, "signature: NOT VERIFIED: %s\n", textsafe.Strict(v.Reason))
				return &signing.ContentError{Reason: v.Reason}
			}
			if f.jsonOut {
				printRaw(out, raw)
			} else {
				renderRecipe(out, r, v)
			}
			return nil
		},
	}
	f.bind(cmd, "Print the recipe as the gateway sent it")
	cmd.Flags().IntVar(&version, "version", 0, "A version other than the current one")
	cmd.Flags().StringVar(&org, "org", "", "Organisation id an organisation recipe must be signed for")
	return cmd
}

func renderRecipe(w io.Writer, r recipes.Recipe, v recipes.Verification) {
	fmt.Fprintf(w, "%s (%s)\n", textsafe.Strict(r.Title), textsafe.Clean(r.ID))
	fmt.Fprintf(w, "%s · %d steps · %d pitfalls · %s · v%d · %s\n", textsafe.Strict(strings.Join(r.Services, ", ")), len(r.Steps), len(r.Pitfalls), textsafe.Clean(r.Source), r.Version, textsafe.Clean(r.Status))
	fmt.Fprintf(w, "Difficulty: %s (%s)\n", textsafe.Clean(r.Difficulty.Level), textsafe.Strict(r.Difficulty.Reason))
	fmt.Fprintf(w, "Use for: %s\n", textsafe.Strict(r.UseCase))
	if len(r.PrimaryTools) > 0 {
		fmt.Fprintf(w, "Tools: %s\n", textsafe.Strict(strings.Join(r.PrimaryTools, ", ")))
	}
	if len(r.RelatedTools) > 0 {
		fmt.Fprintf(w, "Related: %s\n", textsafe.Strict(strings.Join(r.RelatedTools, ", ")))
	}
	fmt.Fprintln(w, "\nSteps")
	for _, s := range r.Steps {
		kind := ""
		switch s.Kind {
		case "optional":
			kind = " (optional: " + textsafe.Strict(s.Condition) + ")"
		case "fallback":
			kind = fmt.Sprintf(" (fallback for step %d)", s.FallbackFor)
			if s.Condition != "" {
				kind = fmt.Sprintf(" (fallback for step %d: %s)", s.FallbackFor, textsafe.Strict(s.Condition))
			}
		}
		fmt.Fprintf(w, "%3d. %s%s\n", s.Order, textsafe.Strict(s.Tool), kind)
		fmt.Fprintf(w, "     %s\n", textsafe.Strict(s.Instruction))
	}
	if len(r.Pitfalls) > 0 {
		fmt.Fprintln(w, "\nPitfalls")
		last := ""
		for _, p := range r.Pitfalls {
			if p.Tool != last {
				fmt.Fprintf(w, "  %s\n", textsafe.Strict(p.Tool))
				last = p.Tool
			}
			codes := ""
			if len(p.ErrorCodes) > 0 {
				codes = " [" + textsafe.Strict(strings.Join(p.ErrorCodes, ", ")) + "]"
			}
			fmt.Fprintf(w, "    - %s%s\n      Instead: %s\n", textsafe.Strict(p.Text), codes, textsafe.Strict(p.Fix))
		}
	}
	fmt.Fprintf(w, "\nsignature: verified (%s recipe, key %s)\n", textsafe.Clean(v.Scope), textsafe.Clean(v.KeyID))
}

func recipesVersionsCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "versions <id>",
		Short: "List the versions of a recipe",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := recipeRef(args[0])
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Versions []struct {
					Version     int    `json:"version"`
					Title       string `json:"title"`
					ContentHash string `json:"contentHash"`
					CreatedAt   string `json:"createdAt"`
				} `json:"versions"`
			}
			raw, err := builderRequest(cmd, c, f.jsonOut, http.MethodGet, recipePath(ref, "/versions"), nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			var rows [][]string
			for _, v := range doc.Versions {
				rows = append(rows, []string{"v" + strconv.Itoa(v.Version), v.Title, v.ContentHash, v.CreatedAt})
			}
			printRows(out, []string{"VERSION", "TITLE", "CONTENT HASH", "CREATED"}, rows)
			return nil
		},
	}
	f.bind(cmd, "Print the versions as JSON")
	return cmd
}

func recipesPromoteCmd() *cobra.Command {
	var f approvalFlags
	var version int
	cmd := &cobra.Command{
		Use:   "promote <id>",
		Short: "Make a recipe into a draft skill your agents can install",
		Long: `Builds a skill from the recipe (its steps, pitfalls and notes), keeps it private
to you as a draft and copies the recipe's eval cases to it when the plan allows
evaluations. The draft goes through the usual publish checks and review before
others get it.

The gateway accepts this only from a signed-in browser session. When it refuses
the credential this command uses, it says so and exits 10 without retrying: make
the draft in the dashboard.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := recipeRef(args[0])
			if err != nil {
				return err
			}
			body := map[string]any{}
			if cmd.Flags().Changed("version") {
				if version < 1 {
					return errors.New("--version must be 1 or more")
				}
				body["version"] = version
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Skill struct {
					ID   string `json:"id"`
					Slug string `json:"slug"`
					URL  string `json:"url"`
				} `json:"skill"`
				VersionID    string `json:"versionId"`
				CasesSkipped string `json:"casesSkipped"`
			}
			raw, err := builderRequest(cmd, c, f.jsonOut, http.MethodPost, recipePath(ref, ":promote"), body, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Created draft skill %s (%s)\n", textsafe.Clean(doc.Skill.Slug), textsafe.Clean(doc.Skill.ID))
			if doc.Skill.URL != "" {
				if link, err := gatewayLink(c.GatewayURL, doc.Skill.URL); err == nil {
					fmt.Fprintf(out, "  %s\n", link)
				}
			}
			if doc.CasesSkipped != "" {
				fmt.Fprintf(out, "No eval cases were copied: %s\n", textsafe.Clean(doc.CasesSkipped))
			}
			fmt.Fprintf(out, "Next: score it with `%s`, then submit it with `%s`.\n", brand.Cmd("eval run --target skill:"+textsafe.Clean(doc.Skill.Slug)), brand.Cmd("skill promote "+textsafe.Clean(doc.Skill.ID)))
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer as JSON")
	cmd.Flags().IntVar(&version, "version", 0, "A version other than the one served")
	return cmd
}

func recipesDiffCmd() *cobra.Command {
	var f approvalFlags
	var from, to int
	var against string
	cmd := &cobra.Command{
		Use:   "diff <id>",
		Short: "Show what changed between two versions of a recipe, or since the platform's recipe it copies",
		Long: `Compares two versions of a recipe: --from defaults to the version being served
and --to to the latest. With --against platform, an organisation recipe that
overrides a platform recipe is compared with it: the platform version the copy
was made from against the one served now.

The gateway computes the difference and does not sign it. Use 'show' to check a
version's signature before you rely on its content.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := recipeRef(args[0])
			if err != nil {
				return err
			}
			if against != "" && against != "version" && against != "platform" {
				return errors.New("--against must be version or platform")
			}
			q := map[string]string{"against": against}
			for name, n := range map[string]int{"from": from, "to": to} {
				if !cmd.Flags().Changed(name) {
					continue
				}
				if n < 1 {
					return fmt.Errorf("--%s must be 1 or more", name)
				}
				if against == "platform" {
					return errors.New("--against platform compares the copy's base with the platform's served version; it takes no --from or --to")
				}
				q[name] = strconv.Itoa(n)
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				Diff recipes.Diff `json:"diff"`
			}
			raw, err := builderRequest(cmd, c, f.jsonOut, http.MethodGet, withQuery(recipePath(ref, "/diff"), q), nil, &doc)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			renderRecipeDiff(cmd.OutOrStdout(), doc.Diff)
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer as JSON")
	cmd.Flags().IntVar(&from, "from", 0, "The version to compare from (default: the one served)")
	cmd.Flags().IntVar(&to, "to", 0, "The version to compare to (default: the latest)")
	cmd.Flags().StringVar(&against, "against", "", "version (the default) or platform")
	return cmd
}

func renderRecipeDiff(w io.Writer, d recipes.Diff) {
	scope := "v" + strconv.Itoa(d.From) + " -> v" + strconv.Itoa(d.To)
	if d.Against == "platform" {
		scope += " of the platform recipe"
	}
	if !d.Changed {
		fmt.Fprintf(w, "No difference between v%d and v%d.\n", d.From, d.To)
		return
	}
	fmt.Fprintf(w, "%s (%s)\n", textsafe.Clean(d.RecipeID), scope)
	change := func(label string, c *recipes.Change[string]) {
		if c != nil {
			fmt.Fprintf(w, "%s: %s -> %s\n", label, textsafe.Strict(c.From), textsafe.Strict(c.To))
		}
	}
	change("Title", d.Title)
	change("Use for", d.UseCase)
	if d.Difficulty != nil {
		fmt.Fprintf(w, "Difficulty: %s -> %s (%s)\n", textsafe.Clean(d.Difficulty.From.Level), textsafe.Clean(d.Difficulty.To.Level), textsafe.Strict(d.Difficulty.To.Reason))
	}
	moved := func(label string, m recipes.Moved) {
		var parts []string
		for _, t := range m.Added {
			parts = append(parts, "+"+textsafe.Strict(t))
		}
		for _, t := range m.Removed {
			parts = append(parts, "-"+textsafe.Strict(t))
		}
		if len(parts) > 0 {
			fmt.Fprintf(w, "%s: %s\n", label, strings.Join(parts, " "))
		}
	}
	moved("Primary tools", d.PrimaryTools)
	moved("Related tools", d.RelatedTools)
	if len(d.Steps) > 0 {
		fmt.Fprintln(w, "\nSteps")
	}
	for _, s := range d.Steps {
		switch s.Change {
		case "added":
			fmt.Fprintf(w, "  + %s\n", diffStep(s.After))
		case "removed":
			fmt.Fprintf(w, "  - %s\n", diffStep(s.Before))
		default:
			fmt.Fprintf(w, "  ~ %s\n      -> %s\n", diffStep(s.Before), diffStep(s.After))
		}
	}
	if len(d.Pitfalls.Added)+len(d.Pitfalls.Removed) > 0 {
		fmt.Fprintln(w, "\nPitfalls")
	}
	for _, side := range []struct {
		sign string
		list []recipes.Pitfall
	}{{"+", d.Pitfalls.Added}, {"-", d.Pitfalls.Removed}} {
		for _, p := range side.list {
			codes := ""
			if len(p.ErrorCodes) > 0 {
				codes = " [" + textsafe.Strict(strings.Join(p.ErrorCodes, ", ")) + "]"
			}
			fmt.Fprintf(w, "  %s %s%s: %s\n", side.sign, textsafe.Strict(p.Tool), codes, textsafe.Strict(p.Text))
		}
	}
	fmt.Fprintln(w, "\nThe gateway computed this difference; it is not signature-checked. `zuano recipes show` verifies a version.")
}

func diffStep(s *recipes.Step) string {
	if s == nil {
		return ""
	}
	kind := ""
	switch s.Kind {
	case "optional":
		kind = " (optional: " + textsafe.Strict(s.Condition) + ")"
	case "fallback":
		kind = fmt.Sprintf(" (fallback for step %d)", s.FallbackFor)
	}
	return fmt.Sprintf("%d. %s%s: %s", s.Order, textsafe.Strict(s.Tool), kind, textsafe.Strict(s.Instruction))
}

func recipesSettingsCmd() *cobra.Command {
	var f approvalFlags
	var serve string
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Show or change which recipes tool search offers",
		Long: `Without flags, prints the settings. Changing them is an administrator's, is
audited, and is refused in an agent session (exit 10).

  --serve all|org_only|off   the platform's and your organisation's recipes, only
                             your organisation's, or none`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			changing := cmd.Flags().Changed("serve")
			body := map[string]any{}
			if changing {
				if err := refuseAgentChange(brand.Cmd("recipes settings")); err != nil {
					return err
				}
				if serve != "all" && serve != "org_only" && serve != "off" {
					return errors.New("--serve must be all, org_only or off")
				}
				body["serveRecipes"] = serve
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			method := http.MethodGet
			var payload any
			if changing {
				method, payload = http.MethodPut, body
			}
			var doc struct {
				ServeRecipes string `json:"serveRecipes"`
			}
			raw, err := builderRequest(cmd, c, f.jsonOut, method, builderAPI+"/org/recipe-settings", payload, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "serve: %s\n", orDash(doc.ServeRecipes))
			return nil
		},
	}
	f.bind(cmd, "Print the settings as JSON")
	cmd.Flags().StringVar(&serve, "serve", "", "all, org_only or off")
	return cmd
}
