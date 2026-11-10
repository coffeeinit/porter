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
      { path: 'projects', component: () => import('./views/Projects.vue') },
      { path: 'projects/:id', name: 'project-detail', component: () => import('./views/ProjectDetail.vue') },
      { path: 'services', alias: 'apps', component: () => import('./views/Apps.vue') },
      { path: 'services/new', name: 'new-service', component: () => import('./views/NewService.vue') },
      { path: 'services/:id', name: 'service-detail', component: () => import('./views/ServiceDetail.vue') },
      { path: 'apps/:id', component: () => import('./views/AppDetail.vue') },
      { path: 'releases', alias: 'deployments', component: () => import('./views/Deployments.vue') },
      { path: 'instances', component: () => import('./views/Instances.vue') },
      { path: 'instances/:id', component: () => import('./views/InstanceDetail.vue') },
      { path: 'observability', component: () => import('./views/Observability.vue'), name: 'observability' },
      { path: 'logs', redirect: { name: 'observability', query: { tab: 'logs' } } },
      { path: 'metrics', redirect: { name: 'observability', query: { tab: 'metrics' } } },
      { path: 'images', component: () => import('./views/Images.vue') },
      { path: 'domains', component: () => import('./views/Domains.vue') },
      { path: 'networking', component: () => import('./views/Networking.vue') },
      { path: 'nodes', component: () => import('./views/Nodes.vue') },
      { path: 'nodes/:id', component: () => import('./views/NodeDetail.vue') },
      { path: 'storage', component: () => import('./views/Storage.vue') },
      { path: 'backups', component: () => import('./views/Backups.vue') },
      { path: 'tasks', component: () => import('./views/Tasks.vue') },
      { path: 'cron', component: () => import('./views/Cron.vue') },
      { path: 'secrets', component: () => import('./views/Secrets.vue') },
      { path: 'variables', component: () => import('./views/Variables.vue') },
      { path: 'billing', redirect: '/home' },
      { path: 'team', redirect: '/home' },
      { path: 'marketplace', redirect: '/home' },
      { path: ':pathMatch(.*)*', redirect: '/home' },
    ],
  },
]

const router = createRouter({ history: createWebHistory(), routes })
router.beforeEach((to) => {
  if (to.path !== '/' && !auth.token) return '/'
  if (to.path === '/' && auth.token) return '/home'
})
createApp(App).use(router).mount('#app')
