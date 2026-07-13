'use client'

import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { cn } from '@/lib/utils'
import {
  LayoutDashboard, Database, Search, MessageCircle, Share2,
  FolderOpen, Brain, Settings, BarChart3, BookOpen, Menu, X, Files, RefreshCw, Shield, Sparkles, Activity,
} from 'lucide-react'
import { useState } from 'react'

const nav = [
  { name: 'Dashboard', href: '/', icon: LayoutDashboard },
  { name: 'Datasets', href: '/datasets', icon: Database },
  { name: 'Search', href: '/search', icon: Search },
  { name: 'Chat', href: '/chat', icon: MessageCircle },
  { name: 'Graph', href: '/graph', icon: Share2 },
  { name: 'Collections', href: '/collections', icon: FolderOpen },
  { name: 'Workspace', href: '/workspace', icon: Files },
  { name: 'Sync', href: '/sync', icon: RefreshCw },
  { name: 'Memories', href: '/memories', icon: Brain },
  { name: 'Notebooks', href: '/notebooks', icon: BookOpen },
  { name: 'Analytics', href: '/analytics', icon: BarChart3 },
  { name: 'Memory Behavior', href: '/memory-behavior', icon: Activity },
  { name: 'Scaffold Proposals', href: '/memory-scaffold', icon: BookOpen },
  { name: 'Admin', href: '/admin', icon: Shield },
  { name: 'Onboarding', href: '/onboarding', icon: Sparkles },
  { name: 'Settings', href: '/settings', icon: Settings },
]

export function Sidebar() {
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

        {/* Nav items */}
        <nav className="flex-1 overflow-y-auto py-2 px-2">
          {nav.map((item) => {
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
                title={collapsed ? item.name : undefined}
              >
                <item.icon className="h-5 w-5 flex-shrink-0" />
                {!collapsed && item.name}
              </Link>
            )
          })}
        </nav>
      </aside>
    </>
  )
}
