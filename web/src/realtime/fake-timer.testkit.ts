/**
 * A CONTROLLABLE `setTimeout`, and it is the only seam the bridge's tests use.
 *
 * The plan gave the bridge a `flush()` and had every test call it. MEASURED:
 * that made the whole suite vacuous for the code production actually runs —
 * delete the scheduling arm entirely and all five plan tests still pass, while
 * the shipped bridge never invalidates anything, because nothing in
 * `useAttentionStream` calls `flush`. A seam with no production caller is not a
 * seam, it is a second implementation that only the tests exercise. So `flush`
 * is gone and the window is driven here instead: `run()` is the timer firing,
 * which is exactly what happens in the browser 250ms later.
 *
 * SHARED rather than copied, since Task 12: `cache-bridge.test.ts` and
 * `refetch-fan.test.tsx` need the identical seam, and two copies of a helper
 * whose whole purpose is to be the ONLY seam would drift with nothing going
 * red. A `*.testkit.ts` beside its subject, the convention `on-the-wire` and
 * `decision-rules` already set — and one `web:arch` is quiet about, because
 * `findMisplacedModules` does not count a test as a consumer.
 */
export function fakeTimer() {
  const scheduled: Array<{ fn: () => void; ms: number }> = []
  const impl = ((fn: () => void, ms?: number) => {
    scheduled.push({ fn, ms: ms ?? 0 })
    return 0 as unknown as ReturnType<typeof setTimeout>
  }) as unknown as typeof setTimeout
  return {
    impl,
    get count() {
      return scheduled.length
    },
    delays: () => scheduled.map((s) => s.ms),
    /**
     * Fire everything pending. Drains first, so a callback that schedules
     * another window is observable rather than re-run.
     */
    run() {
      const due = scheduled.splice(0, scheduled.length)
      for (const s of due) s.fn()
    },
    /**
     * Fire only the timers armed at THIS delay.
     *
     * Two tiers means two delays live in one queue, and `run()` cannot express
     * "ten debounce windows inside one throttle hold" — the drain that fires
     * the second window would fire the throttle with it, which is the exact
     * arrangement the collapse is about. Selecting by delay is also how a test
     * says WHICH timer it means without holding an index into a private queue.
     */
    runOnly(ms: number) {
      const due: Array<() => void> = []
      for (let i = scheduled.length - 1; i >= 0; i--) {
        const s = scheduled[i] as { fn: () => void; ms: number }
        if (s.ms === ms) {
          due.unshift(s.fn)
          scheduled.splice(i, 1)
        }
      }
      for (const fn of due) fn()
    },
  }
}
