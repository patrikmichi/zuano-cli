# Step-up confirmations

Some gateway actions need a person to confirm them with a passkey before they
run, even though the CLI session is signed in: connecting an account,
publishing or promoting, changing a policy, resuming a failed run, and a
destructive call from a non-browser credential. Organisation admin actions and,
in a strict organisation, more of them, ask the same way. The gateway decides
which.

A passkey works only in a signed-in browser. The CLI cannot give the
confirmation and does not try: it tells the person what needs confirming and
where, then sends the same request again.

## The flow

1. The request is refused with `401` (or `403`) and a body naming the action:

   ```json
   {"error":"step_up_required","message":"Confirm this change with your passkey.","step_up":{"action":"policy_change","digest":"9c1f..."}}
   ```

   The digest covers this exact change, so a confirmation made for another
   change cannot be spent on it.

2. zuano names the action and points to the console (it opens the gateway in
   the browser, or prints the link and a QR code):

   ```
   The action "policy_change" needs your confirmation with a passkey, which only a signed-in browser can give.
   Confirm it in the console: https://app.zuano.ai/
   Press Enter once you have confirmed it to try again, or Ctrl-C to cancel:
   ```

3. The person makes the same change in the console, which asks for the passkey.
   When they press Enter, zuano sends the original request again, unchanged. The
   gateway spends the confirmation for that change if it finds one. A second
   `step_up_required` ends the command with exit status 14; zuano never asks
   twice.

Only the request that was refused is retried.

## Without a person at the keyboard

When `ZUANO_AGENT` is set or stdin is not a terminal, zuano does not open
anything and does not ask. The command ends with exit status **14** and names the
action:

```
this action (policy_change) needs a passkey confirmation from a person in the console
```

A person confirms the change in the console and the command is run again; a
confirmation is used up by the request that spends it and expires after a few
minutes.

## Exit status

| Status | Meaning |
|---|---|
| 14 | a step-up confirmation is needed and nobody could give it (agent session, no terminal, no confirmation by the retry) |

Every command inherits this: the mapping is in the gateway client, not in the
commands. The other exit statuses are in
[approvals.md](approvals.md#exit-statuses) and [billing.md](billing.md#exit-statuses).
