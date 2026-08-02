package records

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

var (
	ErrInvalidQueryRange = errors.New("invalid query range")
	ErrQueryWindow       = errors.New("query window exceeds limit")
	ErrQueryPointBudget  = errors.New("query point budget exceeds limit")
	ErrQueryNodeBudget   = errors.New("query node budget exceeds limit")
	ErrInvalidLoadType   = errors.New("invalid load_type")
)

type QueryPermission uint8

const (
	QueryPermissionPublic QueryPermission = iota
	QueryPermissionAdmin
)

type QueryBudget struct {
	MaxWindow     time.Duration
	DefaultPoints int
	MaxPoints     int
	MaxNodes      int
}

var publicQueryBudget = QueryBudget{
	MaxWindow: 366 * 24 * time.Hour, DefaultPoints: 4_000, MaxPoints: 20_000, MaxNodes: 10_000,
}

var adminQueryBudget = QueryBudget{
	MaxWindow: 10 * 366 * 24 * time.Hour, DefaultPoints: 20_000, MaxPoints: 100_000, MaxNodes: 100_000,
}

func BudgetForPermission(permission QueryPermission) QueryBudget {
	if permission == QueryPermissionAdmin {
		return adminQueryBudget
	}
	return publicQueryBudget
}

// ValidateQueryBudget validates the range and returns the effective total
// point limit. A legacy -1 request is bounded by the permission hard limit.
func ValidateQueryBudget(start, end time.Time, nodes, requestedPoints int, permission QueryPermission) (int, error) {
	budget := BudgetForPermission(permission)
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return 0, ErrInvalidQueryRange
	}
	if end.Sub(start) > budget.MaxWindow {
		return 0, ErrQueryWindow
	}
	if nodes < 0 || nodes > budget.MaxNodes {
		return 0, ErrQueryNodeBudget
	}
	if requestedPoints == 0 {
		return budget.DefaultPoints, nil
	}
	if requestedPoints == -1 {
		return budget.MaxPoints, nil
	}
	if requestedPoints < 1 || requestedPoints > budget.MaxPoints {
		return 0, ErrQueryPointBudget
	}
	return requestedPoints, nil
}

var recordProjections = map[string]string{
	"cpu":         "client,time,cpu",
	"gpu":         "client,time,gpu",
	"ram":         "client,time,ram,ram_total",
	"swap":        "client,time,swap,swap_total",
	"load":        "client,time,load",
	"temp":        "client,time,temp",
	"disk":        "client,time,disk,disk_total",
	"network":     "client,time,net_in,net_out,net_total_up,net_total_down",
	"process":     "client,time,process",
	"connections": "client,time,connections,connections_udp",
}

func RecordProjection(loadType string) (string, error) {
	loadType = strings.ToLower(strings.TrimSpace(loadType))
	if loadType == "" || loadType == "all" {
		return "*", nil
	}
	projection, ok := recordProjections[loadType]
	if !ok {
		return "", ErrInvalidLoadType
	}
	return projection, nil
}

func ValidLoadType(loadType string) bool {
	_, err := RecordProjection(loadType)
	return err == nil
}

// DownsampleRecords applies Largest-Triangle-Three-Buckets to a sorted series.
// It always keeps the first/last point and uses the requested metric for peak
// salience. The result has exactly maxPoints entries when 2 <= maxPoints < n.
func DownsampleRecords(input []models.Record, maxPoints int, loadType string) []models.Record {
	if maxPoints <= 0 || len(input) == 0 {
		return []models.Record{}
	}
	if maxPoints >= len(input) {
		return input
	}
	if maxPoints == 1 {
		return []models.Record{input[len(input)-1]}
	}
	if maxPoints == 2 {
		return []models.Record{input[0], input[len(input)-1]}
	}

	threshold := maxPoints
	every := float64(len(input)-2) / float64(threshold-2)
	result := make([]models.Record, 0, threshold)
	result = append(result, input[0])
	anchor := 0
	for bucket := 0; bucket < threshold-2; bucket++ {
		avgStart := int(math.Floor(float64(bucket+1)*every)) + 1
		avgEnd := int(math.Floor(float64(bucket+2)*every)) + 1
		if avgEnd > len(input) {
			avgEnd = len(input)
		}
		if avgStart >= avgEnd {
			avgStart = min(avgStart, len(input)-1)
			avgEnd = min(avgStart+1, len(input))
		}
		var avgX, avgY float64
		for index := avgStart; index < avgEnd; index++ {
			avgX += float64(input[index].Time.ToTime().UnixNano())
			avgY += recordMetric(input[index], loadType)
		}
		averageCount := float64(avgEnd - avgStart)
		avgX /= averageCount
		avgY /= averageCount

		rangeStart := int(math.Floor(float64(bucket)*every)) + 1
		rangeEnd := int(math.Floor(float64(bucket+1)*every)) + 1
		if rangeEnd > len(input)-1 {
			rangeEnd = len(input) - 1
		}
		anchorX := float64(input[anchor].Time.ToTime().UnixNano())
		anchorY := recordMetric(input[anchor], loadType)
		selected := rangeStart
		maxArea := -1.0
		for index := rangeStart; index < rangeEnd; index++ {
			pointX := float64(input[index].Time.ToTime().UnixNano())
			pointY := recordMetric(input[index], loadType)
			area := math.Abs((anchorX-avgX)*(pointY-anchorY)-(anchorX-pointX)*(avgY-anchorY)) * 0.5
			if area > maxArea {
				maxArea = area
				selected = index
			}
		}
		result = append(result, input[selected])
		anchor = selected
	}
	result = append(result, input[len(input)-1])
	return result
}

