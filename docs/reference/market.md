# Models, the workflow marketplace and publishing

`zuano models` shows which models the organisation can run and how its model
calls are paid for. `zuano market` searches the workflow marketplace and, for a
publisher, offers a version to the review. `zuano apps` asks for an app that is
not in the catalog. Routes and shapes are in
[gateway-contracts.md](gateway-contracts.md#models-marketplace-and-app-requests).

## Models

| Command | Request |
|---|---|
| `zuano models [--surface S]` | `GET /api/v1/models?surface=`: the models offered on a surface (`studio_ai`, `workflow_step`, `agent`, `eval_subject`, `eval_judge`, `internal`) and whether your own key (`byo`) or Zuano credit (`platform`) pays for each |
| `zuano models settings` | `GET /api/v1/models/settings`: the allowed sources, the default source and models, who may spend credit, and a platform pause |
| `zuano models resume` | `POST /api/v1/models/settings/resume`: lets platform model calls run again |
| `zuano models defaults get` | `GET /api/v1/models/defaults` for the client named with `--client` |
| `zuano models defaults set <surface=model>... [--unset S] [--replace]` | `PUT /api/v1/models/defaults` for that client |

Platform model calls of an organisation are paused automatically when its spend
runs away; calls on your own key are never paused. While paused, a platform call
is refused with `403` `platform_paused` (exit 1, with the gateway's sentence) and
`models settings` prints the reason and the time. `models resume` is for an owner
or admin: the gateway asks for a passkey confirmation (see
[step-up.md](step-up.md); exit 14 when nobody can give it) and answers `409`
`not_paused` when nothing is paused.

`models defaults` is for an agency whose link to a client grants the
`models:defaults` scope. Name the client with `--client ORG` (or `ZUANO_CLIENT`);
without it the command stops with exit 2 before sending anything. `set` reads the
client's current defaults, applies the given `surface=model` pairs and sends the
whole map, because the gateway replaces it; `--replace` sends exactly the pairs
named and `--unset S` drops a surface. Only the defaults change: the allowed
sources and who pays stay with the client.

## Marketplace

| Command | Request |
|---|---|
| `zuano market search [query] [--category C] [--connectors A,B] [--invocation K] [--max-risk R] [--badge B] [--sort S] [--ready] [--limit N] [--offset N]` | `GET /api/v1/market/listings` |
| `zuano market show <handle>/<slug>` | `GET /api/v1/market/listings/{handle}/{slug}` |
| `zuano market cost <handle>/<slug>` | `GET .../cost-estimate` |
| `zuano market update <installation>` | `GET /api/v1/market/installations/{id}/update` |
| `zuano market update-policy <installation> <auto_compatible\|manual> [--channel stable\|beta]` | `PUT /api/v1/market/installations/{id}/update-policy` |

Reading and searching are never metered. Install a listing with
`zuano workflow install <handle>/<slug>`.

## Publishing

A publisher is an organisation with a verified support email and domain. Its
admins can register an Ed25519 public key; from then on every submission must
carry a signature made with the matching private key, and the gateway keeps it
beside its own signature. An install checks both.

| Command | Request |
|---|---|
| `zuano market publisher signing-key generate [--key-file F]` | none: writes a new private key (PKCS#8 PEM, mode 0600) and prints the public key; never replaces a key |
| `zuano market publisher signing-key set [--key-file F \| --public-key K]` | `PUT /api/v1/market/publisher/signing-key` |
| `zuano market publisher signing-key remove` | `DELETE /api/v1/market/publisher/signing-key` |
| `zuano market submit <resource_id>@<version> --version-id ID --listing-file F [--key-file F]` | `POST /api/v1/market/submissions` |

The private key is `--key-file`, else `$ZUANO_PUBLISHER_KEY_FILE`, else
`publisher-signing-key.pem` in the configuration directory (`~/.config/zuano`). A
key file that anyone but its owner can read is refused. `set` derives the public
key from the private key unless `--public-key` gives it (raw 32 bytes, base64url).

`submit` takes the version id that `zuano workflow publish` printed and a listing
file in JSON; the screenshots the listing names are read from beside the file and
must stay inside its directory. When a signing key exists the version is signed
before it is sent: zuano reads the version's manifest and digest from the
gateway (`GET /api/v1/workflows/templates/{resource_id}?version=`) and the
publisher handle (`GET /api/v1/market/publisher`), signs the canonical JSON of
`{content_hash, handle, manifest, slug, version}` and sends it as
`publisher_signature`. Without a key nothing is signed, and the gateway decides.
A submission the gateway accepts but that needs changes (`changes_requested`)
prints the problems and exits 1.

| Exit | Gateway refusal | Meaning |
|---|---|---|
| 4 | `signature_invalid` | the signature does not match the version or the registered key |
| 64 | `signature_required` | a signing key is registered, so the submission must be signed |
| 65 | `publisher_key_missing` | a signature was sent but no key is registered |
| 66 | `publisher_not_verified` | only a verified publisher submits or registers a signing key |

## App requests

| Command | Request |
|---|---|
| `zuano apps request <name> [--url U]` | `POST /api/v1/apps/requests` |
| `zuano apps requests [--status S] [--cursor C] [--limit N]` | `GET /api/v1/apps/requests` |
| `zuano apps vote <id>` | `POST /api/v1/apps/requests/{id}/vote` |

One request per app; your organisation's vote counts once. An app that is
already available is refused.

All commands take `--gateway-url`; the ones that print data take `--json`.
