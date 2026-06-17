'use client'

import { useState, useEffect, useRef, useCallback } from 'react'

interface SSEOptions {
  onMessage?: (data: unknown) => void
  onError?: (error: Event) => void
  maxRetries?: number
  enabled?: boolean
}

interface SSEState {
  status: 'connecting' | 'connected' | 'reconnecting' | 'disconnected' | 'error'
  data: unknown | null
  retryCount: number
}

export function useSSE(url: string | null, options: SSEOptions = {}) {
  const { onMessage, onError, maxRetries = 10, enabled = true } = options
  const [state, setState] = useState<SSEState>({ status: 'disconnected', data: null, retryCount: 0 })
  const esRef = useRef<EventSource | null>(null)
  const retryRef = useRef(0)
  const timerRef = useRef<ReturnType<typeof setTimeout>>(null)
  // Stable reference to the connect function so the onerror handler
  // inside connect's closure can schedule a reconnect without referencing
  // the `connect` variable before it is assigned by useCallback (TDZ).
  const connectRef = useRef<() => void>(() => {})

  // Stabilise callbacks across renders (M7 from the 2d15b38 review). If
  // consumers passed inline closures for onMessage/onError, they'd change
  // identity every render and drive `connect` (via useCallback deps) to a
  // fresh closure, which `useEffect(() => connect(), [connect])` interpreted
  // as "rebuild the EventSource" — producing a reconnect storm on every
  // parent re-render. Refs keep the latest handlers reachable without
  // invalidating `connect`.
  const onMessageRef = useRef(onMessage)
  const onErrorRef = useRef(onError)
  useEffect(() => {
    onMessageRef.current = onMessage
    onErrorRef.current = onError
  }, [onMessage, onError])

  const connect = useCallback(() => {
    if (!url || !enabled) return

    setState((s) => ({ ...s, status: retryRef.current > 0 ? 'reconnecting' : 'connecting' }))

    const es = new EventSource(url, { withCredentials: true })
    esRef.current = es

    // closedByDone guards against onerror firing AFTER markComplete has
    // already closed the stream (M6 from the 2d15b38 review). Some
    // browsers/networks report the close itself as an error, which would
    // otherwise schedule a reconnect against a run that already finished.
    let closedByDone = false

    es.onopen = () => {
      retryRef.current = 0
      setState((s) => ({ ...s, status: 'connected', retryCount: 0 }))
    }

    es.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data)
        setState((s) => ({ ...s, data }))
        onMessageRef.current?.(data)
      } catch {
        setState((s) => ({ ...s, data: event.data }))
      }
    }

    es.addEventListener('progress', (event) => {
      try {
        const data = JSON.parse((event as MessageEvent).data)
        setState((s) => ({ ...s, data }))
        onMessageRef.current?.(data)
      } catch {}
    })

    // Backend emits `event: done` when a run reaches a terminal state
    // (COMPLETED / FAILED); we also accept `event: complete` as an alias
    // for older / alternate stream producers. Both close the EventSource
    // and stamp `_complete: true` on the final payload so the UI layer
    // can switch out of the "running" view without polling.
    const markComplete = (event: Event) => {
      closedByDone = true
      try {
        const data = JSON.parse((event as MessageEvent).data)
        setState({ status: 'disconnected', data: { ...data, _complete: true }, retryCount: 0 })
        onMessageRef.current?.({ ...data, _complete: true })
      } catch {
        setState((s) => ({ ...s, status: 'disconnected' }))
      }
      es.close()
    }
    es.addEventListener('done', markComplete)
    es.addEventListener('complete', markComplete)

    es.addEventListener('error', (event) => {
      try {
        const data = JSON.parse((event as MessageEvent).data)
        setState({ status: 'error', data, retryCount: retryRef.current })
        onMessageRef.current?.(data)
      } catch {}
    })

    es.onerror = (event) => {
      if (closedByDone) {
        // Terminal state already reached via `done`; close error is noise.
        return
      }
      es.close()
      onErrorRef.current?.(event)

      if (retryRef.current < maxRetries) {
        retryRef.current++
        // Exponential backoff with jitter: 1s, 2s, 4s, 8s... max 30s
        const delay = Math.min(1000 * Math.pow(2, retryRef.current - 1), 30000)
        const jitter = delay * 0.2 * (Math.random() - 0.5)
        setState((s) => ({ ...s, status: 'reconnecting', retryCount: retryRef.current }))
        timerRef.current = setTimeout(() => connectRef.current(), delay + jitter)
      } else {
        setState((s) => ({ ...s, status: 'error', retryCount: retryRef.current }))
      }
    }
  }, [url, enabled, maxRetries])

  // Keep connectRef in sync so the onerror handler can reschedule
  // reconnects without referencing `connect` before assignment (TDZ).
  useEffect(() => {
    connectRef.current = connect
  }, [connect])

  useEffect(() => {
    connect()
    return () => {
      esRef.current?.close()
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [connect])

  const reconnect = useCallback(() => {
    esRef.current?.close()
    retryRef.current = 0
    connect()
  }, [connect])

  return { ...state, reconnect }
}

// Typed hook for Cognify progress. Mirrors pkg/runreg.Status JSON tags
// on the Go side — keep this in sync if the backend adds fields.
export interface CognifyProgress {
  status?: 'RUNNING' | 'COMPLETED' | 'FAILED' | string
  stage: string
  items_total?: number
  items_processed?: number
  chunks_created?: number
  entities_extracted?: number
  edges_extracted?: number
  message?: string
  elapsed_ms?: number
  _complete?: boolean
}

export function useCognifyProgress(runId: string | null) {
  const url = runId
    ? `${process.env.NEXT_PUBLIC_API_URL || ''}/api/v1/cognify/${runId}/stream`
    : null

  return useSSE(url, { enabled: !!runId }) as {
    status: 'connecting' | 'connected' | 'reconnecting' | 'disconnected' | 'error'
    data: CognifyProgress | null
    retryCount: number
    reconnect: () => void
  }
}
