-- +goose Up
-- An organization is the ownership boundary for its resources. Delete those
-- resources with the organization after the application selects a replacement
-- default organization.
ALTER TABLE queues DROP CONSTRAINT queues_org_id_fkey;
ALTER TABLE queues ADD CONSTRAINT queues_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;

ALTER TABLE worker_pools DROP CONSTRAINT worker_pools_org_id_fkey;
ALTER TABLE worker_pools ADD CONSTRAINT worker_pools_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;

ALTER TABLE projects DROP CONSTRAINT projects_org_id_fkey;
ALTER TABLE projects ADD CONSTRAINT projects_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;

ALTER TABLE jobs DROP CONSTRAINT jobs_org_id_fkey;
ALTER TABLE jobs ADD CONSTRAINT jobs_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;

ALTER TABLE workflow_instances DROP CONSTRAINT workflow_instances_org_id_fkey;
ALTER TABLE workflow_instances ADD CONSTRAINT workflow_instances_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;

ALTER TABLE workers DROP CONSTRAINT workers_pool_id_fkey;
ALTER TABLE workers ADD CONSTRAINT workers_pool_id_fkey
    FOREIGN KEY (pool_id) REFERENCES worker_pools(pool_id) ON DELETE CASCADE;

ALTER TABLE worker_leases DROP CONSTRAINT worker_leases_worker_id_fkey;
ALTER TABLE worker_leases ADD CONSTRAINT worker_leases_worker_id_fkey
    FOREIGN KEY (worker_id) REFERENCES workers(worker_id) ON DELETE CASCADE;

ALTER TABLE worker_leases DROP CONSTRAINT worker_leases_job_id_fkey;
ALTER TABLE worker_leases ADD CONSTRAINT worker_leases_job_id_fkey
    FOREIGN KEY (job_id) REFERENCES jobs(job_id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE worker_leases DROP CONSTRAINT worker_leases_job_id_fkey;
ALTER TABLE worker_leases ADD CONSTRAINT worker_leases_job_id_fkey
    FOREIGN KEY (job_id) REFERENCES jobs(job_id);

ALTER TABLE worker_leases DROP CONSTRAINT worker_leases_worker_id_fkey;
ALTER TABLE worker_leases ADD CONSTRAINT worker_leases_worker_id_fkey
    FOREIGN KEY (worker_id) REFERENCES workers(worker_id);

ALTER TABLE workers DROP CONSTRAINT workers_pool_id_fkey;
ALTER TABLE workers ADD CONSTRAINT workers_pool_id_fkey
    FOREIGN KEY (pool_id) REFERENCES worker_pools(pool_id);

ALTER TABLE workflow_instances DROP CONSTRAINT workflow_instances_org_id_fkey;
ALTER TABLE workflow_instances ADD CONSTRAINT workflow_instances_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id);

ALTER TABLE jobs DROP CONSTRAINT jobs_org_id_fkey;
ALTER TABLE jobs ADD CONSTRAINT jobs_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id);

ALTER TABLE projects DROP CONSTRAINT projects_org_id_fkey;
ALTER TABLE projects ADD CONSTRAINT projects_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id);

ALTER TABLE worker_pools DROP CONSTRAINT worker_pools_org_id_fkey;
ALTER TABLE worker_pools ADD CONSTRAINT worker_pools_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id);

ALTER TABLE queues DROP CONSTRAINT queues_org_id_fkey;
ALTER TABLE queues ADD CONSTRAINT queues_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(org_id);
