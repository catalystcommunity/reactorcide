/** Choices of the execution settings of an organization and a project. */

/** Infra retries 0-5 (docs/runtime-behavior.md). */
export const RETRY_CHOICES = [0, 1, 2, 3, 4, 5].map((n) => ({
  value: String(n),
  label: n === 0 ? 'No retry' : n === 1 ? '1 retry' : `${n} retries`,
  description: n === 1 ? 'The default.' : '',
}))

/** Egress control on or off (docs/egress-proxy.md). */
export const EGRESS_CHOICES = [
  { value: 'off', label: 'Off', description: 'Jobs reach the network directly.' },
  { value: 'on', label: 'On', description: 'All job traffic goes through the egress proxy.' },
]
