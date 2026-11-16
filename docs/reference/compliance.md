# Compliance packs and evidence bundles

A compliance pack maps a framework's controls (SOC 2, ISO 27001, GDPR, EU AI
Act, NIS2 and DORA) to evidence the gateway holds. An export is a signed bundle
for a date range that an auditor can check without trusting the gateway. A pack
shows what the gateway can evidence and where a person must supply it; it is not
legal advice and does not certify compliance.

## Commands

| Command | Request |
|---|---|
| `zuano compliance packs [--from D --to D]` | `GET /api/compliance/packs` |
| `zuano compliance controls --pack P [--from D --to D] [--gaps]` | `GET /api/compliance/packs/{pack}/controls` |
| `zuano compliance export --pack P --from D --to D [--wait [--wait-timeout T] [--out F]]` | `POST /api/compliance/exports`, the job, then `GET .../{id}/download` |
| `zuano compliance exports list\|show <id>\|delete <id>` | `/api/compliance/exports[/{id}]` |
| `zuano compliance schedules list\|create\|delete <id>` | `/api/compliance/schedules[/{id}]` |
| `zuano compliance share <export> --to EMAIL [--expires 14d]` | `POST /api/compliance/exports/{id}/shares` |
| `zuano compliance verify <volume.zip>... [--keys F]` | none for a keys file; otherwise `GET /.well-known/zuano-evidence-keys` |
| `zuano compliance incidents list\|show <id>\|create` | `/api/compliance/incidents` |
| `zuano compliance dsr list\|create\|execute <id>` | `/api/compliance/dsr` |
| `zuano compliance access-review start\|status <id>\|apply <id>` | `/api/compliance/access-reviews` |

Dates are `YYYY-MM-DD`; a range includes `--from` and stops before `--to`. The
shapes are in [gateway-contracts.md](gateway-contracts.md).

`export --wait` follows the job and, with `--out`, downloads the bundle from its
short-lived addresses, one per volume (https, or on the gateway; no credential is
sent). A bundle of several volumes needs `--out` to be a directory; the gateway's
file names are used, reduced to their last path element, and a failed volume
removes the ones already written. The gateway gives no hash for a volume: check
the result with `verify`. Exit 0 ready,
4 still running when `--wait-timeout` ended, 20 the export failed, 30-33 the plan
does not allow it.

`share` prints a link for the auditor, who needs a code sent to the email you
name; it stops working after `--expires`.

## Verifying a bundle

`verify` needs no login. Give it every volume archive of the bundle, in any
order (a bundle over 2 GiB is split; the manifest is in the last volume). Nothing
is extracted: each file is hashed as it streams out of its archive. It checks:

1. the manifest's Ed25519 signature, made over the canonical JSON of
   `{"purpose":"compliance-manifest","document":<manifest>}`, with an evidence
   key from a source you trust. The key never comes from the bundle: a forged
   bundle carries its own `keys/public-keys.json`, so that file's evidence keys
   are ignored. The key is the one in `--keys`, or the key set the configured
   gateway publishes at `/.well-known/zuano-evidence-keys` over https;
2. that every file the manifest lists has the size, hash and volume given there,
   that no listed file is missing, that the archives hold nothing else, and that
   every volume is present;
3. the audit log: for each UTC hour the Merkle root recomputed from the rows
   (leaf `SHA-256(0x00 || canonical JSON of the row's non-null columns)`, pairs
   `SHA-256(0x01 || left || right)`, an odd node carried up) must equal the
   hour's checkpoint, with its row count and first and last id; the checkpoint's
   signature is checked with the audit key its `kid` names. The gateway does not
   publish audit keys, so they are read from the bundle's `keys/public-keys.json`,
   which the signed manifest covers by hash; `--keys` may carry `audit` keys to
   use instead. Rows must be in ascending id order, inside the manifest's audit
   range, of the manifest's organization, and the parts numbered from 1 without a
   gap; `audit/verification.json` must equal the verification recomputed here.

Archive entries that could escape a folder or hide another are refused: parent
directory or absolute paths, backslashes, colons, control characters, symbolic
links, devices, directories, encrypted entries and repeated names. An entry
whose declared size differs from the manifest is not read, and the files may
add up to at most `--max-bytes` (16 GiB by default). Failure messages name the
file or the audit hour and never print rows or file content.

An audit hour with rows and no signed checkpoint is not verified: it is shown as
a warning, and `--require-checkpoints` makes it a failure. Exit 0 when valid; 45
when not, naming each file or audit hour that failed; 46 when the public keys
could not be obtained. `--keys` takes a file you got from the operator by another
route, the stricter way: `{"keys": [...]}` or `{"evidence": [...], "audit":
[...]}`.

## Agents

Everything that changes something (export, schedules, share, delete, incident,
request, access review) is refused while `ZUANO_AGENT` is set (exit 10); the
read commands and `verify` are not. `dsr execute` (export or erase a person's
data) and `access-review apply` (remove the access the reviewers revoked) ask
first and need a passkey confirmation.
