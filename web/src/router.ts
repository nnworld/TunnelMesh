import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from './stores/auth'

const Login = () => import('./views/Login.vue')
const Dashboard = () => import('./views/Dashboard.vue')
const Agents = () => import('./views/Agents.vue')
const AgentDetail = () => import('./views/AgentDetail.vue')
const Routes = () => import('./views/Routes.vue')
const Tunnels = () => import('./views/Tunnels.vue')
const AuditLogs = () => import('./views/AuditLogs.vue')
const Tokens = () => import('./views/Tokens.vue')
const Servers = () => import('./views/Servers.vue')
const Clients = () => import('./views/Clients.vue')
const Downloads = () => import('./views/Downloads.vue')
const Users = () => import('./views/Users.vue')
const SSOProviders = () => import('./views/SSOProviders.vue')
const AccountSecurity = () => import('./views/AccountSecurity.vue')
const RemoteServers = () => import('./views/RemoteServers.vue')
const Credentials = () => import('./views/Credentials.vue')
const WebSSHTerminal = () => import('./views/WebSSHTerminal.vue')
const WebSFTP = () => import('./views/WebSFTP.vue')

const router = createRouter({ history: createWebHistory(), routes: [
  {path:'/login', component:Login}, {path:'/', component:Dashboard, meta:{auth:true}},
  {path:'/agents', component:Agents, meta:{auth:true}}, {path:'/agents/:id', component:AgentDetail, meta:{auth:true}}, {path:'/routes', component:Routes, meta:{auth:true}},
{path:'/tunnels', component:Tunnels, meta:{auth:true}}, {path:'/tokens', component:Tokens, meta:{auth:true}}, {path:'/clients', component:Clients, meta:{auth:true}}, {path:'/remote-servers', component:RemoteServers, meta:{auth:true}}, {path:'/credentials', component:Credentials, meta:{auth:true}}, {path:'/webssh/:sessionId', name:'webssh-terminal', component:WebSSHTerminal, meta:{auth:true}}, {path:'/webssh/:sessionId/sftp', name:'web-sftp', component:WebSFTP, meta:{auth:true}}, {path:'/servers', component:Servers, meta:{auth:true, admin:true}}, {path:'/downloads', component:Downloads, meta:{auth:true, admin:true}}, {path:'/users', component:Users, meta:{auth:true, admin:true}}, {path:'/sso-providers', component:SSOProviders, meta:{auth:true, admin:true}}, {path:'/account/security', component:AccountSecurity, meta:{auth:true}}, {path:'/audit-logs', component:AuditLogs, meta:{auth:true, admin:true}}
]})
router.beforeEach(async (to) => { const auth = useAuthStore(); if (auth.token && !auth.user) await auth.load(); if (to.meta.auth && !auth.user) return '/login'; if (to.meta.admin && !auth.isAdmin) return '/'; return true })
export default router
