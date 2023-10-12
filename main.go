// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Binary pgo-analysis parses the JSON output of the Go compiler's
// `-d=pgodebug=3` flag and summarizes devirtualization of indirect calls.
//
// Example:
//
//	$ go build -gcflags=all=-d=pgodebug=3 >/tmp/log.txt 2>&1
//	$ go run github.com/prattmic/pgo-analysis@latest </tmp/log.txt | less
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), `pgo-analysis parses the JSON output of the Go compiler's
-d=pgodebug=3 flag and summarizes devirtualization of indirect calls.

Example:
	$ go build -gcflags=all=-d=pgodebug=3 >/tmp/log.txt 2>&1
	$ go run github.com/prattmic/pgo-analysis@latest </tmp/log.txt | less
`)
		flag.PrintDefaults()
	}
}

var inlinedCallRe = regexp.MustCompile(`^(\S+): inlining call to (.*)$`)

// From cmd/compile/internal/pgo.
type CallStat struct {
	Pkg string
	Pos string

	Caller string

	// Call type. Interface must not be Direct.
	Direct    bool
	Interface bool

	Weight int64

	Hottest       string
	HottestWeight int64

	// Devirtualized callee if != "".
	//
	// Note that this may be different than Hottest because we apply
	// type-check restrictions, which helps distinguish multiple calls on
	// the same line. Hottest doesn't do that.
	Devirtualized       string
	DevirtualizedWeight int64
}

var cwd = func() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}()

// cmd/go takes absolute filenames and makes them relative if possible. This
// makes the log line positions not match the stat JSON. Undo this.
func normalizePos(pos string) string {
	if len(pos) >= 1 && pos[0] == '/' {
		return pos
	}
	return filepath.Join(cwd, pos)
}

func readStats() ([]CallStat, map[string][]string, error) {
	var stats []CallStat
	inlined := make(map[string][]string) // pos -> []symbol

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()

		m := inlinedCallRe.FindStringSubmatch(string(line))
		if len(m) == 3 {
			pos := normalizePos(m[1])
			inlined[pos] = append(inlined[pos], m[2])
		}

		var stat CallStat
		if err := json.Unmarshal(line, &stat); err != nil {
			//log.Printf("Failed to unmarshal %q: %v", scanner.Text(), err)
			continue
		}
		stats = append(stats, stat)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}

	return stats, inlined, nil
}

type sum struct {
	direct         int64
	indirectFunc   int64
	indirectMethod int64
}

func (s *sum) total() int64 {
	return s.direct + s.indirectFunc + s.indirectMethod
}

func pct(n, d int64) float64 {
	return 100 * float64(n) / float64(d)
}

