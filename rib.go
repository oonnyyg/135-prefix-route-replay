package main

import (
	"fmt"
	"net/netip"
	"sort"
)

// Candidate is one neighbor's path for a prefix.
type Candidate struct {
	Neighbor     string
	LocalPref    uint64
	ASPath       []uint32
	InternalCost uint64
}

// Route is the winning entry exported in the routing table.
type Route struct {
	Prefix       string   `json:"prefix"`
	Neighbor     string   `json:"neighbor"`
	LocalPref    uint64   `json:"local_pref"`
	ASPath       []uint32 `json:"as_path"`
	InternalCost uint64   `json:"internal_cost"`
}

// RIB holds per-prefix candidates keyed by neighbor.
type RIB struct {
	localASN uint32
	online   map[string]bool
	known    map[string]bool
	// prefix -> neighbor -> candidate
	table map[netip.Prefix]map[string]Candidate
}

func NewRIB(localASN uint32, neighbors []string) *RIB {
	r := &RIB{
		localASN: localASN,
		online:   make(map[string]bool),
		known:    make(map[string]bool),
		table:    make(map[netip.Prefix]map[string]Candidate),
	}
	for _, n := range neighbors {
		r.known[n] = true
		r.online[n] = true
	}
	return r
}

// better reports whether a beats b under the decision subset:
// local pref, AS path length, internal cost, neighbor id.
func better(a, b Candidate) bool {
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

// best returns the winning candidate for a prefix, if any.
func (r *RIB) best(p netip.Prefix) (Candidate, bool) {
	cands, ok := r.table[p]
	if !ok || len(cands) == 0 {
		return Candidate{}, false
	}
	var best Candidate
	first := true
	for _, c := range cands {
		if first || better(c, best) {
			best = c
			first = false
		}
	}
	return best, true
}

// Announce validates and installs a candidate. It returns the old and new
// winners plus a reason describing the effect.
func (r *RIB) Announce(neighbor string, p netip.Prefix, localPref uint64, asPath []uint32, cost uint64) (Result, error) {
	if !r.known[neighbor] {
		return Result{}, fmt.Errorf("unknown neighbor %q", neighbor)
	}
	if !r.online[neighbor] {
		return Result{}, fmt.Errorf("neighbor %q is offline", neighbor)
	}
	for _, asn := range asPath {
		if asn == r.localASN {
			return Result{}, fmt.Errorf("as_path contains local ASN %d", r.localASN)
		}
	}
	old, hadOld := r.best(p)
	cand := Candidate{Neighbor: neighbor, LocalPref: localPref, ASPath: asPath, InternalCost: cost}
	cands := r.table[p]
	if cands == nil {
		cands = make(map[string]Candidate)
		r.table[p] = cands
	}
	_, replaced := cands[neighbor]
	cands[neighbor] = cand
	now, _ := r.best(p)
	res := Result{Prefix: p.String()}
	switch {
	case !hadOld:
		res.Change = "installed"
		res.Reason = "first candidate for prefix"
	case !sameWinner(old, now):
		res.Change = "best_changed"
		res.Reason = "new candidate wins by decision rules"
	case replaced:
		res.Change = "updated"
		res.Reason = "existing winner replaced by same neighbor"
	default:
		res.Change = "unchanged"
		res.Reason = "candidate stored but does not beat current best"
	}
	w := routeFrom(p, now)
	res.Winner = &w
	return res, nil
}

// Withdraw removes one neighbor's candidate for a prefix.
func (r *RIB) Withdraw(neighbor string, p netip.Prefix) (Result, error) {
	if !r.known[neighbor] {
		return Result{}, fmt.Errorf("unknown neighbor %q", neighbor)
	}
	cands := r.table[p]
	if cands == nil {
		return Result{}, fmt.Errorf("no candidate from %q for %s", neighbor, p)
	}
	if _, ok := cands[neighbor]; !ok {
		return Result{}, fmt.Errorf("no candidate from %q for %s", neighbor, p)
	}
	old, hadOld := r.best(p)
	delete(cands, neighbor)
	if len(cands) == 0 {
		delete(r.table, p)
	}
	res := Result{Prefix: p.String()}
	now, hasNow := r.best(p)
	switch {
	case hadOld && !hasNow:
		res.Change = "removed"
		res.Reason = "winning candidate withdrawn; no candidates remain"
	case hadOld && !sameWinner(old, now):
		res.Change = "best_changed"
		res.Reason = "winning candidate withdrawn; fallback selected"
	default:
		res.Change = "unchanged"
		res.Reason = "non-winning candidate withdrawn"
	}
	if hasNow {
		w := routeFrom(p, now)
		res.Winner = &w
	}
	return res, nil
}

// NeighborDown removes all candidates from a neighbor.
func (r *RIB) NeighborDown(neighbor string) ([]Result, error) {
	if !r.known[neighbor] {
		return nil, fmt.Errorf("unknown neighbor %q", neighbor)
	}
	r.online[neighbor] = false
	var results []Result
	for p, cands := range r.table {
		if _, ok := cands[neighbor]; !ok {
			continue
		}
		old, hadOld := r.best(p)
		delete(cands, neighbor)
		if len(cands) == 0 {
			delete(r.table, p)
		}
		res := Result{Prefix: p.String()}
		now, hasNow := r.best(p)
		switch {
		case hadOld && !hasNow:
			res.Change = "removed"
			res.Reason = "neighbor down removed last candidate"
		case hadOld && !sameWinner(old, now):
			res.Change = "best_changed"
			res.Reason = "neighbor down removed winner; fallback selected"
		default:
			res.Change = "unchanged"
			res.Reason = "neighbor down removed non-winning candidate"
		}
		if hasNow {
			w := routeFrom(p, now)
			res.Winner = &w
		}
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Prefix < results[j].Prefix })
	return results, nil
}

