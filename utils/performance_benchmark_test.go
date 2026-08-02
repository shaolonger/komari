package utils

import (
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/common"
)

var benchmarkAggregatedReport any

func benchmarkReports(count int) []common.Report {
	reports := make([]common.Report, count)
	for i := range reports {
		reports[i] = common.Report{
			CPU:         common.CPUReport{Usage: float64(i%100) + 0.5},
			Ram:         common.RamReport{Total: 16 << 30, Used: int64(1+i%12) << 30},
			Swap:        common.RamReport{Total: 4 << 30, Used: int64(i%4) << 28},
			Load:        common.LoadReport{Load1: float64(i%16) / 4},
			Disk:        common.DiskReport{Total: 256 << 30, Used: int64(64+i%128) << 30},
			Network:     common.NetworkReport{Up: int64(i * 101), Down: int64(i * 211), TotalUp: int64(i) << 30, TotalDown: int64(i) << 31},
			Connections: common.ConnectionsReport{TCP: i % 256, UDP: i % 64},
			Process:     100 + i%50,
			GPU: &common.GPUDetailReport{Count: 2, AverageUsage: float64(i % 100), DetailedInfo: []common.GPUDeviceInfo{
				{Name: "GPU-0", MemoryTotal: 24 << 30, MemoryUsed: int64(i%24) << 30, Utilization: float64(i % 100), Temperature: 40 + i%40},
				{Name: "GPU-1", MemoryTotal: 24 << 30, MemoryUsed: int64((i+7)%24) << 30, Utilization: float64((i + 13) % 100), Temperature: 42 + i%38},
			}},
		}
	}
	return reports
}

func BenchmarkMinuteAggregation(b *testing.B) {
	for _, samples := range []int{60, 600} {
		b.Run(fmt.Sprintf("reports=%d", samples), func(b *testing.B) {
			reports := benchmarkReports(samples)
			now := time.Unix(1_700_000_000, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				benchmarkAggregatedReport = AverageReport("benchmark-node", now, reports, 0.3)
				benchmarkAggregatedReport = AverageGPUReports("benchmark-node", now, reports, 0.3)
			}
		})
	}
}
