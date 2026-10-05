import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import { auth } from './lib/api.js'
import './style.css'

const routes = [
  { path: '/', component: () => import('./views/Login.vue') },
  {
    path: '/', component: () => import('./components/Shell.vue'),
    children: [
      { path: 'home', component: () => import('./views/Home.vue') },
      { path: 'apps', component: () => import('./views/Apps.vue') },
      { path: 'apps/:id', component: () => import('./views/AppDetail.vue') },
      { path: 'images', component: () => import('./views/Images.vue') },
      { path: 'deployments', component: () => import('./views/Deployments.vue') },
      { path: 'projects', component: () => import('./views/Projects.vue') },
      { path: 'domains', component: () => import('./views/Domains.vue') },
      { path: 'networking', component: () => import('./views/Networking.vue') },
      { path: 'nodes', component: () => import('./views/Nodes.vue') },
      { path: 'nodes/:id', component: () => import('./views/NodeDetail.vue') },
      { path: 'storage', component: () => import('./views/Storage.vue') },
      { path: 'backups', component: () => import('./views/Backups.vue') },
      { path: 'logs', component: () => import('./views/Logs.vue') },
      { path: 'metrics', component: () => import('./views/Metrics.vue') },
      { path: 'tasks', component: () => import('./views/Tasks.vue') },
      { path: 'cron', component: () => import('./views/Cron.vue') },
      { path: 'secrets', component: () => import('./views/Secrets.vue') },
      { path: 'variables', component: () => import('./views/Variables.vue') },
      { path: 'team', component: () => import('./views/Team.vue') },
      { path: 'billing', component: () => import('./views/Billing.vue') },
      { path: 'marketplace', component: () => import('./views/Marketplace.vue') },
    ],
  },
]

const router = createRouter({ history: createWebHistory(), routes })
router.beforeEach((to) => {
  if (to.path !== '/' && !auth.token) return '/'
  if (to.path === '/' && auth.token) return '/home'
})
createApp(App).use(router).mount('#app')
