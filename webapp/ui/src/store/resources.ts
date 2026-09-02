import { api } from '~/api/client.ts'
import type {
  GetWorkflowResponse,
  JobSummary,
  ListJobsResponse,
  ListWorkflowsResponse,
  ProjectDetail,
  ProjectSummary,
  DescribeFormMetadataResponse,
} from '~/api/csilapi/types.gen.ts'
import { useResource, type ResourceState } from './index.ts'
import { topics, type LiveEvent } from './events.ts'
import type { Accessor } from 'solid-js'

/**
 * The application's datasets.
 *
 * Each one declares the live topics it needs and how it reacts to an event.
 * `shouldRefresh` is deliberately narrow everywhere: a refresh per irrelevant
 * frame turns a busy instance into a load generator, and the coordinator emits
 * an event for every job transition across everything the caller can see.
 *
 * Every hook keyed on a route param or a filter passes useResource a BUILDER
 * rather than a fixed object. The router reuses a route component when only a
 * path param or a search param changes, so options read once at setup would pin
 * the page to whatever it was first opened with.
 */

export interface WorkflowFilters {
  status?: string
  projectId?: string
}

export function useWorkflows(filters: Accessor<WorkflowFilters>): {
  state: Accessor<ResourceState<ListWorkflowsResponse>>
  refresh: () => Promise<void>
} {
  return useResource<ListWorkflowsResponse>(() => {
    const f = filters()
    return {
      key: `workflows:${f.status ?? ''}:${f.projectId ?? ''}`,
      fetcher: () =>
        api.listWorkflows({
          status: f.status || undefined,
          projectId: f.projectId || undefined,
          limit: 50,
        }),
      topics: [topics.workflows, topics.jobs],
      shouldRefresh: (event) => {
        // A new workflow must appear without a reload -- that is the whole
        // point of workflow_created existing. A status change reorders and
        // recolours the list. A job event matters because a workflow row shows
        // its jobs' aggregate counts.
        return (
          event.type === 'workflow_created' ||
          event.type === 'workflow_update' ||
          event.type === 'job_created' ||
          event.type === 'job_update'
        )
      },
    }
  })
}

export interface JobFilters {
  status?: string
  projectId?: string
  workflowId?: string
}

export function useJobs(filters: Accessor<JobFilters>): {
  state: Accessor<ResourceState<ListJobsResponse>>
  refresh: () => Promise<void>
} {
  return useResource<ListJobsResponse>(() => {
    const f = filters()
    return {
      key: `jobs:${f.status ?? ''}:${f.projectId ?? ''}:${f.workflowId ?? ''}`,
      fetcher: () =>
        api.listJobs({
          status: f.status || undefined,
          projectId: f.projectId || undefined,
          workflowId: f.workflowId || undefined,
          limit: 50,
        }),
      topics: [topics.jobs],
      shouldRefresh: (event) => event.type === 'job_created' || event.type === 'job_update',
      /**
       * A status change is applied in place rather than refetched.
       *
       * The event carries everything a row needs, so a running job ticking
       * through its states costs no requests at all. Anything the event cannot
       * describe -- a new job, a changed count -- falls through to a refresh.
       */
      applyEvent: (current, event) => {
        if (!current || event.type !== 'job_update' || !event.job_id || !event.status) {
          return undefined
        }
        const index = current.jobs.findIndex((job) => job.jobId === event.job_id)
        if (index === -1) return undefined

        const updated: JobSummary = {
          ...current.jobs[index],
          status: event.status,
          updatedAt: event.updated_at ?? current.jobs[index].updatedAt,
        }
        const jobs = [...current.jobs]
        jobs[index] = updated
        return { ...current, jobs }
      },
    }
  })
}

export function useWorkflow(workflowId: Accessor<string>): {
  state: Accessor<ResourceState<GetWorkflowResponse>>
  refresh: () => Promise<void>
} {
  return useResource<GetWorkflowResponse>(() => {
    const id = workflowId()
    return {
      key: `workflow:${id}`,
      fetcher: () => api.getWorkflow({ workflowId: id }),
      // One topic covers the workflow, its nodes and its jobs, so a detail page
      // needs a single subscription rather than one per node.
      topics: [topics.workflow(id)],
      shouldRefresh: (event) => event.workflow_id === id,
    }
  })
}

export function useJob(jobId: Accessor<string>) {
  return useResource(() => {
    const id = jobId()
    return {
      key: `job:${id}`,
      fetcher: () => api.getJob({ jobId: id }),
      topics: [topics.job(id)],
      shouldRefresh: (event: LiveEvent) => event.job_id === id && event.type === 'job_update',
    }
  })
}

export function useProjects() {
  return useResource<{ projects: ProjectSummary[] }>({
    key: 'projects',
    fetcher: () => api.listProjects({}),
    // The PROJECTS collection topic, not the jobs one. "jobs" carries only job
    // events, so a project_update frame never reached this listener and the
    // list never noticed a project being renamed or turned private.
    topics: [topics.projects],
    shouldRefresh: (event) => event.type === 'project_update',
  })
}

export function useProject(projectId: Accessor<string>) {
  return useResource<{ project: ProjectDetail }>(() => {
    const id = projectId()
    return {
      key: `project:${id}`,
      fetcher: () => api.getProject({ projectId: id }),
      topics: [topics.project(id)],
      shouldRefresh: (event) => event.type === 'project_update' && event.project_id === id,
    }
  })
}

/**
 * The server's form vocabulary.
 *
 * Cached under one key for the whole session and never invalidated by an event:
 * these are the software's own constants, not deployment state, so they change
 * only when the server is upgraded -- at which point the page reloads anyway.
 */
export function useFormMetadata() {
  return useResource<DescribeFormMetadataResponse>({
    key: 'form-metadata',
    fetcher: () => api.describeFormMetadata({}),
  })
}
