import type { CheckBox, PushButton, TextArea } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { ImagePolicySummary } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, checkBox, setButtonText, setFieldValue, textArea } from '~/ui/controls.ts'
import { h, setText, show } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/** Splits the prefixes text: one prefix per line, blank lines dropped. */
export function splitPrefixes(text: string): string[] {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

/**
 * The job image policy of an organization: the image prefixes that jobs may
 * use, and an optional digest pinning rule. The coordinator enforces it at
 * submit and at claim time. Off by default.
 */
export class ImagePolicyCard extends RcElement {
  /** The organization NAME. Nothing loads while it is empty. */
  organization = ''

  #load = new Loader<string, ImagePolicySummary>(this, async (organization) => (await api.getImagePolicy({ organization })).policy)
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #enabled!: CheckBox
  #requireDigest!: CheckBox
  #prefixes!: TextArea
  #submit!: PushButton
  #status!: HTMLParagraphElement
  #loaded = ''

  setScope(organization: string): void {
    if (this.organization === organization) return
    this.organization = organization
    this.invalidate()
  }

  protected build(): void {
    this.#enabled = checkBox('Enforce the image policy')
    this.#requireDigest = checkBox('Require an @sha256 digest')
    this.#prefixes = textArea({ mono: true, rows: 4, placeholder: 'ghcr.io/example/\ndocker.io/library/' })
    this.#submit = button('Save image policy', { variant: 'primary', type: 'submit' })
    this.#status = h('p', { class: 'meta image-policy-status' })
    this.classList.add('card')
    this.append(
      h('h3', { class: 'section-title' }, 'Image policy'),
      h(
        'p',
        { class: 'meta' },
        'When enforced, a job image must start with an allowed prefix (for example "ghcr.io/example/"). "postgres:17" is "docker.io/library/postgres:17". The coordinator refuses other images when a job is submitted and when a worker claims it.',
      ),
      this.#error.el,
      this.#status,
      h(
        'form',
        { onSubmit: (event: Event) => void this.#onSave(event) },
        h('div', { class: 'checkbox-row' }, this.#enabled),
        field({ label: 'Allowed prefixes', hint: 'One prefix per line. No wildcards.', control: this.#prefixes }).el,
        h('div', { class: 'checkbox-row' }, this.#requireDigest),
        this.#submit,
      ),
    )
  }

  async #onSave(event: Event): Promise<void> {
    event.preventDefault()
    const policy: ImagePolicySummary = {
      enabled: this.#enabled.checked,
      allowedPrefixes: splitPrefixes(this.#prefixes.value),
      requireDigest: this.#requireDigest.checked,
    }
    const organization = this.organization
    let saved: ImagePolicySummary | undefined
    const ok = await this.#action.run('The image policy could not be saved.', async () => {
      saved = (await api.putImagePolicy({ organization, policy })).policy
    })
    if (ok && saved && this.isActive()) this.#load.mutate(saved)
  }

  render(): void {
    this.#load.sync(this.organization || undefined)
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    const policy = this.#load.state.data
    show(this.#status, policy)
    if (policy) {
      setText(this.#status, policy.enabled ? `Enforced. ${policy.allowedPrefixes.length} allowed prefixes.` : 'Not enforced. Jobs can use any image.')
      // Copy the stored values into the form once per load, so a refresh
      // does not overwrite what the person is typing.
      const signature = JSON.stringify(policy)
      if (this.#loaded !== signature) {
        this.#loaded = signature
        this.#enabled.checked = policy.enabled
        this.#requireDigest.checked = policy.requireDigest
        setFieldValue(this.#prefixes, policy.allowedPrefixes.join('\n'))
      }
    }
    this.#submit.disabled = this.#action.busy || !policy
    setButtonText(this.#submit, this.#action.busy ? 'Saving…' : 'Save image policy')
  }
}

export const imagePolicyCardTag = 'rc-image-policy-card'
define(imagePolicyCardTag, ImagePolicyCard)

export function imagePolicyCard(organization: string): ImagePolicyCard {
  const el = document.createElement(imagePolicyCardTag) as ImagePolicyCard
  el.organization = organization
  return el
}
