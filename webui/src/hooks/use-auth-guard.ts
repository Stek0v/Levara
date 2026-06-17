'use client'

import { useEffect, useState, startTransition } from 'react'
import { usePathname, useRouter } from 'next/navigation'
import { levara, ApiError } from '@/lib/api'

/**
 * useAuthGuard — client-side auth check for protected routes.
 *
 * Calls GET /auth/me on mount. On 401 redirects to /login?next=<pathname>
 * so the login page can return the user to the page they requested.
 *
 * Returns a boolean `checked` flag — true once the auth probe has completed
 * (success OR redirect kicked off). Consumers should render a Skeleton / null
 * until `checked` is true to avoid flashing protected UI at unauthenticated users.
 *
 * Does nothing on /login itself (prevents redirect loops if /auth/me happens
 * to 401 after logout).
 */
export function useAuthGuard(): { checked: boolean } {
  const router = useRouter()
  const pathname = usePathname()
  const [checked, setChecked] = useState(false)

  useEffect(() => {
    let cancelled = false

    // Don't probe on the login page itself — a 401 there would be normal
    // (user is logging in) and would create a redirect loop.
    if (pathname?.startsWith('/login')) {
      startTransition(() => setChecked(true))
      return
    }

    levara
      .me()
      .then(() => {
        if (!cancelled) setChecked(true)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 401) {
          const next = pathname && pathname !== '/' ? `?next=${encodeURIComponent(pathname)}` : ''
          router.replace(`/login${next}`)
          return
        }
        // Non-401 errors (network down, 5xx): still mark as checked so UI can
        // render — the error will surface via React Query / handleResponse.
        setChecked(true)
      })

    return () => {
      cancelled = true
    }
  }, [pathname, router])

  return { checked }
}
