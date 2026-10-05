package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

// run 从空状态依次应用事件流，逐事件输出 JSON 结果；
// save 非空时把原始事件行规范化后写入该文件，供之后重放。
func run(cfg *Config, events io.Reader, out io.Writer, save string) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	rib := NewRIB(cfg.LocalAS, cfg.Neighbors)

	var saveFile *os.File
	if save != "" {
		f, err := os.Create(save)
		if err != nil {
			return err
		}
		defer f.Close()
		saveFile = f
	}

	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(events)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	seq := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		seq++
		ev, err := decodeEvent(line)
		var res Result
		if err != nil {
			res = Result{Seq: seq, OK: false, Error: "invalid JSON: " + err.Error()}
		} else {
			res = rib.apply(seq, ev)
			if saveFile != nil {
				b, _ := json.Marshal(ev)
				if _, err := saveFile.Write(append(b, '\n')); err != nil {
					return err
				}
			}
		}
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return sc.Err()
}

func main() {
	configPath := flag.String("config", "", "path to JSON config file")
	eventsPath := flag.String("events", "", "path to JSONL events file (default: stdin)")
	savePath := flag.String("save", "", "save normalized events to this file for replay")
	flag.Parse()

	data, err := os.ReadFile(*configPath)
	if err != nil {
		fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fatal(err)
	}

	var events io.Reader = os.Stdin
	if *eventsPath != "" {
		f, err := os.Open(*eventsPath)
		if err != nil {
			fatal(err)
		}
		defer f.Close()
		events = f
	}

	if err := run(&cfg, events, os.Stdout, *savePath); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
