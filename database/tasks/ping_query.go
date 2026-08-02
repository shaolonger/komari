package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/komari-monitor/komari/database/metrics"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

const MaxPingQueryPoints = 100_000

type PingQuery struct {
	Client    string
	Clients   []string
	TaskID    int
	Start     time.Time
	End       time.Time
	MaxPoints int
}

type PingQueryResult struct {
	Records           []models.PingRecord
	SampleCounts      []int64
	ValidCounts       []int64
	LossCounts        []int64
	SumValues         []int64
	MinValues         []int
	MaxValues         []int
	LastValues        []int
	LastTimes         []models.LocalTime
	ResolutionSeconds int
	RowsScanned       int
	Truncated         bool
}

func validatePingQuery(query PingQuery) error {
	if query.Start.IsZero() || query.End.IsZero() || !query.End.After(query.Start) {
		return errors.New("invalid Ping query range")
	}
	if query.MaxPoints <= 0 || query.MaxPoints > MaxPingQueryPoints {
		return fmt.Errorf("Ping point budget must be between 1 and %d", MaxPingQueryPoints)
	}
	return nil
}

func pingSeriesIntervals(index *pingAssignmentIndex, query PingQuery) []int {
	intervals := make([]int, 0)
	if query.Client != "" {
		for _, task := range index.tasksByClient[query.Client] {
			if query.TaskID >= 0 && task.Id != uint(query.TaskID) {
				continue
			}
			intervals = append(intervals, max(task.Interval, 1))
		}
		return intervals
	}
	if query.Clients != nil {
		for _, client := range query.Clients {
			for _, task := range index.tasksByClient[client] {
				if query.TaskID >= 0 && task.Id != uint(query.TaskID) {
					continue
				}
				intervals = append(intervals, max(task.Interval, 1))
			}
		}
		return intervals
	}
	for assignment := range index.assignments {
		if query.TaskID >= 0 && assignment.taskID != uint(query.TaskID) {
			continue
		}
		task := index.tasksByID[assignment.taskID]
		intervals = append(intervals, max(task.Interval, 1))
	}
	return intervals
}

func estimatedBuckets(window time.Duration, seconds int) int64 {
	if window <= 0 {
		return 0
	}
	nanoseconds := int64(time.Duration(seconds) * time.Second)
	return max((window.Nanoseconds()+nanoseconds-1)/nanoseconds, 1)
}

func choosePingResolution(index *pingAssignmentIndex, query PingQuery) int {
	intervals := pingSeriesIntervals(index, query)
	if len(intervals) == 0 {
		return 0
	}
	var rawEstimate int64
	for _, interval := range intervals {
		rawEstimate += estimatedBuckets(query.End.Sub(query.Start), interval)
	}
	if rawEstimate <= int64(query.MaxPoints) {
		return 0
	}
	for _, resolution := range []int{60, 900, 3600} {
		estimate := estimatedBuckets(query.End.Sub(query.Start), resolution) * int64(len(intervals))
		if estimate <= int64(query.MaxPoints) {
			return resolution
		}
	}
	return 3600
}