// NeighborUp marks a neighbor online; old paths are not restored.
func (r *RIB) NeighborUp(neighbor string) error {
	if !r.known[neighbor] {
		return fmt.Errorf("unknown neighbor %q", neighbor)
	}
	r.online[neighbor] = true
	return nil
}

// Query finds the longest matching prefix with a best path.
func (r *RIB) Query(addr netip.Addr) (Route, bool) {
	var bestP netip.Prefix
	found := false
	for p := range r.table {
		if p.Contains(addr) && (!found || p.Bits() > bestP.Bits()) {
			if _, ok := r.best(p); ok {
				bestP = p
				found = true
			}
		}
	}
	if !found {
		return Route{}, false
	}
	c, _ := r.best(bestP)
	return routeFrom(bestP, c), true
}

// Export returns the routing table sorted by prefix address then length.
func (r *RIB) Export() []Route {
	prefixes := make([]netip.Prefix, 0, len(r.table))
	for p := range r.table {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		a, b := prefixes[i].Addr(), prefixes[j].Addr()
		if a != b {
			return a.Less(b)
		}
		return prefixes[i].Bits() < prefixes[j].Bits()
	})
	routes := make([]Route, 0, len(prefixes))
	for _, p := range prefixes {
		if c, ok := r.best(p); ok {
			routes = append(routes, routeFrom(p, c))
		}
	}
	return routes
}

func sameWinner(a, b Candidate) bool {
	return a.Neighbor == b.Neighbor && a.LocalPref == b.LocalPref &&
		a.InternalCost == b.InternalCost && equalASPath(a.ASPath, b.ASPath)
}

func equalASPath(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func routeFrom(p netip.Prefix, c Candidate) Route {
	return Route{
		Prefix:       p.String(),
		Neighbor:     c.Neighbor,
		LocalPref:    c.LocalPref,
		ASPath:       c.ASPath,
		InternalCost: c.InternalCost,
	}
}
