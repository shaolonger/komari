package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var cpuSuffix = regexp.MustCompile(`-\d+$`)

type samples map[string]map[string][]float64

func parseBenchmarks(reader io.Reader) (samples, error) {
	result := make(samples)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		name := cpuSuffix.ReplaceAllString(fields[0], "")
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			continue
		}
		for index := 2; index+1 < len(fields); index += 2 {
			value, err := strconv.ParseFloat(fields[index], 64)
			if err != nil {
				break
			}
			unit := fields[index+1]
			if unit != "ns/op" && unit != "B/op" && unit != "allocs/op" {
				continue
			}
			if result[name] == nil {
				result[name] = make(map[string][]float64)
			}
			result[name][unit] = append(result[name][unit], value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("no Go benchmark samples found")
	}
	return result, nil
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

type limits struct {
	time   float64
	memory float64
	allocs float64
}

func compare(base, candidate samples, minSamples int, allowed limits, writer io.Writer) error {
	units := []struct {
		name  string
		limit float64
	}{
		{name: "ns/op", limit: allowed.time},
		{name: "B/op", limit: allowed.memory},
		{name: "allocs/op", limit: allowed.allocs},
	}

	names := make([]string, 0, len(base))
	for name := range base {
		names = append(names, name)
	}
	sort.Strings(names)

	var failures []string
	for _, name := range names {
		candidateMetrics, ok := candidate[name]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s missing from candidate", name))
			continue
		}
		for _, unit := range units {
			baseValues := base[name][unit.name]
			candidateValues := candidateMetrics[unit.name]
			if len(baseValues) < minSamples || len(candidateValues) < minSamples {
				failures = append(failures, fmt.Sprintf(
					"%s %s has %d baseline/%d candidate samples; need %d",
					name, unit.name, len(baseValues), len(candidateValues), minSamples,
				))
				continue
			}
			baseMedian := median(baseValues)
			candidateMedian := median(candidateValues)
			ratio := 1.0
			if baseMedian == 0 {
				if candidateMedian > 0 {
					ratio = math.Inf(1)
				}
			} else {
				ratio = candidateMedian / baseMedian
			}
			fmt.Fprintf(writer, "%-48s %-9s baseline=%-12g candidate=%-12g delta=%+.2f%%\n",
				name, unit.name, baseMedian, candidateMedian, (ratio-1)*100)
			if ratio > 1+unit.limit {
				failures = append(failures, fmt.Sprintf(
					"%s %s regressed %.2f%% (limit %.2f%%)",
					name, unit.name, (ratio-1)*100, unit.limit*100,
				))
			}
		}
	}
	for name := range candidate {
		if _, ok := base[name]; !ok {
			failures = append(failures, fmt.Sprintf("%s missing from baseline", name))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("performance gate failed:\n- %s", strings.Join(failures, "\n- "))
	}
	return nil
}

func readSamples(path string) (samples, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseBenchmarks(file)
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("benchguard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	baselinePath := flags.String("baseline", "", "Go benchmark output from the base revision")
	candidatePath := flags.String("candidate", "", "Go benchmark output from the candidate revision")
	timeLimit := flags.Float64("time-limit", 0.20, "maximum median ns/op regression ratio")
	memoryLimit := flags.Float64("memory-limit", 0.02, "maximum median B/op regression ratio")
	allocLimit := flags.Float64("alloc-limit", 0, "maximum median allocs/op regression ratio")
	minSamples := flags.Int("min-samples", 5, "minimum samples per benchmark metric")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *baselinePath == "" || *candidatePath == "" {
		return errors.New("-baseline and -candidate are required")
	}
	if *minSamples < 1 || *timeLimit < 0 || *memoryLimit < 0 || *allocLimit < 0 {
		return errors.New("limits must be non-negative and min-samples must be positive")
	}
	base, err := readSamples(*baselinePath)
	if err != nil {
		return fmt.Errorf("read baseline: %w", err)
	}
	candidate, err := readSamples(*candidatePath)
	if err != nil {
		return fmt.Errorf("read candidate: %w", err)
	}
	return compare(base, candidate, *minSamples, limits{
		time:   *timeLimit,
		memory: *memoryLimit,
		allocs: *allocLimit,
	}, stdout)
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