func run() error {
	stats, inlined, err := readStats()
	if err != nil {
		return err
	}

	var (
		count               sum
		weight              sum
		hottestWeight       sum
		devirtualizedCount  sum
		devirtualizedWeight sum
	)

	for _, s := range stats {
		if s.Direct {
			count.direct++
			weight.direct += s.Weight
			hottestWeight.direct += s.Weight
		} else if s.Interface {
			count.indirectMethod++
			weight.indirectMethod += s.Weight
			hottestWeight.indirectMethod += s.HottestWeight
			if s.Devirtualized != "" {
				devirtualizedCount.indirectMethod++
				devirtualizedWeight.indirectMethod += s.DevirtualizedWeight
			}
		} else {
			count.indirectFunc++
			weight.indirectFunc += s.Weight
			hottestWeight.indirectFunc += s.HottestWeight
			if s.Devirtualized != "" {
				devirtualizedCount.indirectFunc++
				devirtualizedWeight.indirectFunc += s.DevirtualizedWeight
			}
		}
	}

	fmt.Printf("Call count breakdown:\n")
	fmt.Printf("\tTotal: %d\n", count.total())
	fmt.Printf("\tDirect: %d (%.2f%% of total)\n", count.direct, pct(count.direct, count.total()))
	fmt.Printf("\tIndirect func: %d (%.2f%% of total)\n", count.indirectFunc, pct(count.indirectFunc, count.total()))
	fmt.Printf("\tInterface method: %d (%.2f%% of total)\n", count.indirectMethod, pct(count.indirectMethod, count.total()))

	fmt.Printf("Call weight breakdown:\n")
	fmt.Printf("\tTotal: %d\n", weight.total())
	fmt.Printf("\tDirect: %d (%.2f%% of total)\n", weight.direct, pct(weight.direct, weight.total()))
	fmt.Printf("\tIndirect func: %d (%.2f%% of total)\n", weight.indirectFunc, pct(weight.indirectFunc, weight.total()))
	fmt.Printf("\tInterface method: %d (%.2f%% of total)\n", weight.indirectMethod, pct(weight.indirectMethod, weight.total()))

	fmt.Printf("Call hottest weight breakdown:\n")
	fmt.Printf("\tTotal: %d (%.2f%% of total)\n", hottestWeight.total(), pct(hottestWeight.total(), weight.total()))
	fmt.Printf("\tDirect: %d (%.2f%% of direct)\n", hottestWeight.direct, pct(hottestWeight.direct, weight.direct))
	fmt.Printf("\tIndirect func: %d (%.2f%% of indirect func)\n", hottestWeight.indirectFunc, pct(hottestWeight.indirectFunc, weight.indirectFunc))
	fmt.Printf("\tInterface method: %d (%.2f%% of interface method)\n", hottestWeight.indirectMethod, pct(hottestWeight.indirectMethod, weight.indirectMethod))

	fmt.Printf("Devirtualized interface call count: %d (%.2f%% of total, %.2f%% of interface method)\n", devirtualizedCount.indirectMethod, pct(devirtualizedCount.indirectMethod, count.total()), pct(devirtualizedCount.indirectMethod, count.indirectMethod))
	fmt.Printf("Devirtualized interface call weight: %d (%.2f%% of total, %.2f%% of interface method)\n", devirtualizedWeight.indirectMethod, pct(devirtualizedWeight.indirectMethod, weight.total()), pct(devirtualizedWeight.indirectMethod, weight.indirectMethod))
	fmt.Printf("Devirtualized function call count: %d (%.2f%% of total, %.2f%% of indirect func)\n", devirtualizedCount.indirectFunc, pct(devirtualizedCount.indirectFunc, count.total()), pct(devirtualizedCount.indirectFunc, count.indirectFunc))
	fmt.Printf("Devirtualized function call weight: %d (%.2f%% of total, %.2f%% of indirect func)\n", devirtualizedWeight.indirectFunc, pct(devirtualizedWeight.indirectFunc, weight.total()), pct(devirtualizedWeight.indirectFunc, weight.indirectFunc))

	const topCount = 100
	fmt.Printf("\nTop %d hottest indirect calls:\n", topCount)
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].HottestWeight != stats[j].HottestWeight {
			return stats[i].HottestWeight < stats[j].HottestWeight
		}
		if stats[i].Pkg != stats[j].Pkg {
			return stats[i].Pkg < stats[j].Pkg
		}
		return stats[i].Pos < stats[j].Pos
	})
	printed := 0
	var topWeight, topHottestWeight int64
	for i := len(stats) - 1; i >= 0 && printed < topCount; i-- {
		s := stats[i]
		if s.Direct {
			continue
		}
		spec := "NOT Devirtualized"
		specExtra := ""
		if s.Devirtualized != "" {
			spec = "    Devirtualized"
			if s.Devirtualized != s.Hottest {
				specExtra = fmt.Sprintf("\t(devirtualized to %s weight %d)", s.Devirtualized, s.DevirtualizedWeight)
			}
		}
		typ := "interface"
		if !s.Interface {
			typ = " function"
		}
		fmt.Printf("\t(%s) (%s) %-40s -> %-40s (weight %d, %.2f%% of callsite weight)%s\t%s\n", spec, typ, s.Caller, s.Hottest, s.HottestWeight, pct(s.HottestWeight, s.Weight), specExtra, s.Pos)
		for _, s := range inlined[s.Pos] {
			fmt.Printf("\t\tinlined %s\n", s)
		}

		printed++
		topWeight += s.Weight
		topHottestWeight += s.HottestWeight
	}
	fmt.Printf("Top %d weight: %d (%.2f%% of indirect weight)\n", topCount, topWeight, pct(topWeight, weight.indirectFunc+weight.indirectMethod))
	fmt.Printf("Top %d hottest weight: %d (%.2f%% of indirect hottest weight)\n", topCount, topHottestWeight, pct(topHottestWeight, hottestWeight.indirectFunc+hottestWeight.indirectMethod))

	return nil
}

func main() {
	flag.Parse()

	if err := run(); err != nil {
		log.Fatal(err)
	}
}
