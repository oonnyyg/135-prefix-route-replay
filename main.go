package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	configPath := flag.String("config", "", "JSON config file (local_asn, neighbors)")
	eventsPath := flag.String("events", "", "JSONL event file (default: stdin)")
	savePath := flag.String("save", "", "save event lines to this file for replay")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "missing -config")
		os.Exit(2)
	}
	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read config:", err)
		os.Exit(1)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "parse config:", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "invalid config:", err)
		os.Exit(1)
	}

	var in io.Reader = os.Stdin
	if *eventsPath != "" {
		f, err := os.Open(*eventsPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "open events:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}

	var saver *bufio.Writer
	if *savePath != "" {
		f, err := os.Create(*savePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "create save file:", err)
			os.Exit(1)
		}
		defer func() {
			saver.Flush()
			f.Close()
		}()
		saver = bufio.NewWriter(f)
	}

	if err := run(&cfg, in, os.Stdout, saver); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run replays events line by line and writes one JSON result per line.
func run(cfg *Config, in io.Reader, out io.Writer, saver *bufio.Writer) error {
	rib := NewRIB(cfg.LocalASN, cfg.Neighbors)
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			res := Result{OK: false, Error: fmt.Sprintf("line %d: invalid JSON: %v", lineNo, err)}
			if err := enc.Encode(res); err != nil {
				return err
			}
			continue
		}
		res := Apply(rib, ev)
		if saver != nil {
			saver.Write(line)
			saver.WriteByte('\n')
		}
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return sc.Err()
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}
