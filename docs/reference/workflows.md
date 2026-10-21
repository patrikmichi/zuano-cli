# Workflows

A workflow is a manifest (how it starts and its steps) installed for the
organisation with its parameters and connections. `zuano workflow` installs
templates and marketplace listings as drafts, runs and follows them, checks
manifests and saves your own. Turning a workflow on, and every standing
permission that gives it, is decided in the browser or on your phone with your
passkey; zuano only opens the page.

## Commands

| Command | Request |
|---|---|
| `zuano workflow list [--gallery] [--category C] [--service S] [--query Q] [--ready] [--json]` | `GET /api/v1/workflows/installations`, or with `--gallery` `GET /api/v1/workflows/templates` |
| `zuano workflow show <wfi_id\|template[@version]> [--json]` | `GET /installations/{id}` and `/preapprovals`, or `GET /templates/{resourceId}[?version=]` |
| `zuano workflow install <template>[@version]\|<handle>/<slug> [--name N] [--param k=v] [--params-file F] [--run-as caller\|MEMBER] [--channel C] [--no-browser]` | `POST /installations`, then opens `/workflows/{id}` |
| `zuano workflow run <installation> [--dry-run\|--test --yes] [--input F] [--set k=v] [--idempotency-key K] [--wait[=DUR]]` | `POST /{installation}/runs`, then `GET /runs/{run}` |
| `zuano workflow runs <installation> [--status S]` | `GET /{installation}/runs` |
| `zuano workflow logs <wfr_id\|installation> [--follow] [--step PATH] [--json]` | `GET /runs/{run}?after=` |
| `zuano workflow cancel <wfr_id>` | `POST /runs/{run}/cancel` |
| `zuano workflow enable <wfi_id>`, `preapprove <wfi_id> [--step ID]` | none: open `/workflows/{id}` |
| `zuano workflow pause\|disable <installation>` | `POST /installations/{id}/pause\|disable` |
| `zuano workflow upgrade <wfi_id> [--to V] [--acknowledge-major]` | `POST /installations/{id}/upgrade` |
| `zuano workflow uninstall <wfi_id> [--yes]` | `DELETE /installations/{id}` |
| `zuano workflow validate <file\|dir>` | `POST /validate` |
| `zuano workflow draft <file\|dir> [--name N] [--param k=v] [--no-browser]` | `POST /draft`, then opens `/workflows/{id}` |
| `zuano workflow schema <installation\|template[@version]> [--input\|--output]` | `GET /templates/{resourceId}` |

All of them take `--gateway-url`; the ones that print data take `--json`. The
shapes are in [gateway-contracts.md](gateway-contracts.md). An id that starts with
`wfi_` is an installation, `wfr_` is a run; an installation can also be named by
its MCP tool name. Anything else is a template id.

## Running

`run` prints the run's typed output as JSON when it succeeds. The start carries
an `Idempotency-Key` (a new one per command, or `--idempotency-key`), so a start
repeated with the same key returns the same run. `--wait` asks the gateway to hold
the request for up to 55 seconds and then polls the run; plain `--wait` is five
minutes. `--input` is a JSON file and `--set key=value` adds or overrides one
field (the value is read as JSON when it is, text otherwise).

`--dry-run` reads for real and shows, for each step, what it would do, without
changing anything; the steps are printed instead of the output. `--test` runs it
for real with the given input, under the normal approvals, and asks first
(`--yes` skips the question).

## Editing a draft

`zuano workflow pull <workflow> [--dir]` writes the draft's `workflow.json` and
its saved samples to `samples/`. Edit the file, then `zuano workflow push <dir>`
saves the difference as one revision and prints what the draft needs
(`--message` names a checkpoint). A directory that was not pulled creates a new
draft. If the draft changed since the pull, push exits 25: pull it again and
redo the edit; `pull` refuses to overwrite unpushed edits without `--force`.

`zuano workflow diff <workflow> --from v:1.2.0 [--to draft]` summarises the
steps, permissions and schemas that changed and the version bump the change
calls for. `simulate <workflow> --input file.json | --sample name | --fixtures`
dry-runs the draft, waits, and reports each run and the branches taken.
`publish <workflow> --version 1.3.0` publishes a revision; it never changes an
installation.

## Pre-approvals

A pre-approval lets one outward step, such as a message to a Slack channel, run
without an approval each time. It is pinned to a target, capped per day, limited
in time and signed with a passkey. Any change to the manifest, its parameters,
the run-as member or the connection voids it, and the step then asks for approval
each run until it is signed again. `show` lists each pre-approval, `upgrade`
voids them, and `zuano doctor` reports workflows that are paused.

## Agents

An agent session (`ZUANO_AGENT` set) may `list`, `show`, `runs`, `logs`,
`cancel`, `validate`, `schema`, `install` or `draft` an installation and `run`;
`enable`, `preapprove`, `uninstall` and an upgrade to a major version are
refused with exit 14 and the gateway refuses them too.

## Exit statuses

| Status | Meaning |
|---|---|
| 0 | the run succeeded, or the change was made |
| 1 | failed, or the run was cancelled or skipped |
| 10 | a step was denied |
| 11 | a step's approval expired |
| 12 | the run waits for an approval (the link is printed) |
| 13 | rate limited |
| 14 | a person must do this in the browser with a passkey |
| 16 | `validate`, `draft` and `push`: the manifest has issues, each printed on its own line |
| 17 | `--wait` ran out, or a gateway request hit the time limit |
| 18 | the input does not match the workflow's input schema |
| 20 | the run failed |
| 25 | `push`: the draft changed since `pull` |
| 30-33 | the plan does not allow it; see [billing.md](billing.md) |

## Triggers

A workflow that starts from an event declares the trigger in its manifest. To wake
a run that waits for an event, `zuano triggers create <type> --signal-workflow W
--signal NAME --correlate TEMPLATE` sends each event as a signal; see
[triggers.md](triggers.md).
