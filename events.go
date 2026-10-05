package main

import (
	"fmt"
	"net/netip"
)

// Config is the JSON configuration for the replayer.
type Config struct {
	LocalASN  uint32   `json:"local_asn"`
	Neighbors []string `json:"neighbors"`
}

func (c *Config) Validate() error {
	if c.LocalASN == 0 {
		return fmt.Errorf("local_asn must be a positive 32-bit integer")
	}
	seen := make(map[string]bool)
	for _, n := range c.Neighbors {
		if n == "" {
			return fmt.Errorf("neighbor id must not be empty")
		}
		if seen[n] {
			return fmt.Errorf("duplicate neighbor %q", n)
		}
		seen[n] = true
	}
	return nil
}

// Event is one JSONL input line.
type Event struct {
	Type         string   `json:"type"`
	Neighbor     string   `json:"neighbor,omitempty"`
	Prefix       string   `json:"prefix,omitempty"`
	Address      string   `json:"address,omitempty"`
	LocalPref    uint64   `json:"local_pref,omitempty"`
	ASPath       []uint32 `json:"as_path,omitempty"`
	InternalCost uint64   `json:"internal_cost,omitempty"`
}

// Result is the JSON output for one event.
type Result struct {
	OK       bool     `json:"ok"`
	Event    string   `json:"event"`
	Prefix   string   `json:"prefix,omitempty"`
	Change   string   `json:"change,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Winner   *Route   `json:"winner,omitempty"`
	Route    *Route   `json:"route,omitempty"`
	Affected []Result `json:"affected,omitempty"`
	Table    []Route  `json:"table,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func parsePrefix(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid prefix %q: %v", s, err)
	}
	if !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("prefix %q is not IPv4", s)
	}
	return p.Masked(), nil
}

// Apply runs one event against the RIB and returns its JSON result.
// Failed events never mutate existing candidates.
func Apply(r *RIB, ev Event) Result {
	switch ev.Type {
	case "announce":
		p, err := parsePrefix(ev.Prefix)
		if err != nil {
			return Result{OK: false, Event: ev.Type, Error: err.Error()}
		}
		res, err := r.Announce(ev.Neighbor, p, ev.LocalPref, ev.ASPath, ev.InternalCost)
		if err != nil {
			return Result{OK: false, Event: ev.Type, Prefix: p.String(), Error: err.Error()}
		}
		res.OK = true
		res.Event = ev.Type
		return res
	case "withdraw":
		p, err := parsePrefix(ev.Prefix)
		if err != nil {
			return Result{OK: false, Event: ev.Type, Error: err.Error()}
		}
		res, err := r.Withdraw(ev.Neighbor, p)
		if err != nil {
			return Result{OK: false, Event: ev.Type, Prefix: p.String(), Error: "not found: " + err.Error()}
		}
		res.OK = true
		res.Event = ev.Type
		return res
	case "neighbor_down":
		affected, err := r.NeighborDown(ev.Neighbor)
		if err != nil {
			return Result{OK: false, Event: ev.Type, Error: err.Error()}
		}
		for i := range affected {
			affected[i].OK = true
			affected[i].Event = ev.Type
		}
		return Result{OK: true, Event: ev.Type, Reason: "neighbor offline; candidates revoked", Affected: affected}
	case "neighbor_up":
		if err := r.NeighborUp(ev.Neighbor); err != nil {
			return Result{OK: false, Event: ev.Type, Error: err.Error()}
		}
		return Result{OK: true, Event: ev.Type, Reason: "neighbor online; old paths not restored"}
	case "query":
		addr, err := netip.ParseAddr(ev.Address)
		if err != nil || !addr.Is4() {
			return Result{OK: false, Event: ev.Type, Error: fmt.Sprintf("invalid IPv4 address %q", ev.Address)}
		}
		route, ok := r.Query(addr)
		if !ok {
			return Result{OK: true, Event: ev.Type, Reason: "no matching prefix"}
		}
		return Result{OK: true, Event: ev.Type, Reason: "longest prefix match", Route: &route}
	case "export":
		return Result{OK: true, Event: ev.Type, Reason: "route table exported", Table: r.Export()}
	default:
		return Result{OK: false, Event: ev.Type, Error: fmt.Sprintf("unknown event type %q", ev.Type)}
	}
}
