import { api } from '~/api/client.ts'
import type {
  GetCapabilitiesResponse,
  GetEventResponse,
  GetWorkflowResponse,
  JobSummary,
  ListJobsResponse,
  ListWorkflowsResponse,
  OrgSummary,
  ProjectDetail,
  ProjectSummary,
  DescribeFormMetadataResponse,
} from '~/api/csilapi/types.gen.ts'
import { useResource, type ResourceHandle, type ResourceHost } from './index.ts'
import { topics, type LiveEvent } from './events.ts'

/**
 * The application's datasets.
 *
 * Each one declares the live topics it needs and how it reacts to an event.
 * `shouldRefresh` is deliberately narrow everywhere: a refresh per irrelevant
 * frame turns a busy instance into a load generator, and the coordinator emits
 * an event for every job transition across everything the caller can see.
 *
 * Every hook keyed on a route param or a filter passes useResource a BUILDER
 * rather than a fixed object, and the page calls `handle.sync()` in render().
 * A page is kept when only its query changes (a filter), so options read once
 * at connect would pin the page to whatever it was first opened with.
 *
 * Each takes the HOST element first: the handle's release goes to its own(),
 * so call these from the host's connected().
 */

export interface WorkflowFilters {
  status?: string
  projectId?: string
}

export function useWorkflows(host: ResourceHost, filters: () => WorkflowFilters): ResourceHandle<ListWorkflowsResponse> {
  return useResource<ListWorkflowsResponse>(host, () => {
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
  failureClass?: string
}

export function useJobs(host: ResourceHost, filters: () => JobFilters): ResourceHandle<ListJobsResponse> {
  return useResource<ListJobsResponse>(host, () => {
    const f = filters()
    return {
      key: `jobs:${f.status ?? ''}:${f.projectId ?? ''}:${f.workflowId ?? ''}:${f.failureClass ?? ''}`,
      fetcher: () =>
        api.listJobs({
          status: f.status || undefined,
          projectId: f.projectId || undefined,
          workflowId: f.workflowId || undefined,
          failureClass: f.failureClass || undefined,
          limit: 50,
        }),
      topics: [topics.jobs],
      shouldRefresh: (event) => event.type === 'job_created' || event.type === 'job_update',
      /**
       * A status change is applied in place rather than refetched.
       *
       * A status transition requires a refresh because it can change runtime
       * timestamps. An update within the same state can use the event fields.
       */
      applyEvent: (current, event) => {
        if (!current || event.type !== 'job_update' || !event.job_id || !event.status) {
          return undefined
        }
        const index = current.jobs.findIndex((job) => job.jobId === event.job_id)
        if (index === -1) return undefined
        // Status transitions can also change start and finish timestamps.
        // Read the complete row before computing runtime duration.
        if (current.jobs[index].status !== event.status) return undefined

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

export function useWorkflow(host: ResourceHost, workflowId: () => string): ResourceHandle<GetWorkflowResponse> {
  return useResource<GetWorkflowResponse>(host, () => {
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

/**
 * One CI event: its facts, its eval job and every workflow decision.
 *
 * An event changes while its eval runs (decisions arrive, workflows are
 * created) and while those workflows run. It refreshes on a new workflow, on
 * a change of one of ITS workflows, and on a change of its eval job. `known`
 * reads the data on screen, so the filter knows which ids are its own.
 */
export function useEvent(
  host: ResourceHost,
  eventId: () => string,
  known: () => GetEventResponse | undefined,
): ResourceHandle<GetEventResponse> {
  return useResource<GetEventResponse>(host, () => {
    const id = eventId()
    return {
      key: `event:${id}`,
      fetcher: () => api.getEvent({ eventId: id }),
      topics: [topics.workflows, topics.jobs],
      shouldRefresh: (event) => {
        const data = known()
        if (event.type === 'workflow_created') return true
        if (event.type === 'workflow_update') return Boolean(event.workflow_id && data?.workflowIds.includes(event.workflow_id))
        if (event.type === 'job_update') return Boolean(event.job_id && event.job_id === data?.event.evalJobId)
        return false
      },
    }
  })
}

export function useJob(host: ResourceHost, jobId: () => string) {
  return useResource(host, () => {
    const id = jobId()
    return {
      key: `job:${id}`,
      fetcher: () => api.getJob({ jobId: id }),
      topics: [topics.job(id)],
      shouldRefresh: (event: LiveEvent) => event.job_id === id && event.type === 'job_update',
    }
  })
}

export function useProjects(host: ResourceHost) {
  return useResource<{ projects: ProjectSummary[] }>(host, {
    key: 'projects',
    fetcher: () => api.listProjects({}),
    // The PROJECTS collection topic, not the jobs one. "jobs" carries only job
    // events, so a project_update frame never reached this listener and the
    // list never noticed a project being renamed or turned private.
    topics: [topics.projects],
    shouldRefresh: (event) => event.type === 'project_update',
  })
}

export function useProject(host: ResourceHost, projectId: () => string) {
  return useResource<{ project: ProjectDetail }>(host, () => {
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
export function useFormMetadata(host: ResourceHost) {
  return useResource<DescribeFormMetadataResponse>(host, {
    key: 'form-metadata',
    fetcher: () => api.describeFormMetadata({}),
  })
}

/** Where a capability question is asked. Both empty means the global scope. */
export interface CapabilityScope {
  projectId?: string
  orgId?: string
  /** One workflow instance: adds the owners of that workflow (WP7). */
  workflowId?: string
}

/**
 * What the caller may do at ONE scope.
 *
 * The session carries only the global-scope set, which is all-false for
 * everyone but a global admin. A project owner may cancel and retry that
 * project's jobs, and an org admin may manage that org's projects, and neither
 * shows up globally. So a detail page asks about its own project, and the
 * coordinator answers from the project's owning organization and the caller's
 * roles there. The answer is cached per scope for the session; roles change
 * rarely, and a login reloads the page.
 *
 * `undefined` from the builder means "not known yet" (the job has not loaded)
 * and yields an all-false answer rather than a global-scope one, so a button
 * never renders off the wrong scope while data is in flight.
 */
export function useCapabilities(host: ResourceHost, scope: () => CapabilityScope | undefined): ResourceHandle<GetCapabilitiesResponse> {
  return useResource<GetCapabilitiesResponse>(host, () => {
    const s = scope()
    if (!s) {
      return {
        key: 'caps:pending',
        fetcher: async () => ({}) as GetCapabilitiesResponse,
      }
    }
    const key = s.workflowId
      ? `caps:workflow:${s.workflowId}`
      : s.projectId
        ? `caps:project:${s.projectId}`
        : s.orgId
          ? `caps:org:${s.orgId}`
          : 'caps:global'
    return {
      key,
      fetcher: () =>
        s.workflowId ? api.getCapabilities({ workflowId: s.workflowId }) : api.getCapabilities({ projectId: s.projectId, orgId: s.orgId }),
    }
  })
}

/**
 * The organizations the caller can see: every one for a global admin, public
 * ones plus those they hold a role in for anyone else. Management pages pick
 * from this list rather than assuming the caller's own user id is an org.
 */
export function useOrgs(host: ResourceHost) {
  return useResource<{ orgs: OrgSummary[] }>(host, {
    key: 'orgs',
    fetcher: () => api.listOrgs({}),
  })
}
