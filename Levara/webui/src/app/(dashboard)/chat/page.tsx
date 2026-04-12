'use client'

import { useState, useRef, useEffect } from 'react'
import { levara, type SearchRequest } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Send, Bot, User, RotateCcw } from 'lucide-react'

interface Message {
  role: 'user' | 'assistant'
  content: string
  sources?: { id: string; score: number; text: string; collection: string }[]
  timestamp: number
  searchType?: string
}

export default function ChatPage() {
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const [loading, setLoading] = useState(false)
  const [sessionId] = useState(() => crypto.randomUUID())
  const [mode, setMode] = useState<'RAG_COMPLETION' | 'GRAPH_COMPLETION_COT'>('RAG_COMPLETION')
  const bottomRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const handleSend = async () => {
    const q = input.trim()
    if (!q || loading) return

    const userMsg: Message = { role: 'user', content: q, timestamp: Date.now() }
    setMessages((prev) => [...prev, userMsg])
    setInput('')
    setLoading(true)

    try {
      const params: SearchRequest = {
        query_text: q,
        query_type: mode,
        top_k: 5,
        session_id: sessionId,
      }
      const data = await levara.search(params)

      let answer = ''
      let sources: Message['sources'] = []

      if (Array.isArray(data)) {
        answer = 'Found results but no AI answer. Try RAG mode.'
        sources = data.slice(0, 3).map((r) => ({
          id: r.id,
          score: r.score,
          text: ((r.metadata?.text as string) || '').slice(0, 200),
          collection: r.collection,
        }))
      } else {
        const d = data as Record<string, unknown>
        answer = (d.answer as string) || 'No answer generated'
        const chunks = (d.chunks as Array<Record<string, unknown>>) || []
        sources = chunks.slice(0, 3).map((c) => ({
          id: c.id as string,
          score: c.score as number,
          text: (((c.metadata as Record<string, unknown>)?.text as string) || '').slice(0, 200),
          collection: c.collection as string,
        }))
      }

      const assistantMsg: Message = {
        role: 'assistant',
        content: answer,
        sources,
        timestamp: Date.now(),
        searchType: mode,
      }
      setMessages((prev) => [...prev, assistantMsg])
    } catch (err) {
      setMessages((prev) => [
        ...prev,
        {
          role: 'assistant',
          content: `Error: ${err instanceof Error ? err.message : 'Unknown error'}`,
          timestamp: Date.now(),
        },
      ])
    } finally {
      setLoading(false)
      inputRef.current?.focus()
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  return (
    <div className="flex flex-col h-[calc(100vh-5rem)]">
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">Chat</h1>
        <div className="flex items-center gap-2">
          <select
            value={mode}
            onChange={(e) => setMode(e.target.value as typeof mode)}
            className="h-8 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 text-sm"
            aria-label="Chat mode"
          >
            <option value="RAG_COMPLETION">RAG</option>
            <option value="GRAPH_COMPLETION_COT">Chain of Thought</option>
          </select>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setMessages([])}
            title="Clear chat"
          >
            <RotateCcw className="h-4 w-4" />
          </Button>
        </div>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto space-y-4 pb-4">
        {messages.length === 0 && (
          <div className="flex flex-col items-center justify-center h-full text-center text-gray-400">
            <Bot className="h-12 w-12 mb-3" strokeWidth={1.5} />
            <p className="text-lg font-medium">Ask a question</p>
            <p className="text-sm mt-1">Levara will search your knowledge base and generate an answer</p>
          </div>
        )}

        {messages.map((msg, i) => (
          <div key={i} className={`flex gap-3 ${msg.role === 'user' ? 'justify-end' : ''}`}>
            {msg.role === 'assistant' && (
              <div className="flex-shrink-0 w-8 h-8 rounded-full bg-blue-100 dark:bg-blue-900 flex items-center justify-center">
                <Bot className="h-4 w-4 text-blue-600 dark:text-blue-300" />
              </div>
            )}
            <div
              className={`max-w-[80%] rounded-lg px-4 py-3 ${
                msg.role === 'user'
                  ? 'bg-blue-600 text-white'
                  : 'bg-white dark:bg-gray-900 border border-gray-200 dark:border-gray-800'
              }`}
            >
              <p className="whitespace-pre-wrap text-sm">{msg.content}</p>

              {/* Sources */}
              {msg.sources && msg.sources.length > 0 && (
                <div className="mt-3 pt-2 border-t border-gray-200 dark:border-gray-700 space-y-2">
                  <p className="text-xs font-medium text-gray-500 dark:text-gray-400">Sources:</p>
                  {msg.sources.map((s, j) => (
                    <div key={j} className="text-xs text-gray-500 dark:text-gray-400 bg-gray-50 dark:bg-gray-800 rounded p-2">
                      <div className="flex items-center gap-1 mb-1">
                        <Badge variant="info" className="text-[10px]">{s.collection}</Badge>
                        <span>score: {s.score.toFixed(3)}</span>
                      </div>
                      <p className="line-clamp-2">{s.text}</p>
                    </div>
                  ))}
                </div>
              )}

              {msg.searchType && (
                <div className="mt-1">
                  <Badge variant="default" className="text-[10px]">{msg.searchType}</Badge>
                </div>
              )}
            </div>
            {msg.role === 'user' && (
              <div className="flex-shrink-0 w-8 h-8 rounded-full bg-gray-200 dark:bg-gray-700 flex items-center justify-center">
                <User className="h-4 w-4" />
              </div>
            )}
          </div>
        ))}

        {loading && (
          <div className="flex gap-3">
            <div className="w-8 h-8 rounded-full bg-blue-100 dark:bg-blue-900 flex items-center justify-center">
              <Bot className="h-4 w-4 text-blue-600 animate-pulse" />
            </div>
            <div className="bg-white dark:bg-gray-900 border border-gray-200 dark:border-gray-800 rounded-lg px-4 py-3">
              <div className="flex gap-1">
                <div className="w-2 h-2 rounded-full bg-gray-400 animate-bounce" style={{ animationDelay: '0ms' }} />
                <div className="w-2 h-2 rounded-full bg-gray-400 animate-bounce" style={{ animationDelay: '150ms' }} />
                <div className="w-2 h-2 rounded-full bg-gray-400 animate-bounce" style={{ animationDelay: '300ms' }} />
              </div>
            </div>
          </div>
        )}
        <div ref={bottomRef} />
      </div>

      {/* Input */}
      <div className="border-t border-gray-200 dark:border-gray-800 pt-3">
        <div className="flex gap-2">
          <textarea
            ref={inputRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Ask a question... (Enter to send, Shift+Enter for newline)"
            rows={1}
            className="flex-1 resize-none rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500"
          />
          <Button onClick={handleSend} loading={loading} disabled={!input.trim()}>
            <Send className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}
