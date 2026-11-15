# Security

## Reporting a vulnerability

Report a vulnerability in the command line tool or the connector agent through
the repository's private vulnerability reporting (Security tab, "Report a
vulnerability"). Do not open a public issue for it. Include the version
(`zuano --version`), your operating system and the steps to reproduce.

Only the latest release is supported.

## What the tools do and do not do

- The tool talks only to the gateway you configured, over https. It has no
  telemetry and contacts no other host.
- Credentials are kept in the operating system keychain, or the storage the tool
  already uses for them, and are never written to logs, error messages or
  exported files.
- Anything the tool downloads and then runs or installs (packs, plugins,
  connector agent updates, the tool's own updates) is checked against a
  signature before it is used. A download that does not verify is refused;
  there is no switch that skips the check.
- A session you turn into a skill is read from the single file you name,
  redacted on your machine, and uploaded only after you accept the gateway's
  consent text. Nothing in a session is executed, and files a session mentions
  are not read. The gateway redacts it again; the tool does not rely on that.
- Retired credentials (the shared gateway key and the environment fallbacks)
  stop working with `401` or a "service not connected" refusal; see
  [docs/reference/retired-credentials.md](docs/reference/retired-credentials.md).

## Exit status for a sign-in problem

A command that needs a session and has none, or whose session the gateway
rejected with `401`, ends with status 5 and says to run `zuano login`.
