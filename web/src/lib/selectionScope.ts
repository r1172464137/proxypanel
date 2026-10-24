import { scopeDigest } from './batchPending'

export interface SelectionQuery {
  q: string
  subscription_id: string
  include_archived: boolean
}
/** Only IDs and query/cursor facts; never retain member records or imply all query results. */
export function createScope(query: SelectionQuery, visited: readonly (string | null)[], selected: Iterable<string>, revision: number) {
  return { query: { ...query }, visited: [...visited], selected: [...selected].sort(), revision }
}
export function selectionScopeDigest(query: SelectionQuery, visited: readonly (string | null)[], selected: Iterable<string>, revision: number): string {
  return scopeDigest(createScope(query, visited, selected, revision))
}
export function otherPageCount(selected: ReadonlySet<string>, visibleIds: readonly string[]): number {
  return selected.size - visibleIds.filter(id => selected.has(id)).length
}
