'use client'

import { useState } from 'react'
import { useTask, useTasks } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { ListTodo, Clock, ShieldAlert, CircleCheck, CircleDashed, LoaderCircle } from 'lucide-react'
import { useT } from '@/lib/i18n'


const statusVariant = (s: string) =>
  s === 'completed' || s === 'passed'
    ? 'success'
    : s === 'failed' || s === 'blocked'
      ? 'error'
      : s === 'in_progress' || s === 'claimed'
        ? 'warning'
        : 'default'

function StepCounts({ counts }: { counts: { pending: number; claimed: number; passed: number; failed: number } }) {
  return (
    <span className="inline-flex items-center gap-3 text-xs text-muted-foreground">
      <span className="inline-flex items-center gap-1">
        <CircleDashed className="h-3 w-3" /> {counts.pending}
      </span>
      <span className="inline-flex items-center gap-1">
        <LoaderCircle className="h-3 w-3" /> {counts.claimed}
      </span>
      <span className="inline-flex items-center gap-1">
        <CircleCheck className="h-3 w-3" /> {counts.passed}
      </span>
      <span className="inline-flex items-center gap-1 text-red-500">
        ✕ {counts.failed}
      </span>
    </span>
  )
}

export default function TasksPage() {
  const t = useT()
  const [selected, setSelected] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState('')
  const { data, isLoading } = useTasks({ status: statusFilter || undefined, limit: 100 })
  const detail = useTask(selected)

  const tasks = data?.tasks ?? []

  return (
    <div>
      <div className="flex items-center justify-between mb-2">
        <h1 className="text-2xl font-bold flex items-center gap-2">
          <ListTodo className="h-6 w-6" /> {t('tasks.title')}
        </h1>
        <Badge variant="warning">{t('tasks.alpha')}</Badge>
      </div>
      <p className="text-sm text-muted-foreground mb-6">
        Observation surface for the long-horizon task runtime. Mutations stay in the
        {t('tasks.desc')}
      </p>

      <div className="flex gap-2 mb-4">
        {['', 'draft', 'in_progress', 'blocked', 'completed'].map((s) => (
          <button
            key={s}
            onClick={() => setStatusFilter(s)}
            className={`px-3 py-1 rounded-md text-sm border ${
              statusFilter === s ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'
            }`}
          >
            {s === '' ? 'all' : s}
          </button>
        ))}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <div className="space-y-3">
          {isLoading && <p className="text-sm text-muted-foreground">{t('tasks.loading')}</p>}
          {!isLoading && tasks.length === 0 && (
            <p className="text-sm text-muted-foreground">{t('tasks.nomatch')}</p>
          )}
          {tasks.map((t) => (
            <button
              key={t.id}
              onClick={() => setSelected(t.id)}
              className={`w-full text-left rounded-lg border p-4 transition-colors ${
                selected === t.id ? 'border-primary' : 'hover:border-muted-foreground/40'
              }`}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="font-medium line-clamp-1">{t.objective}</span>
                <Badge variant={statusVariant(t.status)}>{t.status}</Badge>
              </div>
              <div className="mt-2 flex items-center justify-between text-xs text-muted-foreground">
                <span>
                  {t.collection_name}/{t.room} · {t.owner_id || '—'}
                </span>
                <StepCounts counts={t.step_counts} />
              </div>
              {t.blocker_count > 0 && (
                <div className="mt-2 inline-flex items-center gap-1 text-xs text-red-500">
                  <ShieldAlert className="h-3 w-3" /> {t.blocker_count} active blocker
                  {t.blocker_count > 1 ? 's' : ''}
                </div>
              )}
            </button>
          ))}
        </div>

        <div>
          {!selected && (
            <div className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
              Select a task to inspect its plan, receipts and checkpoints.
            </div>
          )}
          {selected && detail.data && (
            <div className="space-y-4">
              <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
                <div className="flex items-center justify-between mb-3">
                  <h3 className="text-sm font-semibold">{t('tasks.plan')}</h3>
                  <Badge variant={statusVariant(detail.data.status)}>{detail.data.status}</Badge>
                </div>
                <ol className="space-y-2">
                  {detail.data.steps.map((s) => (
                    <li key={s.id} className="flex items-start gap-2 text-sm">
                      <span className="mt-0.5">
                        {s.status === 'passed' ? (
                          <CircleCheck className="h-4 w-4 text-green-500" />
                        ) : s.status === 'claimed' ? (
                          <LoaderCircle className="h-4 w-4 text-amber-500" />
                        ) : s.status === 'failed' ? (
                          <span className="text-red-500">✕</span>
                        ) : (
                          <CircleDashed className="h-4 w-4 text-muted-foreground" />
                        )}
                      </span>
                      <span>
                        {s.description}
                        {s.leased_by && (
                          <span className="ml-2 text-xs text-amber-600">lease: {s.leased_by}</span>
                        )}
                        {s.attempts > 1 && (
                          <span className="ml-2 text-xs text-muted-foreground">attempt {s.attempts}</span>
                        )}
                      </span>
                    </li>
                  ))}
                </ol>
              </section>

              {detail.data.blockers.length > 0 && (
                <section className="bg-white dark:bg-gray-900 rounded-lg border border-red-300 dark:border-red-900 p-5">
                  <h3 className="text-sm font-semibold mb-3 text-red-500">{t('tasks.blockers')}</h3>
                  <ul className="space-y-1 text-sm">
                    {detail.data.blockers.map((b, i) => (
                      <li key={i}>
                        {String(b.reason)}
                        {b.required_decision ? (
                          <span className="text-muted-foreground">
                            {' '}— needs: {String(b.required_decision)}
                          </span>
                        ) : null}
                      </li>
                    ))}
                  </ul>
                </section>
              )}

              <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
                <h3 className="text-sm font-semibold mb-3">{t('tasks.receipts')}</h3>
                {detail.data.receipts.length === 0 && (
                  <p className="text-sm text-muted-foreground">{t('tasks.noreceipts')}</p>
                )}
                <ul className="space-y-2 text-sm">
                  {detail.data.receipts.slice(0, 5).map((r, i) => (
                    <li key={i} className="flex items-center gap-2">
                      <Badge variant={statusVariant(String(r.status))}>{String(r.receipt_type)}</Badge>
                      <span className="line-clamp-1 text-muted-foreground">
                        {String(r.observation || '(no observation)')}
                      </span>
                    </li>
                  ))}
                </ul>
              </section>

              <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
                <h3 className="text-sm font-semibold mb-3 flex items-center gap-2">
                  <Clock className="h-4 w-4" /> Checkpoints
                </h3>
                {detail.data.checkpoints.length === 0 && (
                  <p className="text-sm text-muted-foreground">{t('tasks.nockpoints')}</p>
                )}
                <ul className="space-y-1 text-sm">
                  {detail.data.checkpoints.slice(0, 5).map((c, i) => (
                    <li key={i}>{String(c.summary)}</li>
                  ))}
                </ul>
              </section>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
