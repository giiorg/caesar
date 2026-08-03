package workflows

import "sort"

// graph is the adjacency view of a shape-valid document. The structural
// phase builds it only after validateShape passes, so node/edge lookups
// never miss.
type graph struct {
	d     Document
	index map[string]int
	out   map[string][]Edge
	in    map[string][]Edge
	order []string // topological order; recorded by cycle() when acyclic
}

func newGraph(d Document) *graph {
	g := &graph{
		d:     d,
		index: make(map[string]int, len(d.Nodes)),
		out:   make(map[string][]Edge, len(d.Nodes)),
		in:    make(map[string][]Edge, len(d.Nodes)),
	}
	for i, n := range d.Nodes {
		g.index[n.ID] = i
	}
	for _, e := range d.Edges {
		g.out[e.From] = append(g.out[e.From], e)
		g.in[e.To] = append(g.in[e.To], e)
	}
	return g
}

func (g *graph) node(id string) Node { return g.d.Nodes[g.index[id]] }

// cycle runs Kahn's algorithm with lexicographic tie-breaking. On an
// acyclic graph it records the topological order and returns nil;
// otherwise it returns the sorted ids of the nodes it could not resolve
// (the cycle members and everything reachable only through them).
func (g *graph) cycle() []string {
	deg := make(map[string]int, len(g.d.Nodes))
	var ready []string
	for _, n := range g.d.Nodes {
		deg[n.ID] = len(g.in[n.ID])
		if deg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	order := make([]string, 0, len(g.d.Nodes))
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, e := range g.out[id] {
			deg[e.To]--
			if deg[e.To] == 0 {
				ready = append(ready, e.To)
			}
		}
	}
	if len(order) == len(g.d.Nodes) {
		g.order = order
		return nil
	}
	var stuck []string
	for id, n := range deg {
		if n > 0 {
			stuck = append(stuck, id)
		}
	}
	sort.Strings(stuck)
	return stuck
}

// ancestors returns the strict transitive-predecessor set of id.
func (g *graph) ancestors(id string) map[string]bool {
	return g.walk(id, g.in, func(e Edge) string { return e.From })
}

// descendants returns the strict transitive-successor set of id.
func (g *graph) descendants(id string) map[string]bool {
	return g.walk(id, g.out, func(e Edge) string { return e.To })
}

func (g *graph) walk(id string, adj map[string][]Edge, next func(Edge) string) map[string]bool {
	seen := map[string]bool{}
	stack := []string{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range adj[cur] {
			if n := next(e); !seen[n] {
				seen[n] = true
				stack = append(stack, n)
			}
		}
	}
	delete(seen, id)
	return seen
}

// dominators computes dom(n): every node lying on every path from the
// virtual root (drawn over all in-degree-0 nodes) to n, n included; the
// virtual root itself stays implicit. Iterative set intersection in
// topological order — document graphs are ≤50 nodes, so clarity beats
// Lengauer-Tarjan. For any node the dominator set is totally ordered by
// dominance, which is what makes "nearest dominating gate" (Task 4)
// well defined. Call only after cycle() returned nil.
func (g *graph) dominators() map[string]map[string]bool {
	dom := make(map[string]map[string]bool, len(g.order))
	for _, id := range g.order {
		var inter map[string]bool
		for i, e := range g.in[id] {
			p := dom[e.From]
			if i == 0 {
				inter = make(map[string]bool, len(p)+1)
				for k := range p {
					inter[k] = true
				}
				continue
			}
			for k := range inter {
				if !p[k] {
					delete(inter, k)
				}
			}
		}
		if inter == nil {
			inter = make(map[string]bool, 1)
		}
		inter[id] = true
		dom[id] = inter
	}
	return dom
}

// examined returns a gate's examined input edge — its single plan|patch
// binding. Shape validation guarantees exactly one exists.
func (g *graph) examined(gateID string) Edge {
	for _, e := range g.in[gateID] {
		if e.Artifact == ArtifactPlan || e.Artifact == ArtifactPatch {
			return e
		}
	}
	panic("workflows: examined called on a gate without an examined input")
}

// origin walks an artifact edge back through pass-through gates to the
// node that actually produced it (plan header, decision 2). Acyclicity
// bounds the walk.
func (g *graph) origin(e Edge) string {
	for g.node(e.From).Type == NodeGate {
		e = g.examined(e.From)
	}
	return e.From
}
