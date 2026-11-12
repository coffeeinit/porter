import { computed } from 'vue'
import { useColorScheme } from 'frappe-ui'

import { sidebarSections } from '@/components/navigation/list'
import { useAppMenu } from '@/components/navigation/useAppMenu'
import { useSession } from '@/composables/auth/useSession'

import {
  DATABASE_SECTIONS,
  GENERAL_SECTIONS,
  SECURITY_SECTIONS,
} from '@/components/settings/sections'

export interface SearchItem {
  name: string
  icon: string
  route?: string
  onSelect?: () => void
}

export type SearchGroups = Record<string, { items: SearchItem[] }>

export const useSearchIndex = () => {
  const { session } = useSession()
  const { setColorScheme } = useColorScheme()
  const { showBenches, showNewBench } = useAppMenu()

  return computed((): SearchGroups => {
    const groups: SearchGroups = {}

    const pages: SearchItem[] = sidebarSections
      .flatMap((section) => section.items)
      .filter((item) => !item.flag || session[item.flag as keyof typeof session])
      .map((item) => ({ name: item.label, icon: item.icon, route: item.to }))

    if (pages.length) groups.Pages = { items: pages }

    const actions: SearchItem[] = [
      { name: 'New site', icon: 'lucide-plus', route: '/sites?new=1' },
      {
        name: 'Add app from GitHub',
        icon: 'lucide-package-plus',
        route: '/marketplace?addFromGithub=1',
      },

      { name: 'Failed tasks', icon: 'lucide-list-x', route: '/insights/tasks?status=failed' },
      ...(session.allowBenchManagement
        ? [
            { name: 'New bench', icon: 'lucide-plus', onSelect: () => (showNewBench.value = true) },
            {
              name: 'Switch bench',
              icon: 'lucide-repeat',
              onSelect: () => (showBenches.value = true),
            },
          ]
        : []),

      { name: 'Settings', icon: 'lucide-settings', route: '/settings' },
      { name: 'Activity', icon: 'lucide-history', route: '/insights/activity' },
    ]

    groups.Actions = { items: actions }

    groups.Settings = {
      items: [
        ...GENERAL_SECTIONS.map((section) => ({
          name: section.label,
          icon: 'lucide-settings',
          route: `/settings/general/${section.id}`,
        })),

        ...DATABASE_SECTIONS.map((section) => ({
          name: section.label,
          icon: 'lucide-database',
          route: `/settings/database/${section.id}`,
        })),

        ...SECURITY_SECTIONS.map((section) => ({
          name: section.label,
          icon: 'lucide-shield',
          route: `/settings/security/${section.id}`,
        })),
      ],
    }

    groups.Theme = {
      items: [
        { name: 'Light', icon: 'lucide-sun', onSelect: () => setColorScheme('light') },
        { name: 'Dark', icon: 'lucide-moon', onSelect: () => setColorScheme('dark') },
        { name: 'System', icon: 'lucide-monitor', onSelect: () => setColorScheme('system') },
      ],
    }

    return groups
  })
}
