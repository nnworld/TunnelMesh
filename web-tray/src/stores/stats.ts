import { defineStore } from 'pinia'
import { getStats, runAction } from '../api/client'
import type { StatsView } from '../api/types'
import { describe } from './settings'

/** Poll interval for the statistics tab. Short enough to feel live, long enough that a
 * stopped client does not turn the tray into a busy loop. */
export const STATS_POLL_MS = 2000

/**
 * The statistics tab's state, and the client's start/stop control.
 *
 * Both live here because they describe one thing: whether the hosted client is running.
 * A control action refreshes the snapshot immediately so the header badge and the table
 * cannot disagree for two seconds after a click.
 */
export const useStatsStore = defineStore('stats', {
  state: () => ({
    view: null as StatsView | null,
    loading: false,
    acting: false,
    error: '' as string,
    autoRefresh: true,
    timer: 0 as ReturnType<typeof setInterval> | 0,
  }),
  getters: {
    running: (state) => state.view?.running ?? false,
    connected: (state) => state.view?.connected ?? false,
    tunnels: (state) => state.view?.tunnels ?? [],
    agents: (state) => state.view?.agents ?? [],
    totals: (state) => state.view?.totals ?? null,
    runtimeError: (state) => state.view?.runtimeError ?? '',
    lockBlocked: (state) => state.view?.lockBlocked ?? false,
  },
  actions: {
    async refresh() {
      this.loading = true
      try {
        this.view = await getStats()
        this.error = ''
      } catch (cause) {
        this.error = describe(cause)
      } finally {
        this.loading = false
      }
      return this.view
    },
    async act(action: 'start' | 'stop' | 'restart') {
      this.acting = true
      try {
        this.view = (await runAction(action)) as unknown as StatsView
        this.error = ''
      } catch (cause) {
        this.error = describe(cause)
        await this.refresh()
        throw cause
      } finally {
        this.acting = false
      }
      return this.view
    },
    beginPolling() {
      this.endPolling()
      void this.refresh()
      if (!this.autoRefresh) return
      this.timer = setInterval(() => {
        void this.refresh()
      }, STATS_POLL_MS)
    },
    endPolling() {
      if (this.timer) {
        clearInterval(this.timer)
        this.timer = 0
      }
    },
    setAutoRefresh(enabled: boolean) {
      this.autoRefresh = enabled
      if (enabled) this.beginPolling()
      else this.endPolling()
    },
  },
})
