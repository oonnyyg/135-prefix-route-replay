package main

import (
	"fmt"
	"net/netip"
	"sort"
)

// Route 是某邻居针对某规范前缀公布的一条候选路径。
type Route struct {
	Neighbor     string   `json:"neighbor"`
	LocalPref    int      `json:"local_pref"`
	ASPath       []uint32 `json:"as_path"`
	InternalCost int      `json:"internal_cost"`
}

// better 比较两条候选：本地优先级高者优先，其次 AS 路径短者，
// 再次内部代价低者，最后按邻居标识字典序。返回 true 表示 a 胜出。
func better(a, b Route) bool {
	if a.LocalPref != b.LocalPref {
		return a.LocalPref > b.LocalPref
	}
	if len(a.ASPath) != len(b.ASPath) {
		return len(a.ASPath) < len(b.ASPath)
	}
	if a.InternalCost != b.InternalCost {
		return a.InternalCost < b.InternalCost
	}
	return a.Neighbor < b.Neighbor
}

// prefixEntry 保存一个规范前缀下所有邻居的候选及当前胜出者。
type prefixEntry struct {
	candidates map[string]Route // key: neighbor id
	best       Route
	hasBest    bool
}

func (e *prefixEntry) recompute() {
	e.hasBest = false
	for _, r := range e.candidates {
		if !e.hasBest || better(r, e.best) {
			e.best = r
			e.hasBest = true
		}
	}
}

// Change 描述一次事件对某前缀最优路径的影响。
type Change struct {
	Prefix  string `json:"prefix"`
	Changed bool   `json:"changed"`
	Reason  string `json:"reason"`
	OldBest *Route `json:"old_best,omitempty"`
	NewBest *Route `json:"new_best,omitempty"`
}

// RIB 维护全部前缀的候选与最优路径。
type RIB struct {
	localAS   uint32
	neighbors map[string]bool // id -> online
	entries   map[netip.Prefix]*prefixEntry
}

func NewRIB(localAS uint32, neighborIDs []string) *RIB {
	r := &RIB{
		localAS:   localAS,
		neighbors: make(map[string]bool),
		entries:   make(map[netip.Prefix]*prefixEntry),
	}
	for _, id := range neighborIDs {
		r.neighbors[id] = true // 配置中的邻居默认在线
	}
	return r
}

func routesEqual(a, b Route) bool {
	if a.Neighbor != b.Neighbor || a.LocalPref != b.LocalPref || a.InternalCost != b.InternalCost || len(a.ASPath) != len(b.ASPath) {
		return false
	}
	for i := range a.ASPath {
		if a.ASPath[i] != b.ASPath[i] {
			return false
		}
	}
	return true
}

// Announce 处理路由发布，返回该前缀最优路径的变化。
func (r *RIB) Announce(neighbor string, prefix netip.Prefix, localPref int, asPath []uint32, cost int) (Change, error) {
	p := prefix.Masked()
	ch := Change{Prefix: p.String()}
	online, known := r.neighbors[neighbor]
	if !known {
		return ch, fmt.Errorf("unknown neighbor %q", neighbor)
	}
	if !online {
		return ch, fmt.Errorf("neighbor %q is offline", neighbor)
	}
	for _, as := range asPath {
		if as == r.localAS {
			return ch, fmt.Errorf("as_path contains local AS %d", r.localAS)
		}
	}
	e := r.entries[p]
	if e == nil {
		e = &prefixEntry{candidates: make(map[string]Route)}
		r.entries[p] = e
	}
	if e.hasBest {
		old := e.best
		ch.OldBest = &old
	}
	_, replaced := e.candidates[neighbor]
	e.candidates[neighbor] = Route{Neighbor: neighbor, LocalPref: localPref, ASPath: asPath, InternalCost: cost}
	e.recompute()
	ch.NewBest = &e.best
	switch {
	case ch.OldBest == nil:
		ch.Changed = true
		ch.Reason = "new prefix announced"
	case replaced && routesEqual(*ch.OldBest, e.best):
		ch.Changed = false
		ch.Reason = "announcement replaced identical best path"
	case replaced:
		ch.Changed = true
		ch.Reason = "announcement replaced previous path from same neighbor"
	case routesEqual(*ch.OldBest, e.best):
		ch.Changed = false
		ch.Reason = "new candidate does not beat current best"
	default:
		ch.Changed = true
		ch.Reason = "new candidate wins best path selection"
	}
	return ch, nil
}

