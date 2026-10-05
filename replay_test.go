package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testEvents = `{"type":"announce","neighbor":"n1","prefix":"10.1.2.3/24","local_pref":100,"as_path":[65002],"internal_cost":10}
{"type":"announce","neighbor":"n2","prefix":"10.1.2.0/24","local_pref":200,"as_path":[65003,65004],"internal_cost":5}
{"type":"announce","neighbor":"n2","prefix":"10.0.0.0/8","local_pref":50,"as_path":[65003],"internal_cost":1}
{"type":"query","address":"10.1.2.9"}
{"type":"withdraw","neighbor":"n2","prefix":"10.1.2.0/24"}
{"type":"query","address":"10.1.2.9"}
{"type":"neighbor_down","neighbor":"n1"}
{"type":"neighbor_up","neighbor":"n1"}
{"type":"query","address":"10.1.2.9"}
{"type":"export"}
`

func testConfig() *Config {
	return &Config{LocalAS: 65001, Neighbors: []string{"n1", "n2", "n3"}}
}

// 保存事件文件并从空状态重放，两次结果应一致。
func TestSaveAndReplay(t *testing.T) {
	dir := t.TempDir()
	saved := filepath.Join(dir, "events.jsonl")

	var first bytes.Buffer
	if err := run(testConfig(), strings.NewReader(testEvents), &first, saved); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := run(testConfig(), bytes.NewReader(data), &second, ""); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("replay mismatch:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}
}

// 导出排序固定，与事件顺序无关。
func TestExportDeterministic(t *testing.T) {
	events := `{"type":"announce","neighbor":"n1","prefix":"10.2.0.0/16","local_pref":100,"as_path":[2],"internal_cost":1}
{"type":"announce","neighbor":"n1","prefix":"10.1.0.0/16","local_pref":100,"as_path":[2],"internal_cost":1}
{"type":"announce","neighbor":"n1","prefix":"10.1.0.0/24","local_pref":100,"as_path":[2],"internal_cost":1}
{"type":"export"}
`
	var out bytes.Buffer
	if err := run(testConfig(), strings.NewReader(events), &out, ""); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	last := lines[len(lines)-1]
	want := `"prefix":"10.1.0.0/16"`
	if !strings.Contains(last, want) ||
		strings.Index(last, `"prefix":"10.1.0.0/16"`) > strings.Index(last, `"prefix":"10.1.0.0/24"`) ||
		strings.Index(last, `"prefix":"10.1.0.0/24"`) > strings.Index(last, `"prefix":"10.2.0.0/16"`) {
		t.Fatalf("export not sorted by (address, bits): %s", last)
	}
}

// 错误事件不影响后续处理，且每个事件都有 JSON 结果。
func TestErrorEventsDoNotHalt(t *testing.T) {
	events := `{"type":"announce","neighbor":"ghost","prefix":"10.0.0.0/8","local_pref":1,"as_path":[2],"internal_cost":1}
{"type":"withdraw","neighbor":"n1","prefix":"192.0.2.0/24"}
{"type":"bogus"}
{"type":"announce","neighbor":"n1","prefix":"192.0.2.0/24","local_pref":1,"as_path":[2],"internal_cost":1}
{"type":"query","address":"192.0.2.1"}
`
	var out bytes.Buffer
	if err := run(testConfig(), strings.NewReader(events), &out, ""); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 results, got %d", len(lines))
	}
	if !strings.Contains(lines[0], `"ok":false`) || !strings.Contains(lines[3], `"ok":true`) {
		t.Fatalf("unexpected results: %v", lines)
	}
	if !strings.Contains(lines[4], `"found":true`) {
		t.Fatalf("final query should succeed: %s", lines[4])
	}
}
