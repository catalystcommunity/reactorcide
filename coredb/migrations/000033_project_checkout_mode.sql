-- +goose Up
ALTER TABLE projects
    ADD COLUMN checkout_mode text;

ALTER TABLE projects
    ADD CONSTRAINT projects_checkout_mode_check
    CHECK (checkout_mode IS NULL OR checkout_mode IN ('', 'isolated', 'shared'));

-- +goose Down
ALTER TABLE projects DROP CONSTRAINT projects_checkout_mode_check;
ALTER TABLE projects DROP COLUMN checkout_mode;
