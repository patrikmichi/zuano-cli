# Recipes

A recipe is a playbook for using a few tools well: the steps in order, which
are optional or fall back to another, and the pitfalls and what to do instead.
The gateway serves the best matching ones next to tool search results. They come
from the platform, from your organisation, or are mined from its usage.

| Command | |
|---|---|
| `zuano recipes list [--service S] [--source platform\|mined\|org] [--status S] [--query Q] [--limit N] [--cursor C]` | id, title, services, steps, pitfalls, source, status, version |
| `zuano recipes show <id> [--version N] [--org ORG]` | the recipe in full, printed only when its signature checks |
| `zuano recipes versions <id>` | the versions of a recipe with the hash of each version's content |
| `zuano recipes diff <id> [--from N] [--to N] [--against version\|platform]` | what changed between two versions, or since the platform recipe an organisation recipe copies |
| `zuano recipes promote <id> [--version N]` | make it into a draft skill; prints the next commands |
| `zuano recipes settings [--serve all\|org_only\|off]` | show or change which recipes tool search offers |

`--json` prints what the gateway sent, and `--gateway-url` names the gateway.
Recipes are read by id (`rcp_...`, as `list` shows it), not by slug.

## What `show` prints

The title and a header line (services, steps, pitfalls, source, version,
status); the difficulty with its reason; the primary and related tools; the
numbered steps, with `optional` and `fallback` steps carrying their condition;
the pitfalls grouped by tool with their error codes and the fix; and the
signature line.

Output has no colour, so `NO_COLOR` needs nothing. Text in a recipe may come
from outside your organisation, so control, bidirectional-override and
zero-width characters are removed from everything printed.

## Signatures

The platform signs a platform recipe with its recipe key, and an organisation's
recipe is signed for that organisation when it is activated. `show` checks the
signature without asking the gateway to vouch for it, and each check refuses the
recipe when it fails:

1. the keys come from the gateway you are signed in to, at
   `/api/v1/recipes/keys` over https, never from the recipe's own answer. They
   are reused only for as long as the gateway's answer allows (five minutes
   today, an hour at most) and fetched again when a recipe names a key the set does not
   have, as after a rotation; a key the gateway has withdrawn stops verifying
   once that time has passed. A set that has a key without a `keyId`, still
   uses the older `kid` field or repeats an id is refused whole;
2. the key named by the signature's `keyId` is in that set and is an `ed25519`
   key; no other algorithm is accepted;
3. the content hash is computed again from the recipe as received (its title,
   use case, difficulty, primary and related tools, steps and pitfalls, with
   nothing dropped or added) and equals the recipe's `contentHash`;
4. the signature verifies over the hash together with the recipe's id, its
   version, its scope and its organisation, so a version cannot be moved to
   another recipe, version or organisation;
5. the recipe, version, scope and organisation are the ones asked for. A
   platform recipe is signed with scope `platform` and no organisation. An
   organisation recipe verifies only for the organisation named by `--org`, else
   the client organisation you act for, else the `orgId` the gateway reports for
   your credential at `GET /api/cli/whoami`; when none is known, and for any
   other organisation, the recipe is refused.

An answer with a repeated JSON key is refused. Slug, status, services, source and
timestamps are not signed and are shown as the gateway sent them.

A recipe that fails any check is not printed, not shown as verified and not kept:
the command prints `signature: NOT VERIFIED` with the reason and exits 4. Do not
follow it.

## Diff

`diff` compares two versions: `--from` defaults to the version being served and
`--to` to the latest. `--against platform` compares an organisation recipe that
overrides a platform recipe with it: the platform version the copy was made from
against the one served now (it takes no `--from` or `--to`). It prints only what
differs: title, use case, difficulty, tools added or removed, steps added (`+`),
removed (`-`) or changed (`~`), and pitfalls added or removed. The gateway
computes the difference and does not sign it, so use `show` to verify a version
before relying on its content. The same character filtering as `show` applies.

## Promote

`promote` makes the served version (or `--version`) into a private draft skill and
copies the recipe's eval cases to it when the plan allows evaluations; when it
does not, the command says no cases were copied. The gateway accepts promotion
only from a signed-in browser session. When it refuses the credential the CLI
uses (`browser_session_required`, exit 10), the command says the draft has to be
made in the dashboard and does not retry.

## Settings

Without flags, `settings` prints them. `--serve` chooses which recipes tool
search offers (`all`, only your organisation's, or none). Changing it is an
administrator's and is audited; it is refused in an agent session (exit 10).

## Exit status

| Status | Meaning |
|---|---|
| 4 | the recipe's signature does not verify |
| 10 | refused for an agent session, not allowed, or (promote) only a browser session may do it |
| 12 | an agent session's write is held for approval |
| 13 | rate limited |
| 30-33 | the plan does not allow it; see [billing.md](billing.md) |
