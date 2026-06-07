'use client'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { ToastProvider, useToast } from '@/components/ui/toast'
import { ApiError } from '@/lib/api'

export function Providers({ children }: { children: React.ReactNode }) {
  const [queryClient] = useState(() => new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30 * 1000,
        retry: (count, error) => {
          if ((error as { status?: number })?.status === 401) return false
          return count < 3
        },
        refetchOnWindowFocus: true,
      },
    },
  }))

  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <UnhandledRejectionListener />
        {children}
      </ToastProvider>
    </QueryClientProvider>
  )
}

// UnhandledRejectionListener surfaces promise rejections that neither
// React Query nor the component tree caught — usually fire-and-forget
// fetches or setTimeout callbacks. 401s are suppressed because api.ts
// already handles them via redirect (T1).
function UnhandledRejectionListener() {
  const { toast } = useToast()

  useEffect(() => {
    function handler(event: PromiseRejectionEvent) {
      const reason = event.reason
      if (reason instanceof ApiError && reason.status === 401) return

      const message =
        reason instanceof Error
          ? reason.message
          : typeof reason === 'string'
            ? reason
            : 'An unexpected error occurred'

      console.error('[unhandledrejection]', reason)
      toast('error', message)
      // Don't preventDefault — devtools should still surface the rejection
      // in the console for debugging. The toast is the user-facing half.
    }

    window.addEventListener('unhandledrejection', handler)
    return () => window.removeEventListener('unhandledrejection', handler)
  }, [toast])

  return null
}
