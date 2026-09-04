'use client'

import { Suspense, useState } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { levara, ApiError } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

// Only allow same-origin relative redirects. Rejects protocol-relative (//evil.com),
// absolute URLs, and paths starting with /login (loop protection).
function sanitizeNext(raw: string | null): string {
  if (!raw) return '/'
  if (!raw.startsWith('/') || raw.startsWith('//')) return '/'
  if (raw.startsWith('/login')) return '/'
  return raw
}

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
  const router = useRouter()
  const searchParams = useSearchParams()
  const nextUrl = sanitizeNext(searchParams.get('next'))
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [isRegister, setIsRegister] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!email || !password) return
    setError('')
    setLoading(true)

    try {
      if (isRegister) {
        await levara.register(email, password)
      } else {
        await levara.login(email, password)
      }
      router.push(nextUrl)
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

          <form onSubmit={handleSubmit} className="space-y-4">
            <Input
              label={t('login.email')}
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@example.com"
              required
              autoFocus
            />
            <Input
              label={t('login.password')}
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="••••••••"
              required
              error={error || undefined}
            />
            <Button type="submit" loading={loading} className="w-full">
              {isRegister ? t('login.register') : t('login.submit')}
            </Button>
          </form>

          <div className="mt-4 text-center">
            <button
              type="button"
              onClick={() => { setIsRegister(!isRegister); setError('') }}
              className="text-sm text-blue-600 hover:text-blue-700 dark:text-blue-400"
            >
              {isRegister ? t('login.haveAccount') + ' ' + t('login.submit') : t('login.noAccount') + ' ' + t('login.register')}
            </button>
          </div>
        </div>

        <p className="text-center text-xs text-gray-400 mt-4">
          Levara — Knowledge memory system
        </p>
      </div>
    </div>
  )
}
