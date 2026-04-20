'use client'

// error.tsx — root error boundary (Next.js 15/16 file convention).
//
// Catches uncaught errors thrown during render of any page in the App
// Router. We surface a minimal "Try again" / "Go home" fallback instead
// of letting the runtime white-screen. The underlying error is still
// reported to `console.error` so devtools/source maps stay useful; the
// `digest` prop is the server-side hash Next.js attaches when the error
// originates on the server (undefined for pure client errors).
//
// 401 redirects are already handled inside api.ts (T1) — this boundary
// is for code-level failures, not auth state.

import { useEffect } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Button } from '@/components/ui/button'

export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string }
  reset: () => void
}) {
  useEffect(() => {
    console.error('[error-boundary]', error)
  }, [error])

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50 dark:bg-gray-950 px-4">
      <div className="w-full max-w-md bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-6 shadow-sm">
        <div className="flex items-center gap-3 mb-4">
          <AlertTriangle className="h-6 w-6 text-red-500" />
          <h1 className="text-xl font-semibold">Something went wrong</h1>
        </div>
        <p className="text-sm text-gray-600 dark:text-gray-400 mb-4">
          {error.message || 'An unexpected error occurred. Please try again.'}
        </p>
        {error.digest && (
          <code className="block text-xs text-gray-400 break-all mb-4">
            ref: {error.digest}
          </code>
        )}
        <div className="flex gap-2">
          <Button onClick={() => reset()}>Try again</Button>
          <Button variant="ghost" onClick={() => { window.location.href = '/' }}>
            Go home
          </Button>
        </div>
      </div>
    </div>
  )
}
