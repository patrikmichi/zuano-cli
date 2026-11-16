# Private networks

A private network lets connectors and MCP servers reach systems that are not on
the internet, through a `zuano-connector` agent that runs inside your network and
dials out (see [connector.md](connector.md)). `zuano networks` is for
organisation administrators. The routes it calls are in
[gateway-contracts.md](gateway-contracts.md#private-networks).

Changes that loosen access, such as deleting a network or adding a binding, need
a passkey: the command opens the page or waits for the confirmation. Every change is refused while `ZUANO_AGENT` is set (exit 10). A
call that reaches an offline network exits 34. `--json` prints what the gateway
sent and `--gateway-url` names the gateway.

| Command | |
|---|---|
| `zuano networks list` | networks with their health, agents online and bindings |
| `zuano networks show <name>` | one network's status and settings |
| `zuano networks create <name> [--description D] [--data-class C]` | create a network; needs the private-networking add-on |
| `zuano networks delete <name> [--yes]` | delete it; its bindings are removed and its agents revoked |
| `zuano networks token create <name> [--uses N] [--ttl D] [--require-approval]` | mint an enrollment token (1 to 10 uses, up to 24h) and print it with the install commands; the token is shown once |
| `zuano networks agents <name>` | a network's connector agents with their live status |
| `zuano networks approve <name> <agent> [--reason R]` | let a pending agent in |
| `zuano networks rotate <name> <agent> [--reason R]` | have an agent replace its key |
| `zuano networks revoke <name> <agent> [--reason R] [--yes]` | cut an agent off |
| `zuano networks allowlist <name>` | what each agent advertises it will serve, and whether they differ |
| `zuano networks events <name> [--limit N] [--before CURSOR]` | the network's events, newest first |
| `zuano networks logs <name> [--agent A] [--target T] [--outcome O] [--since D]` | exchange logs the agents uploaded (metadata only) |
| `zuano networks bind <connector\|mcp:server> --network N --target T [--credentials gateway\|agent]` | route a connector or MCP server through a network target |
| `zuano networks unbind <connector\|mcp:server> --network N [--target T]` | stop routing it |
| `zuano networks test <name>` | ping each online agent and show the round-trip time |

A bound connector never falls back to the public internet: when the network
cannot serve a call, the call fails. Built-in services such as Google or Slack
cannot be bound.

`logs` and `test` call routes the gateway does not publish yet; against a gateway
without them they end with the gateway's refusal.
