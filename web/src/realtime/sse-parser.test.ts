import { describe, expect, it } from 'vitest'
import { createSseParser } from './sse-parser'

const frameBody = (id: number, kind: string) => ({
  id,
  run_id: 'r1',
  kind,
  payload: {},
  occurred_at: '2026-08-15T00:00:00Z',
})

const eventFrame = (id: number, kind = 'gate.waiting') =>
  `id: ${id}\nevent: ${kind}\ndata: ${JSON.stringify(frameBody(id, kind))}\n\n`

describe('createSseParser', () => {
  it('parses one complete frame', () => {
    const p = createSseParser()
    const out = p.push(eventFrame(7))
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ kind: 'event', id: 7, event: 'gate.waiting' })
  })

  it('parses the data: line into the StreamFrame rather than carrying it as text', () => {
    // Without this the first test passes on a parser that never touched data:
    // — id and event alone come from the OTHER two lines. Every consumer of
    // this layer reads `frame`, so an unparsed or dropped body would surface as
    // a bridge that invalidates nothing, three layers away from the cause.
    const p = createSseParser()
    const out = p.push(
      `id: 7\nevent: gate.waiting\ndata: ${JSON.stringify({
        id: 7,
        run_id: 'r1',
        kind: 'gate.waiting',
        payload: { node_id: 'n1', decision: 'pending' },
        occurred_at: '2026-08-15T00:00:00Z',
      })}\n\n`,
    )
    expect(out).toEqual([
      {
        kind: 'event',
        id: 7,
        event: 'gate.waiting',
        frame: {
          id: 7,
          run_id: 'r1',
          kind: 'gate.waiting',
          payload: { node_id: 'n1', decision: 'pending' },
          occurred_at: '2026-08-15T00:00:00Z',
        },
      },
    ])
  })

  it('reassembles a frame split across chunks at an arbitrary byte', () => {
    // The transport hands over whatever the network gave it. A parser that
    // assumed chunk boundaries were frame boundaries would work on a fast
    // localhost and drop frames in production.
    const whole = eventFrame(1)
    const [expected] = createSseParser().push(whole)
    for (let cut = 1; cut < whole.length; cut++) {
      const p = createSseParser()
      const first = p.push(whole.slice(0, cut))
      const out = [...first, ...p.push(whole.slice(cut))]
      const events = out.filter((f) => f.kind === 'event')
      expect(events).toHaveLength(1)
      // And the reassembled frame is the SAME frame, not merely one frame: a
      // parser that emitted an event whose body was the truncated first half
      // would satisfy the count alone.
      expect(events[0]).toEqual(expected)
      // Nothing is emitted before the terminator arrives. `\n\n` only ever
      // completes at the very end of this frame, so every earlier cut must
      // yield an empty first push.
      if (cut < whole.length) expect(first).toEqual([])
    }
  })

  it('consumes ": hb" comment heartbeats as keepalives rather than skipping them as junk', () => {
    // The server pads an idle stream every 20s with an SSE COMMENT. The
    // watchdog resets on it, so the parser must SURFACE it, not silently drop
    // it — a parser that dropped it would leave the watchdog firing on a
    // perfectly healthy idle stream.
    const p = createSseParser()
    expect(p.push(': hb\n\n')).toEqual([{ kind: 'heartbeat' }])
  })

  it('treats any comment line as a keepalive, not only the literal ": hb"', () => {
    // A parser that string-matched ": hb" passes the test above and starves its
    // watchdog the day a proxy pads the stream with its own comment. The rule
    // is the SSE one — a line beginning with ':' is a comment — and this pins
    // it against the cheaper literal.
    const p = createSseParser()
    expect(p.push(': keep-alive\n\n')).toEqual([{ kind: 'heartbeat' }])
  })

  it('reports the forbidden frame as its own variant, with no id', () => {
    // The terminal frame carries NO id: — it is not an event, and a client
    // that adopted it as a resume point would store a position naming nothing.
    const p = createSseParser()
    expect(p.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')).toEqual([
      { kind: 'forbidden' },
    ])
  })

  it('handles heartbeats interleaved between frames', () => {
    const p = createSseParser()
    const out = p.push(`${eventFrame(1)}: hb\n\n${eventFrame(2)}`)
    expect(out.map((f) => f.kind)).toEqual(['event', 'heartbeat', 'event'])
  })

  it('emits a replayed backlog as-is and never dedupes on its own', () => {
    // The server deliberately re-sends a backlog on resume — "one duplicated
    // backlog rather than a gap". Dedup is not the parser's job; the bridge's
    // invalidation is idempotent under duplicates and that is the design.
    const p = createSseParser()
    const out = p.push(eventFrame(5) + eventFrame(5) + eventFrame(6))
    expect(out.map((f) => (f.kind === 'event' ? f.id : null))).toEqual([5, 5, 6])
  })

  it('drops a frame whose data: is not JSON instead of throwing', () => {
    // A throw here kills the transport's read loop, and the transport cannot
    // tell a malformed frame from a dead socket. Dropping keeps the stream
    // alive and keeps the damage to the one frame.
    const p = createSseParser()
    expect(p.push('id: 3\nevent: gate.waiting\ndata: {not json\n\n')).toEqual([])
    // …and the parser is still usable afterwards: the buffer is not left
    // holding the bad block.
    expect(p.push(eventFrame(4))).toHaveLength(1)
  })

  it('strips exactly one leading space from a field value', () => {
    // `data:{...}` with no space is legal SSE and the value is then the whole
    // rest of the line. A parser that always sliced one character would eat the
    // leading `{`; one that stripped every leading space would corrupt a value
    // that meant to start with one.
    const p = createSseParser()
    const body = JSON.stringify(frameBody(9, 'gate.waiting'))
    expect(p.push(`id:9\nevent:gate.waiting\ndata:${body}\n\n`)).toEqual([
      { kind: 'event', id: 9, event: 'gate.waiting', frame: frameBody(9, 'gate.waiting') },
    ])
  })
})
