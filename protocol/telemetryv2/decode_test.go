package telemetryv2

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

var benchmarkDecoded Report

func TestDecodeCrossRepositoryFixture(t *testing.T) {
	frame := readFixture(t)
	decoded, err := Decode(frame)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.CPUUsage != 12.5 || decoded.RAM != (Memory{Total: 8_000, Used: 4_000}) ||
		decoded.Swap != (Memory{Total: 2_000, Used: 100}) || decoded.Disk != (Memory{Total: 1_000, Used: 500}) ||
		decoded.Load != (Load{Load1: 1.1, Load5: 1.2, Load15: 1.3}) ||
		decoded.Network != (Network{Up: 100, Down: 200, TotalUp: 1_000, TotalDown: 2_000}) ||
		decoded.Connections != (Connections{TCP: 12, UDP: 3}) || decoded.Uptime != 999 ||
		decoded.Process != 42 || decoded.Message != "ok" {
		t.Fatalf("unexpected decoded fixture: %+v", decoded)
	}
	if decoded.GPU == nil || !decoded.GPU.Detailed || decoded.GPU.AverageUsage != 75 || len(decoded.GPU.Devices) != 1 {
		t.Fatalf("unexpected fixture GPU: %+v", decoded.GPU)
	}
	device := decoded.GPU.Devices[0]
	if device.Name != "GPU" || device.MemoryTotal != 100 || device.MemoryUsed != 50 || device.Utilization != 75 || device.Temperature != 65 {
		t.Fatalf("unexpected fixture GPU device: %+v", device)
	}
}

func TestDecodeRejectsMalformedAndOversizedFrames(t *testing.T) {
	valid := readFixture(t)
	for size := range len(valid) {
		if _, err := Decode(valid[:size]); err == nil {
			t.Fatalf("truncated frame of %d bytes was accepted", size)
		}
	}
	mutations := map[string]func([]byte){
		"magic":       func(frame []byte) { frame[0] = 'X' },
		"version":     func(frame []byte) { frame[4]++ },
		"flags":       func(frame []byte) { frame[5] = 0x80 },
		"header size": func(frame []byte) { binary.LittleEndian.PutUint16(frame[6:8], 15) },
		"payload size": func(frame []byte) {
			binary.LittleEndian.PutUint32(frame[8:12], uint32(len(frame)))
		},
		"schema": func(frame []byte) { binary.LittleEndian.PutUint32(frame[12:16], 0) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			frame := append([]byte(nil), valid...)
			mutate(frame)
			if _, err := Decode(frame); err == nil {
				t.Fatal("malformed frame was accepted")
			}
		})
	}
	trailing := append(append([]byte(nil), valid...), 0)
	binary.LittleEndian.PutUint32(trailing[8:12], uint32(len(trailing)-HeaderSize))
	if _, err := Decode(trailing); err == nil {
		t.Fatal("trailing data was accepted")
	}
	if _, err := Decode(make([]byte, MaxFrameSize+1)); err == nil {
		t.Fatal("oversized frame was accepted")
	}
}

func FuzzDecodeNeverPanics(f *testing.F) {
	fixture, err := os.ReadFile("testdata/report_v2.hex")
	if err != nil {
		f.Fatal(err)
	}
	frame, err := hex.DecodeString(strings.TrimSpace(string(fixture)))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(frame)
	f.Add([]byte("KMR2"))
	f.Add(make([]byte, MaxFrameSize+1))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Decode(data)
	})
}

func BenchmarkDecode(b *testing.B) {
	frame := readFixture(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for range b.N {
		benchmarkDecoded, _ = Decode(frame)
	}
}

type fixtureTesting interface {
	Helper()
	Fatalf(string, ...any)
}

func readFixture(t fixtureTesting) []byte {
	t.Helper()
	fixture, err := os.ReadFile("testdata/report_v2.hex")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	frame, err := hex.DecodeString(strings.TrimSpace(string(fixture)))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return frame
}
