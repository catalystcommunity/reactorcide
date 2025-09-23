-- +goose Up
-- Used for ULID generation
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- The ULID generator
CREATE OR REPLACE FUNCTION generate_ulid() RETURNS uuid
    AS $$ SELECT (lpad(to_hex(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint), 12, '0') || encode(gen_random_bytes(10), 'hex'))::uuid $$
    LANGUAGE SQL;

-- Our roles are constrained to a set enforced by the database
CREATE TYPE user_role AS ENUM ('user', 'task_admin', 'system_admin');

-- Users are just a UUID@domain like the open IDP system will use, and
-- created/updated at are timestamps for our metadata about them.
-- Roles are for future work, for now only the system_admin role is used
-- In fact, we will probably just have a default user and just use that,
-- but that will be done in api code
create table users
(
    user_id    uuid default generate_ulid() not null primary key,
    created_at timestamp default timezone('utc', now()) not null,
    updated_at timestamp default timezone('utc', now()) not null,
    roles      user_role[] default ARRAY['system_admin'::user_role] not null
);

-- A task is a thing that is running. It doesn't have a config, it was submitted.
-- All task definition/config comes from the submitter. We're just executing and 
-- managing the lifecycle of it.
create table tasks
(
    -- Technically using an int primary key would be faster, and this will be a limited table
    -- but we want consistency over performance here
    task_id          uuid default generate_ulid() not null primary key,
    created_at       timestamp default timezone('utc', now()) not null,
    updated_at       timestamp default timezone('utc', now()) not null,
    task_name        text not null default 'task',
    substep_name     text not null default '',
    pipeline_name    text not null default 'builds',
    user_id          uuid not null references users on delete set null
);
CREATE INDEX tasks_task_name_idx ON tasks(task_name);
CREATE INDEX tasks_substep_name_idx ON tasks(substep_name);
CREATE INDEX tasks_pipeline_name_idx ON tasks(pipeline_name);
