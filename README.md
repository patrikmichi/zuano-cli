# Zuano

`zuano` is a CLI for managing AI agent skills (Claude, Codex, OpenCode,
Cursor, Cline, Gemini CLI, Windsurf — see [Skill provider
support](#skill-provider-support)) and agent definitions (Claude, opencode,
Codex, Cursor, Gemini CLI — see [Agent provider
support](#agent-provider-support)) across providers, and, when you're
connected to a gateway, for pulling skills/agents/MCP servers from a shared
catalog. Every command works fully offline against your local machine; a
handful of catalog commands need a gateway to reach. `zuano providers` is
the live, authoritative list of loaded providers — it never goes stale as
new providers are added.

## Install

> **v0.1.0 is withdrawn.** Its binaries had a private gateway hostname
> compiled in and its tag had no source. Upgrade to v0.2.0 or later; see the
> [v0.2.0 release notes](docs/releases/v0.2.0.md).

**Homebrew (macOS/Linux):**

```bash
brew install patrikmichi/tap/zuano
```

**Scoop (Windows):**

```powershell
scoop bucket add patrikmichi https://github.com/patrikmichi/scoop-bucket
scoop install patrikmichi/zuano
```

**Go install:**

```bash
go install github.com/patrikmichi/zuano-cli/cmd/zuano@latest
```

**Direct download:** archives for macOS, Linux and Windows (amd64, arm64) are
on the [releases page](https://github.com/patrikmichi/zuano-cli/releases).
`SHA256SUMS.txt` is signed with Sigstore cosign, and every archive has an
SBOM and a GitHub build provenance attestation
(`gh attestation verify <archive> --repo patrikmichi/zuano-cli`).

### Upgrading from Relay

The command and release packages are now named `zuano`; source builds use
`make install`. On first use, existing `~/.config/relay` (or older `~/.config/gw`)
configuration, provider overrides and installation manifests move to
`~/.config/zuano` only if that directory does not already exist. Existing Zuano
configuration wins. The `gw` installation alias remains available.

Use `ZUANO_URL`, `ZUANO_API_KEY` and `ZUANO_EMAIL` for environment overrides.
The older `GATEWAY_URL`, `GATEWAY_API_KEY` and `RELAY_EMAIL` remain fallbacks.
Existing `.relay` project overrides remain readable, with `.zuano` taking precedence.
OAuth client and keychain identifiers retain their existing values for gateway
compatibility and to preserve saved sessions; login again is not required by the rename.
Historical release notes describe the old artifact names.

Every install is a single static `zuano` binary with no runtime
dependencies and no built-in gateway: point it at one with
`zuano config set-gateway <url>` or `GATEWAY_URL`.

## Quickstart

```bash
# Authenticate against a gateway (only needed for catalog commands below).
zuano login

# Fully offline: migrate a skill you already have installed for one
# provider so it's also available to another.
zuano skill migrate my-skill --from claude --to codex

# Requires a gateway: install a skill by catalog id/slug.
zuano skill install pr-triage --to claude
```

```bash
# Fully offline: migrate an installed agent from one provider to another.
zuano agent migrate reviewer --from claude --to opencode

# Requires a gateway: install an agent by catalog id/slug.
zuano agent install res_abc123 --to claude
```

> `zuano mcp list` reads the catalog's MCP-server resources (read-only).
> `zuano mcp install`/`zuano mcp remove` — writing an MCP-server registration
> into a per-provider client config — are deliberately NOT part of this CLI;
> see [MCP server support](#mcp-server-support) for why.

## Skill provider support

`zuano skill migrate/install/list/diff/scan/score/uninstall/rollback` work
offline against 7 providers, every one vendor-verified against its own
docs (run `zuano providers` for the live, detected list — this table is a
point-in-time summary, not the source of truth). "Extra fields" is what
that provider's on-disk format can carry beyond the universal name +
description; migrating a skill that uses one of them onto a provider whose
column doesn't list it reports that field `[dropped]` in the fidelity
report (verified 2026-09-05 against a real 7-way `skill migrate` run):

| Provider | Format status | Extra fields carried |
|---|---|---|
| **Claude Code** | supported | allowed-tools, disable-model-invocation, license |
| **Codex** | supported | (name + description only; sidecar `agents/openai.yaml` carries Codex-only interface/policy/dependency extras) |
| **opencode** | supported | license, compatibility, metadata |
| **Cursor** | supported | paths, disable-model-invocation, metadata |
| **Cline** | supported | metadata |
| **Gemini CLI** | supported | metadata |
| **Windsurf** | supported | metadata |

## Agent provider support

`zuano agent migrate/list/diff/scan/uninstall/rollback` work offline against
5 providers; `zuano agent install <catalog-id>` (gateway-only — installs an
agent definition from the catalog, same fan-out/`--to`/manifest/rollback
model as `zuano skill install`) projects onto the same 5:

| Provider | Shape | Support |
|---|---|---|
| **Claude Code** | flat `<name>.md` (frontmatter + body) at `~/.claude/agents/` (user) / `.claude/agents/` (project) | **Supported** |
| **opencode** | flat `<name>.md` (frontmatter + body) at `~/.config/opencode/agents/` (user) / `.opencode/agents/` (project); `tools` is reshaped to/from opencode's `{tool: bool}` map | **Supported** |
| **Codex** | flat `<name>.toml` (GA 2026-03-16 custom-agent format — NOT the deprecated `[profiles.*]` block) at `~/.codex/agents/` (user) / `.codex/agents/` (project); no frontmatter/body split — the instructions live in a `developer_instructions` TOML string key | **Supported** — via a stdlib-only flat-TOML codec, no new dependency |
| **Cursor** | flat `<name>.md` (frontmatter + body) at `~/.cursor/agents/` (user) / `.cursor/agents/` (project, also reads `.claude/agents/`/`.codex/agents/` for compat) | **Supported** — no `tools` allowlist key (subagents inherit the parent's tools) |
| **Gemini CLI** | flat `<name>.md` (frontmatter + body) at `~/.gemini/agents/` (user) / `.gemini/agents/` (project) | **Supported** — `tools` carried as a real list |
| **Cline**, **Windsurf** | no subagent/custom-agent file primitive published | **Unsupported** — `agent migrate --to cline` (or windsurf) errors naming the supported agent providers instead of silently no-op'ing |

Fidelity matrix (verified 2026-09-05 against a real 5-way `agent migrate`
run) — `x` = preserved/carried, `~` = degraded (reshaped or alias-mapped,
not silently identical), `-` = dropped, reported in the fidelity report:

| Field | claude | opencode | codex | cursor | gemini-cli |
|---|---|---|---|---|---|
| Name/Description/Body | x | x | x | x | x |
| Model | x | ~ (alias-mapped) | ~ (passthrough, no alias table) | ~ (passthrough, no alias table) | ~ (passthrough, no alias table) |
| Tools | x | ~ (list ↔ `{tool: bool}` map) | - (no tools key) | - (no tools key) | x (real list) |
| Temperature | - | x | - | - | x |
| Mode | - | x | - | - | - |
| Memory | x | - | - | - | - |
| Skills | x | - | - | - | - |

Skill management (`zuano skill ...`) supports 7 providers — see [Skill
provider support](#skill-provider-support) above.

## MCP server support

`zuano mcp list` is read-only: it prints the gateway catalog's `mcp_server`
resources (id, slug, name, registration source, current published version).
That's the full extent of MCP-server management this CLI ships.

`zuano mcp install`/`zuano mcp remove` are **deliberately NOT implemented.**
Unlike a skill or agent (both project onto a single markdown-shaped file per
provider), registering an MCP server means writing into a fourth, genuinely
different artifact kind — a per-provider client config file
(`~/.claude.json`, `~/.codex/config.toml`, `.cursor/mcp.json`, ...) — each
with its own schema and its own IR, so it's out of scope for this CLI;
`zuano mcp list` gives you the id/slug to register a server manually today.

`zuano sync` is **also NOT** a portable way to distribute MCP servers,
skills, or agents to non-Claude-Code providers — see the next paragraph.

## `zuano sync` is Claude-Code-only — no `--to <provider>`

`zuano sync` materializes a Claude-Code-specific plugin **marketplace**
(`marketplace.json` + plugin bundles + a managed-settings fragment) — a
distribution format with no analogue in codex/cursor/gemini-cli/opencode.
`zuano sync --to <provider>` is rejected outright rather than silently
no-op'ing or guessing at an unsupported projection:

```
$ zuano sync --to cursor
zuano sync --to cursor is not supported: sync materializes a
Claude-Code-specific plugin marketplace with no per-provider equivalent —
use `zuano skill install <catalog-id> --to cursor` and
`zuano agent install <catalog-id> --to cursor` instead
```

The portable path to the other providers is per-artifact catalog install:
`zuano skill install <catalog-id> --to <provider>` and
`zuano agent install <catalog-id> --to <provider>`.

## The offline-vs-gateway model

Every `zuano` command falls into exactly one of two buckets:

| | LOCAL | GATEWAY |
|---|---|---|
| Touches | Only files on this machine (`~/.claude/skills/`, `~/.claude/agents/`, `~/.codex/`, `~/.cursor/`, `~/.config/opencode/`, `~/.gemini/`, and zuano's own manifest) | A configured gateway over HTTP |
| Needs auth | No | Yes — `zuano login` or `GATEWAY_API_KEY` |
| Examples | `zuano skill migrate`, `zuano skill install <path>`, `zuano skill list`, `zuano skill diff`, `zuano skill scan`, `zuano skill uninstall`, `zuano skill rollback`, `zuano agent migrate`, `zuano agent list`, `zuano agent diff`, `zuano agent scan`, `zuano agent uninstall`, `zuano agent rollback`, `zuano providers` | `zuano skill install <catalog-id>`, `zuano skill search`, `zuano agent install <catalog-id>`, `zuano mcp list`, `zuano publish`, `zuano sync`, `zuano services`, `zuano call`, `zuano help-tools`, `zuano login`/`logout`/`whoami`/`authorize`/`tokens` |

**Fail-closed guidance.** If a GATEWAY command can't resolve a gateway URL —
none configured, `--offline` passed, or you're not authenticated — it refuses
to run rather than guessing, and prints:

```
no gateway configured. Run `zuano config set-gateway <url>` then `zuano login`
to install from the catalog. Local `zuano skill install <path>` and
`zuano skill migrate` work offline.
```

`--offline` is a root flag (`zuano --offline <command>`) that forces this
fail-closed behavior even if a gateway is otherwise configured — useful for
scripts/CI that must never make a network call.

## Command reference

| Command | Kind | Description |
|---|---|---|
| `zuano login [--device]` | gateway | Authenticate via Google OAuth (browser or device-code flow) |
| `zuano logout` | gateway | Revoke the current session and remove the stored token |
| `zuano whoami [--full]` | gateway | Show the current authenticated identity |
| `zuano authorize <service>` | gateway | Authorize a specific service (e.g. Google Workspace scopes) |
| `zuano tokens list` | gateway | Show active session info |
| `zuano tokens revoke` | gateway | Revoke the current session token |
| `zuano config set-gateway <url>` | local | Persist a gateway URL to `~/.config/zuano/config.json` |
| `zuano config get-gateway` | local | Print the effective gateway URL |
| `zuano config show` | local | Print all resolved config as JSON |
| `zuano services` | gateway | List services available on the configured gateway |
| `zuano help-tools [service]` | gateway | List tools for one or all services |
| `zuano call <service> <tool> [--arg k=v]` | gateway | Call a tool on a gateway service |
| `zuano sync [--dir] [--dry-run]` | gateway | Pull your marketplace manifest into a local Claude Code plugin directory (Claude-Code-only — `--to <provider>` errors naming the portable alternative) |
| `zuano publish <path> [--type] [--watch]` | gateway | Publish a skill/agent/MCP server/prompt/plugin to the catalog |
| `zuano publish status <versionId> [--watch]` | gateway | Poll a publish's review status |
| `zuano skill publish <path>` | gateway | Alias for `zuano publish` scoped to skills |
| `zuano agent publish <path>` | gateway | Publish an agent definition |
| `zuano mcp publish [--descriptor]` | gateway | Publish an MCP server descriptor |
| `zuano mcp list [--json]` | gateway | List catalog `mcp_server` resources (read-only; `install`/`remove` are not implemented) |
| `zuano skill install <path>` | local | Install a skill from a local directory/file into one or more providers |
| `zuano skill install <catalog-id>` | gateway | Install a skill by catalog id/slug into one or more providers |
| `zuano agent install <catalog-id> [--to <p>...]` | gateway | Install an agent by catalog id/slug into one or more agent providers |
| `zuano skill search [query]` | gateway | Search the catalog for installable skills |
| `zuano skill migrate <name> --from <p> [--to <p>...]` | local | Project an installed skill from one provider to another |
| `zuano skill list` | local | List installed skills across providers |
| `zuano skill diff <name>` | local | Diff a skill's projection across providers |
| `zuano skill scan <name>` / `zuano skill score <name>` | local | Inspect a skill's manifest/fidelity |
| `zuano skill uninstall <name>` | local | Remove an installed skill |
| `zuano skill rollback [manifest-entry-id]` | local | Revert to a prior manifest entry |
| `zuano agent migrate <name> --from <p> [--to <p>...]` | local | Project an installed agent from one provider to another (claude, opencode, codex, cursor, gemini-cli) |
| `zuano agent list` | local | List installed agents across agent providers |
| `zuano agent diff <name>` | local | Diff an agent's projection across providers |
| `zuano agent scan <name>` | local | Inspect an agent for dangerous shell patterns/hardcoded secrets |
| `zuano agent uninstall <name>` | local | Remove an installed agent |
| `zuano agent rollback [manifest-entry-id]` | local | Revert to a prior agent manifest entry |
| `zuano providers` | local | List supported providers and their detection status |
| `--offline` (root flag) | — | Force every gateway command in this invocation to fail closed |
| `--version` | — | Print the CLI version, commit, and build date |

Run `zuano <command> --help` for full flag documentation on any command.

## Configuration & environment

Persistent config lives at `~/.config/zuano/config.json` (migrated
automatically from the older `~/.config/gw/` if present). Manage it with
`zuano config set-gateway` / `get-gateway` / `show`, or override per-invocation
with environment variables — env always wins over the config file:

| Variable | Purpose |
|---|---|
| `GATEWAY_URL` | Gateway base URL (overrides the config file) |
| `GATEWAY_API_KEY` | Non-interactive bearer auth — use in scripts/CI instead of `zuano login` |
| `ZUANO_EMAIL` | Selects which keychain-stored session to use (overrides the last `zuano login`'d identity) |

Interactive sessions authenticate via `zuano login` (OAuth, tokens stored in
the OS keychain, auto-refreshing). Non-interactive contexts should set
`GATEWAY_URL` + `GATEWAY_API_KEY` instead.

## License

[Apache-2.0](LICENSE)
