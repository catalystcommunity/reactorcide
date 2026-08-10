-- +goose Up
CREATE TABLE audit_events (
    audit_event_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid REFERENCES organizations(org_id) ON DELETE SET NULL,
    actor_credential_id uuid,
    actor_credential_type text,
    actor_user_id uuid REFERENCES users(user_id) ON DELETE SET NULL,
    action text NOT NULL,
    subject_type text NOT NULL,
    subject_id text,
    details jsonb NOT NULL DEFAULT '{}',
    created_at timestamp NOT NULL DEFAULT timezone('utc', now())
);
CREATE INDEX audit_events_org_created_idx ON audit_events(org_id, created_at DESC);
CREATE INDEX audit_events_action_idx ON audit_events(action, created_at DESC);

-- +goose Down
DROP TABLE audit_events;
