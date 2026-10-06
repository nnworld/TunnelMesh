import { defineStore } from 'pinia'
import { getAbout, runAction } from '../api/client'
import type { AboutView } from '../api/types'
import { describe } from './settings'

export const useAboutStore = defineStore('about', {
  state: () => ({
    view: null as AboutView | null,
    loading: false,
    error: '' as string,
  }),
  actions: {
    async load() {
      this.loading = true
      this.error = ''
      try {
        this.view = await getAbout()
      } catch (cause) {
        this.error = describe(cause)
        throw cause
      } finally {
        this.loading = false
      }
    },
    async open(target: 'open-website' | 'open-releases' | 'open-docs') {
      try {
        await runAction(target)
        this.error = ''
      } catch (cause) {
        this.error = describe(cause)
        throw cause
      }
    },
  },
})
