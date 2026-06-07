'use client'

import { Sidebar } from '@/components/layout/sidebar'
import { useAuthGuard } from '@/hooks/use-auth-guard'

export default function DashboardLayout({ children }: { children: React.ReactNode }) {
  const { checked } = useAuthGuard()

  // Before the auth probe completes, render a minimal skeleton. This avoids
  // flashing the full dashboard UI at unauthenticated users before the
  // redirect to /login kicks in (useAuthGuard calls router.replace on 401).
  if (!checked) {
    return (
      <div className="min-h-screen bg-gray-50 dark:bg-gray-950">
        <div className="md:pl-60">
          <div className="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8 py-6">
            <div className="h-8 w-40 bg-gray-200 dark:bg-gray-800 rounded animate-pulse" />
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-gray-50 dark:bg-gray-950">
      <Sidebar />
      <main className="md:pl-60 transition-all duration-200">
        <div className="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8 py-6">
          {children}
        </div>
      </main>
    </div>
  )
}
