# Knowledge base

`zuano kb` reads and writes the organisation's knowledge base: Markdown pages
grouped in spaces, and the documents indexed from its tools. The requests are
the gateway's `/api/v1/kb` routes (the OpenAPI operations `kb.*`).

## Who calls what

A signed-in person (the human profile, or a login session) calls the REST
routes. An agent credential (`ZUANO_AGENT`, or a key the gateway marks as an
agent's) is refused on every REST route with a `403`; it reaches the knowledge
base through the gateway's tools instead, which are the same four commands:

| Command | Person | Agent credential |
|---|---|---|
| `search` | `GET /kb/search` | tool `kb_search` |
| `get` | `GET /kb/spaces/{space}/pages/{slug}`, `GET /kb/pages/{id}` or `GET /kb/documents/{id}` | tool `kb_get` |
| `write` | looks the page up, then `POST /kb/spaces/{space}/pages` or `POST /kb/pages/{id}/versions` | tool `kb_write`, held for approval |
| `link` | `POST` or `DELETE /kb/pages/{id}/run-links` for `--kind runs`; tool `kb_link` for `conflicts` | tool `kb_link`, held for approval |

A write or a link from an agent credential is held until a person approves it:
the command prints the approval and exits 12, or with `--wait[=SECONDS]`
collects the result like `zuano approvals wait`. `--no-browser` prints an
approval link instead of opening it. The tool results withhold page text that
reads like instructions to a model; the text printed by the REST commands comes
from the network and is shown as data.

## Commands

| Command | Use |
|---|---|
| `zuano kb search <query> [--space S] [--limit N] [--json]` | passages of the pages and indexed documents you may read; `--limit` is 1 to 25. A search that ranks by words only says why |
| `zuano kb get <space/slug \| kpg_... \| ksd_...> [--version N] [--json]` | a page and its Markdown, or a document indexed from another tool (its read is audited) |
| `zuano kb write <space/slug> --file <path \| -> [--title T] [--wait] [--no-browser] [--json]` | create the page, or add the text as its next version; at most 512 KiB; needs the editor role |
| `zuano kb link <page> --kind runs\|conflicts --target T [--remove] [--wait] [--no-browser] [--json]` | `runs`: link to `skill:<id>`, `workflow:<id>` or `schedule:<id>`; `conflicts`: file a conflict with another page (by id) and tell the owners; `--remove` takes a `runs` link away |
| `zuano kb status [--json]` | which embedding model search ranks with, or why it is words-only |
| `zuano kb reindex [--json]` | try embedding the pages again after fixing the key or switching the model (admins) |
| `zuano kb usage [--json]` | pages, passages and bytes per space and this month's embedding tokens (admins) |
| `zuano kb export <space> [--out PATH] [--force] [--json]` | `GET /kb/spaces/{space}/export`: a ZIP of the readable pages as Markdown files an Obsidian vault can open |
| `zuano kb import <space> <zip> [--json]` | `POST /kb/spaces/{space}/import`: each Markdown file becomes a page or the next version of the page with that slug; at most 15 MiB; needs the editor role |

### Spaces

| Command | Use |
|---|---|
| `zuano kb spaces list` | the spaces you may read, with your role |
| `zuano kb spaces show <space>` | one space |
| `zuano kb spaces create <slug> --name N [--default-access none\|reader\|editor] [--review-days N]` | an organisation space |
| `zuano kb spaces update <space> [--name N] [--default-access A] [--review-days N]` | change one |
| `zuano kb spaces graph <space>` | the readable pages and the wikilinks among them |
| `zuano kb spaces routines <space>` | the workflows and schedules its pages link to |
| `zuano kb spaces members list <space>` | who has a role |
| `zuano kb spaces members set <space> (--user EMAIL \| --group NAME) --role reader\|editor\|admin` | give a role |
| `zuano kb spaces members remove <space> (--user EMAIL \| --group NAME)` | take it away |
| `zuano kb spaces git show <space>` | the repository the space is kept in step with and how the last sync went (admins) |
| `zuano kb spaces git set <space> --repo OWNER/NAME [--branch B] [--path P]` | keep the space in step with a folder of a repository the Zuano GitHub App can reach; commits become page versions by their authors, who must be members who can edit the space |
| `zuano kb spaces git remove <space>` | stop syncing with Git; the pages stay |
| `zuano kb spaces git sync <space>` | sync now; the space also syncs hourly |
| `zuano kb spaces git push enable <space>` | sync the moment the branch is pushed to; needs access to manage the repository's webhooks |
| `zuano kb spaces git push disable <space>` | stop syncing on pushes; the hourly sync continues |

### Pages

A page is named by id (`kpg_...`) or by `space/slug`.

| Command | Use |
|---|---|
| `zuano kb pages list <space>` | the pages you may read; `stale` marks one past its review date |
| `zuano kb pages versions <page>` | its versions, newest first |
| `zuano kb pages backlinks <page>` | the readable pages that link to it |
| `zuano kb pages runs <page>` | the skills, workflows and schedules it links to, with their last run |
| `zuano kb pages verify <page>` | mark it checked by you and start the next review period (editors) |
| `zuano kb pages status <page> draft\|published\|archived` | publish, archive or return it to draft |
| `zuano kb pages promote <page> --space S` | copy a personal page into an organisation space as a draft |
| `zuano kb pages grants <page>` | who has a role on this page (admins) |
| `zuano kb pages grant <page> (--user EMAIL \| --group NAME) --role none\|reader\|editor\|admin` | a role on one page, over the space's; `none` removes access |
| `zuano kb pages ungrant <page> (--user EMAIL \| --group NAME)` | remove a page grant |

### Sources

A source indexes another tool into a space; each document keeps the readers it
has in the tool. Admins only. The config is a JSON object on a file or standard
input, never a flag.

| Command | Use |
|---|---|
| `zuano kb sources list` | the indexed tools, their schedule, status and document count |
| `zuano kb sources show <id>` | one, with its last sync |
| `zuano kb sources create --kind K --name N --space S --config-file F [--schedule hourly\|daily\|manual]` | `K` is `google_drive`, `notion`, `confluence`, `slack`, `github` or `jira` |
| `zuano kb sources update <id> [--name N] [--schedule S] [--status active\|paused] [--config-file F]` | rename, reschedule, pause or change what it indexes |
| `zuano kb sources delete <id> --yes` | stop indexing and remove its documents, unless they are under legal hold (`under_hold`) |
| `zuano kb sources sync <id>` | sync now |
| `zuano kb sources push enable <id>` | sync a Google Drive or GitHub source the moment the provider reports a change; the schedule continues |
| `zuano kb sources push disable <id>` | stop syncing on provider changes |
| `zuano kb sources documents <id>` | its documents with how many readers each has; `ADMINS ONLY` marks one no reader could be matched for |

`--json` on any command prints the response body unchanged.

## Exit status

| Status | Meaning |
|---|---|
| 1 | a request the gateway refuses as wrong: `not_found`, `validation_error`, `payload_too_large`, `space_exists`, `page_exists`, `under_hold` |
| 10 | a denial: `forbidden` (the role is too low, or an agent credential on a REST route) |
| 12 | an agent credential's write or link is held for approval |
| 25 | `version_conflict`: the page changed between the read and the write; read it again |
| 30-33 | the plan, an allowance, billing or a suspension; see [billing.md](billing.md) |
