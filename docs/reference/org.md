# Organisation settings

## zuano org domains

An organisation claims the email domains it owns. A claim counts once the DNS
`TXT` record in the answer is published and `verify` finds it. Owners and
admins only; the routes are `/api/v1/domains` (OpenAPI operations `domains.*`).

| Command | Request |
|---|---|
| `zuano org domains list [--json]` | `GET /domains`: each claim, its status (`pending` or `verified`) and when it was last checked |
| `zuano org domains add <domain> [--json]` | `POST /domains {"domain"}`; prints the record to publish |
| `zuano org domains verify <domain> [--json]` | `POST /domains/{id}/verify`; the domain can be given by name or by id |
| `zuano org domains remove <domain> --yes [--json]` | `DELETE /domains/{id}`: release the claim |

Only one organisation at a time can hold a domain verified.

### Exit status

| Status | Refusals |
|---|---|
| 70 | `domain_unverified` (`409`): `verify` found no DNS record yet; DNS changes can take a while to spread |
| 1 | `invalid_domain`, `domain_exists`, `domain_limit`, `domain_verified_elsewhere`, an unknown id, and `dns_unavailable` (`503`, try again shortly) |
| 10 | a denial: the caller is not an owner or admin |
| 30-33 | the plan, an allowance, billing or a suspension; see [billing.md](billing.md) |
