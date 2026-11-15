import { QueryClient, type InfiniteData, type QueryKey } from "@tanstack/react-query"

import type { Paginated } from "@/lib/types"

export function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
}

/** Whether a cached query was invalidated (no observers: nothing refetches). */
export function invalidated(qc: QueryClient, key: QueryKey): boolean {
  return qc.getQueryState(key)?.isInvalidated === true
}

export function page<T>(items: T[]): Paginated<T> {
  return { items, page: { total: items.length, limit: 20, offset: 0 } }
}

export function infinite<T>(items: T[]): InfiniteData<Paginated<T>> {
  return { pages: [page(items)], pageParams: [0] }
}
