import { computed, reactive, ref, type Ref } from 'vue'

export type SortOrder = 'ascending' | 'descending'
export interface ListPage<T> { items: T[]; nextCursor?: string; hasMore?: boolean }
export interface FilterOption { value: string; label: string }
export type FilterField =
  | { key: string; kind: 'text'; label: string; placeholder?: string; primary?: boolean }
  | { key: string; kind: 'select'; label: string; options: FilterOption[]; primary?: boolean }
  | { key: string; kind: 'daterange'; label: string; to: [string, string]; primary?: boolean }
export interface UseListQueryOptions<T> {
  load: (params: Record<string, string>) => Promise<ListPage<T>>
  fields?: FilterField[]
  sortable?: Record<string, string>
  defaultSort?: string
  defaultOrder?: SortOrder
  pageSize?: number
  pageSizes?: number[]
}

export function useListQuery<T>(options: UseListQueryOptions<T>) {
  const fields = options.fields ?? []
  const sortable = options.sortable ?? {}
  const pageSizes = options.pageSizes ?? [20, 50, 100]
  const initialPageSize = options.pageSize ?? pageSizes[0]
  const defaultSort = options.defaultSort ?? ''
  const defaultOrder = options.defaultOrder ?? 'descending'
  const pageSize = ref(initialPageSize)
  const sort = ref(defaultSort)
  const order = ref<SortOrder>(defaultOrder)
  const items = ref([]) as Ref<T[]>
  const loading = ref(false)
  const error = ref(false)
  const cursors = ref<string[]>([''])
  const pageIndex = ref(0)
  const nextCursor = ref('')
  const model = reactive<Record<string, unknown>>({})
  for (const field of fields) model[field.key] = field.kind === 'text' ? '' : []

  function params(): Record<string, string> {
    const out: Record<string, string> = { limit: String(pageSize.value) }
    for (const field of fields) {
      const value = model[field.key]
      if (field.kind === 'daterange') {
        const range = (value ?? []) as string[]
        if (range[0]) out[field.to[0]] = range[0]
        if (range[1]) out[field.to[1]] = range[1]
      } else if (Array.isArray(value)) {
        const joined = value.filter(Boolean).join(',')
        if (joined) out[field.key] = joined
      } else {
        const text = String(value ?? '').trim()
        if (text) out[field.key] = text
      }
    }
    if (sort.value) {
      out.sort = sort.value
      out.order = order.value === 'ascending' ? 'asc' : 'desc'
    }
    const cursor = cursors.value[pageIndex.value]
    if (cursor) out.cursor = cursor
    return out
  }

  async function reload() {
    loading.value = true
    error.value = false
    try {
      const page = await options.load(params())
      items.value = page.items
      nextCursor.value = page.nextCursor ?? ''
    } catch {
      items.value = []
      nextCursor.value = ''
      error.value = true
    } finally {
      loading.value = false
    }
  }

  function search() { cursors.value = ['']; pageIndex.value = 0; return reload() }
  function next() {
    if (!nextCursor.value) return Promise.resolve()
    cursors.value[pageIndex.value + 1] = nextCursor.value
    pageIndex.value += 1
    return reload()
  }
  function previous() {
    if (pageIndex.value === 0) return Promise.resolve()
    pageIndex.value -= 1
    return reload()
  }
  function reset() {
    for (const field of fields) model[field.key] = field.kind === 'text' ? '' : []
    sort.value = defaultSort
    order.value = defaultOrder
    pageSize.value = initialPageSize
    return search()
  }
  function onSortChange(payload: { prop?: string | null; order?: SortOrder | null }) {
    const column = payload.prop ? sortable[payload.prop] : undefined
    if (!column || !payload.order) { sort.value = defaultSort; order.value = defaultOrder }
    else { sort.value = column; order.value = payload.order }
    return search()
  }
  function changePageSize(size: number) { pageSize.value = size; return search() }

  const primaryFields = computed(() => fields.filter((field) => field.primary))
  const moreFields = computed(() => fields.filter((field) => !field.primary))
  const activeMoreCount = computed(() => moreFields.value.filter((field) => {
    const value = model[field.key]
    return Array.isArray(value) ? value.length > 0 : String(value ?? '').trim() !== ''
  }).length)
  const hasPrevious = computed(() => pageIndex.value > 0)
  const hasNext = computed(() => Boolean(nextCursor.value))

  return { model, fields, primaryFields, moreFields, activeMoreCount, sortable, items, loading, error, pageSize, pageSizes, sort, order, pageIndex, hasPrevious, hasNext, params, reload, search, next, previous, reset, onSortChange, changePageSize }
}
