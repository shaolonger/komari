package records

import (
	"math"
	"sort"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

type TrafficBucket struct {
	Time  string `json:"time"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Total int64  `json:"total"`
}

type TrafficStats struct {
	Up          int64           `json:"up"`
	Down        int64           `json:"down"`
	Total       int64           `json:"total"`
	AvgBps      float64         `json:"avg_bps"`
	PeakBps     float64         `json:"peak_bps"`
	Samples     int             `json:"samples"`
	Coverage    float64         `json:"coverage"`
	Resets      int             `json:"resets"`
	Estimated   bool            `json:"estimated"`
	Quality     string          `json:"quality"`
	FirstSample string          `json:"first_sample,omitempty"`
	LastSample  string          `json:"last_sample,omitempty"`
	Series      []TrafficBucket `json:"series,omitempty"`
}

// SummarizeTrafficRecords converts cumulative network counters into traffic used
// inside [start, end]. Counter resets are treated as partial-quality segments.
func SummarizeTrafficRecords(recs []models.Record, start, end time.Time, bucketSize time.Duration) TrafficStats {
	stats := TrafficStats{}
	if !end.After(start) {
		stats.Quality = "empty"
		return stats
	}

	records := normalizeTrafficRecords(recs)
	stats.Samples = len(records)
	if len(records) == 0 {
		stats.Quality = "empty"
		stats.Coverage = 0
		if bucketSize > 0 {
			stats.Series = makeTrafficBuckets(start, end, bucketSize)
		}
		return stats
	}

	stats.FirstSample = records[0].Time.ToTime().Format(time.RFC3339)
	stats.LastSample = records[len(records)-1].Time.ToTime().Format(time.RFC3339)
	if bucketSize > 0 {
		stats.Series = makeTrafficBuckets(start, end, bucketSize)
	}

	coveredSeconds := 0.0
	for i := 1; i < len(records); i++ {
		prev := records[i-1]
		cur := records[i]
		prevTime := prev.Time.ToTime()
		curTime := cur.Time.ToTime()
		if !curTime.After(prevTime) {
			continue
		}

		overlapStart := maxTime(prevTime, start)
		overlapEnd := minTime(curTime, end)
		if !overlapEnd.After(overlapStart) {
			continue
		}

		intervalSeconds := curTime.Sub(prevTime).Seconds()
		overlapSeconds := overlapEnd.Sub(overlapStart).Seconds()
		if intervalSeconds <= 0 || overlapSeconds <= 0 {
			continue
		}

		up, upReset, upEstimated := trafficDelta(prev.NetTotalUp, cur.NetTotalUp, prev.NetOut, cur.NetOut, intervalSeconds)
		down, downReset, downEstimated := trafficDelta(prev.NetTotalDown, cur.NetTotalDown, prev.NetIn, cur.NetIn, intervalSeconds)
		if upReset || downReset {
			stats.Resets++
		}
		if upEstimated || downEstimated {
			stats.Estimated = true
		}

		ratio := overlapSeconds / intervalSeconds
		segUp := int64(math.Round(float64(up) * ratio))
		segDown := int64(math.Round(float64(down) * ratio))
		if segUp < 0 {
			segUp = 0
		}
		if segDown < 0 {
			segDown = 0
		}
		stats.Up += segUp
		stats.Down += segDown
		coveredSeconds += overlapSeconds

		segmentBps := float64(segUp+segDown) / overlapSeconds
		if segmentBps > stats.PeakBps {
			stats.PeakBps = segmentBps
		}
		if len(stats.Series) > 0 {
			distributeTraffic(stats.Series, start, bucketSize, overlapStart, overlapEnd, segUp, segDown)
		}
	}

	stats.Total = stats.Up + stats.Down
	durationSeconds := end.Sub(start).Seconds()
	if durationSeconds > 0 {
		stats.Coverage = math.Min(1, coveredSeconds/durationSeconds)
		stats.AvgBps = float64(stats.Total) / durationSeconds
	}
	stats.Quality = trafficQuality(stats)
	return stats
}

func normalizeTrafficRecords(recs []models.Record) []models.Record {
	if len(recs) == 0 {
		return nil
	}
	records := append([]models.Record(nil), recs...)
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].Time.ToTime().Before(records[j].Time.ToTime())
	})
	deduped := records[:0]
	for _, rec := range records {
		if len(deduped) == 0 {
			deduped = append(deduped, rec)
			continue
		}
		last := &deduped[len(deduped)-1]
		if rec.Time.ToTime().Equal(last.Time.ToTime()) {
			*last = rec
			continue
		}
		deduped = append(deduped, rec)
	}
	return deduped
}

func trafficDelta(prevTotal, curTotal, prevRate, curRate int64, intervalSeconds float64) (int64, bool, bool) {
	hasCounter := prevTotal > 0 || curTotal > 0
	if hasCounter {
		if curTotal >= prevTotal {
			return curTotal - prevTotal, false, false
		}
		// Counter reset: count the post-reset counter, but mark quality partial.
		return maxInt64(curTotal, 0), true, false
	}

	if prevRate > 0 || curRate > 0 {
		avgRate := float64(maxInt64(prevRate, 0)+maxInt64(curRate, 0)) / 2
		return int64(math.Round(avgRate * intervalSeconds)), false, true
	}
	return 0, false, false
}

func trafficQuality(stats TrafficStats) string {
	if stats.Samples == 0 {
		return "empty"
	}
	if stats.Coverage < 0.8 || stats.Resets > 0 {
		return "partial"
	}
	if stats.Estimated {
		return "estimated"
	}
	return "exact"
}

func makeTrafficBuckets(start, end time.Time, bucketSize time.Duration) []TrafficBucket {
	if bucketSize <= 0 || !end.After(start) {
		return nil
	}
	count := int(math.Ceil(end.Sub(start).Seconds() / bucketSize.Seconds()))
	if count <= 0 {
		return nil
	}
	buckets := make([]TrafficBucket, count)
	for i := range buckets {
		buckets[i].Time = start.Add(time.Duration(i) * bucketSize).Format(time.RFC3339)
	}
	return buckets
}

func distributeTraffic(buckets []TrafficBucket, rangeStart time.Time, bucketSize time.Duration, segmentStart, segmentEnd time.Time, up, down int64) {
	segmentSeconds := segmentEnd.Sub(segmentStart).Seconds()
	if len(buckets) == 0 || bucketSize <= 0 || segmentSeconds <= 0 {
		return
	}

	first := int(math.Floor(segmentStart.Sub(rangeStart).Seconds() / bucketSize.Seconds()))
	last := int(math.Floor(segmentEnd.Add(-time.Nanosecond).Sub(rangeStart).Seconds() / bucketSize.Seconds()))
	if first < 0 {
		first = 0
	}
	if last >= len(buckets) {
		last = len(buckets) - 1
	}
	for i := first; i <= last; i++ {
		bucketStart := rangeStart.Add(time.Duration(i) * bucketSize)
		bucketEnd := bucketStart.Add(bucketSize)
		overlapStart := maxTime(segmentStart, bucketStart)
		overlapEnd := minTime(segmentEnd, bucketEnd)
		if !overlapEnd.After(overlapStart) {
			continue
		}
		ratio := overlapEnd.Sub(overlapStart).Seconds() / segmentSeconds
		buckets[i].Up += int64(math.Round(float64(up) * ratio))
		buckets[i].Down += int64(math.Round(float64(down) * ratio))
		buckets[i].Total = buckets[i].Up + buckets[i].Down
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
