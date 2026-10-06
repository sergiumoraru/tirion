const PREFIX_LABELS: Record<string, string> = {
  jquery: 'jQuery',
  dom: 'DOM',
  event: 'Event',
  promise: 'Promise',
  timer: 'Timer',
}

function humanizeToken(token: string): string {
  return token
    .replace(/^_+|_+$/g, '')
    .replace(/_/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
}

function humanizeSelector(selector: string): string {
  const trimmed = selector.trim()
  if (!trimmed) return ''

  const leading = trimmed[0]
  const rest = leading === '.' || leading === '#' ? trimmed.slice(1) : trimmed
  const normalizedRest = rest.replace(/_/g, '-')
  return leading === '.' || leading === '#' ? `${leading}${normalizedRest}` : normalizedRest
}

function formatSyntheticCallbackName(name: string): string {
  const match = name.match(/^(jquery|dom|event|promise|timer)\.(.+)_L\d+$/)
  if (!match) {
    return name
  }

  const [, prefix, rawLabel] = match
  const prefixLabel = PREFIX_LABELS[prefix] ?? prefix
  const parts = rawLabel.split('_').filter(Boolean)

  if (prefix === 'jquery') {
    const [method = 'callback', maybeEvent = '', ...rest] = parts
    let event = ''
    let selector = ''

    if (method === 'ready') {
      selector = rest.length ? rest.join('_') : maybeEvent
      return `${prefixLabel} ready handler${selector ? ` (${humanizeSelector(selector)})` : ''}`
    }

    if ((method === 'on' || method === 'bind' || method === 'delegate') && maybeEvent) {
      event = humanizeToken(maybeEvent)
      selector = rest.join('_')
    } else {
      event = humanizeToken(method)
      selector = [maybeEvent, ...rest].filter(Boolean).join('_')
    }

    return `${prefixLabel} ${event || 'callback'} handler${selector ? ` (${humanizeSelector(selector)})` : ''}`
  }

  if (prefix === 'dom') {
    const event = humanizeToken(parts[0] ?? 'event')
    return `${prefixLabel} ${event} handler`
  }

  if (prefix === 'event') {
    const [method = 'event', maybeEvent = ''] = parts
    const event = maybeEvent || method
    return `${prefixLabel} ${humanizeToken(event)} handler`
  }

  if (prefix === 'promise') {
    return `${prefixLabel} ${humanizeToken(parts[0] ?? 'callback')} callback`
  }

  if (prefix === 'timer') {
    return `${humanizeToken(parts[0] ?? 'timer')} callback`
  }

  return name
}

export function formatFunctionDisplayName(name: string): string {
  if (!name) {
    return name
  }
  if (name === '_module_') {
    return 'file init'
  }
  if (/^(jquery|dom|event|promise|timer)\..+_L\d+$/.test(name)) {
    return formatSyntheticCallbackName(name)
  }
  return name
}

