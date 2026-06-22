package public

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	recordsdb "github.com/komari-monitor/komari/database/records"
)

type trafficRangeResponse struct {
	From              string                    `json:"from"`
	To                string                    `json:"to"`
	Timezone          string                    `json:"timezone"`
	GroupBy           string                    `json:"group_by"`
	BucketSizeSeconds int64                     `json:"bucket_size_seconds"`
	Summary           trafficSummary            `json:"summary"`
	Nodes             []trafficNodeSummary      `json:"nodes"`
	Series            []recordsdb.TrafficBucket `json:"series,omitempty"`
}

type trafficSummary struct {
	Up        int64   `json:"up"`
	Down      int64   `json:"down"`
	Total     int64   `json:"total"`
	AvgBps    float64 `json:"avg_bps"`
	PeakBps   float64 `json:"peak_bps"`
	Nodes     int     `json:"nodes"`
	Samples   int     `json:"samples"`
	Coverage  float64 `json:"coverage"`
	Resets    int     `json:"resets"`
	Estimated bool    `json:"estimated"`
	Quality   string  `json:"quality"`
}

type trafficNodeSummary struct {
	UUID        string                    `json:"uuid"`
	Name        string                    `json:"name"`
	Region      string                    `json:"region,omitempty"`
	Group       string                    `json:"group,omitempty"`
	Up          int64                     `json:"up"`
	Down        int64                     `json:"down"`
	Total       int64                     `json:"total"`
	AvgBps      float64                   `json:"avg_bps"`
	PeakBps     float64                   `json:"peak_bps"`
	Samples     int                       `json:"samples"`
	Coverage    float64                   `json:"coverage"`
	Resets      int                       `json:"resets"`
	Estimated   bool                      `json:"estimated"`
	Quality     string                    `json:"quality"`
	FirstSample string                    `json:"first_sample,omitempty"`
	LastSample  string                    `json:"last_sample,omitempty"`
	Series      []recordsdb.TrafficBucket `json:"series,omitempty"`
}

func GetTrafficRange(c *gin.Context) {
	loc, err := parseTrafficLocation(c.Query("timezone"))
	if err != nil {
		api.RespondError(c, 400, "Invalid timezone parameter")
		return
	}
	start, end, err := parseTrafficRange(c, loc)
	if err != nil {
		api.RespondError(c, 400, err.Error())
		return
	}

	clientList, err := clients.GetAllClientBasicInfo()
	if err != nil {
		api.RespondError(c, 500, "Failed to retrieve client information: "+err.Error())
		return
	}

	visibleClients := filterTrafficClients(clientList, requestedTrafficUUIDs(c), trafficIsLoggedIn(c))
	bucketSize, groupBy := trafficBucketSize(c.Query("group_by"), start, end)
	includeNodeSeries := parseBoolQuery(c.Query("include_node_series"))

	response := trafficRangeResponse{
		From:              start.In(loc).Format(time.RFC3339),
		To:                end.In(loc).Format(time.RFC3339),
		Timezone:          loc.String(),
		GroupBy:           groupBy,
		BucketSizeSeconds: int64(bucketSize.Seconds()),
		Series:            makeTrafficSeries(start, end, bucketSize, loc),
	}

	for _, client := range visibleClients {
		// Include one hour before the range so the first in-range sample can be
		// compared with a nearby baseline counter.
		nodeRecords, err := recordsdb.GetRecordsByClientAndTime(client.UUID, start.Add(-time.Hour), end)
		if err != nil {
			api.RespondError(c, 500, "Failed to fetch traffic records: "+err.Error())
			return
		}
		stats := recordsdb.SummarizeTrafficRecords(nodeRecords, start, end, bucketSize)
		nodeSummary := trafficNodeSummary{
			UUID:        client.UUID,
			Name:        client.Name,
			Region:      client.Region,
			Group:       client.Group,
			Up:          stats.Up,
			Down:        stats.Down,
			Total:       stats.Total,
			AvgBps:      stats.AvgBps,
			PeakBps:     stats.PeakBps,
			Samples:     stats.Samples,
			Coverage:    stats.Coverage,
			Resets:      stats.Resets,
			Estimated:   stats.Estimated,
			Quality:     stats.Quality,
			FirstSample: stats.FirstSample,
			LastSample:  stats.LastSample,
		}
		if includeNodeSeries {
			nodeSummary.Series = stats.Series
		}
		response.Nodes = append(response.Nodes, nodeSummary)
		mergeTrafficSummary(&response.Summary, stats)
		mergeTrafficSeries(response.Series, stats.Series)
	}

	response.Summary.Nodes = len(visibleClients)
	response.Summary.Total = response.Summary.Up + response.Summary.Down
	if response.Summary.Nodes > 0 {
		response.Summary.Coverage = response.Summary.Coverage / float64(response.Summary.Nodes)
	}
	durationSeconds := end.Sub(start).Seconds()
	if durationSeconds > 0 {
		response.Summary.AvgBps = float64(response.Summary.Total) / durationSeconds
	}
	response.Summary.Quality = summarizeTrafficQuality(response.Summary)
	api.RespondSuccess(c, response)
}

