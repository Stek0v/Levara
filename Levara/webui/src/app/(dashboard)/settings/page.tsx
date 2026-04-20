'use client'

import { useEffect } from 'react'
import { Badge } from '@/components/ui/badge'
import { Settings, Globe, Palette, Key } from 'lucide-react'
import { useSettings, useUpdateSettings } from '@/hooks/use-levara'
import type { Theme, Locale } from '@/lib/api'

const THEMES: Theme[] = ['light', 'dark', 'system']
const LOCALES: { value: Locale; label: string }[] = [
  { value: 'ru', label: 'Русский' },
  { value: 'en', label: 'English' },
]

// applyTheme mirrors the inline themeScript in app/layout.tsx so theme
// changes take effect without a reload. localStorage writes stay so the
// root layout's pre-paint script can avoid FOUC on the next visit even
// before the backend query resolves.
function applyTheme(t: Theme) {
  if (typeof window === 'undefined') return
  const dark =
    t === 'dark' ||
    (t === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', dark)
  try {
    localStorage.setItem('levara-theme', t)
  } catch {
    // Private browsing / storage disabled — theme still applies for this session.
  }
}

function applyLocale(l: Locale) {
  if (typeof document === 'undefined') return
  document.documentElement.lang = l
  try {
    localStorage.setItem('levara-locale', l)
  } catch {
    // ignore
  }
}

export default function SettingsPage() {
  const { data: settings, isPending } = useSettings()
  const updateMutation = useUpdateSettings()

  // Resolve effective values: backend settings first, then localStorage
  // fallback (for first-load FOUC), then defaults. The ?? chain means the
  // UI doesn't flicker between "no setting" and "backend setting" on hydration.
  const theme: Theme =
    settings?.theme ??
    (typeof window !== 'undefined' ? (localStorage.getItem('levara-theme') as Theme | null) : null) ??
    'system'
  const locale: Locale =
    settings?.locale ??
    (typeof window !== 'undefined' ? (localStorage.getItem('levara-locale') as Locale | null) : null) ??
    'ru'

  // Re-apply theme/locale whenever the backend-resolved value changes — covers
  // the case where the user logs in on a new device and their stored theme
  // differs from the pre-paint inline script's guess.
  useEffect(() => {
    applyTheme(theme)
  }, [theme])
  useEffect(() => {
    applyLocale(locale)
  }, [locale])

  const handleTheme = (t: Theme) => {
    applyTheme(t)
    updateMutation.mutate({ theme: t })
  }
  const handleLocale = (l: Locale) => {
    applyLocale(l)
    updateMutation.mutate({ locale: l })
  }

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Settings</h1>

      <div className="space-y-6 max-w-2xl">
        {/* Appearance */}
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <div className="flex items-center gap-2 mb-4">
            <Palette className="h-5 w-5 text-gray-400" />
            <h2 className="text-lg font-medium">Appearance</h2>
          </div>
          <div className="flex gap-3">
            {THEMES.map((t) => (
              <button
                key={t}
                onClick={() => handleTheme(t)}
                disabled={isPending}
                className={`px-4 py-2 rounded-md text-sm font-medium capitalize transition-colors ${
                  theme === t
                    ? 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
                    : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-gray-800 dark:text-gray-400'
                } disabled:opacity-50`}
              >
                {t}
              </button>
            ))}
          </div>
        </section>

        {/* Language */}
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <div className="flex items-center gap-2 mb-4">
            <Globe className="h-5 w-5 text-gray-400" />
            <h2 className="text-lg font-medium">Language</h2>
          </div>
          <select
            value={locale}
            onChange={(e) => handleLocale(e.target.value as Locale)}
            disabled={isPending}
            className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm"
          >
            {LOCALES.map((l) => (
              <option key={l.value} value={l.value}>{l.label}</option>
            ))}
          </select>
        </section>

        {/* API */}
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <div className="flex items-center gap-2 mb-4">
            <Key className="h-5 w-5 text-gray-400" />
            <h2 className="text-lg font-medium">API</h2>
          </div>
          <div className="space-y-2 text-sm">
            <div className="flex items-center justify-between">
              <span className="text-gray-500">Endpoint</span>
              <code className="text-xs bg-gray-100 dark:bg-gray-800 px-2 py-1 rounded">
                {process.env.NEXT_PUBLIC_API_URL || ''}
              </code>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-gray-500">Status</span>
              <Badge variant="success">Connected</Badge>
            </div>
          </div>
        </section>

        {/* About */}
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <div className="flex items-center gap-2 mb-4">
            <Settings className="h-5 w-5 text-gray-400" />
            <h2 className="text-lg font-medium">About</h2>
          </div>
          <div className="space-y-1 text-sm text-gray-500">
            <p>Levara WebUI v0.1.0</p>
            <p>Backend: Levara Go HNSW + BM25 + Neo4j</p>
            <p>© 2026</p>
          </div>
        </section>
      </div>
    </div>
  )
}
