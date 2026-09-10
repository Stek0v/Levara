'use client'

import { useEffect, useState, startTransition } from 'react'
import { usePathname, useRouter } from 'next/navigation'
import { levara, ApiError, sanitizeAuthNext, SSO_NEXT_KEY } from '@/lib/api'

// Only a successful live session probe reveals protected UI. A transport error
// leads to a retryable login message, never an authenticated-looking dashboard.
export function useAuthGuard(): { checked: boolean } {
  const router = useRouter()
  const pathname = usePathname()
  const [checkedPath, setCheckedPath] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    if (pathname?.startsWith('/login')) {
      startTransition(() => setCheckedPath(pathname))
      return
    }
    levara.me().then(() => {
      if (cancelled) return
      const pending = sessionStorage.getItem(SSO_NEXT_KEY)
      sessionStorage.removeItem(SSO_NEXT_KEY)
      if (pending) {
        const next = sanitizeAuthNext(pending)
        if (next !== window.location.pathname + window.location.search + window.location.hash) {
          router.replace(next)
          return
        }
      }
      setCheckedPath(pathname)
    }).catch((err: unknown) => {
      if (cancelled) return
      const next = sanitizeAuthNext(window.location.pathname + window.location.search + window.location.hash)
      const query = new URLSearchParams()
      if (next !== '/') query.set('next', next)
      if (!(err instanceof ApiError && err.status === 401)) query.set('error', 'unavailable')
      router.replace(`/login${query.size ? '?' + query : ''}`)
    })
    return () => { cancelled = true }
  }, [pathname, router])

  return { checked: checkedPath === pathname }
}
