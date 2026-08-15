export type StreamFrame = {
  id: number
  run_id: string
  kind: string
  payload: Record<string, unknown>
  occurred_at: string
}

export type ParsedFrame =
  | { kind: 'event'; id: number; event: string; frame: StreamFrame }
  | { kind: 'heartbeat' }
  | { kind: 'forbidden' }

/**
 * Pure incremental SSE framing. No I/O, no clock, no reconnection.
 *
 * THE WIRE IS ONE SHAPE AND THIS PARSES ONE SHAPE. The server emits its
 * opening snapshot as ordinary event frames — there is no snapshot
 * discriminator and no client-visible watermark, so a client cannot be built
 * on one. Do not add a "snapshot" variant here; there is nothing on the wire
 * to populate it from.
 *
 * Frames are separated by a bare `\n\n`, which is what the server writes
 * (`realtime/sse.go`: `id: %d\nevent: %s\ndata: %s\n\n`, and the keepalive
 * comment `: hb\n\n`). CRLF — legal SSE, never produced here, and impossible
 * for the same-origin embedded deployment to acquire in transit — is
 * deliberately not handled: a `\r` would land inside a field value and fail
 * loudly at JSON.parse rather than silently mis-framing.
 */
export function createSseParser() {
  let buffer = ''

  return {
    push(chunk: string): ParsedFrame[] {
      buffer += chunk
      const out: ParsedFrame[] = []

      let sep = buffer.indexOf('\n\n')
      while (sep !== -1) {
        const block = buffer.slice(0, sep)
        buffer = buffer.slice(sep + 2)
        const parsed = parseBlock(block)
        if (parsed !== null) out.push(parsed)
        sep = buffer.indexOf('\n\n')
      }
      return out
    },
  }
}

function parseBlock(block: string): ParsedFrame | null {
  if (block === '') return null

  let id: number | null = null
  let event = ''
  let data = ''
  let sawComment = false

  for (const line of block.split('\n')) {
    if (line.startsWith(':')) {
      // The keepalive. Surfaced rather than skipped: the watchdog resets on it.
      sawComment = true
      continue
    }
    const colon = line.indexOf(':')
    if (colon === -1) continue
    const field = line.slice(0, colon)
    const value = line.slice(colon + 1).replace(/^ /, '')
    if (field === 'id') id = Number.parseInt(value, 10)
    else if (field === 'event') event = value
    else if (field === 'data') data = data === '' ? value : `${data}\n${value}`
  }

  if (event === 'forbidden') {
    // Its own variant, and it always ends the connection. It carries no id by
    // design — see sse-connection.ts for what the client does about it, which
    // is emphatically not "conclude the user lost access".
    return { kind: 'forbidden' }
  }
  if (id === null || Number.isNaN(id)) {
    return sawComment ? { kind: 'heartbeat' } : null
  }

  let frame: StreamFrame
  try {
    frame = JSON.parse(data) as StreamFrame
  } catch {
    return null
  }
  return { kind: 'event', id, event, frame }
}
