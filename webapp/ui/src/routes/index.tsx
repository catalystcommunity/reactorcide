import { lazy } from 'solid-js'
import type { RouteDefinition } from '@solidjs/router'

/**
 * The route table.
 *
 * Every route is lazy so the initial bundle carries only the shell. The
 * workflow list is the landing page because that is what an operator opens the
 * UI to look at.
 */
export const routes: RouteDefinition[] = [
  { path: '/', component: lazy(() => import('./Workflows.tsx').then((m) => ({ default: m.Workflows }))) },
  { path: '/workflows/:id', component: lazy(() => import('./WorkflowDetail.tsx').then((m) => ({ default: m.WorkflowDetail }))) },
  { path: '/jobs', component: lazy(() => import('./Jobs.tsx').then((m) => ({ default: m.Jobs }))) },
  { path: '/jobs/:id', component: lazy(() => import('./JobDetail.tsx').then((m) => ({ default: m.JobDetail }))) },
  { path: '/projects', component: lazy(() => import('./Projects.tsx').then((m) => ({ default: m.Projects }))) },
  { path: '/projects/new', component: lazy(() => import('./ProjectNew.tsx').then((m) => ({ default: m.ProjectNew }))) },
  { path: '/projects/:id', component: lazy(() => import('./ProjectDetail.tsx').then((m) => ({ default: m.ProjectDetail }))) },
  { path: '/workers', component: lazy(() => import('./Workers.tsx').then((m) => ({ default: m.Workers }))) },
  { path: '/workers/classes', component: lazy(() => import('./WorkerClasses.tsx').then((m) => ({ default: m.WorkerClasses }))) },
  { path: '/workers/queues', component: lazy(() => import('./Queues.tsx').then((m) => ({ default: m.Queues }))) },
  { path: '/org/groups', component: lazy(() => import('./OrgGroups.tsx').then((m) => ({ default: m.OrgGroups }))) },
  { path: '/org/roles', component: lazy(() => import('./OrgRoles.tsx').then((m) => ({ default: m.OrgRoles }))) },
  { path: '/org/secrets', component: lazy(() => import('./OrgSecrets.tsx').then((m) => ({ default: m.OrgSecrets }))) },
  { path: '/admin', component: lazy(() => import('./Admin.tsx').then((m) => ({ default: m.Admin }))) },
  { path: '/signin', component: lazy(() => import('./SignIn.tsx').then((m) => ({ default: m.SignIn }))) },
  { path: '*', component: lazy(() => import('./NotFound.tsx').then((m) => ({ default: m.NotFound }))) },
]
