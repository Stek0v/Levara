'use client'

import { useState, useEffect, useRef, useMemo } from 'react'
import { useDatasets, useDatasetGraph } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { Share2 } from 'lucide-react'
import * as d3 from 'd3'
import type { GraphNode, GraphEdge } from '@/lib/api'

// Local aliases keep d3 simulation types stable while referencing the
// canonical shape from @/lib/api. Before T7 this file redefined GNode
// inline and pulled the data via raw fetch().
type GNode = GraphNode
type GEdge = GraphEdge
interface SimNode extends d3.SimulationNodeDatum, GNode {}
interface SimLink extends d3.SimulationLinkDatum<SimNode> { label: string }

const COLORS: Record<string, string> = {
  Person: '#3b82f6', Organization: '#10b981', Location: '#f59e0b',
  Entity: '#8b5cf6', TemporalEvent: '#ef4444', default: '#6b7280',
}

export default function GraphPage() {
  const svgRef = useRef<SVGSVGElement>(null)
  const [dsId, setDsId] = useState('')
  const [selected, setSelected] = useState<GNode | null>(null)
  const [typeFilter, setTypeFilter] = useState<Set<string>>(new Set())
  const [search, setSearch] = useState('')

  const { data: datasetsRes } = useDatasets()
  const datasets = datasetsRes?.data ?? []
  const { data: graph, isFetching: loading } = useDatasetGraph(dsId)
  const nodes = useMemo(() => graph?.nodes ?? [], [graph])
  const edges = useMemo(() => graph?.edges ?? [], [graph])

  const load = (id: string) => {
    setDsId(id)
    setSelected(null)
    // useDatasetGraph fires automatically on dsId change — no manual fetch.
  }

  const types = [...new Set(nodes.map((n) => n.type || 'Entity'))]
  const fNodes = nodes.filter((n) => {
    if (typeFilter.size > 0 && !typeFilter.has(n.type || 'Entity')) return false
    if (search && !n.name.toLowerCase().includes(search.toLowerCase())) return false
    return true
  })
  const fIds = new Set(fNodes.map((n) => n.id))
  const fEdges = edges.filter((e) => fIds.has(e.source) && fIds.has(e.target))

  useEffect(() => {
    if (!svgRef.current || fNodes.length === 0) return
    const svg = d3.select(svgRef.current); svg.selectAll('*').remove()
    const w = svgRef.current.clientWidth, h = svgRef.current.clientHeight
    const g = svg.append('g')
    svg.call(d3.zoom<SVGSVGElement, unknown>().scaleExtent([0.1, 4]).on('zoom', (e) => g.attr('transform', e.transform)))

    const sn: SimNode[] = fNodes.map((n) => ({ ...n }))
    const nm = new Map(sn.map((n) => [n.id, n]))
    const sl: SimLink[] = fEdges.filter((e) => nm.has(e.source) && nm.has(e.target))
      .map((e) => ({ source: nm.get(e.source)!, target: nm.get(e.target)!, label: e.label }))

    const sim = d3.forceSimulation(sn)
      .force('link', d3.forceLink<SimNode, SimLink>(sl).id((d) => d.id).distance(80))
      .force('charge', d3.forceManyBody().strength(-200))
      .force('center', d3.forceCenter(w / 2, h / 2))
      .force('collision', d3.forceCollide().radius(25))

    const link = g.append('g').selectAll('line').data(sl).join('line')
      .attr('stroke', '#d1d5db').attr('stroke-width', 1).attr('stroke-opacity', 0.6)
    const linkLbl = g.append('g').selectAll('text').data(sl).join('text')
      .text((d) => d.label).attr('font-size', 8).attr('fill', '#9ca3af').attr('text-anchor', 'middle')
    const node = g.append('g').selectAll('circle').data(sn).join('circle')
      .attr('r', 8).attr('fill', (d) => COLORS[d.type] || COLORS.default)
      .attr('stroke', '#fff').attr('stroke-width', 1.5).attr('cursor', 'pointer')
      .on('click', (_, d) => setSelected(d))
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    node.call(d3.drag<any, SimNode>()
        .on('start', (e, d) => { if (!e.active) sim.alphaTarget(0.3).restart(); d.fx = d.x; d.fy = d.y })
        .on('drag', (e, d) => { d.fx = e.x; d.fy = e.y })
        .on('end', (e, d) => { if (!e.active) sim.alphaTarget(0); d.fx = null; d.fy = null }) as any)
    const lbl = g.append('g').selectAll('text').data(sn).join('text')
      .text((d) => d.name.length > 20 ? d.name.slice(0, 20) + '…' : d.name)
      .attr('font-size', 10).attr('dx', 12).attr('dy', 4).attr('fill', 'currentColor')

    sim.on('tick', () => {
      link.attr('x1', (d) => (d.source as SimNode).x!).attr('y1', (d) => (d.source as SimNode).y!)
        .attr('x2', (d) => (d.target as SimNode).x!).attr('y2', (d) => (d.target as SimNode).y!)
      linkLbl.attr('x', (d) => ((d.source as SimNode).x! + (d.target as SimNode).x!) / 2)
        .attr('y', (d) => ((d.source as SimNode).y! + (d.target as SimNode).y!) / 2)
      node.attr('cx', (d) => d.x!).attr('cy', (d) => d.y!)
      lbl.attr('x', (d) => d.x!).attr('y', (d) => d.y!)
    })
    return () => { sim.stop() }
  }, [fNodes, fEdges])

  return (
    <div className="flex flex-col h-[calc(100vh-5rem)]">
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">Knowledge Graph</h1>
        <div className="flex items-center gap-2">
          <select value={dsId} onChange={(e) => load(e.target.value)}
            className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm" aria-label="Dataset">
            <option value="">Select dataset…</option>
            {datasets.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
          </select>
          <Input placeholder="Search nodes…" value={search} onChange={(e) => setSearch(e.target.value)} className="w-48" />
        </div>
      </div>

      {types.length > 0 && (
        <div className="flex gap-2 mb-3 flex-wrap">
          {types.map((t) => (
            <button key={t} onClick={() => { const s = new Set(typeFilter); s.has(t) ? s.delete(t) : s.add(t); setTypeFilter(s) }}
              className="flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium"
              style={{ backgroundColor: (!typeFilter.size || typeFilter.has(t)) ? (COLORS[t]||COLORS.default)+'20' : '#f3f4f6',
                color: (!typeFilter.size || typeFilter.has(t)) ? COLORS[t]||COLORS.default : '#9ca3af' }}>
              <span className="w-2 h-2 rounded-full" style={{ backgroundColor: COLORS[t]||COLORS.default }} />{t}
            </button>
          ))}
          {typeFilter.size > 0 && <button onClick={() => setTypeFilter(new Set())} className="text-xs text-gray-400 hover:text-gray-600">Clear</button>}
        </div>
      )}

      <div className="flex-1 flex gap-4">
        <div className="flex-1 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 relative overflow-hidden">
          {loading && <div className="absolute inset-0 flex items-center justify-center"><Skeleton className="w-32 h-32 rounded-full" /></div>}
          {!loading && !dsId && <EmptyState icon={Share2} title="Select a dataset" description="Choose a dataset to visualize its knowledge graph" className="h-full" />}
          {!loading && dsId && fNodes.length === 0 && <EmptyState icon={Share2} title="Graph is empty" description="Run Cognify to extract entities" className="h-full" />}
          <svg ref={svgRef} className="w-full h-full" />
          {fNodes.length > 0 && (
            <div className="absolute bottom-3 left-3 flex gap-2">
              <Badge variant="info">{fNodes.length} nodes</Badge>
              <Badge variant="default">{fEdges.length} edges</Badge>
            </div>
          )}
        </div>

        {selected && (
          <div className="w-72 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4 overflow-y-auto">
            <div className="flex items-center justify-between mb-3">
              <h3 className="font-medium truncate">{selected.name}</h3>
              <button onClick={() => setSelected(null)} className="text-gray-400 hover:text-gray-600 text-lg">×</button>
            </div>
            <Badge style={{ backgroundColor: (COLORS[selected.type]||COLORS.default)+'20', color: COLORS[selected.type]||COLORS.default }}>
              {selected.type || 'Entity'}
            </Badge>
            <div className="mt-3 space-y-2 text-sm">
              <div><span className="text-gray-500">ID:</span> <code className="text-xs break-all">{selected.id}</code></div>
              {selected.properties && Object.entries(selected.properties).map(([k, v]) => (
                <div key={k}><span className="text-gray-500">{k}:</span> <span className="ml-1">{String(v)}</span></div>
              ))}
            </div>
            <h4 className="font-medium mt-4 mb-2 text-sm">Connections</h4>
            <div className="space-y-1">
              {fEdges.filter((e) => e.source === selected.id || e.target === selected.id).slice(0, 20).map((e, i) => {
                const otherId = e.source === selected.id ? e.target : e.source
                const other = fNodes.find((n) => n.id === otherId)
                return (
                  <div key={i} className="text-xs text-gray-500 flex items-center gap-1">
                    <span>→</span><Badge variant="default" className="text-[10px]">{e.label}</Badge>
                    <button onClick={() => other && setSelected(other)} className="text-blue-600 hover:underline truncate">{other?.name || otherId}</button>
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
