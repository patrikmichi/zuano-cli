# Background runs

Agent and prompt runs, code runs, evaluation runs and optimisations, webhook
deliveries and host builds run on the gateway as durable jobs. A job survives
a crash or a deploy, keeps a status, and can be cancelled or, once it failed
every retry, started again.

Job status: `queued`, `running`, then one of `succeeded`, `failed`,
`cancelled` or `dead` (it failed every retry and was dead-lettered). A job
that will be tried again is `queued` once more. `queued` is not the end: every
`--wait` keeps polling through it, and `dead` ends it as a failure.

## Calls that start a job

A tool that starts background work answers before it finishes. zuano
recognises two shapes of the answer, both with a `job_id` or a `run_id` and a
status of `queued` or `running`:

- HTTP `202` with `{"run_id", "job_id", "status"}`;
- an MCP tool result whose `structuredContent` has that shape.

`zuano call <service> <tool>` prints what started and how to follow it, and
exits 0:

```
Started on the gateway: job_01J9ZK3 (queued).
Follow it with `zuano runs show job_01J9ZK3`, or repeat the call with --wait.
```

With `--wait[=SECONDS]` it polls `GET /api/v1/jobs/{id}` (a run id with no job id
polls `GET /api/runtime/runs/{id}`, which carries `job_status`) at one, two,
three, then every five seconds, and prints the finished job. The exit status is
0 for `succeeded`, 1 for `failed`, `dead` or `cancelled`, and 12 when the time
ended with the job still going. `--json` prints only the final document.

## zuano runs

| Command | Request |
|---|---|
| `zuano runs list [--kind K] [--status S] [--cursor C] [--limit N] [--json]` | `GET /api/v1/jobs?kind=&status=&cursor=&limit=` |
| `zuano runs show <id> [--json]` | `GET /api/v1/jobs/{id}`, or `GET /api/runtime/runs/{id}` for an id that does not start with `job_` |
| `zuano runs wait <id> [--wait-timeout T] [--json]` | the same route, polled until finished; exit status as above |
| `zuano runs cancel <id>` | `POST /api/v1/jobs/{id}/cancel`, or `POST /api/runtime/runs/{id}/cancel` for a run id |
| `zuano runs retry <job-id>` | `POST /api/v1/jobs/{id}/retry` (organisation admins; failed or dead jobs only) |
| `zuano runs message <run-id> --steer "<text>"`<br>`zuano runs message <run-id> --follow-up "<text>"` | `POST /api/runtime/runs/{id}/messages` with `{"mode": "steer" \| "follow_up", "text"}` |

Members see their own jobs; admins see all of the organisation's, metadata
only. `zuano usage` shows the quotas that bound how many jobs may start; a
start refused by a quota exits 31 (see [billing.md](billing.md#exit-statuses)).

## Runs

The run routes (`/api/runtime/runs`) are part of the gateway's OpenAPI
document under an `/api/runtime` server and answer with the same refusal
documents as `/api/v1`: `{"error": CODE, "message"}`. A run is `running`, then
`succeeded`, `failed`, `timed_out`, `killed`, `cancelled` or `pending_approval`
(it stopped at a call nobody had decided). `zuano runs wait` ends on every one
of them: 0 for `succeeded`, 1 for `failed`, `timed_out`, `killed` and
`cancelled`, 12 for `pending_approval`. A run whose row still says `running`
while its job is `dead` is reported as `dead`.

`zuano runs show <run-id>` prints the run and, for an agent run, what its
transcript carries: the model, steps (and how many were cut off at the output
limit), tokens, the stop reason, compactions (earlier work replaced by a
summary), the stored results the run read back with `read_result`, the messages
people sent with the step they were read at (or that the run ended before
reading them), and a call still waiting for approval. A stop of `output_limit`
(the model reply was cut off twice in a row) or `cost_budget`, like
`max_steps`, `token_budget` and `model_error`, is a failed run, so `wait` exits
1; no new exit status.

`message` sends a steering note, read before the run's next model call, or a
follow-up, read when the run would otherwise finish. Only the person who
started the run, or an administrator, may send one; an agent session gets
`403`. A run takes 20 messages of at most 4 KB (checked before sending).

| Refusal | Meaning | Exit |
|---|---|---|
| `409` `not_running` | on `message` or `cancel`: the run ended, or is not an agent run | 1 |
| `409` `message_limit_reached` | on `message`: the run has its 20 messages | 1 |
| `404` `not_found` | a run you may not see | 1 |

Both name the gateway's sentence and, for `not_running`, point to
`zuano runs show`.

## Evaluations

`zuano eval run` and `zuano eval optimize` wait for the job and follow `queued` and
`running` and stop at `succeeded`, `failed`, `cancelled` or `dead`; the last
three exit with the eval failure status.
