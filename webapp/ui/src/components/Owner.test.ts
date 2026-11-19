import { describe, expect, it } from 'vitest'
import { OwnerView } from './Owner.ts'

describe('OwnerView', () => {
  it('shows the trusted owner, the contact, and where the snapshot came from', () => {
    const view = new OwnerView()
    view.update({
      group: 'payments-ci',
      subjects: ['vcs_user:github/alice'],
      channel: '#payments-ci',
      helpUrl: 'https://wiki.example/pay',
      email: 'pay@example.com',
      source: 'ci',
      ciSha: 'abcdef1234567890',
    })
    expect(view.el.textContent).toContain('payments-ci, vcs_user:github/alice')
    expect(view.el.textContent).toContain('#payments-ci')
    expect(view.el.querySelector<HTMLAnchorElement>('a.owner-help')!.href).toBe('https://wiki.example/pay')
    expect(view.el.querySelector<HTMLAnchorElement>('a.owner-email')!.getAttribute('href')).toBe('mailto:pay@example.com')
    expect(view.el.textContent).toContain('Read from the trusted CI definition at abcdef123456.')
  })

  it('says "None" for a trusted definition with no owner, and "Unknown" without a snapshot', () => {
    const view = new OwnerView()
    view.update({ source: 'ci' })
    expect(view.el.textContent).toContain('None')
    expect(view.el.querySelector<HTMLElement>('.owner-contact')!.hidden).toBe(true)
    view.update(undefined)
    expect(view.el.textContent).toContain('Unknown')
  })

  it('never links a help URL that is not http or https', () => {
    const view = new OwnerView()
    view.update({ group: 'pay', helpUrl: 'javascript:alert(1)' })
    const help = view.el.querySelector<HTMLAnchorElement>('a.owner-help')!
    expect(help.hidden).toBe(true)
    expect(help.getAttribute('href')).toBeNull()
  })

  it('changes in place on update (no rebuild)', () => {
    const view = new OwnerView()
    view.update({ group: 'a' })
    const first = view.el.firstElementChild
    view.update({ group: 'b', source: 'prior' })
    expect(view.el.firstElementChild).toBe(first)
    expect(view.el.textContent).toContain('b')
    expect(view.el.textContent).toContain('Copied from an earlier trusted run')
  })
})
