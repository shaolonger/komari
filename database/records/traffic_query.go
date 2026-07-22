package records

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

const maxTrafficSQLClientFilter = 256

type TrafficQueryResult struct {
	ByClient   map[string]TrafficStats
	SQLQueries int
}

type trafficPoint struct {
	Time         models.LocalTime
	NetIn        int64
	NetOut       int64
	NetTotalUp   int64
	NetTotalDown int64
}

type trafficStreamAccumulator struct {
	start, end     time.Time
	bucketSize     time.Duration
	stats          TrafficStats
	previous       trafficPoint
	hasPrevious    bool
	coveredSeconds float64
}

func newTrafficStreamAccumulator(start, end time.Time, bucketSize time.Duration, includeSeries bool) *trafficStreamAccumulator {
	accumulator := &trafficStreamAccumulator{start: start, end: end, bucketSize: bucketSize}
	if includeSeries && bucketSize > 0 {
		accumulator.stats.Series = makeTrafficBuckets(start, end, bucketSize)
	}
	return accumulator
}

func (accumulator *trafficStreamAccumulator) add(point trafficPoint) {
	pointTime := point.Time.ToTime()
	if !accumulator.hasPrevious {
		accumulator.previous = point
		accumulator.hasPrevious = true
		accumulator.stats.Samples = 1
		accumulator.stats.FirstSample = pointTime.Format(time.RFC3339)
		accumulator.stats.LastSample = pointTime.Format(time.RFC3339)
		return
	}
	previousTime := accumulator.previous.Time.ToTime()
	if !pointTime.After(previousTime) {
		if pointTime.Equal(previousTime) {
			accumulator.previous = point
		}
		return
	}
	accumulator.stats.Samples++
	accumulator.stats.LastSample = pointTime.Format(time.RFC3339)
	overlapStart := maxTime(previousTime, accumulator.start)
	overlapEnd := minTime(pointTime, accumulator.end)
	if overlapEnd.After(overlapStart) {
		intervalSeconds := pointTime.Sub(previousTime).Seconds()
		overlapSeconds := overlapEnd.Sub(overlapStart).Seconds()
		up, upReset, upEstimated := trafficDelta(
			accumulator.previous.NetTotalUp, point.NetTotalUp, accumulator.previous.NetOut, point.NetOut, intervalSeconds,
		)
		down, downReset, downEstimated := trafficDelta(
			accumulator.previous.NetTotalDown, point.NetTotalDown, accumulator.previous.NetIn, point.NetIn, intervalSeconds,
		)
		if upReset || downReset {
			accumulator.stats.Resets++
		}
		if upEstimated || downEstimated {
			accumulator.stats.Estimated = true
		}
		ratio := overlapSeconds / intervalSeconds
		segmentUp := max(int64(0), int64(float64(up)*ratio+0.5))
		segmentDown := max(int64(0), int64(float64(down)*ratio+0.5))
		accumulator.stats.Up += segmentUp
		accumulator.stats.Down += segmentDown
		accumulator.coveredSeconds += overlapSeconds
		segmentBPS := float64(segmentUp+segmentDown) / overlapSeconds
		accumulator.stats.PeakBps = max(accumulator.stats.PeakBps, segmentBPS)
		if len(accumulator.stats.Series) > 0 {
			distributeTraffic(accumulator.stats.Series, accumulator.start, accumulator.bucketSize, overlapStart, overlapEnd, segmentUp, segmentDown)
		}
	}
	accumulator.previous = point
}

func (accumulator *trafficStreamAccumulator) finish() TrafficStats {
	accumulator.stats.Total = accumulator.stats.Up + accumulator.stats.Down
	durationSeconds := accumulator.end.Sub(accumulator.start).Seconds()
	if durationSeconds > 0 {
		accumulator.stats.Coverage = min(1.0, accumulator.coveredSeconds/durationSeconds)
		accumulator.stats.AvgBps = float64(accumulator.stats.Total) / durationSeconds
	}
	accumulator.stats.Quality = trafficQuality(accumulator.stats)
	return accumulator.stats
}

