package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

const testEvents = `
{"type":"announce","neighbor":"n1","prefix":"10.1.2.3/8","local_pref":100,"as_path":[65001],"internal_cost":10}
{"type":"announce","neighbor":"n2","prefix":"10.0.0.0/8","local_pref":200,"as_path":[65002,65003],"internal_cost":5}
{"type":"announce","neighbor":"n1","prefix":"10.0.0.0/8","local_pref":300,"as_path":[65001],"internal_cost":1}
{"type":"announce","neighbor":"n2","prefix":"10.0.0.0/8","local_pref":999,"as_path":[65000],"internal_cost":0}
{"type":"query","address":"10.9.9.9"}
{"type":"withdraw","neighbor":"n1","prefix":"10.0.0.0/8"}
{"type":"withdraw","neighbor":"n1","prefix":"10.0.0.0/8"}
{"type":"neighbor_down","neighbor":"n2"}
{"type":"neighbor_up","neighbor":"n2"}
{"type":"query","address":"10.9.9.9"}
{"type":"export"}
`

func testConfig() *Config {
	return &Config{LocalASN: 65000, Neighbors: []string{"n1", "n2"}}
}

func TestReplayFromSavedFileIsDeterministic(t *testing.T) {
	var first bytes.Buffer
	var saved bytes.Buffer
	saver := bufio.NewWriter(&saved)
	if err := run(testConfig(), strings.NewReader(testEvents), &first, saver); err != nil {
		t.Fatal(err)
	}
	saver.Flush()

	var second bytes.Buffer
	if err := run(testConfig(), &saved, &second, nil); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("replay mismatch:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}
	if !strings.Contains(first.String(), `"not found:`) {
		t.Fatal("expected explicit not-found for repeated withdraw")
	}
}

func TestInvalidJSONLineDoesNotStopReplay(t *testing.T) {
	input := "{bad json}\n" + `{"type":"announce","neighbor":"n1","prefix":"10.0.0.0/8","local_pref":1}` + "\n"
	var out bytes.Buffer
	if err := run(testConfig(), strings.NewReader(input), &out, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 results, got %d", len(lines))
	}
	if !strings.Contains(lines[0], `"ok":false`) || !strings.Contains(lines[1], `"ok":true`) {
		t.Fatalf("unexpected results: %v", lines)
	}
}
