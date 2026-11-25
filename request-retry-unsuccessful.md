# Request: `retry-unsuccessful` does not run skipped downstream nodes

From: csilgen (release of `csilgen/v0.3.1`).
Date: 2026-11-24.
Status: resolved on branch `todpunk/default-org-with-filter-views` (not deployed)

## Problem

`reactorcide workflows retry-unsuccessful <workflow>` retries the failed,
cancelled and timed-out jobs in place. It does not re-evaluate the nodes
that were skipped because a dependency failed. When the retried jobs pass,
the workflow goes to `success`, but the skipped nodes never run.

After that, the workflow cannot be recovered:

- `workflows retry` refuses: `only a failed or cancelled workflow can be
  retried; workflow ... is success`.
- `jobs retry` on the eval job refuses: `only a failed, cancelled, or
  timed-out job can be retried; job ... is completed`.

## What happened

Workflow `01a20b34-7b40-1167-a956-a3868c830a23` (`CSILgen Release`, 31
nodes, tag `csilgen/v0.3.1` at `09f35b0`):

1. Job `01a20b34-d08b-9e1f-3db1-c55e017b9a97` (`asset-transports`, one
   matrix item) failed because its pod did not start
   (CreateContainerError). All test jobs passed.
2. Because of the failure, `asset-seal` and `release` (it depends on
   `asset-seal`) were skipped. `asset-cleanup` has `condition: always`.
3. `retry-unsuccessful` made job `01a20b45-e7b7-29b0-c388-acae4419ed20`.
   It passed. `asset-cleanup` ran again.
4. The workflow went to `success` with 31 jobs and 0 failed. `asset-seal`
   and `release` did not run. The GitHub release stayed a draft with 0
   assets.

The only recovery was to delete the tag and push it again. That makes a new
workflow and runs every job again.

## Cause (from the code)

`retryUnsuccessfulJobs` in
`coordinator_api/internal/jobcontrol/retry.go` loops over the nodes and
calls `retryJob` only for a node whose job `IsRetryable()`. A skipped node
has no retryable job, so it is not changed. `rebindWorkflowNodeForRetry`
sets the workflow to `running`. When the retried job completes, the normal
completion hook computes the workflow status from the node statuses. A
skipped node does not block `success`, and nothing evaluates the skipped
node again.

## Outcome needed

- After `retry-unsuccessful`, each node that was skipped because a
  dependency was not successful goes back to pending. When its
  dependencies complete, its `condition` is evaluated again, as in a new
  workflow. If the retried dependencies pass, the node runs.
- A node that was skipped by its own `condition` with all dependencies
  successful stays skipped.
- The workflow is not `success` while a node can still run.
- `retry-unsuccessful` on a workflow that is `success` but has nodes that
  were skipped because of a failed dependency is either accepted (and
  runs those nodes) or refused with a clear error. It does not silently do
  nothing.

## How to verify

A workflow with `a` (fails on the first run), `b` (depends on `a`) and `c`
(depends on `b`, `condition: always`):

1. First run: `a` fails, `b` is skipped, `c` runs.
2. `retry-unsuccessful`: `a` passes, then `b` runs, then `c` runs again.
3. The workflow is `success` only after `b` completes.
4. The node that `b` would have skipped by its own `condition` (for
   example `condition: false`) stays skipped.

## Resolution

`retryUnsuccessfulJobs` (`coordinator_api/internal/jobcontrol/retry.go`):

1. It sets these nodes to pending: each node that is skipped because of a
   dependency that is not successful, or a dependency that an earlier retry
   made pass; and each finished node downstream of such a node or of a
   retried node. A node that is skipped by its own `if:` stays skipped.
2. It retries each failed, cancelled, or timed-out job that is not
   downstream of a node in step 1.
3. It evaluates the workflow once. The completion hooks evaluate the
   waiting nodes when the retried jobs finish.

A `success` workflow with such a skipped node is accepted. A workflow with
no node to retry is refused (`not_retryable` in REST, `conflict` in the UI
service). The UI shows **Retry unsuccessful** when `get-workflow` sets
`retry_unsuccessful_available`.

To recover workflow `01a20b34-7b40-1167-a956-a3868c830a23` after the
deploy: `reactorcide workflows retry-unsuccessful 01a20b34-7b40-1167-a956-a3868c830a23`.
`asset-seal` then runs, then `release`.

Also fixed: `workflows rerun` refused every `success` workflow
(`IsWorkflowRerunnable` checked `completed`, which is a job status).

Test: `coordinator_api/internal/jobcontrol/retry_unsuccessful_test.go`
(the graph in "How to verify", end to end through the completion hooks).
