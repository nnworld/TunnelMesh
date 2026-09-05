import { defineStore } from 'pinia'
import { api, getToken, setToken } from '../api/client'

export type User = { id: string; username: string; role: 'admin' | 'user' }
export const useAuthStore = defineStore('auth', {
  state: () => ({ token: getToken(), user: null as User | null }),
  getters: { isAdmin: (state) => state.user?.role === 'admin' },
  actions: {
    async login(username: string, password: string) { const data = await api<{token:string;user:User}>('/auth/login', {method:'POST', body: JSON.stringify({username,password})}); this.token=data.token; this.user=data.user; setToken(data.token) },
    async load() { if (!this.token) return; try { this.user = await api<User>('/auth/me') } catch { this.logout() } },
    logout() { this.token = ''; this.user = null; setToken('') }
  }
})
