# Sessions, audit and policy

Read-mostly views of the gateway's security state. Changing a policy, enrolling
a passkey or enabling strict mode is done in the dashboard, where a passkey
confirms it.

## zuano sessions

| Command | Request |
|---|---|
| `zuano sessions list [--json]` | `GET /api/me/sessions`: your browser sessions |
| `zuano sessions end <id>` (alias `revoke`) | `DELETE /api/me/sessions?id={id}` |
| `zuano sessions end-all` (alias `revoke-all`) | `DELETE /api/me/sessions?include=current`: every browser session, refresh family and CLI key, this one included; no passkey |

`end-all` also ends your refresh tokens, OAuth families and the keys your CLI
logins created, in every organisation you belong to: run `zuano login` again
afterwards.

The routes accept your CLI login: zuano authenticates with a bearer token and
sends no cookie, so no session is marked "this one". A `401` means the token
expired or was revoked; run `zuano login`.

## zuano audit

Both commands need an organisation admin.

| Command | Request |
|---|---|
| `zuano audit verify [--from D] [--to D] [--json]` | `GET /api/admin/audit/verify?from=&to=` |
| `zuano audit export [--from D] [--to D] [--format jsonl\|csv] [--user EMAIL] [--service] [--tool] [--outcome] [-o FILE] [--resume] [--org ID]` | `GET /api/org/{org}/observability/audit?...&cursor=` |

`verify` asks the gateway to recompute the hash chain and check the signed
daily checkpoints. It exits 0 when the log verifies and 1 when it does not,
naming the first broken event. `export` pages through the events and writes
them to stdout, or to `-o FILE`, which must not exist yet and is created with
mode 0600. A file export keeps its position in `FILE.cursor`; if it stops,
the same command with `--resume` continues and appends. CSV cells that start
with `=`, `+`, `-` or `@` are prefixed with a quote so a spreadsheet does not
run them. See [observability.md](observability.md).

## zuano policy

`zuano policy explain <service> <tool> [--arg k=v] [--arg-json k=<json>] [--principal KIND] [--json]`
calls `GET /api/admin/risk-policy/preview?service=&tool=&params=<json>&principal=`
and prints the decision the organisation's risk matrix, the tool's overrides and
the platform blocks give that call:

```
stripe.create_refund (destructive) is Off for cli_personal
  from the built-in default
  Can be changed by: org admins
```

The principal defaults to the session's: `cli_agent` in an agent session,
`cli_personal` otherwise. `--arg` and `--arg-json` give the arguments an
override's threshold depends on.

By default destructive and outward calls are off for everyone and writes need
an approval for every CLI session; an admin changes a cell in the dashboard.
The command is read-only.