// QueryPingSeries selects the narrowest storage tier that can satisfy the
// caller's total point budget. LIMIT is applied in SQL, so adversarial ranges
// cannot first materialize millions of rows and downsample them afterward.
func QueryPingSeries(ctx context.Context, db *gorm.DB, query PingQuery) (PingQueryResult, error) {
	result := PingQueryResult{Records: []models.PingRecord{}}
	if ctx == nil || db == nil {
		return result, errors.New("Ping query context and database are required")
	}
	if err := validatePingQuery(query); err != nil {
		return result, err
	}
	if query.Client != "" && query.Clients != nil {
		return result, errors.New("Ping query cannot combine client and clients")
	}
	if query.Clients != nil && len(query.Clients) == 0 {
		return result, nil
	}
	index, err := loadPingAssignmentIndex()
	if err != nil {
		return result, err
	}
	result.ResolutionSeconds = choosePingResolution(index, query)
	if result.ResolutionSeconds > 0 && !metrics.RollupsReady(ctx, db) {
		result.ResolutionSeconds = 0
	}
	limit := query.MaxPoints + 1
	seriesCount := max(len(pingSeriesIntervals(index, query)), 1)
	perSeriesLimit := max(query.MaxPoints/seriesCount, 1)

	if result.ResolutionSeconds == 0 {
		request := db.WithContext(ctx).Model(&models.PingRecord{}).
			Joins("INNER JOIN ping_tasks ON ping_tasks.id = ping_records.task_id").
			Where("ping_records.time >= ? AND ping_records.time <= ?", models.FromTime(query.Start), models.FromTime(query.End))
		if query.Client != "" {
			request = request.Where("ping_records.client = ?", query.Client)
		} else if query.Clients != nil {
			request = request.Where("ping_records.client IN ?", query.Clients)
		}
		if query.TaskID >= 0 {
			request = request.Where("ping_records.task_id = ?", uint(query.TaskID))
		}
		type rankedPingRecord struct {
			models.PingRecord
			SeriesRow int `gorm:"column:series_row"`
		}
		ranked := request.Select("ping_records.client,ping_records.task_id,ping_records.time,ping_records.value,ROW_NUMBER() OVER (PARTITION BY ping_records.client,ping_records.task_id ORDER BY ping_records.time DESC) AS series_row")
		var rows []rankedPingRecord
		if err := db.WithContext(ctx).Table("(?) AS ranked", ranked).
			Select("client,task_id,time,value,series_row").
			Where("series_row <= ?", perSeriesLimit).
			Order("time DESC,client ASC,task_id ASC").Limit(limit).Scan(&rows).Error; err != nil {
			return result, err
		}
		result.Records = make([]models.PingRecord, 0, len(rows))
		for _, row := range rows {
			result.Records = append(result.Records, row.PingRecord)
		}
		for _, record := range result.Records {
			result.SampleCounts = append(result.SampleCounts, 1)
			if record.Value < 0 {
				result.ValidCounts = append(result.ValidCounts, 0)
				result.LossCounts = append(result.LossCounts, 1)
				result.MinValues = append(result.MinValues, 0)
				result.MaxValues = append(result.MaxValues, 0)
				result.SumValues = append(result.SumValues, 0)
				result.LastValues = append(result.LastValues, record.Value)
				result.LastTimes = append(result.LastTimes, record.Time)
			} else {
				result.ValidCounts = append(result.ValidCounts, 1)
				result.LossCounts = append(result.LossCounts, 0)
				result.MinValues = append(result.MinValues, record.Value)
				result.MaxValues = append(result.MaxValues, record.Value)
				result.SumValues = append(result.SumValues, int64(record.Value))
				result.LastValues = append(result.LastValues, record.Value)
				result.LastTimes = append(result.LastTimes, record.Time)
			}
		}
	} else {
		type rollupProjection struct {
			Client      string
			TaskId      uint
			BucketTime  models.LocalTime
			SampleCount int64
			ValidCount  int64
			LossCount   int64
			SumValue    int64
			MinValue    int
			MaxValue    int
			LastValue   int
			LastTime    models.LocalTime
			SeriesRow   int
		}
		var rows []rollupProjection
		request := db.WithContext(ctx).Model(&models.PingRollup{}).
			Joins("INNER JOIN ping_tasks ON ping_tasks.id = ping_rollups.task_id").
			Where("ping_rollups.resolution_seconds = ? AND ping_rollups.bucket_time >= ? AND ping_rollups.bucket_time <= ?", result.ResolutionSeconds, models.FromTime(query.Start), models.FromTime(query.End))
		if query.Client != "" {
			request = request.Where("ping_rollups.client = ?", query.Client)
		} else if query.Clients != nil {
			request = request.Where("ping_rollups.client IN ?", query.Clients)
		}
		if query.TaskID >= 0 {
			request = request.Where("ping_rollups.task_id = ?", uint(query.TaskID))
		}
		ranked := request.Select("ping_rollups.client,ping_rollups.task_id,ping_rollups.bucket_time,ping_rollups.sample_count,ping_rollups.valid_count,ping_rollups.loss_count,ping_rollups.sum_value,ping_rollups.min_value,ping_rollups.max_value,ping_rollups.last_value,ping_rollups.last_time,ROW_NUMBER() OVER (PARTITION BY ping_rollups.client,ping_rollups.task_id ORDER BY ping_rollups.bucket_time DESC) AS series_row")
		if err := db.WithContext(ctx).Table("(?) AS ranked", ranked).
			Select("client,task_id,bucket_time,sample_count,valid_count,loss_count,sum_value,min_value,max_value,last_value,last_time,series_row").
			Where("series_row <= ?", perSeriesLimit).
			Order("bucket_time DESC,client ASC,task_id ASC").Limit(limit).Scan(&rows).Error; err != nil {
			return result, err
		}
		result.Records = make([]models.PingRecord, 0, len(rows))
		for _, row := range rows {
			value := -1
			if row.ValidCount > 0 {
				value = int(row.SumValue / row.ValidCount)
			}
			result.Records = append(result.Records, models.PingRecord{Client: row.Client, TaskId: row.TaskId, Time: row.BucketTime, Value: value})
			result.SampleCounts = append(result.SampleCounts, row.SampleCount)
			result.ValidCounts = append(result.ValidCounts, row.ValidCount)
			result.LossCounts = append(result.LossCounts, row.LossCount)
			result.SumValues = append(result.SumValues, row.SumValue)
			result.MinValues = append(result.MinValues, row.MinValue)
			result.MaxValues = append(result.MaxValues, row.MaxValue)
			result.LastValues = append(result.LastValues, row.LastValue)
			result.LastTimes = append(result.LastTimes, row.LastTime)
		}
	}

	result.RowsScanned = len(result.Records)
	if len(result.Records) > query.MaxPoints {
		result.Truncated = true
		result.Records = result.Records[:query.MaxPoints]
		result.SampleCounts = result.SampleCounts[:query.MaxPoints]
		result.ValidCounts = result.ValidCounts[:query.MaxPoints]
		result.LossCounts = result.LossCounts[:query.MaxPoints]
		result.SumValues = result.SumValues[:query.MaxPoints]
		result.MinValues = result.MinValues[:query.MaxPoints]
		result.MaxValues = result.MaxValues[:query.MaxPoints]
		result.LastValues = result.LastValues[:query.MaxPoints]
		result.LastTimes = result.LastTimes[:query.MaxPoints]
	}
	return result, ctx.Err()
}
