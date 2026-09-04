'use client'

import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { cn } from '@/lib/utils'
import {
  LayoutDashboard, Database, Search, MessageCircle, Share2,
  Brain, Settings, BarChart3, BookOpen, Menu, X, Files, RefreshCw, Shield, Activity, ListTodo,
} from 'lucide-react'
import { useState } from 'react'
import { useT } from '@/lib/i18n'

// nameKey is resolved through i18n at render time so the sidebar follows
// the active locale without a reload.
//
// Nav is grouped (Работа / Память / Инфраструктура): 17 flat entries did
// not scale. Collections, Notebooks and Onboarding pages still exist but
// are intentionally not in the nav — collections duplicate the project
// card, notebooks are an in-memory toy, onboarding duplicates the
// dashboard quick actions.
interface NavItem {
  nameKey: string
  name: string
  href: string
  icon: typeof LayoutDashboard
}

interface NavGroup {
  titleKey: string
  title: string
  items: NavItem[]
}

const navGroups: NavGroup[] = [
  {
    titleKey: 'nav.group.work',
    title: 'Работа',
    items: [
      { nameKey: 'nav.dashboard', name: 'Dashboard', href: '/', icon: LayoutDashboard },
      { nameKey: 'nav.projects', name: 'Datasets', href: '/datasets', icon: Database },
      { nameKey: 'nav.search', name: 'Search', href: '/search', icon: Search },
      { nameKey: 'nav.chat', name: 'Chat', href: '/chat', icon: MessageCircle },
      { nameKey: 'nav.graph', name: 'Graph', href: '/graph', icon: Share2 },
    ],
  },
  {
    titleKey: 'nav.group.memory',
    title: 'Память',
    items: [
      { nameKey: 'nav.memories', name: 'Memories', href: '/memories', icon: Brain },
      { nameKey: 'nav.memoryBehavior', name: 'Memory Behavior', href: '/memory-behavior', icon: Activity },
      { nameKey: 'nav.scaffold', name: 'Scaffold Proposals', href: '/memory-scaffold', icon: BookOpen },
    ],
  },
  {
    titleKey: 'nav.group.infra',
    title: 'Инфраструктура',
    items: [
      { nameKey: 'nav.tasks', name: 'Tasks', href: '/tasks', icon: ListTodo },
      { nameKey: 'nav.workspace', name: 'Workspace', href: '/workspace', icon: Files },
      { nameKey: 'nav.sync', name: 'Sync', href: '/sync', icon: RefreshCw },
      { nameKey: 'nav.analytics', name: 'Analytics', href: '/analytics', icon: BarChart3 },
      { nameKey: 'nav.admin', name: 'Admin', href: '/admin', icon: Shield },
      { nameKey: 'nav.settings', name: 'Settings', href: '/settings', icon: Settings },
    ],
  },
]

export function Sidebar() {
  const t = useT()
  const pathname = usePathname()
  const [collapsed, setCollapsed] = useState(true) // default collapsed on mobile

  return (
    <>
      {/* Mobile hamburger */}
      <button
        className="fixed top-3 left-3 z-50 md:hidden p-2 rounded-md bg-white dark:bg-gray-900 shadow"
        onClick={() => setCollapsed(!collapsed)}
        aria-label={collapsed ? 'Open menu' : 'Close menu'}
      >
        {collapsed ? <Menu className="h-5 w-5" /> : <X className="h-5 w-5" />}
      </button>

      {/* Sidebar */}
      <aside
        className={cn(
          'fixed inset-y-0 left-0 z-40 flex flex-col bg-white dark:bg-gray-900 border-r border-gray-200 dark:border-gray-800 transition-transform duration-200',
          collapsed ? '-translate-x-full md:translate-x-0 md:w-16' : 'w-60',
          'md:translate-x-0',
        )}
      >
        {/* Logo */}
        <div className="flex h-14 items-center px-4 border-b border-gray-200 dark:border-gray-800">
          <Link href="/" className="flex items-center gap-2">
            <div className="h-7 w-7 rounded-lg bg-blue-600 flex items-center justify-center">
              <span className="text-white font-bold text-sm">L</span>
            </div>
            {!collapsed && <span className="font-semibold text-lg">Levara</span>}
          </Link>
          <button
            className="ml-auto hidden md:block p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-800"
            onClick={() => setCollapsed(!collapsed)}
            aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          >
            <Menu className="h-4 w-4" />
          </button>
        </div>

        {/* Nav groups */}
        <nav className="flex-1 overflow-y-auto py-2 px-2">
          {navGroups.map((group) => (
            <div key={group.titleKey} className="mb-3">
              {!collapsed && (
                <p className="px-3 pt-2 pb-1 text-[11px] font-semibold uppercase tracking-wide text-gray-400 dark:text-gray-500">
                  {t(group.titleKey)}
                </p>
              )}
              {collapsed && <div className="mx-3 my-1.5 border-t border-gray-100 dark:border-gray-800" />}
              {group.items.map((item) => {
                const active = pathname === item.href || (item.href !== '/' && pathname.startsWith(item.href))
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    className={cn(
                      'flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors mb-0.5',
                      active
                        ? 'bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
                        : 'text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-800',
                    )}
                    title={collapsed ? t(item.nameKey) : undefined}
                  >
                    <item.icon className="h-5 w-5 flex-shrink-0" />
                    {!collapsed && t(item.nameKey)}
                  </Link>
                )
              })}
            </div>
          ))}
        </nav>
      </aside>
    </>
  )
}
