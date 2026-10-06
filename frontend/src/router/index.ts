import { createRouter, createWebHistory } from 'vue-router'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      name: 'search',
      component: () => import('@/views/SearchView.vue'),
    },
    {
      path: '/trace',
      name: 'trace',
      component: () => import('@/views/TraceView.vue'),
    },
    {
      path: '/flow',
      name: 'flow',
      component: () => import('@/views/FlowView.vue'),
    },
    {
      path: '/impact',
      name: 'impact',
      component: () => import('@/views/ImpactView.vue'),
    },
    {
      path: '/admin',
      name: 'admin',
      component: () => import('@/views/AdminView.vue'),
    },
    {
      path: '/contracts',
      name: 'contracts',
      component: () => import('@/views/ContractsView.vue'),
    },
  ],
})

export default router
