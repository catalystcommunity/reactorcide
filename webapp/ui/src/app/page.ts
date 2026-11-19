/**
 * The base class of a routed page.
 *
 * The shell makes one page element per route and keeps it while
 * `acceptRoute` says the next route is still this page -- by default, the
 * same page with the same id, so a query change (a filter) updates the page
 * in place and an open control stays open. A different id makes a new page.
 *
 * A page watches the route, so render() runs after each accepted change.
 * Read the route with `this.route()`; resource builders read it too, and
 * `handle.sync()` in render() follows the change.
 */
import { RcElement } from '~/ui/element.ts'
import { currentRoute, ROUTE, type Route, type RouteName } from '~/ui/router.ts'

export class RcPage extends RcElement {
  static watches: readonly string[] = [ROUTE]

  /** Set by the shell before the page connects. */
  routeName: RouteName = 'notFound'

  route(): Route {
    return currentRoute()
  }

  /** True to keep this element for `next`. */
  acceptRoute(next: Route, current: Route): boolean {
    return next.name === this.routeName && next.id === current.id
  }
}

/** Lazy page modules, so the first bundle carries only the shell. */
export const PAGES: Readonly<Record<RouteName, () => Promise<string>>> = {
  workflows: () => import('~/routes/Workflows.ts').then((m) => m.tag),
  workflow: () => import('~/routes/WorkflowDetail.ts').then((m) => m.tag),
  event: () => import('~/routes/EventDetail.ts').then((m) => m.tag),
  jobs: () => import('~/routes/Jobs.ts').then((m) => m.tag),
  job: () => import('~/routes/JobDetail.ts').then((m) => m.tag),
  projects: () => import('~/routes/Projects.ts').then((m) => m.tag),
  projectNew: () => import('~/routes/ProjectNew.ts').then((m) => m.tag),
  project: () => import('~/routes/ProjectDetail.ts').then((m) => m.tag),
  projectTrust: () => import('~/routes/ProjectTrust.ts').then((m) => m.tag),
  projectPolicy: () => import('~/routes/ProjectPolicy.ts').then((m) => m.tag),
  projectExecution: () => import('~/routes/ProjectExecution.ts').then((m) => m.tag),
  projectSubscriptions: () => import('~/routes/ProjectSubscriptions.ts').then((m) => m.tag),
  projectImages: () => import('~/routes/ProjectImages.ts').then((m) => m.tag),
  workers: () => import('~/routes/Workers.ts').then((m) => m.tag),
  worker: () => import('~/routes/WorkerDetail.ts').then((m) => m.tag),
  workerClasses: () => import('~/routes/WorkerClasses.ts').then((m) => m.tag),
  queues: () => import('~/routes/Queues.ts').then((m) => m.tag),
  insights: () => import('~/routes/Insights.ts').then((m) => m.tag),
  orgGroups: () => import('~/routes/OrgGroups.ts').then((m) => m.tag),
  orgPrincipals: () => import('~/routes/OrgPrincipals.ts').then((m) => m.tag),
  orgSubscriptions: () => import('~/routes/OrgSubscriptions.ts').then((m) => m.tag),
  orgRoles: () => import('~/routes/OrgRoles.ts').then((m) => m.tag),
  orgSecrets: () => import('~/routes/OrgSecrets.ts').then((m) => m.tag),
  orgTrust: () => import('~/routes/OrgTrust.ts').then((m) => m.tag),
  orgProfiles: () => import('~/routes/OrgProfiles.ts').then((m) => m.tag),
  orgImages: () => import('~/routes/OrgImages.ts').then((m) => m.tag),
  admin: () => import('~/routes/Admin.ts').then((m) => m.tag),
  signin: () => import('~/routes/SignIn.ts').then((m) => m.tag),
  notFound: () => import('~/routes/NotFound.ts').then((m) => m.tag),
}
