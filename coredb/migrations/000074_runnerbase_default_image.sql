-- +goose Up
-- The column defaults of the runner image named an image that the project
-- does not build (quay.io/catalystcommunity/reactorcide_runner). Change them
-- to the runnerbase image (worker.DefaultRunnerImage). Existing rows do not
-- change.
ALTER TABLE jobs ALTER COLUMN runner_image SET DEFAULT 'containers.catalystsquad.com/public/reactorcide/runnerbase:dev';
ALTER TABLE projects ALTER COLUMN default_runner_image SET DEFAULT 'containers.catalystsquad.com/public/reactorcide/runnerbase:dev';

-- +goose Down
ALTER TABLE jobs ALTER COLUMN runner_image SET DEFAULT 'quay.io/catalystcommunity/reactorcide_runner';
ALTER TABLE projects ALTER COLUMN default_runner_image SET DEFAULT 'quay.io/catalystcommunity/reactorcide_runner';
