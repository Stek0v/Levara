'use client'

import { Suspense, useEffect, useState } from 'react'
import { useSearchParams } from 'next/navigation'
import { levara, ApiError, sanitizeAuthNext, SSO_NEXT_KEY, type AuthMethods } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

// useSearchParams in Next.js 15/16 forces the parent to opt into client-side
// bailout at build time. Wrapping the hook consumer in <Suspense> satisfies
// the CSR-bailout guard and lets /login still prerender its static shell.
export default function LoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginForm />
    </Suspense>
  )
}

function LoginForm() {
  const t = useT()
  const locale = t('login.submit') === 'Войти' ? 'ru' : 'en'
  const searchParams = useSearchParams()
  const nextUrl = sanitizeAuthNext(searchParams.get('next'))
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [isRegister, setIsRegister] = useState(false)
  const [directory, setDirectory] = useState(false)
  const [methods, setMethods] = useState<AuthMethods | null>(null)
  const [methodsError, setMethodsError] = useState(false)
  const [retry, setRetry] = useState(0)
  const [unavailable, setUnavailable] = useState(searchParams.get('error') === 'unavailable')
  const retryConnection = async () => {
    setRetry(value => value + 1)
    if (unavailable) {
      try {
        await levara.me()
        window.location.assign(nextUrl)
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) setUnavailable(false)
      }
    }
  }

  useEffect(() => {
    let cancelled = false
    levara.authMethods().then(value => {
      if (!cancelled) { setMethods(value); setMethodsError(false) }
    }).catch(() => { if (!cancelled) setMethodsError(true) })
    return () => { cancelled = true }
  }, [retry])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!email || !password) return
    setError('')
    setLoading(true)

    try {
      sessionStorage.removeItem(SSO_NEXT_KEY)
      if (directory) {
        await levara.directoryLogin(email, password)
      } else if (isRegister) {
        await levara.register(email, password)
      } else {
        await levara.login(email, password)
      }
      // A full navigation clears account-specific React Query state.
      window.location.assign(nextUrl)
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError(locale === 'ru' ? 'Нет соединения с сервером' : 'Connection failed. Is the server running?')
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50 dark:bg-gray-950 px-4">
      <div className="w-full max-w-sm">
        {/* Logo */}
        <div className="flex justify-center mb-8">
          <div className="flex items-center gap-3">
            <div className="h-10 w-10 rounded-xl bg-blue-600 flex items-center justify-center">
              <span className="text-white font-bold text-xl">L</span>
            </div>
            <span className="text-2xl font-bold">Levara</span>
          </div>
        </div>

        {/* Form */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-6 shadow-sm">
          <h1 className="text-lg font-semibold text-center mb-6">
            {isRegister ? t('login.register') : t('login.submit')}
          </h1>

          {(unavailable || methodsError) && (
            <div className="mb-4 text-sm text-amber-700" role="alert">
              <p>{locale === 'ru' ? 'Не удалось проверить соединение с сервером. Попробуйте ещё раз.' : 'Could not verify the server connection. Please try again.'}</p>
              <button type="button" className="underline mt-1" onClick={retryConnection}>{locale === 'ru' ? 'Повторить' : 'Retry'}</button>
            </div>
          )}

          {methods?.directory && methods.password && !isRegister && (
            <div className="flex gap-2 mb-4">
              <Button type="button" variant={directory ? 'ghost' : 'secondary'} disabled={loading} onClick={() => { setDirectory(false); setError(''); setEmail(''); setPassword('') }}>{locale === 'ru' ? 'Локальная учётная запись' : 'Local account'}</Button>
              <Button type="button" variant={directory ? 'secondary' : 'ghost'} disabled={loading} onClick={() => { setDirectory(true); setError(''); setEmail(''); setPassword('') }}>{locale === 'ru' ? 'Учётная запись каталога' : 'Directory account'}</Button>
            </div>
          )}

          {(methods?.password || methods?.directory || methodsError) && <form onSubmit={handleSubmit} className="space-y-4">
            <Input
              label={directory ? (locale === 'ru' ? 'Имя пользователя' : 'Username') : t('login.email')}
              type={directory ? 'text' : 'email'}
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder={directory ? 'username' : 'you@example.com'}
              required
              autoFocus
            />
            <Input
              label={t('login.password')}
              type="password"
              autoComplete={isRegister ? 'new-password' : 'current-password'}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="••••••••"
              required
              error={error || undefined}
            />
            <Button type="submit" loading={loading} className="w-full">
              {isRegister ? t('login.register') : t('login.submit')}
            </Button>
          </form>}

          {methods?.registration && !directory && <div className="mt-4 text-center">
            <button
              type="button"
              onClick={() => { setIsRegister(!isRegister); setError('') }}
              className="text-sm text-blue-600 hover:text-blue-700 dark:text-blue-400"
            >
              {isRegister ? t('login.haveAccount') + ' ' + t('login.submit') : t('login.noAccount') + ' ' + t('login.register')}
            </button>
          </div>}
          {!methods && !methodsError && <p role="status" className="text-sm text-center">{locale === 'ru' ? 'Проверка способов входа…' : 'Checking sign-in methods…'}</p>}
          {!isRegister && (methods?.oidc || methods?.saml) && <div className="mt-4 space-y-2">
            {methods.oidc && <Button type="button" variant="secondary" className="w-full" disabled={loading} onClick={() => levara.startSSO('oidc', nextUrl)}>Continue with OIDC</Button>}
            {methods.saml && <Button type="button" variant="secondary" className="w-full" disabled={loading} onClick={() => levara.startSSO('saml', nextUrl)}>Continue with SAML</Button>}
          </div>}
        </div>

        <p className="text-center text-xs text-gray-400 mt-4">
          Levara — Knowledge memory system
        </p>
      </div>
    </div>
  )
}
