# MCP firewall

The firewall sits between your coding agents and MCP servers. An agent sees
only the tool definitions your organisation approved. A tool that is added or
whose definition changes stays hidden until it is reviewed. Each call is
checked before it reaches the server and each result before it reaches the
agent.

## Routing servers through it

```
zuano sync --firewall            # remembered for later syncs
zuano sync --firewall=off        # servers go back as installed
zuano mcp install files --command npx --arg -y --arg @acme/files-mcp --firewall
zuano firewall import-local files    # take over a server you wrote into an agent
```

A local (stdio) server starts as `zuano mcp wrap --upstream <name> -- <command>`.
A remote server is reached through the gateway at `/api/fw/<name>/mcp`; the
gateway holds its credentials, so `--bearer-env` tokens are not sent. Sign the
gateway in to it with `zuano firewall connect <name>`.

`--move-secrets` (`--env-to-keychain` on `mcp install` and `import-local`)
stores literal environment values whose name looks like a key, token, secret or
password in the system keychain; the wrapper reads them back with
`--env-from-keychain`. The wrapper's child never sees `ZUANO_*` variables.

Servers stay in zuano's own list as installed, so turning the firewall off
needs no stored originals. Servers an agent's config holds that zuano did not
write are untouched until `firewall import-local` hands them over. Wrapping,
importing and turning the firewall on or off are refused while `ZUANO_AGENT` is
set. `zuano doctor` reports whether servers are routed, and fails (`firewall-required`)
when the organisation requires routing and an agent still has a server that is not;
`firewall import-local <name>` routes it.

## Fast reads

When the organisation turns on fast reads, `zuano mcp wrap` decides calls to
read tools itself, in milliseconds, from a signed policy snapshot it keeps for 60
seconds. A tool qualifies when it is pinned with the hash the snapshot lists, is
classified read and no deny rule matches. Results are screened on the machine
and the audit (digests, never arguments) goes to the gateway in batches every
10 seconds, at 50 entries and on exit. Writes, and every call made while the
snapshot is missing, stale or fails verification, are decided by the gateway.

## Offline

`zuano mcp wrap --offline` (also used when `--offline` is set on zuano itself)
needs no account. Tools wait in `zuano mcp changes` until you approve them with
`zuano mcp pins approve <server> <tool>` (or `--all`); approvals take effect in
a running wrapper. `~/.config/zuano/firewall.yaml` holds the `firewall:` and
`dlp:` sections of the organisation's policy document, written exactly as the
gateway reads them (snake_case, no key it does not know), so a section written
for an organisation works here unchanged:

```yaml
firewall:
  upstreams:
    files:
      on_definition_change: block   # or warn: keep serving the approved definition
      new_tools: hidden             # or auto_read_only: pin a tool classified read at once
      tools:
        allow: ["read_*", "list_*"] # only these, when the list is not empty
        deny: ["delete_*"]
        classify: {sync: read}
      approvals: {people_require_approval: [destructive]}
      dlp: secrets-and-pii
dlp:
  profiles:
    secrets-and-pii:
      detect: [secrets, pii]
      outbound: {secret: block, pii: block}
      inbound: {secret: redact}
      custom:
        - {name: project-codes, regex: "PRJ-[0-9]{6}", action: redact}
```

A tool name matches a glob where `*` is any run of characters and `?` one.
Offline there is no second person: a call of a class listed in
`approvals.people_require_approval`, and a DLP rule whose action is `approval`,
refuse the call. An upstream without a `dlp` profile gets the default one:
secrets are blocked in arguments and redacted in results. `redact` replaces the
text with the rules used for session upload, so a value in the same string that
matches another built-in detector is replaced too. Settings that only matter to
a gateway session (`sampling`, `rate`, `token_exchange` and the like) are
checked and ignored; `on_definition_change: auto` and an `external` DLP scanner
are refused. A result that carries instructions for the model is always
withheld.

Pins are in `~/.config/zuano/mcp-pins/`, the hash-chained audit log in
`~/.config/zuano/mcp-audit/<server>.jsonl`. The log holds names, decisions and
digests, never arguments or results.

## Screening

Definitions are checked for hidden instructions, references to credentials and
paths, invisible characters, cross-tool references, encoded blobs, very long
descriptions and parameters that ask for unrelated data. Results are checked
for instructions aimed at the model. With a gateway, `--screen gateway`
(default) sends arguments and results to it; `--screen local` sends only
digests and local findings. A result that cannot be checked is withheld.

## Commands

