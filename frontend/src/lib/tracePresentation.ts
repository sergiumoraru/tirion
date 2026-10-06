import type { TraceNode } from '@/types'

// Keep graph paths and integration evidence in the main tree. A leaf call with
// no indexed destination is still useful call-site evidence, but cannot extend
// the path. Group it under its caller without guessing whether its name belongs
// to a library or to unresolved application code.
export function isUnindexedLeafCall(node: TraceNode): boolean {
  if (node.file || node.repo || node.caller_id || node.children?.length || node.injected) return false
  if (node.is_cross_service || node.is_sqs || node.http_method || node.http_target || node.queue_target) return false
  if (node.edge_type && node.edge_type !== 'call') return false
  if (node.source) return false
  const source = node.evidence?.source
  if (source && source !== 'function_calls' && source !== 'pending_calls') return false
  return source === 'function_calls' || source === 'pending_calls' || node.edge_type === 'call'
}

const callSiteSources = new Set([
  'function_calls', 'pending_calls', 'http_client', 'http_interface',
  'sqs_producer', 'function_callers', 'pending_callers',
])
const callerSources = new Set(['function_callers', 'pending_callers'])

function location(file: string | undefined, line: number | undefined, repo: string | undefined): string {
  const path = file ? `${file}${line && line > 0 ? `:${line}` : ''}`
    : line && line > 0 ? `line ${line} (file unavailable)` : ''
  return `${path}${repo ? ` [${repo}]` : ''}`.trim()
}

export function traceLocations(node: TraceNode, isRoot: boolean): { target: string; evidence: string } {
  const source = node.evidence?.source ?? ''
  const isCallSite = callSiteSources.has(source)
  const isCaller = callerSources.has(source)
  const isDefinition = isRoot && !source && !node.edge_type
  // A downstream node's file is the target file, but its line commonly belongs
  // to the caller. Never concatenate those into a fabricated source location.
  const target = node.file
    ? `${isDefinition ? 'Definition' : isCaller ? 'Caller file' : 'Target file'}: ${location(node.file, isDefinition ? node.line : undefined, node.repo)}`
    : node.repo ? `Repository: ${node.repo}` : ''
  const evidence = node.evidence
  const evidencePath = location(evidence?.file, evidence?.line, evidence?.repo)
  if (evidencePath) {
    const label = isCallSite ? 'Call site' : 'Evidence'
    return {
      target: isCaller && evidence?.file === node.file ? '' : target,
      evidence: `${label}: ${evidencePath}`,
    }
  }
  if (node.line && !isDefinition) {
    return {
      target,
      evidence: `${isCallSite ? 'Call site' : 'Reported location'}: ${location(isCaller ? node.file : undefined, node.line, isCaller ? node.repo : undefined)}`,
    }
  }
  return { target, evidence: '' }
}
