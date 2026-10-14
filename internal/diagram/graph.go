// Package diagram derives Mermaid flowcharts from this module's own Go code
// (imports, call graph, switch statements, route tables), so the diagrams
// cannot drift from the source. Everything is stdlib-only and deterministic:
// the same code always renders to the same bytes.
package diagram

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

// Shape is how a node is drawn.
type Shape int

const (
	ShapeBox Shape = iota
	ShapeRound
	ShapeStadium
)

// EdgeStyle is how an edge is drawn.
type EdgeStyle int

const (
	EdgeSolid  EdgeStyle = iota // -->
	EdgeDotted                  // -.->
)

// Node is one box in a diagram. Key identifies it within its Graph; the
// Mermaid ID is derived from it at render time.
type Node struct {
	Key   string
	Label string // may contain "\n" for line breaks
	Shape Shape
	Class string // one of the classDef names in classDefs, or ""
	Group string // key of the containing group, or "" for top level
}

// Edge connects two node keys.
type Edge struct {
	From, To string
	Label    string
	Style    EdgeStyle
}

type group struct{ key, label, parent string }

// Graph is a set of nodes, edges and nested groups. Insertion order does not
// matter: rendering sorts everything.
type Graph struct {
	nodes  map[string]Node
	edges  map[Edge]bool
	groups map[string]group
}

func NewGraph() *Graph {
	return &Graph{nodes: map[string]Node{}, edges: map[Edge]bool{}, groups: map[string]group{}}
}

// AddGroup declares a (possibly nested) subgraph. parent "" means top level.
func (g *Graph) AddGroup(key, label, parent string) {
	if _, ok := g.groups[key]; !ok {
		g.groups[key] = group{key, label, parent}
	}
}

// AddNode adds a node; a second definition of the same key is ignored.
func (g *Graph) AddNode(n Node) {
	if _, ok := g.nodes[n.Key]; !ok {
		g.nodes[n.Key] = n
	}
}

// AddEdge adds an edge; exact duplicates collapse.
func (g *Graph) AddEdge(from, to, label string, style EdgeStyle) {
	g.edges[Edge{from, to, label, style}] = true
}

func (g *Graph) NodeCount() int { return len(g.nodes) }
func (g *Graph) EdgeCount() int { return len(g.edges) }

// classDefs are emitted once per rendered file. They only use stroke styles,
// no fills, so they read correctly on light and dark themes.
var classDefs = []string{
	"classDef external stroke-dasharray:5 3",
	"classDef dropped stroke-dasharray:2 4",
	"classDef entry stroke-width:3px",
}

// esc makes s safe inside a double-quoted Mermaid label. Mermaid entity codes
// (#name; / #NN;) are used because a backslash escape is not supported. '#'
// must be escaped first, since every other escape introduces one.
func esc(s string) string {
	r := strings.NewReplacer(
		"#", "#35;",
		`"`, "#quot;",
		"<", "#lt;",
		">", "#gt;",
		"%", "#37;", // "%%" would start a comment
		"`", "#96;", // a leading backtick would switch to a markdown string
		"\n", "<br/>",
	)
	return r.Replace(s)
}

// sanitizeID keeps only characters that are always valid in a Mermaid node ID.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "n"
	}
	return b.String()
}

func shortHash(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	return fmt.Sprintf("%06x", h.Sum32()&0xffffff)
}

// uniqueIDs maps each key to a unique Mermaid ID "<prefix>_<sanitized key>".
// The prefix also guarantees no ID can collide with a keyword such as "end",
// "graph" or "subgraph". Keys whose sanitized forms collide get a stable hash
// suffix (assigned in sorted key order, so output is deterministic).
func uniqueIDs(prefix string, keys []string) map[string]string {
	sort.Strings(keys)
	ids := make(map[string]string, len(keys))
	used := map[string]bool{}
	for _, k := range keys {
		id := prefix + "_" + sanitizeID(k)
		for n := 0; used[id]; n++ {
			id = fmt.Sprintf("%s_%s%d", prefix+"_"+sanitizeID(k), shortHash(k), n)
		}
		used[id] = true
		ids[k] = id
	}
	return ids
}