func recordMetric(record models.Record, loadType string) float64 {
	switch strings.ToLower(loadType) {
	case "gpu":
		return float64(record.Gpu)
	case "ram":
		if record.RamTotal > 0 {
			return float64(record.Ram) / float64(record.RamTotal)
		}
		return float64(record.Ram)
	case "swap":
		if record.SwapTotal > 0 {
			return float64(record.Swap) / float64(record.SwapTotal)
		}
		return float64(record.Swap)
	case "load":
		return float64(record.Load)
	case "temp":
		return float64(record.Temp)
	case "disk":
		if record.DiskTotal > 0 {
			return float64(record.Disk) / float64(record.DiskTotal)
		}
		return float64(record.Disk)
	case "network":
		return float64(record.NetIn + record.NetOut)
	case "process":
		return float64(record.Process)
	case "connections":
		return float64(record.Connections)
	default:
		return float64(record.Cpu)
	}
}

// DownsampleGPURecords applies the same total point budget across devices and
// keeps each device series independent so one noisy GPU cannot hide another.
func DownsampleGPURecords(input []models.GPURecord, maxPoints int) []models.GPURecord {
	if maxPoints <= 0 || len(input) == 0 {
		return []models.GPURecord{}
	}
	if len(input) <= maxPoints {
		return input
	}
	groups := make(map[int][]models.GPURecord)
	keys := make([]int, 0)
	for _, record := range input {
		if _, exists := groups[record.DeviceIndex]; !exists {
			keys = append(keys, record.DeviceIndex)
		}
		groups[record.DeviceIndex] = append(groups[record.DeviceIndex], record)
	}
	sort.Ints(keys)
	remaining := maxPoints
	remainingRows := len(input)
	output := make([]models.GPURecord, 0, maxPoints)
	for index, key := range keys {
		series := groups[key]
		sort.Slice(series, func(i, j int) bool { return series[i].Time.ToTime().Before(series[j].Time.ToTime()) })
		target := 0
		if remaining > 0 {
			target = max(1, int(math.Round(float64(len(series))*float64(remaining)/float64(remainingRows))))
			target = min(target, remaining, len(series))
			if index == len(keys)-1 {
				target = min(remaining, len(series))
			}
		}
		output = append(output, downsampleGPUDevice(series, target)...)
		remaining -= target
		remainingRows -= len(series)
	}
	sort.Slice(output, func(i, j int) bool {
		if !output[i].Time.ToTime().Equal(output[j].Time.ToTime()) {
			return output[i].Time.ToTime().Before(output[j].Time.ToTime())
		}
		return output[i].DeviceIndex < output[j].DeviceIndex
	})
	return output
}

func downsampleGPUDevice(input []models.GPURecord, maxPoints int) []models.GPURecord {
	if maxPoints <= 0 || len(input) == 0 {
		return nil
	}
	if maxPoints >= len(input) {
		return input
	}
	if maxPoints == 1 {
		return []models.GPURecord{input[len(input)-1]}
	}
	if maxPoints == 2 {
		return []models.GPURecord{input[0], input[len(input)-1]}
	}
	every := float64(len(input)-2) / float64(maxPoints-2)
	result := make([]models.GPURecord, 0, maxPoints)
	result = append(result, input[0])
	anchor := 0
	for bucket := 0; bucket < maxPoints-2; bucket++ {
		avgStart := int(math.Floor(float64(bucket+1)*every)) + 1
		avgEnd := min(int(math.Floor(float64(bucket+2)*every))+1, len(input))
		if avgStart >= avgEnd {
			avgStart = min(avgStart, len(input)-1)
			avgEnd = min(avgStart+1, len(input))
		}
		var avgX, avgY float64
		for index := avgStart; index < avgEnd; index++ {
			avgX += float64(input[index].Time.ToTime().UnixNano())
			avgY += float64(input[index].Utilization)
		}
		count := float64(avgEnd - avgStart)
		avgX, avgY = avgX/count, avgY/count
		rangeStart := int(math.Floor(float64(bucket)*every)) + 1
		rangeEnd := min(int(math.Floor(float64(bucket+1)*every))+1, len(input)-1)
		anchorX := float64(input[anchor].Time.ToTime().UnixNano())
		anchorY := float64(input[anchor].Utilization)
		selected, maxArea := rangeStart, -1.0
		for index := rangeStart; index < rangeEnd; index++ {
			pointX := float64(input[index].Time.ToTime().UnixNano())
			pointY := float64(input[index].Utilization)
			area := math.Abs((anchorX-avgX)*(pointY-anchorY)-(anchorX-pointX)*(avgY-anchorY)) * 0.5
			if area > maxArea {
				selected, maxArea = index, area
			}
		}
		result = append(result, input[selected])
		anchor = selected
	}
	return append(result, input[len(input)-1])
}
