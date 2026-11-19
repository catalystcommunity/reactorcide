-- +goose Up
-- The structured cause of a terminal job that did not complete. The class
-- tells who must act: the pipeline owner (job, timeout, oom), the CI owner
-- (infra.*), or nobody (cancelled). NULL means not terminal, completed, or
-- unknown. Keep the set equal to models.FailureClasses.
ALTER TABLE jobs ADD COLUMN failure_class text NULL
    CHECK (failure_class IS NULL OR failure_class IN (
        'job', 'timeout', 'oom',
        'infra.image_pull', 'infra.scheduling', 'infra.worker_lost', 'infra.platform',
        'cancelled'
    ));
ALTER TABLE jobs ADD COLUMN failure_detail text NULL;

-- Conservative backfill of existing terminal rows. Only causes that the
-- stored fields prove get a class; every other row stays NULL (unknown).
UPDATE jobs SET failure_class = 'cancelled' WHERE status = 'cancelled';
UPDATE jobs SET failure_class = 'timeout' WHERE status = 'timeout';
-- The lost-job reconciler (internal/workerapi/reaper.go) writes "lost: ...".
UPDATE jobs SET failure_class = 'infra.worker_lost'
    WHERE status = 'failed' AND last_error LIKE 'lost:%';
-- Kubernetes startup failures (PodStartupError text).
UPDATE jobs SET failure_class = 'infra.image_pull'
    WHERE status = 'failed' AND failure_class IS NULL
      AND (last_error LIKE 'pod failed to start: ImagePullBackOff%'
        OR last_error LIKE 'pod failed to start: ErrImagePull%'
        OR last_error LIKE 'pod failed to start: ErrImageNeverPull%'
        OR last_error LIKE 'pod failed to start: ImageInspectError%'
        OR last_error LIKE 'pod failed to start: InvalidImageName%');
UPDATE jobs SET failure_class = 'infra.scheduling'
    WHERE status = 'failed' AND failure_class IS NULL
      AND (last_error LIKE 'pod failed to start: SchedulingBlocked%'
        OR last_error LIKE 'pod failed to start: ResourceRequestTooLarge%'
        OR last_error LIKE 'pod failed to start: StartupTimeout%');
-- The worker's own text for a non-zero exit of the job command.
UPDATE jobs SET failure_class = 'job'
    WHERE status = 'failed' AND failure_class IS NULL
      AND exit_code IS NOT NULL AND exit_code > 0
      AND (last_error IS NULL OR last_error = ''
        OR last_error LIKE 'job exited with code %'
        OR last_error = 'job execution failed');

-- Automatic retries of infra.platform failures, and the least retry budget
-- of a lost job above the reconciler's fixed budget of 2. The project value
-- NULL inherits the organization value.
ALTER TABLE organizations ADD COLUMN infra_retries integer NOT NULL DEFAULT 1
    CHECK (infra_retries BETWEEN 0 AND 5);
ALTER TABLE projects ADD COLUMN infra_retries integer NULL
    CHECK (infra_retries IS NULL OR infra_retries BETWEEN 0 AND 5);

-- +goose Down
ALTER TABLE projects DROP COLUMN infra_retries;
ALTER TABLE organizations DROP COLUMN infra_retries;
ALTER TABLE jobs DROP COLUMN failure_detail;
ALTER TABLE jobs DROP COLUMN failure_class;