func (g *Graph) validate() error {
	for _, n := range g.nodes {
		if n.Group != "" {
			if _, ok := g.groups[n.Group]; !ok {
				return fmt.Errorf("node %q is in unknown group %q", n.Key, n.Group)
			}
		}
	}
	for _, gr := range g.groups {
		seen := map[string]bool{gr.key: true}
		for p := gr.parent; p != ""; p = g.groups[p].parent {
			if _, ok := g.groups[p]; !ok {
				return fmt.Errorf("group %q has unknown parent %q", gr.key, p)
			}
			if seen[p] {
				return fmt.Errorf("group %q is in a parent cycle", gr.key)
			}
			seen[p] = true
		}
	}
	for e := range g.edges {
		if _, ok := g.nodes[e.From]; !ok {
			return fmt.Errorf("edge from unknown node %q", e.From)
		}
		if _, ok := g.nodes[e.To]; !ok {
			return fmt.Errorf("edge to unknown node %q", e.To)
		}
	}
	return nil
}

// render writes the groups, nodes and edges of g, one statement per line,
// each prefixed with indent. IDs are prefixed with prefix so several graphs
// can share one Mermaid file without clashing.
func (g *Graph) render(prefix, indent string) (string, error) {
	if err := g.validate(); err != nil {
		return "", err
	}
	nodeKeys := make([]string, 0, len(g.nodes))
	for k := range g.nodes {
		nodeKeys = append(nodeKeys, k)
	}
	groupKeys := make([]string, 0, len(g.groups))
	for k := range g.groups {
		groupKeys = append(groupKeys, k)
	}
	nid := uniqueIDs(prefix, nodeKeys)
	gid := uniqueIDs(prefix+"_g", groupKeys)

	sort.Strings(nodeKeys)
	sort.Strings(groupKeys)

	var b strings.Builder
	var writeScope func(parent, ind string)
	writeScope = func(parent, ind string) {
		for _, k := range nodeKeys {
			if n := g.nodes[k]; n.Group == parent {
				b.WriteString(ind + nodeLine(nid[k], n) + "\n")
			}
		}
		for _, k := range groupKeys {
			if gr := g.groups[k]; gr.parent == parent {
				fmt.Fprintf(&b, "%ssubgraph %s[\"%s\"]\n", ind, gid[k], esc(gr.label))
				writeScope(k, ind+"  ")
				b.WriteString(ind + "end\n")
			}
		}
	}
	writeScope("", indent)

	edges := make([]Edge, 0, len(g.edges))
	for e := range g.edges {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		a, c := edges[i], edges[j]
		if a.From != c.From {
			return a.From < c.From
		}
		if a.To != c.To {
			return a.To < c.To
		}
		if a.Label != c.Label {
			return a.Label < c.Label
		}
		return a.Style < c.Style
	})
	for _, e := range edges {
		arrow := "-->"
		if e.Style == EdgeDotted {
			arrow = "-.->"
		}
		if e.Label != "" {
			arrow += fmt.Sprintf("|\"%s\"|", esc(e.Label))
		}
		b.WriteString(fmt.Sprintf("%s%s %s %s\n", indent, nid[e.From], arrow, nid[e.To]))
	}
	return b.String(), nil
}

func nodeLine(id string, n Node) string {
	l := esc(n.Label)
	var s string
	switch n.Shape {
	case ShapeRound:
		s = fmt.Sprintf(`%s("%s")`, id, l)
	case ShapeStadium:
		s = fmt.Sprintf(`%s(["%s"])`, id, l)
	default:
		s = fmt.Sprintf(`%s["%s"]`, id, l)
	}
	if n.Class != "" {
		s += ":::" + n.Class
	}
	return s
}