func parseTrafficLocation(name string) (*time.Location, error) {
	if strings.TrimSpace(name) == "" {
		return models.GetAppLocation(), nil
	}
	return time.LoadLocation(name)
}

func parseTrafficRange(c *gin.Context, loc *time.Location) (time.Time, time.Time, error) {
	now := time.Now().In(loc)
	start := time.Time{}
	end := now
	var err error

	switch strings.ToLower(strings.TrimSpace(c.Query("preset"))) {
	case "", "today":
		y, m, d := now.Date()
		start = time.Date(y, m, d, 0, 0, 0, 0, loc)
	case "3d", "72h":
		start = now.Add(-72 * time.Hour)
	case "7d", "168h":
		start = now.Add(-168 * time.Hour)
	default:
		start = now.Add(-24 * time.Hour)
	}

	if fromParam := strings.TrimSpace(c.Query("from")); fromParam != "" {
		start, err = parseTrafficTime(fromParam, loc)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if toParam := strings.TrimSpace(c.Query("to")); toParam != "" {
		end, err = parseTrafficTime(toParam, loc)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, errInvalidTrafficRange()
	}
	return start, end, nil
}

func parseTrafficTime(value string, loc *time.Location) (time.Time, error) {
	if ts, err := strconv.ParseInt(value, 10, 64); err == nil {
		if ts > 1_000_000_000_000 {
			return time.UnixMilli(ts).In(loc), nil
		}
		return time.Unix(ts, 0).In(loc), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.In(loc), nil
	}
	return time.ParseInLocation("2006-01-02 15:04", value, loc)
}

type trafficRangeError string

func (e trafficRangeError) Error() string { return string(e) }

func errInvalidTrafficRange() error {
	return trafficRangeError("Invalid time range")
}

func requestedTrafficUUIDs(c *gin.Context) map[string]struct{} {
	raw := strings.TrimSpace(c.Query("uuids"))
	if raw == "" {
		raw = strings.TrimSpace(c.Query("uuid"))
	}
	if raw == "" {
		return nil
	}
	requested := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		uuid := strings.TrimSpace(part)
		if uuid != "" {
			requested[uuid] = struct{}{}
		}
	}
	return requested
}

func trafficIsLoggedIn(c *gin.Context) bool {
	session, _ := c.Cookie("session_token")
	_, err := accounts.GetUserBySession(session)
	return err == nil
}

func filterTrafficClients(clientList []models.Client, requested map[string]struct{}, isLogin bool) []models.Client {
	visible := make([]models.Client, 0, len(clientList))
	for _, client := range clientList {
		if client.Hidden && !isLogin {
			continue
		}
		if requested != nil {
			if _, ok := requested[client.UUID]; !ok {
				continue
			}
		}
		visible = append(visible, client)
	}
	return visible
}

func trafficBucketSize(groupBy string, start, end time.Time) (time.Duration, string) {
	group := strings.ToLower(strings.TrimSpace(groupBy))
	if group == "" || group == "auto" {
		if end.Sub(start) <= 48*time.Hour {
			return time.Hour, "hour"
		}
		return 24 * time.Hour, "day"
	}
	switch group {
	case "hour":
		return time.Hour, "hour"
	case "day":
		return 24 * time.Hour, "day"
	case "none":
		return 0, "none"
	default:
		return time.Hour, "hour"
	}
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func makeTrafficSeries(start, end time.Time, bucketSize time.Duration, loc *time.Location) []recordsdb.TrafficBucket {
	series := recordsdb.SummarizeTrafficRecords(nil, start, end, bucketSize).Series
	for i := range series {
		if t, err := time.Parse(time.RFC3339, series[i].Time); err == nil {
			series[i].Time = t.In(loc).Format(time.RFC3339)
		}
	}
	return series
}

func mergeTrafficSummary(summary *trafficSummary, stats recordsdb.TrafficStats) {
	summary.Up += stats.Up
	summary.Down += stats.Down
	summary.Samples += stats.Samples
	summary.Resets += stats.Resets
	if stats.Estimated {
		summary.Estimated = true
	}
	if stats.PeakBps > summary.PeakBps {
		summary.PeakBps = stats.PeakBps
	}
	summary.Coverage += stats.Coverage
}

func mergeTrafficSeries(target []recordsdb.TrafficBucket, source []recordsdb.TrafficBucket) {
	for i := range target {
		if i >= len(source) {
			break
		}
		target[i].Up += source[i].Up
		target[i].Down += source[i].Down
		target[i].Total = target[i].Up + target[i].Down
	}
}

func summarizeTrafficQuality(summary trafficSummary) string {
	if summary.Nodes == 0 || summary.Samples == 0 {
		return "empty"
	}
	if summary.Coverage < 0.8 || summary.Resets > 0 {
		return "partial"
	}
	if summary.Estimated {
		return "estimated"
	}
	return "exact"
}
