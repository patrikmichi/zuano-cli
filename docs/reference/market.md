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

`zuano publisher` is the same group as `zuano market publisher`.

| Command | Request |
|---|---|
| `zuano market publisher init --handle H --name N --support-email E --country CC [--website U] [--support-url U] [--privacy-url U] [--bio T]` | `POST /api/v1/market/publisher`: creates the profile as a community publisher (an admin; the plan must include publishing) |
| `zuano market publisher status [--listings]` | `GET /api/v1/market/publisher`: the profile and which proofs it has given; `--listings` reads `GET /api/v1/market/publisher/listings` |
| `zuano market publisher verify email [--code C]` | `POST .../verifications/email` mails a code to the support address (one a minute, valid 24 hours); with `--code`, `POST .../verifications/email/confirm` |
| `zuano market publisher verify domain <domain>` | `POST .../verifications/domain`: prints the DNS TXT record and the well-known file that prove control |
| `zuano market publisher verify domain --check` | `POST .../verifications/domain/check`: looks for either and confirms the domain |
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
| 67 | `review_not_eligible` | only an organisation with three successful live runs, and an admin or the installer, reviews; a publisher never reviews its own listing |
| 68 | `notice_too_short` | an end date needs 7 days of notice for a free listing and 90 for a paid one, and must be in the future |
| 69 | `version_not_withdrawable` | `version_not_published` (only a published version is deprecated; a published or deprecated one is yanked) or `version_already_yanked` |
| 70 | `verification_failed` | `invalid_token` (a wrong or expired code), `verification_expired`, or a domain check that found no proof yet |
| 71 | `handle_unavailable` | `handle_taken` or `handle_reserved` |

## Withdrawing, reviews and reports

The same commands are under `zuano workflow market`. Publisher actions are for
the admins of the publisher organisation.

| Command | Request |
|---|---|
| `zuano market deprecate <handle>/<slug>@<version> [--note T]` | `POST /api/v1/market/listings/{handle}/{slug}/versions/{version}/deprecate`: hides the version from new installs; installations keep running and are offered the update |
| `zuano market yank <handle>/<slug>@<version> --reason security\|broken\|legal\|policy [--note T]` | `POST .../versions/{version}/yank`: withdraws the version; its installations pause and are offered the newest other version; `security` also revokes their standing grants |
| `zuano market end-of-life <handle>/<slug> --at DATE` | `POST .../end-of-life` with `ends_at` in UTC: installers are told and the listing is archived that day |
| `zuano market reviews list <handle>/<slug> [--cursor C] [--limit N]` | `GET .../reviews`: the visible reviews, the average and whether your organisation may review |
| `zuano market reviews post <handle>/<slug> --rating 1-5 [--text T]` | `POST .../reviews`: one review per organisation |
| `zuano market reviews edit <handle>/<slug> [--rating N] [--text T]` | `PATCH .../reviews`; an empty `--text` removes the text |
| `zuano market reviews reply <review-id> --reply T` | `POST /api/v1/market/reviews/{id}/reply`: the publisher answers once |
| `zuano market reports list [--status open\|responded]` | `GET /api/v1/market/publisher/reports`: the problems reported on your listings; the reporter is not named |
| `zuano market reports file <handle>/<slug> --kind K --text T` | `POST .../reports`; `K` is `broken`, `security`, `misleading`, `abuse` or `other` |
| `zuano market reports respond <report-id> --message T` | `POST /api/v1/market/reports/{id}/respond`: the first answer ends the response clock |

`--at` is a date such as `2027-03-31` (the start of that day in UTC) or an
RFC 3339 time. A review that holds an email address or a phone number is refused
with `422` `review_contains_personal_data` (exit 1, with the gateway's sentence).

## App requests

| Command | Request |
|---|---|
| `zuano apps request <name> [--url U]` | `POST /api/v1/apps/requests` |
| `zuano apps requests [--status S] [--cursor C] [--limit N]` | `GET /api/v1/apps/requests` |
| `zuano apps vote <id>` | `POST /api/v1/apps/requests/{id}/vote` |

One request per app; your organisation's vote counts once. An app that is
already available is refused.

All commands take `--gateway-url`; the ones that print data take `--json`.
