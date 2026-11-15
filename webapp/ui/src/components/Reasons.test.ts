import { describe, expect, it } from 'vitest'
import type { CIDecisionReason } from '~/api/csilapi/types.gen.ts'
import { FILES_SHOWN, ReasonList, decisionBadge, reasonSentence, reasonSummary, setDecision } from './Reasons.ts'

describe('reasonSentence', () => {
  it('says each kind in words', () => {
    const cases: [CIDecisionReason, string][] = [
      [{ kind: 'event', matched: true, value: 'push', patterns: ['push', 'tag_created'] }, 'Event "push" matches: push, tag_created'],
      [{ kind: 'event', matched: false, value: 'push', patterns: ['pull_request_opened'] }, 'Event "push" is not one of: pull_request_opened'],
      [{ kind: 'branch', matched: false, value: 'dev', patterns: ['main'] }, 'Branch "dev" does not match: main'],
      [{ kind: 'paths', matched: false, pathsInclude: ['docs/**'], matchedCount: 0 }, 'No changed file matches paths include docs/**'],
      [
        { kind: 'paths', matched: true, pathsInclude: ['api/**'], pathsExclude: ['api/*.md'], matchedFiles: ['api/a.go'], matchedCount: 14 },
        '14 changed files match paths include api/**, exclude api/*.md',
      ],
      [
        { kind: 'paths', matched: true, pathsInclude: ['x/**'], detail: 'changed files are unknown, so the path filter was not applied' },
        'Paths include x/** (changed files are unknown, so the path filter was not applied)',
      ],
      [
        { kind: 'schedule', matched: true, value: '0 3 * * *', scheduleId: 's1', fireTime: '2026-11-14T03:00:00Z', detail: "schedule 'nightly' fired at 2026-11-14T03:00:00Z" },
        "Schedule 'nightly' fired at 2026-11-14T03:00:00Z, cron 0 3 * * *",
      ],
      [{ kind: 'schedule', matched: false, value: 'push', required: true }, 'Runs only on schedule events; this event is "push"'],
      [{ kind: 'schedule', matched: false, value: 'schedule', required: false }, 'Does not run on schedule events'],
      [
        { kind: 'policy', ciOrigin: 'head', profile: 'trusted', ruleId: 'r1', policyRevision: '7' },
        'Policy (rule r1, revision 7): CI code from head, profile trusted',
      ],
      [{ kind: 'manual', value: 'alice' }, 'Started manually by alice'],
      [
        { kind: 'dependency', matched: false, condition: 'all_success', deps: [{ name: 'build', status: 'failed' }] },
        'Condition all_success is not met: build failed',
      ],
      [{ kind: 'admission', matched: false, value: 'not_admitted', detail: 'profile refused' }, 'Not admitted (not_admitted): profile refused'],
      [{ kind: 'invalid', detail: 'workflow not found' }, 'Invalid workflow: workflow not found'],
    ]
    for (const [reason, sentence] of cases) expect(reasonSentence(reason), reason.kind).toBe(sentence)
  })

  it('renders a kind it does not know as kind and value, never throwing', () => {
    expect(reasonSentence({ kind: 'future_thing', value: 'x', detail: 'y' })).toBe('future_thing: x (y)')
    expect(reasonSentence({ kind: '' })).toBe('reason')
  })

  it('summarises with the reason that did not match', () => {
    const reasons: CIDecisionReason[] = [
      { kind: 'event', matched: true, value: 'push', patterns: ['push'] },
      { kind: 'paths', matched: false, pathsInclude: ['docs/**'] },
    ]
    expect(reasonSummary(reasons)).toBe('No changed file matches paths include docs/**')
    expect(reasonSummary([{ kind: 'event', matched: true, value: 'push' }])).toBe('Event "push"')
    expect(reasonSummary([])).toBe('')
  })
})

describe('ReasonList', () => {
  it('caps matched files and says how many more there are', () => {
    const files = Array.from({ length: 50 }, (_, i) => `src/file${i}.go`)
    const list = new ReasonList()
    list.update([{ kind: 'paths', matched: true, pathsInclude: ['src/**'], matchedFiles: files, matchedCount: 120 }])
    expect(list.el.querySelectorAll('.reason-files li.mono')).toHaveLength(FILES_SHOWN)
    expect(list.el.querySelector('.reason-more')?.textContent).toBe(`+${120 - FILES_SHOWN} more`)
  })

  it('marks a reason that did not match, in text as well as colour', () => {
    const list = new ReasonList()
    list.update([{ kind: 'branch', matched: false, value: 'dev', patterns: ['main'] }])
    const item = list.el.querySelector('.reason')!
    expect(item.classList.contains('reason-miss')).toBe(true)
    expect(item.textContent).toContain('Did not match')
  })

  it('keeps its elements when equal reasons arrive again', () => {
    const list = new ReasonList()
    const reasons: CIDecisionReason[] = [{ kind: 'event', matched: true, value: 'push' }]
    list.update(reasons)
    const first = list.el.firstElementChild
    list.update([{ kind: 'event', matched: true, value: 'push' }])
    expect(list.el.firstElementChild).toBe(first)
  })

  it('links a schedule reason to the project that owns the schedule', () => {
    const list = new ReasonList()
    list.update([{ kind: 'schedule', matched: true, scheduleId: 's1' }], { projectId: 'p1' })
    expect(list.el.querySelector('a')?.getAttribute('href')).toBe('/app/projects/p1')
  })
})

describe('decision badge', () => {
  it('shows a glyph and a word for each decision', () => {
    const badge = decisionBadge()
    setDecision(badge, 'not_admitted')
    expect(badge.textContent).toBe('⊘Not admitted')
    expect(badge.classList.contains('status-warn')).toBe(true)
    setDecision(badge, 'something_new')
    expect(badge.textContent).toBe('?something_new')
  })
})
