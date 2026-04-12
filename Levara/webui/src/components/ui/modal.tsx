'use client'

import { useEffect, useRef } from 'react'
import { cn } from '@/lib/utils'
import { X } from 'lucide-react'
import { Button } from './button'

interface ModalProps {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  children?: React.ReactNode
  footer?: React.ReactNode
  size?: 'sm' | 'md' | 'lg'
}

const sizes = { sm: 'max-w-sm', md: 'max-w-md', lg: 'max-w-lg' }

export function Modal({ open, onClose, title, description, children, footer, size = 'md' }: ModalProps) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const firstFocusRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (open) {
      dialogRef.current?.showModal()
      firstFocusRef.current?.focus()
    } else {
      dialogRef.current?.close()
    }
  }, [open])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    if (open) document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [open, onClose])

  if (!open) return null

  return (
    <dialog
      ref={dialogRef}
      className="fixed inset-0 z-50 bg-transparent p-0 m-0 max-w-none max-h-none w-full h-full"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      {/* Backdrop */}
      <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4">
        {/* Panel */}
        <div className={cn('bg-white dark:bg-gray-900 rounded-lg shadow-xl w-full', sizes[size])}>
          {/* Header */}
          <div className="flex items-center justify-between px-5 py-4 border-b border-gray-200 dark:border-gray-800">
            <div>
              <h2 className="text-lg font-semibold">{title}</h2>
              {description && <p className="text-sm text-gray-500 mt-0.5">{description}</p>}
            </div>
            <button ref={firstFocusRef} onClick={onClose} className="p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-800" aria-label="Close">
              <X className="h-5 w-5" />
            </button>
          </div>

          {/* Body */}
          {children && <div className="px-5 py-4">{children}</div>}

          {/* Footer */}
          {footer && (
            <div className="flex justify-end gap-2 px-5 py-3 border-t border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50 rounded-b-lg">
              {footer}
            </div>
          )}
        </div>
      </div>
    </dialog>
  )
}

// Convenience: Confirm modal
interface ConfirmModalProps {
  open: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  description: string
  confirmLabel?: string
  variant?: 'primary' | 'danger'
  loading?: boolean
}

export function ConfirmModal({ open, onClose, onConfirm, title, description, confirmLabel = 'Confirm', variant = 'danger', loading }: ConfirmModalProps) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      description={description}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant={variant} onClick={onConfirm} loading={loading}>{confirmLabel}</Button>
        </>
      }
    />
  )
}
