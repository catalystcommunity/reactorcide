-- +goose Up
-- Worker enrollment token audit. See docs/workers.md "Enrollment token
-- rotation".
--
-- A worker records the ID of the enrollment token that it last registered
-- with. An admin can then see which hosts use which token (by ID and name,
-- never the token value), and find the workers that still run on a
-- deactivated token. Such a worker keeps its session until it stops, but it
-- cannot register again.
--
-- Deactivation is permanent: a trigger refuses a change of is_active from
-- false to true.
--
-- This migration adds one nullable column with no default value. Existing
-- workers get the token ID at their next registration. v0.12.x writes no
-- value and never activates a token again, so it continues to work.
ALTER TABLE workers
    ADD COLUMN enrollment_token_id uuid REFERENCES pool_enrollment_tokens(token_id) ON DELETE SET NULL;
CREATE INDEX workers_enrollment_token_id_idx ON workers(enrollment_token_id) WHERE enrollment_token_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION pool_enrollment_tokens_no_reactivate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.is_active = false AND NEW.is_active = true THEN
        RAISE EXCEPTION 'a deactivated enrollment token cannot be activated again (token %)', OLD.token_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER pool_enrollment_tokens_no_reactivate
    BEFORE UPDATE OF is_active ON pool_enrollment_tokens
    FOR EACH ROW EXECUTE FUNCTION pool_enrollment_tokens_no_reactivate();

-- +goose Down
DROP TRIGGER pool_enrollment_tokens_no_reactivate ON pool_enrollment_tokens;
DROP FUNCTION pool_enrollment_tokens_no_reactivate();
DROP INDEX workers_enrollment_token_id_idx;
ALTER TABLE workers DROP COLUMN enrollment_token_id;
