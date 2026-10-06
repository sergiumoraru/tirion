import type { GraphEdge, GraphResponse } from '@/types'

export interface RepoGraphLayoutNode {
  id: string
  name: string
  properties: Record<string, unknown>
  x: number
  y: number
  size: number
}

export interface RepoGraphLayoutEdge {
  source: string
  target: string
  type: GraphEdge['type']
  count: number
}

export interface RepoGraphLayout {
  nodes: RepoGraphLayoutNode[]
  edges: RepoGraphLayoutEdge[]
}

const REPO_GRAPH_LAYOUT_CACHE_VERSION = 4

let cachedRepoGraphResponse: GraphResponse | null = null
let cachedRepoGraphLayout: { version: number; layout: RepoGraphLayout } | null = null

export function getCachedRepoGraphResponse(): GraphResponse | null {
  return cachedRepoGraphResponse
}

export function setCachedRepoGraphResponse(response: GraphResponse): void {
  cachedRepoGraphResponse = response
}

export function getCachedRepoGraphLayout(): RepoGraphLayout | null {
  if (!cachedRepoGraphLayout) {
    return null
  }
  if (cachedRepoGraphLayout.version !== REPO_GRAPH_LAYOUT_CACHE_VERSION) {
    cachedRepoGraphLayout = null
    return null
  }
  return cachedRepoGraphLayout.layout
}

export function setCachedRepoGraphLayout(layout: RepoGraphLayout): void {
  cachedRepoGraphLayout = {
    version: REPO_GRAPH_LAYOUT_CACHE_VERSION,
    layout,
  }
}

export function clearCachedRepoGraph(): void {
  cachedRepoGraphResponse = null
  cachedRepoGraphLayout = null
}

export function repoNodeColor(name: string, menuTheme: boolean): string {
  const menuPalette = ['#2f54c9', '#1a8a6e', '#b85c2f', '#4a7fe0', '#7c4dcc', '#2e86ab', '#8e44ad', '#c27c3e', '#27ae60']
  const defaultPalette = ['#a855f7', '#ec4899', '#06b6d4', '#f97316', '#10b981', '#6366f1', '#eab308']
  const palette = menuTheme ? menuPalette : defaultPalette

  let hash = 0
  for (let i = 0; i < name.length; i++) {
    hash = (hash * 31 + name.charCodeAt(i)) | 0
  }
  return palette[Math.abs(hash) % palette.length]
}

export function filterRepoGraph(response: GraphResponse): RepoGraphLayout {
  const nodes = response.nodes ?? []
  const edges = response.edges ?? []
  const sortedEdges = [...edges].sort((a, b) => ((b.properties?.count as number) || 1) - ((a.properties?.count as number) || 1))
  const retainedEdges = sortedEdges.length > 420 ? sortedEdges.slice(0, 420) : sortedEdges

  const connectedNodeIds = new Set<string>()
  retainedEdges.forEach((edge) => {
    connectedNodeIds.add(edge.source)
    connectedNodeIds.add(edge.target)
  })

  const importantNodes = [...nodes]
    .sort((a, b) => {
      const aFunctions = (a.properties?.functions as number) || 0
      const bFunctions = (b.properties?.functions as number) || 0
      if (aFunctions !== bFunctions) return bFunctions - aFunctions
      const aEndpoints = (a.properties?.endpoints as number) || 0
      const bEndpoints = (b.properties?.endpoints as number) || 0
      return bEndpoints - aEndpoints
    })
    .slice(0, 24)

  importantNodes.forEach((node) => connectedNodeIds.add(node.id))

  const visibleNodes = nodes.filter((node) => connectedNodeIds.has(node.id))
  const maxFunctions = Math.max(1, ...visibleNodes.map((node) => (node.properties?.functions as number) || 1))

  return {
    nodes: visibleNodes.map((node, index) => {
      const functions = (node.properties?.functions as number) || 1
      return {
        id: node.id,
        name: node.name,
        properties: node.properties,
        size: 10 + (functions / maxFunctions) * 28,
        x: Math.cos((index / Math.max(visibleNodes.length, 1)) * 2 * Math.PI) * 420,
        y: Math.sin((index / Math.max(visibleNodes.length, 1)) * 2 * Math.PI) * 260,
      }
    }),
    edges: retainedEdges.map((edge) => ({
      source: edge.source,
      target: edge.target,
      type: edge.type,
      count: (edge.properties?.count as number) || 1,
    })),
  }
}
