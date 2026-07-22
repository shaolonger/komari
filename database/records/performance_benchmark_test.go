package records

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type statementCounter struct {
	logger.Interface
	statements atomic.Uint64
}

func (l *statementCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.statements.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func benchmarkDB(b *testing.B, suffix string) (*gorm.DB, *statementCounter) {
	b.Helper()
	counter := &statementCounter{Interface: logger.Default.LogMode(logger.Silent)}
	dsn := fmt.Sprintf("file:benchmark-%s-%s?mode=memory&cache=shared", b.Name(), suffix)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: counter})
	if err != nil {
		b.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		b.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&models.Record{}, &models.GPURecord{}); err != nil {
		b.Fatal(err)
	}
	if err := db.Table("records_long_term").AutoMigrate(&models.Record{}); err != nil {
		b.Fatal(err)
	}
	if err := db.Table("gpu_records_long_term").AutoMigrate(&models.GPURecord{}); err != nil {
		b.Fatal(err)
	}
	counter.statements.Store(0)
	return db, counter
}

func makeRecords(client string, start time.Time, count int) []models.Record {
	records := make([]models.Record, count)
	for i := range records {
		records[i] = models.Record{
			Client: client, Time: models.FromTime(start.Add(time.Duration(i) * time.Minute)),
			Cpu: float32(i % 100), Ram: int64(i) << 20, RamTotal: 16 << 30,
			NetIn: int64(i * 1024), NetOut: int64(i * 512),
			NetTotalUp: int64(i) << 20, NetTotalDown: int64(i) << 21,
		}
	}
	return records
}

func BenchmarkBatchWrite(b *testing.B) {
	db, counter := benchmarkDB(b, "batch")
	base := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		records := makeRecords(fmt.Sprintf("node-%d", i), base, 256)
		if err := db.Create(&records).Error; err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(counter.statements.Load())/float64(b.N), "sql/op")
}

func BenchmarkHistoryCompression(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db, counter := benchmarkDB(b, fmt.Sprintf("compression-%d", i))
		now := time.Unix(1_700_100_000+int64(i*24*60*60), 0).UTC()
		records := makeRecords(fmt.Sprintf("node-%d", i), now.Add(-48*time.Hour), 2_000)
		if err := db.CreateInBatches(&records, 250).Error; err != nil {
			b.Fatal(err)
		}
		counter.statements.Store(0)
		b.StartTimer()
		if err := migrateOldRecordsAt(db, now); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		b.ReportMetric(float64(counter.statements.Load()), "sql/op")
	}
}

func BenchmarkIncrementalCompactionMillionRows(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db, counter := benchmarkDB(b, fmt.Sprintf("million-row-compression-%d", i))
		now := time.Unix(1_700_100_000+int64(i*24*60*60), 0).UTC().Truncate(15 * time.Minute)
		bucket := now.Add(-7 * time.Hour)
		if err := db.Exec(`WITH RECURSIVE sequence(value) AS (
			VALUES(0)
			UNION ALL SELECT value + 1 FROM sequence WHERE value < 999999
		)
		INSERT INTO records(client,time,cpu,ram,net_in,net_out,net_total_up,net_total_down)
		SELECT printf('node-%04d', value % 1000),
		       strftime('%Y-%m-%d %H:%M:%S.0000000', ?, printf('+%d seconds', value % 900)),
		       value % 100, value, value * 2, value * 3, value * 4, value * 5
		FROM sequence`, bucket).Error; err != nil {
			b.Fatal(err)
		}
		counter.statements.Store(0)
		b.StartTimer()
		stats, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{})
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if stats.Rows != 1_000_000 {
			b.Fatalf("compacted rows=%d, want 1000000", stats.Rows)
		}
		b.ReportMetric(float64(counter.statements.Load()), "sql/op")
	}
}

func BenchmarkHistoryRangeQuery(b *testing.B) {
	db, counter := benchmarkDB(b, "query")
	start := time.Unix(1_700_000_000, 0).UTC()
	for node := 0; node < 10; node++ {
		records := makeRecords(fmt.Sprintf("node-%02d", node), start, 10_000)
		if err := db.CreateInBatches(&records, 500).Error; err != nil {
			b.Fatal(err)
		}
	}
	counter.statements.Store(0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var result []models.Record
		if err := db.Where("client = ? AND time >= ? AND time <= ?", "node-05", start.Add(24*time.Hour), start.Add(48*time.Hour)).Order("time ASC").Find(&result).Error; err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(counter.statements.Load())/float64(b.N), "sql/op")
}

func BenchmarkTieredYearRangeQuery(b *testing.B) {
	db, counter := benchmarkDB(b, "tiered-year-query")
	if err := ensureTierSchema(db); err != nil {
		b.Fatal(err)
	}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	hourly := make([]models.Record, 365*24)
	for index := range hourly {
		hourly[index] = models.Record{Client: "year-node", Time: models.FromTime(start.Add(time.Duration(index) * time.Hour)), Cpu: float32(index % 100)}
	}
	fifteen := make([]models.Record, 365*24*4)
	for index := range fifteen {
		fifteen[index] = models.Record{Client: "year-node", Time: models.FromTime(start.Add(time.Duration(index) * 15 * time.Minute)), Cpu: float32(index % 100)}
	}
	if err := db.Table("records_hourly").CreateInBatches(&hourly, 500).Error; err != nil {
		b.Fatal(err)
	}
	if err := db.Table("records_long_term").CreateInBatches(&fifteen, 500).Error; err != nil {
		b.Fatal(err)
	}
	end := start.Add(365 * 24 * time.Hour)
	b.Run("15m", func(b *testing.B) {
		counter.statements.Store(0)
		b.ReportAllocs()
		for range b.N {
			var result []models.Record
			if err := db.Table("records_long_term").Where("client = ? AND time >= ? AND time < ?", "year-node", start, end).Order("time").Find(&result).Error; err != nil {
				b.Fatal(err)
			}
			if len(result) != len(fifteen) {
				b.Fatalf("15m rows=%d", len(result))
			}
		}
		b.ReportMetric(float64(counter.statements.Load())/float64(b.N), "sql/op")
	})
	b.Run("1h", func(b *testing.B) {
		counter.statements.Store(0)
		b.ReportAllocs()
		for range b.N {
			var result []models.Record
			if err := db.Table("records_hourly").Where("client = ? AND time >= ? AND time < ?", "year-node", start, end).Order("time").Find(&result).Error; err != nil {
				b.Fatal(err)
			}
			if len(result) != len(hourly) {
				b.Fatalf("1h rows=%d", len(result))
			}
		}
		b.ReportMetric(float64(counter.statements.Load())/float64(b.N), "sql/op")
	})
}

var benchmarkTrafficStats TrafficStats

func BenchmarkTrafficSummary(b *testing.B) {
	start := time.Unix(1_700_000_000, 0).UTC()
	records := makeRecords("node", start, 10_000)
	end := start.Add(time.Duration(len(records)-1) * time.Minute)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkTrafficStats = SummarizeTrafficRecords(records, start, end, 15*time.Minute)
	}
}
