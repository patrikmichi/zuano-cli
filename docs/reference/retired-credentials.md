# Retired credentials

The gateway no longer accepts a few ways of authenticating that older setups
used. They fail on purpose; there is no flag in `zuano` to bring them back.

| What stopped working | What you see | What to do |
|---|---|---|
| The shared gateway key (`GATEWAY_API_KEY`, `GATEWAY_API_KEY_NEXT`) sent as a bearer token | `401`; `zuano` ends with status 5 | Mint a key under Settings, API keys (or `POST /api/admin/keys`) and use that. Platform administrators use a key minted with the `platform:admin` scope. |
| A key or token that belongs to no organisation | `401` on every route, including catalog reads and token refresh | Sign in again with `zuano login`, or mint a new key inside the organisation. |
| Provider keys in the gateway's environment (`GITHUB_TOKEN`, `CLOCKIFY_API_KEY` and the other service variables) | A call is refused with `No credential configured for this caller; service not connected` | Save the credential for the organisation under Settings, Connections, or connect the account with `zuano connect`. |
| `n8n` and `ollama` hosts on a LAN or loopback address | The call is refused by the egress check | Point them at a host that resolves to a public address. |

A `401` ends with status 5, the same for every command, so a script can tell
"sign in again" from a refusal by policy (10) or by the plan (30 to 33).

Settings the gateway removed with these changes (`ALLOW_ENV_CREDENTIAL_FALLBACK`,
`ALLOW_DEFAULT_ORG_BEARER_FALLBACK`, `HOSTED`) have no counterpart in the tool.
Operators remove them from the deployment environment.
