export function StubPanel(props: { plan: string; what: string }) {
  // The backend's rule, kept on this side of the wire: stubs never fake
  // success. A stub that rendered plausible empty content would read as a
  // working screen with no data — which is indistinguishable, to the person
  // looking at it, from the feature being broken.
  return (
    <section className="border-border text-text-muted rounded-md border border-dashed p-6 text-sm">
      <p className="text-text font-medium">{props.what}</p>
      <p className="font-mono text-xs">not built yet — {props.plan}</p>
    </section>
  )
}
