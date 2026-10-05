package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
)

// Config 是 JSON 配置文件：本地 AS 号与邻居列表。
type Config struct {
	LocalAS   uint32   `json:"local_as"`
	Neighbors []string `json:"neighbors"`
}

func (c *Config) validate() error {
	if c.LocalAS == 0 {
		return fmt.Errorf("local_as must be a positive 32-bit integer")
	}
	seen := map[string]bool{}
	for _, n := range c.Neighbors {
		if n == "" {
			return fmt.Errorf("neighbor id must not be empty")
		}
		if seen[n] {
			return fmt.Errorf("duplicate neighbor id %q", n)
		}
		seen[n] = true
	}
	return nil
}

// Event 是 JSONL 中的一行事件。
type Event struct {
	Type         string   `json:"type"` // announce | withdraw | neighbor_up | neighbor_down | query | export
	Neighbor     string   `json:"neighbor,omitempty"`
	Prefix       string   `json:"prefix,omitempty"`
	LocalPref    int      `json:"local_pref,omitempty"`
	ASPath       []uint32 `json:"as_path,omitempty"`
	InternalCost int      `json:"internal_cost,omitempty"`
	Address      string   `json:"address,omitempty"`
}

// Result 是每个事件输出的 JSON 结果。
type Result struct {
	Seq     int          `json:"seq"`
	Type    string       `json:"type"`
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Changes []Change     `json:"changes,omitempty"`
	Query   *QueryResult `json:"query,omitempty"`
	Table   []TableEntry `json:"table,omitempty"`
}

// QueryResult 是地址查询的结果。
type QueryResult struct {
	Address string `json:"address"`
	Found   bool   `json:"found"`
	Prefix  string `json:"prefix,omitempty"`
	Route   *Route `json:"route,omitempty"`
}

// apply 校验并执行一个事件，返回结果；出错时状态不变。
func (r *RIB) apply(seq int, ev Event) Result {
	res := Result{Seq: seq, Type: ev.Type}
	fail := func(err error) Result {
		res.OK = false
		res.Error = err.Error()
		return res
	}
	switch ev.Type {
	case "announce":
		p, err := parsePrefix(ev.Prefix)
		if err != nil {
			return fail(err)
		}
		if ev.LocalPref < 0 {
			return fail(fmt.Errorf("local_pref must be non-negative"))
		}
		if ev.InternalCost < 0 {
			return fail(fmt.Errorf("internal_cost must be non-negative"))
		}
		ch, err := r.Announce(ev.Neighbor, p, ev.LocalPref, ev.ASPath, ev.InternalCost)
		if err != nil {
			return fail(err)
		}
		res.OK = true
		res.Changes = []Change{ch}
	case "withdraw":
		p, err := parsePrefix(ev.Prefix)
		if err != nil {
			return fail(err)
		}
		ch, err := r.Withdraw(ev.Neighbor, p)
		if err != nil {
			return fail(err)
		}
		res.OK = true
		res.Changes = []Change{ch}
	case "neighbor_down":
		changes, err := r.NeighborDown(ev.Neighbor)
		if err != nil {
			return fail(err)
		}
		res.OK = true
		res.Changes = changes
	case "neighbor_up":
		if err := r.NeighborUp(ev.Neighbor); err != nil {
			return fail(err)
		}
		res.OK = true
	case "query":
		addr, err := netip.ParseAddr(ev.Address)
		if err != nil {
			return fail(fmt.Errorf("invalid address: %v", err))
		}
		qr := QueryResult{Address: addr.String()}
		if p, route, ok := r.Query(addr); ok {
			qr.Found = true
			qr.Prefix = p.String()
			qr.Route = &route
		}
		res.OK = true
		res.Query = &qr
	case "export":
		res.OK = true
		res.Table = r.Export()
	default:
		return fail(fmt.Errorf("unknown event type %q", ev.Type))
	}
	return res
}

// parsePrefix 解析前缀并统一清除主机位。
func parsePrefix(s string) (netip.Prefix, error) {
	if s == "" {
		return netip.Prefix{}, fmt.Errorf("prefix is required")
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid prefix %q: %v", s, err)
	}
	if !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("only IPv4 prefixes are supported: %q", s)
	}
	return p.Masked(), nil
}

// decodeEvent 解析一行 JSONL 事件。
func decodeEvent(line []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}
