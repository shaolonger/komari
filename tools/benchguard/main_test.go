package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAndCompareBenchmarks(t *testing.T) {
	baseline := `
BenchmarkDecode-10 1000 100 ns/op 64 B/op 2 allocs/op
BenchmarkDecode-10 1000 110 ns/op 64 B/op 2 allocs/op
BenchmarkDecode-10 1000 105 ns/op 64 B/op 2 allocs/op
`
	candidate := `
BenchmarkDecode-12 1000 109 ns/op 64 B/op 2 allocs/op
BenchmarkDecode-12 1000 111 ns/op 64 B/op 2 allocs/op
BenchmarkDecode-12 1000 110 ns/op 64 B/op 2 allocs/op
`
	baseSamples, err := parseBenchmarks(strings.NewReader(baseline))
	if err != nil {
		t.Fatal(err)
	}
	candidateSamples, err := parseBenchmarks(strings.NewReader(candidate))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := compare(baseSamples, candidateSamples, 3, limits{
		time: 0.10, memory: 0, allocs: 0,
	}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "BenchmarkDecode") {
		t.Fatalf("missing report: %s", output.String())
	}
}

func TestCompareRejectsTimeAndAllocationRegression(t *testing.T) {
	base, err := parseBenchmarks(strings.NewReader(
		"BenchmarkHot-8 100 100 ns/op 10 B/op 1 allocs/op\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := parseBenchmarks(strings.NewReader(
		"BenchmarkHot-10 100 150 ns/op 10 B/op 2 allocs/op\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	err = compare(base, candidate, 1, limits{time: 0.20}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "ns/op regressed") ||
		!strings.Contains(err.Error(), "allocs/op regressed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParserRejectsEmptyInput(t *testing.T) {
	if _, err := parseBenchmarks(strings.NewReader("PASS\n")); err == nil {
		t.Fatal("empty benchmark output was accepted")
	}
}