// Withdraw 撤回指定邻居对某前缀的候选。不存在时返回错误。
func (r *RIB) Withdraw(neighbor string, prefix netip.Prefix) (Change, error) {
	p := prefix.Masked()
	ch := Change{Prefix: p.String()}
	e := r.entries[p]
	if e == nil {
		return ch, fmt.Errorf("no candidates for prefix %s", p)
	}
	if _, ok := e.candidates[neighbor]; !ok {
		return ch, fmt.Errorf("no route from neighbor %q for prefix %s", neighbor, p)
	}
	if e.hasBest {
		old := e.best
		ch.OldBest = &old
	}
	delete(e.candidates, neighbor)
	if len(e.candidates) == 0 {
		delete(r.entries, p)
		ch.Changed = true
		ch.Reason = "withdraw removed last candidate; prefix unreachable"
		return ch, nil
	}
	e.recompute()
	ch.NewBest = &e.best
	if routesEqual(*ch.OldBest, e.best) {
		ch.Changed = false
		ch.Reason = "withdraw did not affect best path"
	} else {
		ch.Changed = true
		ch.Reason = "withdraw removed best path; recomputed"
	}
	return ch, nil
}

// NeighborDown 撤销邻居全部候选，返回所有受影响前缀的变化。
func (r *RIB) NeighborDown(neighbor string) ([]Change, error) {
	online, known := r.neighbors[neighbor]
	if !known {
		return nil, fmt.Errorf("unknown neighbor %q", neighbor)
	}
	if !online {
		return nil, fmt.Errorf("neighbor %q already offline", neighbor)
	}
	r.neighbors[neighbor] = false
	var changes []Change
	for _, p := range r.sortedPrefixes() {
		e := r.entries[p]
		if _, ok := e.candidates[neighbor]; !ok {
			continue
		}
		ch := Change{Prefix: p.String()}
		if e.hasBest {
			old := e.best
			ch.OldBest = &old
		}
		delete(e.candidates, neighbor)
		if len(e.candidates) == 0 {
			delete(r.entries, p)
			ch.Changed = true
			ch.Reason = "neighbor down removed last candidate; prefix unreachable"
		} else {
			e.recompute()
			ch.NewBest = &e.best
			if routesEqual(*ch.OldBest, e.best) {
				ch.Changed = false
				ch.Reason = "neighbor down did not affect best path"
			} else {
				ch.Changed = true
				ch.Reason = "neighbor down removed best path; recomputed"
			}
		}
		changes = append(changes, ch)
	}
	return changes, nil
}

// NeighborUp 将邻居标记为上线，不恢复旧路径。
func (r *RIB) NeighborUp(neighbor string) error {
	if _, known := r.neighbors[neighbor]; !known {
		return fmt.Errorf("unknown neighbor %q", neighbor)
	}
	r.neighbors[neighbor] = true
	return nil
}

// Query 对地址做最长前缀匹配，返回命中前缀及其胜出路径。
func (r *RIB) Query(addr netip.Addr) (netip.Prefix, Route, bool) {
	var bestP netip.Prefix
	var bestR Route
	found := false
	for p, e := range r.entries {
		if !e.hasBest || !p.Contains(addr) {
			continue
		}
		if !found || p.Bits() > bestP.Bits() {
			bestP, bestR, found = p, e.best, true
		}
	}
	return bestP, bestR, found
}

// TableEntry 是导出路由表的一行。
type TableEntry struct {
	Prefix string `json:"prefix"`
	Route  Route  `json:"route"`
}

// Export 以固定排序导出当前路由表。
func (r *RIB) Export() []TableEntry {
	out := []TableEntry{}
	for _, p := range r.sortedPrefixes() {
		e := r.entries[p]
		if e.hasBest {
			out = append(out, TableEntry{Prefix: p.String(), Route: e.best})
		}
	}
	return out
}

// sortedPrefixes 以固定顺序（先地址后掩码长）导出前缀。
func (r *RIB) sortedPrefixes() []netip.Prefix {
	ps := make([]netip.Prefix, 0, len(r.entries))
	for p := range r.entries {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool {
		a, b := ps[i].Addr(), ps[j].Addr()
		if a != b {
			return a.Less(b)
		}
		return ps[i].Bits() < ps[j].Bits()
	})
	return ps
}
