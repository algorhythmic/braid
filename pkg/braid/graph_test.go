package braid

import (
	"math"
	"testing"
)

func TestInverseDegreeDownweightsHubAnchor(t *testing.T) {
	c := DefaultConfig()
	c.Graph.Damping = 0
	c.Graph.Seed = "inverse_degree"
	e := openTest(t, c)
	mustUpsert(t, e, Batch{Nodes: []Node{node("hub", "n", "", 1), node("a", "n", "", 1), node("b", "n", "", 1), node("c", "n", "", 1)}, Edges: []Edge{{Src: "hub", Dst: "a", Type: "link", Weight: 1, TS: testTime}, {Src: "hub", Dst: "b", Type: "link", Weight: 1, TS: testTime}, {Src: "hub", Dst: "c", Type: "link", Weight: 1, TS: testTime}}})
	r, _ := e.Retriever("graph")
	hits, err := r.Retrieve(testCtx, Query{Anchors: []string{"hub", "a"}}, 10)
	if err != nil || len(hits) != 2 || hits[0].ID != "a" || math.Abs(hits[0].Score-.75) > 1e-12 || math.Abs(hits[1].Score-.25) > 1e-12 {
		t.Fatal(hits, err)
	}
}
