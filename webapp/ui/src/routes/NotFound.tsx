import { A } from '@solidjs/router'
import type { JSX } from 'solid-js'

export function NotFound(): JSX.Element {
  return (
    <div class="page">
      <div class="empty">
        <div class="empty-title">That page does not exist.</div>
        <p>
          <A href="/">Go to workflows</A>
        </p>
      </div>
    </div>
  )
}
