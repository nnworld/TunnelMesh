import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from './stores/auth'
import Login from './views/Login.vue'
import Dashboard from './views/Dashboard.vue'
import Agents from './views/Agents.vue'
import AgentDetail from './views/AgentDetail.vue'
import Routes from './views/Routes.vue'
import Tunnels from './views/Tunnels.vue'
import AuditLogs from './views/AuditLogs.vue'

const router = createRouter({ history: createWebHistory(), routes: [
  {path:'/login', component:Login}, {path:'/', component:Dashboard, meta:{auth:true}},
  {path:'/agents', component:Agents, meta:{auth:true}}, {path:'/agents/:id', component:AgentDetail, meta:{auth:true}}, {path:'/routes', component:Routes, meta:{auth:true}},
  {path:'/tunnels', component:Tunnels, meta:{auth:true}}, {path:'/audit-logs', component:AuditLogs, meta:{auth:true, admin:true}}
]})
router.beforeEach(async (to) => { const auth = useAuthStore(); if (auth.token && !auth.user) await auth.load(); if (to.meta.auth && !auth.user) return '/login'; if (to.meta.admin && !auth.isAdmin) return '/'; return true })
export default router
