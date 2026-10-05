package main

import (
	"net/netip"
	"testing"
)

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := parsePrefix(s)
	if err != nil {
		t.Fatalf("parsePrefix(%q): %v", s, err)
	}
	return p
}

func TestPrefixMaskedOnAnnounce(t *testing.T) {
	r := NewRIB(65000, []string{"n1"})
	res, err := r.Announce("n1", mustPrefix(t, "10.1.2.3/8"), 100, []uint32{65001}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Prefix != "10.0.0.0/8" {
		t.Fatalf("host bits not cleared: %s", res.Prefix)
	}
}

func TestAnnounceReplacesSameNeighbor(t *testing.T) {
	r := NewRIB(65000, []string{"n1", "n2"})
	p := mustPrefix(t, "10.0.0.0/8")
	if _, err := r.Announce("n1", p, 100, []uint32{1}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Announce("n1", p, 50, []uint32{1, 2, 3}, 0); err != nil {
		t.Fatal(err)
	}
	if len(r.table[p]) != 1 {
		t.Fatalf("expected replacement, got %d candidates", len(r.table[p]))
	}
	best, _ := r.best(p)
	if best.LocalPref != 50 {
		t.Fatalf("old path not replaced: local_pref=%d", best.LocalPref)
	}
}

func TestDecisionOrder(t *testing.T) {
	r := NewRIB(65000, []string{"a", "b", "c", "d"})
	p := mustPrefix(t, "10.0.0.0/8")
	r.Announce("a", p, 100, []uint32{1, 2}, 5)
	r.Announce("b", p, 200, []uint32{1, 2, 3}, 9) // wins on local_pref
	if best, _ := r.best(p); best.Neighbor != "b" {
		t.Fatal("local_pref not decisive")
	}
	r.Announce("c", p, 200, []uint32{1}, 9) // wins on shorter path
	if best, _ := r.best(p); best.Neighbor != "c" {
		t.Fatal("as_path length not decisive")
	}
	r.Announce("d", p, 200, []uint32{1}, 1) // wins on lower cost
	if best, _ := r.best(p); best.Neighbor != "d" {
		t.Fatal("internal cost not decisive")
	}
	// tie on everything: lexicographically smaller neighbor id wins
	r.Announce("a", p, 200, []uint32{1}, 1)
	if best, _ := r.best(p); best.Neighbor != "a" {
		t.Fatal("neighbor id tiebreak not decisive")
	}
}

func TestDuplicateASNCountsInPathLength(t *testing.T) {
	r := NewRIB(65000, []string{"a", "b"})
	p := mustPrefix(t, "10.0.0.0/8")
	r.Announce("a", p, 100, []uint32{7, 7, 7}, 0)
	r.Announce("b", p, 100, []uint32{7}, 0)
	if best, _ := r.best(p); best.Neighbor != "b" {
		t.Fatal("repeated ASNs must count toward path length")
	}
}

func TestRejectLocalASNAndBadNeighbor(t *testing.T) {
	r := NewRIB(65000, []string{"n1", "n2"})
	p := mustPrefix(t, "10.0.0.0/8")
	if _, err := r.Announce("n1", p, 100, []uint32{1, 65000}, 0); err == nil {
		t.Fatal("expected loop rejection")
	}
	if _, err := r.Announce("ghost", p, 100, []uint32{1}, 0); err == nil {
		t.Fatal("expected unknown neighbor rejection")
	}
	r.NeighborDown("n1")
	if _, err := r.Announce("n1", p, 100, []uint32{1}, 0); err == nil {
		t.Fatal("expected offline neighbor rejection")
	}
	// failed events must not disturb existing candidates
	r.Announce("n2", p, 100, []uint32{1}, 0)
	if _, err := r.Announce("n2", p, 999, []uint32{65000}, 0); err == nil {
		t.Fatal("expected loop rejection")
	}
	if best, _ := r.best(p); best.LocalPref != 100 {
		t.Fatal("rejected announce overwrote existing candidate")
	}
}

func TestWithdrawPrimarySelectsFallback(t *testing.T) {
	r := NewRIB(65000, []string{"n1", "n2"})
	p := mustPrefix(t, "10.0.0.0/8")
	r.Announce("n1", p, 200, []uint32{1}, 0)
	r.Announce("n2", p, 100, []uint32{1}, 0)
	res, err := r.Withdraw("n1", p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Change != "best_changed" || res.Winner == nil || res.Winner.Neighbor != "n2" {
		t.Fatalf("expected fallback to n2, got %+v", res)
	}
	// withdrawing the last candidate removes the prefix
	res, err = r.Withdraw("n2", p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Change != "removed" || res.Winner != nil {
		t.Fatalf("expected removal, got %+v", res)
	}
	if _, err := r.Withdraw("n2", p); err == nil {
		t.Fatal("expected not-found for missing withdraw")
	}
}

func TestNeighborDownRevokesAndUpDoesNotRestore(t *testing.T) {
	r := NewRIB(65000, []string{"n1", "n2"})
	p1 := mustPrefix(t, "10.0.0.0/8")
	p2 := mustPrefix(t, "192.168.0.0/16")
	r.Announce("n1", p1, 200, []uint32{1}, 0)
	r.Announce("n2", p1, 100, []uint32{1}, 0)
	r.Announce("n1", p2, 100, []uint32{1}, 0)
	affected, err := r.NeighborDown("n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 2 {
		t.Fatalf("expected 2 affected prefixes, got %d", len(affected))
	}
	if best, _ := r.best(p1); best.Neighbor != "n2" {
		t.Fatal("n1 winner not replaced by n2")
	}
	if _, ok := r.best(p2); ok {
		t.Fatal("p2 should have no candidates left")
	}
	if err := r.NeighborUp("n1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.best(p2); ok {
		t.Fatal("neighbor up must not restore old paths")
	}
}

func TestQueryLongestPrefixMatch(t *testing.T) {
	r := NewRIB(65000, []string{"n1", "n2"})
	// shorter prefix has higher local_pref; query must still prefer longer match
	r.Announce("n1", mustPrefix(t, "10.0.0.0/8"), 500, []uint32{1}, 0)
	r.Announce("n2", mustPrefix(t, "10.1.0.0/16"), 100, []uint32{2}, 0)
	route, ok := r.Query(netip.MustParseAddr("10.1.2.3"))
	if !ok || route.Prefix != "10.1.0.0/16" || route.Neighbor != "n2" {
		t.Fatalf("longest match failed: %+v", route)
	}
	route, ok = r.Query(netip.MustParseAddr("10.2.0.1"))
	if !ok || route.Prefix != "10.0.0.0/8" {
		t.Fatalf("expected /8 match, got %+v", route)
	}
	if _, ok = r.Query(netip.MustParseAddr("11.0.0.1")); ok {
		t.Fatal("expected no match")
	}
}

func TestExportSorted(t *testing.T) {
	r := NewRIB(65000, []string{"n1"})
	r.Announce("n1", mustPrefix(t, "192.168.0.0/16"), 1, nil, 0)
	r.Announce("n1", mustPrefix(t, "10.0.0.0/8"), 1, nil, 0)
	r.Announce("n1", mustPrefix(t, "10.1.0.0/16"), 1, nil, 0)
	routes := r.Export()
	want := []string{"10.0.0.0/8", "10.1.0.0/16", "192.168.0.0/16"}
	if len(routes) != len(want) {
		t.Fatalf("got %d routes", len(routes))
	}
	for i, w := range want {
		if routes[i].Prefix != w {
			t.Fatalf("position %d: want %s got %s", i, w, routes[i].Prefix)
		}
	}
}