func EmptyTrafficStats(start, end time.Time, bucketSize time.Duration, includeSeries bool) TrafficStats {
	return newTrafficStreamAccumulator(start, end, bucketSize, includeSeries).finish()
}

func buildTrafficUnion(segments []QuerySegment, clientIDs []string) (string, []any) {
	parts := make([]string, 0, len(segments))
	args := make([]any, 0, len(segments)*2+len(clientIDs)*len(segments))
	filterClients := len(clientIDs) > 0 && len(clientIDs) <= maxTrafficSQLClientFilter
	for _, segment := range segments {
		part := fmt.Sprintf(
			"SELECT client,time,net_in,net_out,net_total_up,net_total_down FROM %s WHERE time >= ? AND time < ?",
			segment.Table,
		)
		// LocalTime's Valuer normalizes offsets to the configured application
		// timezone, matching the timezone-less TEXT representation in SQLite.
		args = append(args, models.FromTime(segment.Start), models.FromTime(segment.End))
		if filterClients {
			part += " AND client IN (" + strings.TrimSuffix(strings.Repeat("?,", len(clientIDs)), ",") + ")"
			for _, clientID := range clientIDs {
				args = append(args, clientID)
			}
		}
		parts = append(parts, part)
	}
	return "SELECT client,time,net_in,net_out,net_total_up,net_total_down FROM (" + strings.Join(parts, " UNION ALL ") + ") ORDER BY client ASC,time ASC", args
}

func StreamTrafficStats(ctx context.Context, db *gorm.DB, clientIDs []string, start, end time.Time, bucketSize time.Duration, includeSeries bool) (TrafficQueryResult, error) {
	return streamTrafficStatsAt(ctx, db, clientIDs, start, end, bucketSize, includeSeries, time.Now())
}

func streamTrafficStatsAt(ctx context.Context, db *gorm.DB, clientIDs []string, start, end time.Time, bucketSize time.Duration, includeSeries bool, now time.Time) (TrafficQueryResult, error) {
	result := TrafficQueryResult{ByClient: make(map[string]TrafficStats)}
	if db == nil {
		return result, fmt.Errorf("traffic database is required")
	}
	if ctx == nil {
		return result, fmt.Errorf("traffic context is required")
	}
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return result, ErrInvalidQueryRange
	}
	// A non-nil empty slice means the caller is authorized to see no clients.
	// A nil slice is reserved for internal all-client queries.
	if clientIDs != nil && len(clientIDs) == 0 {
		return result, nil
	}
	queryStart := start.Add(-time.Hour)
	segments, err := PlanRecordQuery(queryStart, end, now, adminQueryBudget.MaxPoints)
	if err != nil {
		return result, err
	}
	if len(segments) == 0 {
		return result, nil
	}
	statement, args := buildTrafficUnion(segments, clientIDs)
	sqlDB, err := db.DB()
	if err != nil {
		return result, err
	}
	rows, err := sqlDB.QueryContext(ctx, statement, args...)
	result.SQLQueries = 1
	if err != nil {
		return result, err
	}
	defer rows.Close()
	allowed := make(map[string]struct{}, len(clientIDs))
	for _, clientID := range clientIDs {
		allowed[clientID] = struct{}{}
	}
	var currentClient string
	var current trafficStreamAccumulator
	hasCurrent := false
	finishCurrent := func() {
		if hasCurrent {
			result.ByClient[currentClient] = current.finish()
		}
	}
	for rows.Next() {
		var client string
		var point trafficPoint
		if err := rows.Scan(&client, &point.Time, &point.NetIn, &point.NetOut, &point.NetTotalUp, &point.NetTotalDown); err != nil {
			return result, err
		}
		if len(allowed) > 0 {
			if _, ok := allowed[client]; !ok {
				continue
			}
		}
		if !hasCurrent || currentClient != client {
			finishCurrent()
			currentClient = client
			current = *newTrafficStreamAccumulator(start, end, bucketSize, includeSeries)
			hasCurrent = true
		}
		current.add(point)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	finishCurrent()
	return result, nil
}