| Command | |
|---|---|
| `zuano mcp wrap [--upstream s] [--offline] [--screen local\|gateway] [--env-from-keychain A,B] -- cmd args` | run a server behind the firewall |
| `zuano mcp pins list\|approve\|reject`, `zuano mcp changes` | offline pins; `list --check` exits 43 when a tool changed |
| `zuano firewall upstreams list [--status S]`<br>`zuano firewall upstreams show <server>` | servers the gateway knows; a `<server>` is a slug or an id |
| `zuano firewall upstreams add <slug> --url U \| --package P [--name N] [--auth none\|stored\|per_user] [--tag T]... [--wait]` | add a remote server, or a package the gateway hosts; a package builds as a job (`--wait` follows it). Needs your passkey |
| `zuano firewall upstreams request <slug> --url U \| --package P [--reason R]` | ask an administrator to add a server (members) |
| `zuano firewall upstreams approve <server> [--wait]` | approve a requested server; builds a hosted package. Needs your passkey |
| `zuano firewall upstreams discover <server>` | ask the server what it offers now and compare it with what is pinned |
| `zuano firewall upstreams quarantine <server>`<br>`zuano firewall upstreams release <server>` | hold every call to a server; lift the hold (needs your passkey) |
| `zuano firewall upstreams deploy <server> [--wait]` | build and start a hosted package again |
| `zuano firewall upstreams env list <server>`<br>`zuano firewall upstreams env set <server> [--from-file F]`<br>`zuano firewall upstreams env clear <server> --yes` | the variables a hosted package starts with: names are readable, values are write-only and come from a JSON object on standard input or a file, never from flags; setting them needs your passkey |
| `zuano firewall changes list [--status S] [--server S] [--limit N]`<br>`zuano firewall changes show <id>` | the changes servers made to what they offer, with the diff and what the screening found |
| `zuano firewall changes decide <id>` | opens the page where a person approves or rejects with a passkey |
| `zuano firewall definitions list <server> [--state S] [--kind K]` | what a server offers, with each definition's state, risk and hash |
| `zuano firewall definitions decide <server> [--approve NAME]... [--reject NAME]... [--approve-all\|--reject-all]` | approve or reject waiting definitions at the hash each has now; approving needs your passkey |
| `zuano firewall definitions accept <server>...` | accept the pending definitions of several servers in one passkey decision (up to 500) |
| `zuano firewall shadow` | servers configured on devices that the firewall does not govern |
| `zuano firewall connect <slug>` | opens the page that signs the gateway in to a remote server |
| `zuano firewall import-local <name>` | bring a server from an agent's config under the firewall |

The administration routes are served under `/api/firewall`, not `/api/v1`, and
answer with the v1 refusal documents. The slugs `local`, `oauth`, `mcp`, `new`,
`changes` and `shadow` are reserved; `add` and `request` refuse them, and any
slug that is not 1 to 63 lowercase letters, digits and hyphens, before a
request.

## Passkey confirmations

Approving definitions (`firewall_pin_approve`), lifting a hold
(`firewall_release`), adding a server (`mcp_server_register`) and setting a
package's variables (`credential_write`) are refused with `step_up_required`
until a person confirms that exact change with a passkey. The commands use the
usual [step-up flow](step-up.md): zuano names the action, opens the firewall
page of the console for the two firewall actions (the front page for the others),
waits for you to confirm and sends the same request again once. Without a
person at the keyboard they end with exit status 14. `approve`, `release`,
`decide --approve`, `accept` and `env set\|clear` are refused up front while
`ZUANO_AGENT` is set. Rejecting a definition needs no passkey.

## Refusals

| Code | Status | Meaning | Exit |
|---|---|---|---|
| `member_requests_off` | 403 | members may not ask for servers; ask an administrator | 10 |
| `upstream_not_found`, `change_not_found`, `definition_not_found` | 404 | no such server, change or definition | 1 |
| `upstream_exists` | 409 | the slug is taken | 1 |
| `upstream_not_active` | 409 | only an active server can be quarantined | 1 |
| `upstream_not_pending` | 409 | only a requested server is approved | 1 |
| `upstream_not_quarantined` | 409 | the server is not held | 1 |
| `definition_not_decidable` | 409 | only a waiting definition can be decided | 1 |
| `deploy_running` | 409 | a build of the package is already running | 1 |
| `env_name_invalid` | 422 | a variable name the package cannot take | 1 |
| `federation_refused` | 422 | a vendor server cannot be added with these settings | 1 |
| `tool_list_too_large` | 422 | the server offers more tools than the firewall pins | 1 |
| `upstream_source_invalid` | 422 | the address or package name is not acceptable | 1 |
| `hosting_unavailable` | 503 | hosting packages is not available | 1 |

The gateway's sentence is printed with the code's hint. These are errors of
their class, so they take no number of their own in the exit-code registry
(400, 409 and 422 end with 1, a 403 denial with 10).

## Exit status

| Code | |
|---|---|
| 10 | a call was denied, or members may not request servers |
| 12 | a call is held for approval |
| 14 | a passkey confirmation is needed and nobody could give it |
| 43 | a server changed a tool it was approved with |
| 44 | the server is quarantined |

The full list is in [gateway-contracts.md](gateway-contracts.md#cli-exit-codes).
