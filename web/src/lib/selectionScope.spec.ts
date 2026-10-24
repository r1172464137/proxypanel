import { describe, expect, it } from 'vitest'
import { createScope, otherPageCount, selectionScopeDigest } from './selectionScope'

const query = { q: 'alice', subscription_id: 'plan-1', include_archived: false }
describe('explicit selected-ID scope', () => {
  it('contains only selected IDs and query/visited cursor facts, independent of selection order', () => {
    const ids = new Set(['b', 'a'])
    expect(createScope(query, [null, 'c2'], ids, 2)).toEqual({ query, visited: [null, 'c2'], selected: ['a', 'b'], revision: 2 })
    expect(otherPageCount(ids, ['b', 'unselected'])).toBe(1)
    expect(selectionScopeDigest(query, [null], ids, 2)).toBe(selectionScopeDigest(query, [null], ['a', 'b'], 2))
  })
  it('filter, archived, plan, selection and leaving/revisiting a page all retire old previews', () => {
    const digest = selectionScopeDigest(query, [null], ['a'], 1)
    for (const changed of [{ ...query, q: '' }, { ...query, subscription_id: '' }, { ...query, include_archived: true }]) {
      expect(selectionScopeDigest(changed, [null], ['a'], 1)).not.toBe(digest)
    }
    expect(selectionScopeDigest(query, [null], ['b'], 1)).not.toBe(digest)
    expect(selectionScopeDigest(query, [null, 'c2'], ['a'], 1)).not.toBe(digest)
    expect(selectionScopeDigest(query, [null], ['a'], 3)).not.toBe(digest)
  })
})
