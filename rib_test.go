package main

import (
	"net/netip"
	"testing"
)

func pfx(s string) netip.Prefix {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		panic(err)
	}
	return p
}

func newTestRIB() *RIB {
	return NewRIB(65001, []string{"n1", "n2", "n3"})
}

// 重复前缀：多邻居公布同一规范前缀，主机位被清除，同邻居新发布替换旧路径。
func TestDuplicatePrefixAndReplace(t *testing.T) {
	r := newTestRIB()
	ch, err := r.Announce("n1", pfx("10.1.2.3/24"), 100, []uint32{65002}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Prefix != "10.1.2.0/24" || !ch.Changed {
		t.Fatalf("host bits not masked or not changed: %+v", ch)
	}
	if _, err := r.Announce("n2", pfx("10.1.2.0/24"), 200, []uint32{65003}, 5); err != nil {
		t.Fatal(err)
	}
	p, route, ok := r.Query(netip.MustParseAddr("10.1.2.9"))
	if !ok || p.String() != "10.1.2.0/24" || route.Neighbor != "n2" {
		t.Fatalf("expected n2 (higher local_pref) to win, got %+v %v", route, ok)
	}
	// 同邻居新发布替换旧路径，降低优先级后 n1 应胜出。
	if _, err := r.Announce("n2", pfx("10.1.2.0/24"), 50, []uint32{65003}, 5); err != nil {
		t.Fatal(err)
	}
	_, route, _ = r.Query(netip.MustParseAddr("10.1.2.9"))
	if route.Neighbor != "n1" {
		t.Fatalf("expected replacement to make n1 win, got %s", route.Neighbor)
	}
	if len(r.entries[pfx("10.1.2.0/24")].candidates) != 2 {
		t.Fatal("replacement must not add extra candidates")
	}
}

// 选路顺序：local_pref > 路径长度 > 内部代价 > 邻居标识。
func TestBestPathOrder(t *testing.T) {
	r := newTestRIB()
	mustAnnounce(t, r, "n3", "192.0.2.0/24", 100, []uint32{2, 2, 3}, 1) // 重复 AS 计入长度
	mustAnnounce(t, r, "n2", "192.0.2.0/24", 100, []uint32{2, 3}, 9)
	mustAnnounce(t, r, "n1", "192.0.2.0/24", 100, []uint32{2, 3}, 5)
	_, route, _ := r.Query(netip.MustParseAddr("192.0.2.1"))
	if route.Neighbor != "n1" {
		t.Fatalf("expected n1 (lower cost), got %s", route.Neighbor)
	}
	mustAnnounce(t, r, "n3", "192.0.2.0/24", 100, []uint32{2, 3}, 5)
	_, route, _ = r.Query(netip.MustParseAddr("192.0.2.1"))
	if route.Neighbor != "n1" {
		t.Fatalf("tie should break by neighbor id, got %s", route.Neighbor)
	}
}

func mustAnnounce(t *testing.T, r *RIB, n, p string, lp int, path []uint32, cost int) {
	t.Helper()
	if _, err := r.Announce(n, pfx(p), lp, path, cost); err != nil {
		t.Fatal(err)
	}
}

// 主路径撤回后回退到次优路径；不存在的撤回返回未找到且状态不变。
func TestWithdrawPrimary(t *testing.T) {
	r := newTestRIB()
	mustAnnounce(t, r, "n1", "203.0.113.0/24", 100, []uint32{2}, 1)
	mustAnnounce(t, r, "n2", "203.0.113.0/24", 200, []uint32{3}, 1)
	ch, err := r.Withdraw("n2", pfx("203.0.113.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Changed || ch.NewBest == nil || ch.NewBest.Neighbor != "n1" {
		t.Fatalf("expected fallback to n1, got %+v", ch)
	}
	if _, err := r.Withdraw("n2", pfx("203.0.113.0/24")); err == nil {
		t.Fatal("expected not-found error for repeated withdraw")
	}
	if _, err := r.Withdraw("n1", pfx("198.51.100.0/24")); err == nil {
		t.Fatal("expected not-found error for unknown prefix")
	}
	_, route, ok := r.Query(netip.MustParseAddr("203.0.113.7"))
	if !ok || route.Neighbor != "n1" {
		t.Fatal("failed withdraw must not change state")
	}
	// 撤回最后候选后前缀不可达。
	ch, err = r.Withdraw("n1", pfx("203.0.113.0/24"))
	if err != nil || !ch.Changed || ch.NewBest != nil {
		t.Fatalf("expected prefix unreachable, got %+v %v", ch, err)
	}
	if _, _, ok := r.Query(netip.MustParseAddr("203.0.113.7")); ok {
		t.Fatal("prefix should be unreachable")
	}
}

// 邻居故障撤销其全部候选并重新选路；上线不恢复旧路径。
func TestNeighborFailure(t *testing.T) {
	r := newTestRIB()
	mustAnnounce(t, r, "n1", "10.0.0.0/8", 100, []uint32{2}, 1)
	mustAnnounce(t, r, "n1", "10.1.0.0/16", 100, []uint32{2}, 1)
	mustAnnounce(t, r, "n2", "10.1.0.0/16", 50, []uint32{3}, 1)
	changes, err := r.NeighborDown("n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 affected prefixes, got %d", len(changes))
	}
	if _, _, ok := r.Query(netip.MustParseAddr("10.0.1.1")); ok {
		t.Fatal("10.0.0.0/8 should be unreachable after n1 down")
	}
	_, route, ok := r.Query(netip.MustParseAddr("10.1.0.1"))
	if !ok || route.Neighbor != "n2" {
		t.Fatal("10.1.0.0/16 should fall back to n2")
	}
	if _, err := r.Announce("n1", pfx("10.2.0.0/16"), 100, []uint32{2}, 1); err == nil {
		t.Fatal("announce from offline neighbor must be rejected")
	}
	if err := r.NeighborUp("n1"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := r.Query(netip.MustParseAddr("10.0.1.1")); ok {
		t.Fatal("neighbor up must not restore old paths")
	}
}

// 含本地 AS 的路径与未知邻居的发布被拒绝，且不覆盖已有候选。
func TestRejections(t *testing.T) {
	r := newTestRIB()
	mustAnnounce(t, r, "n1", "192.0.2.0/24", 100, []uint32{2}, 1)
	if _, err := r.Announce("n2", pfx("192.0.2.0/24"), 300, []uint32{65001, 2}, 1); err == nil {
		t.Fatal("path containing local AS must be rejected")
	}
	if _, err := r.Announce("ghost", pfx("192.0.2.0/24"), 300, []uint32{2}, 1); err == nil {
		t.Fatal("unknown neighbor must be rejected")
	}
	_, route, _ := r.Query(netip.MustParseAddr("192.0.2.1"))
	if route.Neighbor != "n1" || route.LocalPref != 100 {
		t.Fatal("rejected events must not overwrite existing candidates")
	}
}

// 查询按最长前缀匹配，不跨长度比较优先级。
func TestLongestPrefixMatch(t *testing.T) {
	r := newTestRIB()
	mustAnnounce(t, r, "n1", "10.0.0.0/8", 1000, []uint32{2}, 1)
	mustAnnounce(t, r, "n2", "10.1.0.0/16", 1, []uint32{3}, 1)
	p, route, ok := r.Query(netip.MustParseAddr("10.1.0.5"))
	if !ok || p.String() != "10.1.0.0/16" || route.Neighbor != "n2" {
		t.Fatalf("longest match must win regardless of local_pref, got %s %+v", p, route)
	}
	p, route, ok = r.Query(netip.MustParseAddr("10.2.0.5"))
	if !ok || p.String() != "10.0.0.0/8" || route.Neighbor != "n1" {
		t.Fatalf("expected /8 match, got %s %+v", p, route)
	}
	if _, _, ok := r.Query(netip.MustParseAddr("11.0.0.1")); ok {
		t.Fatal("expected no match")
	}
}
