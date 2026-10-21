package cli

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const modelsSettingsAPI = "/api/v1/models/settings"

type modelSettingsDoc struct {
	AllowedSources  []string          `json:"allowed_sources"`
	DefaultSource   string            `json:"default_source"`
	DefaultModels   map[string]string `json:"default_models"`
	PlatformMembers string            `json:"platform_members"`
	ListedMembers   []string          `json:"listed_members"`
	PlatformPause   *struct {
		Reason string `json:"reason"`
		Since  string `json:"since"`
	} `json:"platform_pause"`
	PlatformAvailable bool `json:"platform_models_available"`
}

func printDefaultModels(out io.Writer, models map[string]string) {
	if len(models) == 0 {
		fmt.Fprintln(out, "No default models are set; the first allowed model of each surface is used.")
		return
	}
	surfaces := make([]string, 0, len(models))
	for s := range models {
		surfaces = append(surfaces, s)
	}
	sort.Strings(surfaces)
	for _, s := range surfaces {
		fmt.Fprintf(out, "  %-14s %s\n", textsafe.Clean(s), textsafe.Clean(models[s]))
	}
}

func modelsSettingsCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Show how the organisation's model calls are paid for, and whether platform models are paused",
		Long: `Shows the sources the organisation allows (its own key, Zuano credit), the default
source and models, who may spend credit, and whether platform model calls are
paused after unusual spend. Calls on your own key are never paused.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc modelSettingsDoc
			raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, modelsSettingsAPI, nil, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Sources allowed: %s; default %s\n", orDash(textsafe.Clean(strings.Join(doc.AllowedSources, ", "))), orDash(textsafe.Clean(doc.DefaultSource)))
			if !doc.PlatformAvailable {
				fmt.Fprintln(out, "Zuano credit is not offered on this gateway.")
			}
			fmt.Fprintf(out, "Who may spend credit: %s", orDash(textsafe.Clean(doc.PlatformMembers)))
			if len(doc.ListedMembers) > 0 {
				fmt.Fprintf(out, " (%d listed)", len(doc.ListedMembers))
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Default models:")
			printDefaultModels(out, doc.DefaultModels)
			if p := doc.PlatformPause; p != nil {
				fmt.Fprintf(out, "\nPlatform models are PAUSED since %s (%s). An admin runs `zuano models resume`.\n", textsafe.Clean(p.Since), textsafe.Clean(p.Reason))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	return cmd
}

func modelsResumeCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Let platform model calls run again after an automatic pause",
		Long: `An organisation's platform model calls are paused when its spend runs away. An
owner or admin resumes them here; the gateway asks for a passkey confirmation
first (exit 14 when nobody can give it) and refuses with 409 when nothing is paused.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPost, modelsSettingsAPI+"/resume", map[string]any{}, nil)
			if err != nil {
				return err
			}
			if f.jsonOut {
				printRaw(cmd.OutOrStdout(), raw)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Platform models run again.")
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	return cmd
}

const modelsDefaultsAPI = "/api/v1/models/defaults"

type modelDefaultsDoc struct {
	DefaultSource string            `json:"default_source"`
	DefaultModels map[string]string `json:"default_models"`
}

// requireActingClient stops a command that only means something inside a
// client organisation before it sends anything.
func requireActingClient() error {
	if client.ActingClient() == "" {
		return &ExitError{Code: exitUsage, Msg: "name the client organisation with --client (or ZUANO_CLIENT); `zuano clients` lists them"}
	}
	return nil
}

func modelsDefaultsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "defaults",
		Short: "Read or change the default model of each surface in a client organisation (agencies)",
		Long: `An agency whose link to a client grants the models:defaults scope reads and
sets that client's default model per surface. Only the defaults change: the
allowed sources and who pays stay with the client. Name the client with --client.`,
		Example: `  zuano --client org_8f2c models defaults get
  zuano --client org_8f2c models defaults set workflow_step=anthropic/claude-sonnet-5.5`,
	}
	cmd.AddCommand(modelsDefaultsGetCmd(), modelsDefaultsSetCmd())
	return cmd
}

func fetchModelDefaults(cmd *cobra.Command, c *client.Client) (modelDefaultsDoc, []byte, error) {
	var doc modelDefaultsDoc
	raw, err := gatewayJSON(cmd.Context(), c, http.MethodGet, modelsDefaultsAPI, nil, &doc)
	return doc, raw, err
}

func modelsDefaultsGetCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "get",
		Short: "The client's default source and the default model of each surface",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireActingClient(); err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			doc, raw, err := fetchModelDefaults(cmd, c)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintf(out, "Default source: %s\n", orDash(textsafe.Clean(doc.DefaultSource)))
			printDefaultModels(out, doc.DefaultModels)
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	return cmd
}

func modelsDefaultsSetCmd() *cobra.Command {
	var f approvalFlags
	var unset []string
	var replace bool
	cmd := &cobra.Command{
		Use:   "set <surface=model>...",
		Short: "Set the default model of one or more surfaces; the others keep theirs",
		Long: `Each argument is surface=model, where surface is one of ` + strings.Join(modelSurfaces, ", ") + `.
The other surfaces keep their current default unless --replace is given, which
sends exactly the surfaces named. --unset surface drops one default.`,
		Example: `  zuano --client org_8f2c models defaults set workflow_step=anthropic/claude-sonnet-5.5 agent=openai/gpt-5.2`,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && len(unset) == 0 {
				return &ExitError{Code: exitUsage, Msg: "name at least one surface=model, or --unset a surface"}
			}
			next := map[string]string{}
			for _, a := range args {
				surface, model, ok := strings.Cut(a, "=")
				if !ok || surface == "" || model == "" {
					return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("%q is not surface=model", textsafe.Clean(a))}
				}
				if err := oneOf("surface", surface, modelSurfaces); err != nil {
					return &ExitError{Code: exitUsage, Msg: err.Error()}
				}
				next[surface] = model
			}
			for _, s := range unset {
				if err := oneOf("unset", s, modelSurfaces); err != nil {
					return &ExitError{Code: exitUsage, Msg: err.Error()}
				}
				if _, set := next[s]; set {
					return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("%s is both set and unset", textsafe.Clean(s))}
				}
			}
			if err := requireActingClient(); err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			if !replace {
				current, _, err := fetchModelDefaults(cmd, c)
				if err != nil {
					return err
				}
				for s, m := range current.DefaultModels {
					if _, set := next[s]; !set {
						next[s] = m
					}
				}
			}
			for _, s := range unset {
				delete(next, s)
			}
			var doc modelDefaultsDoc
			raw, err := gatewayWrite(cmd, c, f.jsonOut, http.MethodPut, modelsDefaultsAPI, map[string]any{"defaultModels": next}, &doc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			fmt.Fprintln(out, "Default models:")
			printDefaultModels(out, doc.DefaultModels)
			return nil
		},
	}
	f.bind(cmd, "Print the gateway's answer unchanged")
	cmd.Flags().StringSliceVar(&unset, "unset", nil, "Drop the default of this surface")
	cmd.Flags().BoolVar(&replace, "replace", false, "Send exactly the surfaces named instead of merging with the current defaults")
	return cmd
}
