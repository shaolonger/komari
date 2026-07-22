package notifier

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	recordsdb "github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/database/tasks"
	"gorm.io/gorm"
)

type FleetReportData struct {
	Kind            string               `json:"kind"`
	Cadence         string               `json:"cadence"`
	CadenceLabel    string               `json:"cadence_label"`
	Timezone        string               `json:"timezone"`
	PeriodStart     string               `json:"period_start"`
	PeriodEnd       string               `json:"period_end"`
	PeriodLabel     string               `json:"period_label"`
	GeneratedAt     string               `json:"generated_at"`
	TopN            int                  `json:"top_n"`
	Summary         FleetReportSummary   `json:"summary"`
	Rankings        []FleetReportRanking `json:"rankings"`
	Anomalies       []FleetReportAnomaly `json:"anomalies"`
	Recommendations []string             `json:"recommendations"`
}

type FleetReportSummary struct {
	TotalNodes        int     `json:"total_nodes"`
	ReportNodes       int     `json:"report_nodes"`
	NoDataNodes       int     `json:"no_data_nodes"`
	AnomalyNodes      int     `json:"anomaly_nodes"`
	CriticalAnomalies int     `json:"critical_anomalies"`
	WarningAnomalies  int     `json:"warning_anomalies"`
	HealthScore       int     `json:"health_score"`
	DataCoverage      float64 `json:"data_coverage"`
	TotalTraffic      int64   `json:"total_traffic"`
	TotalTrafficText  string  `json:"total_traffic_text"`
	AvgPingP95        float64 `json:"avg_ping_p95"`
	AvgPingLoss       float64 `json:"avg_ping_loss"`
}

type FleetReportRanking struct {
	Key       string                `json:"key"`
	Title     string                `json:"title"`
	Unit      string                `json:"unit"`
	Direction string                `json:"direction"`
	Items     []FleetReportRankItem `json:"items"`
}

type FleetReportRankItem struct {
	Rank         int     `json:"rank"`
	UUID         string  `json:"uuid"`
	Name         string  `json:"name"`
	Group        string  `json:"group,omitempty"`
	Value        float64 `json:"value"`
	DisplayValue string  `json:"display_value"`
	Percent      float64 `json:"percent"`
	Bar          string  `json:"bar"`
	Detail       string  `json:"detail,omitempty"`
}

type FleetReportAnomaly struct {
	Severity string `json:"severity"`
	UUID     string `json:"uuid,omitempty"`
	Name     string `json:"name,omitempty"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

type fleetNodeMetrics struct {
	client            models.Client
	samples           int
	coverage          float64
	cpuAvg            float64
	cpuP95            float64
	cpuMax            float64
	memoryAvg         float64
	memoryMax         float64
	diskMax           float64
	loadP95           float64
	loadPressure      float64
	traffic           recordsdb.TrafficStats
	trafficLimitUsed  int64
	trafficLimitUsage float64
	pingSamples       int
	pingAvg           float64
	pingP95           float64
	pingMax           float64
	pingLoss          float64
	anomalies         []FleetReportAnomaly
	criticalAnomaly   bool
	warningAnomaly    bool
}

type fleetReportInputs struct {
	recordsByClient map[string][]models.Record
	pingByClient    map[string][]models.PingRecord
	SQLQueries      int
}

func buildFleetOperationsReport(cadence trafficReportCadence, now time.Time, loc *time.Location, topN int) (FleetReportData, string, []models.Client, error) {
	start, end := trafficReportWindow(cadence, now, loc)
	allClients, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return FleetReportData{}, "", nil, err
	}

	inputs, err := queryFleetReportInputs(context.Background(), dbcore.GetDBInstance(), allClients, start, end)
	if err != nil {
		return FleetReportData{}, "", nil, err
	}

	data := buildFleetReportData(allClients, inputs.recordsByClient, inputs.pingByClient, cadence, start, end, now, loc, topN)
	return data, buildFleetReportText(data), allClients, nil
}

func queryFleetReportInputs(ctx context.Context, db *gorm.DB, allClients []models.Client, start, end time.Time) (fleetReportInputs, error) {
	inputs := fleetReportInputs{
		recordsByClient: make(map[string][]models.Record, len(allClients)),
		pingByClient:    make(map[string][]models.PingRecord, len(allClients)),
	}
	clientIDs := make([]string, 0, len(allClients))
	for _, client := range allClients {
		if client.UUID != "" {
			clientIDs = append(clientIDs, client.UUID)
		}
	}
	recordResult, err := recordsdb.QueryRecordsForClients(ctx, db, clientIDs, start, end, "all")
	if err != nil {
		return inputs, err
	}
	inputs.SQLQueries += recordResult.SQLQueries
	for _, record := range recordResult.Records {
		inputs.recordsByClient[record.Client] = append(inputs.recordsByClient[record.Client], record)
	}
	pingResult, err := tasks.QueryPingRecordsForClients(ctx, db, clientIDs, -1, start, end)
	if err != nil {
		return inputs, err
	}
	inputs.SQLQueries += pingResult.SQLQueries
	for _, record := range pingResult.Records {
		inputs.pingByClient[record.Client] = append(inputs.pingByClient[record.Client], record)
	}
	return inputs, nil
}

func buildFleetReportEvent(data FleetReportData, message string, allClients []models.Client, generatedAt time.Time, loc *time.Location) models.EventMessage {
	return models.EventMessage{
		Event:    messageevent.FleetReport,
		Clients:  allClients,
		Time:     generatedAt,
		Emoji:    "📊",
		Message:  message,
		Timezone: loc.String(),
		Data:     data,
	}
}

func buildFleetReportData(allClients []models.Client, recordsByClient map[string][]models.Record, pingByClient map[string][]models.PingRecord, cadence trafficReportCadence, start, end, generatedAt time.Time, loc *time.Location, topN int) FleetReportData {
	topN = normalizeFleetReportTopN(topN)
	metrics := make([]fleetNodeMetrics, 0, len(allClients))
	for _, client := range allClients {
		node := summarizeFleetNode(client, recordsByClient[client.UUID], pingByClient[client.UUID], start, end)
		metrics = append(metrics, node)
	}

	summary, anomalies := summarizeFleetReport(metrics)
	data := FleetReportData{
		Kind:            "fleet_report",
		Cadence:         string(cadence),
		CadenceLabel:    trafficReportCadenceLabel(cadence),
		Timezone:        loc.String(),
		PeriodStart:     formatTrafficReportTime(start, loc),
		PeriodEnd:       formatTrafficReportTime(end, loc),
		PeriodLabel:     formatFleetReportPeriodLabel(cadence, start, end, loc),
		GeneratedAt:     formatTrafficReportTime(generatedAt, loc),
		TopN:            topN,
		Summary:         summary,
		Rankings:        buildFleetReportRankings(metrics, topN),
		Anomalies:       anomalies,
		Recommendations: buildFleetReportRecommendations(metrics, summary),
	}
	return data
}

func summarizeFleetNode(client models.Client, recs []models.Record, pingRecords []models.PingRecord, start, end time.Time) fleetNodeMetrics {
	node := fleetNodeMetrics{
		client:   client,
		samples:  len(recs),
		coverage: estimateRecordCoverage(recs, start, end),
	}
	node.traffic = recordsdb.SummarizeTrafficRecords(recs, start, end, 0)
	if node.traffic.Coverage > node.coverage {
		node.coverage = node.traffic.Coverage
	}

	cpuValues := make([]float64, 0, len(recs))
	memValues := make([]float64, 0, len(recs))
	diskValues := make([]float64, 0, len(recs))
	loadValues := make([]float64, 0, len(recs))
	for _, rec := range recs {
		cpuValues = append(cpuValues, float64(rec.Cpu))
		if memPercent := percentFromBytes(rec.Ram, firstPositiveInt64(rec.RamTotal, client.MemTotal)); memPercent >= 0 {
			memValues = append(memValues, memPercent)
		}
		if diskPercent := percentFromBytes(rec.Disk, firstPositiveInt64(rec.DiskTotal, client.DiskTotal)); diskPercent >= 0 {
			diskValues = append(diskValues, diskPercent)
		}
		loadValues = append(loadValues, float64(rec.Load))
	}

	node.cpuAvg = averageFloat64(cpuValues)
	node.cpuP95 = percentileFloat64(cpuValues, 0.95)
	node.cpuMax = maxFloat64(cpuValues)
	node.memoryAvg = averageFloat64(memValues)
	node.memoryMax = maxFloat64(memValues)
	node.diskMax = maxFloat64(diskValues)
	node.loadP95 = percentileFloat64(loadValues, 0.95)
	if client.CpuCores > 0 {
		node.loadPressure = node.loadP95 / float64(client.CpuCores) * 100
	}

	if client.TrafficLimit > 0 {
		node.trafficLimitUsed = computeUsedByType(strings.ToLower(client.TrafficLimitType), node.traffic.Up, node.traffic.Down)
		node.trafficLimitUsage = float64(node.trafficLimitUsed) / float64(client.TrafficLimit) * 100
	}

	node.pingSamples, node.pingAvg, node.pingP95, node.pingMax, node.pingLoss = summarizeFleetPing(pingRecords)
	node.anomalies = detectFleetNodeAnomalies(node)
	for _, anomaly := range node.anomalies {
		switch anomaly.Severity {
		case "critical":
			node.criticalAnomaly = true
		case "warning":
			node.warningAnomaly = true
		}
	}
	return node
}

func summarizeFleetReport(metrics []fleetNodeMetrics) (FleetReportSummary, []FleetReportAnomaly) {
	summary := FleetReportSummary{
		TotalNodes: len(metrics),
	}
	anomalies := make([]FleetReportAnomaly, 0)
	totalCoverage := 0.0
	nodesWithPing := 0
	for _, node := range metrics {
		if node.samples > 0 {
			summary.ReportNodes++
		} else {
			summary.NoDataNodes++
		}
		totalCoverage += node.coverage
		summary.TotalTraffic += node.traffic.Total
		if node.criticalAnomaly || node.warningAnomaly {
			summary.AnomalyNodes++
		}
		for _, anomaly := range node.anomalies {
			anomalies = append(anomalies, anomaly)
			if anomaly.Severity == "critical" {
				summary.CriticalAnomalies++
			} else if anomaly.Severity == "warning" {
				summary.WarningAnomalies++
			}
		}
		if node.pingSamples > 0 {
			summary.AvgPingP95 += node.pingP95
			summary.AvgPingLoss += node.pingLoss
			nodesWithPing++
		}
	}
	if len(metrics) > 0 {
		summary.DataCoverage = totalCoverage / float64(len(metrics)) * 100
	}
	if nodesWithPing > 0 {
		summary.AvgPingP95 /= float64(nodesWithPing)
		summary.AvgPingLoss /= float64(nodesWithPing)
	}
	summary.HealthScore = computeFleetHealthScore(summary)
	summary.TotalTrafficText = humanBytes(summary.TotalTraffic)
	sortFleetAnomalies(anomalies)
	return summary, anomalies
}

func detectFleetNodeAnomalies(node fleetNodeMetrics) []FleetReportAnomaly {
	anomalies := make([]FleetReportAnomaly, 0)
	add := func(severity, title, detail string) {
		anomalies = append(anomalies, FleetReportAnomaly{
			Severity: severity,
			UUID:     node.client.UUID,
			Name:     fleetClientName(node.client),
			Title:    title,
			Detail:   detail,
		})
	}
	if node.samples == 0 {
		add("critical", "无监控样本", "该周期没有系统指标样本，需检查 Agent 在线状态或历史记录配置。")
		return anomalies
	}
	if node.coverage < 0.5 {
		add("warning", "样本覆盖率偏低", "覆盖率 "+formatPercent(node.coverage*100)+"，报告可信度有限。")
	}
	if node.cpuP95 >= 85 {
		add("warning", "CPU P95 偏高", "P95 "+formatPercent(node.cpuP95)+"，建议检查持续高负载进程。")
	}
	if node.memoryMax >= 90 {
		add("critical", "内存峰值过高", "峰值 "+formatPercent(node.memoryMax)+"，有 OOM 或抖动风险。")
	} else if node.memoryMax >= 80 {
		add("warning", "内存使用偏高", "峰值 "+formatPercent(node.memoryMax)+"。")
	}
	if node.diskMax >= 90 {
		add("critical", "磁盘空间紧张", "峰值 "+formatPercent(node.diskMax)+"，建议尽快清理或扩容。")
	} else if node.diskMax >= 80 {
		add("warning", "磁盘使用偏高", "峰值 "+formatPercent(node.diskMax)+"。")
	}
	if node.loadPressure >= 150 {
		add("warning", "负载压力偏高", "P95 负载约为核心数的 "+formatPercent(node.loadPressure)+"。")
	}
	if node.trafficLimitUsage >= 90 {
		add("critical", "流量额度接近用尽", "已用 "+formatPercent(node.trafficLimitUsage)+"，本周期 "+humanBytes(node.trafficLimitUsed)+"。")
	} else if node.trafficLimitUsage >= 75 {
		add("warning", "流量额度偏高", "已用 "+formatPercent(node.trafficLimitUsage)+"。")
	}
	if node.pingSamples > 0 {
		if node.pingLoss >= 5 {
			add("critical", "Ping 丢包明显", "丢包率 "+formatPercent(node.pingLoss)+"。")
		} else if node.pingLoss >= 1 {
			add("warning", "Ping 有轻微丢包", "丢包率 "+formatPercent(node.pingLoss)+"。")
		}
		if node.pingP95 >= 250 {
			add("warning", "Ping P95 延迟偏高", "P95 "+fmt.Sprintf("%.0f ms", node.pingP95)+"。")
		}
	}
	return anomalies
}

func buildFleetReportRankings(metrics []fleetNodeMetrics, topN int) []FleetReportRanking {
	return []FleetReportRanking{
		makeFleetRanking("cpu_pressure", "CPU 压力 Top", "%", "higher_worse", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			return node.cpuP95, "平均 " + formatPercent(node.cpuAvg)
		}),
		makeFleetRanking("memory_pressure", "内存压力 Top", "%", "higher_worse", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			return node.memoryMax, "平均 " + formatPercent(node.memoryAvg)
		}),
		makeFleetRanking("disk_risk", "磁盘风险 Top", "%", "higher_worse", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			return node.diskMax, "磁盘峰值"
		}),
		makeFleetRanking("load_pressure", "负载压力 Top", "%", "higher_worse", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			return node.loadPressure, "P95 负载 " + fmt.Sprintf("%.2f", node.loadP95)
		}),
		makeFleetRanking("traffic_usage", "流量消耗 Top", "bytes", "higher_watch", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			return float64(node.traffic.Total), "上行 " + humanBytes(node.traffic.Up) + " / 下行 " + humanBytes(node.traffic.Down)
		}),
		makeFleetRanking("quota_usage", "流量额度 Top", "%", "higher_worse", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			if node.client.TrafficLimit <= 0 {
				return 0, "未设置额度"
			}
			return node.trafficLimitUsage, humanBytes(node.trafficLimitUsed) + " / " + humanBytes(node.client.TrafficLimit)
		}),
		makeFleetRanking("ping_latency", "Ping P95 Top", "ms", "higher_worse", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			return node.pingP95, "平均 " + fmt.Sprintf("%.0f ms", node.pingAvg)
		}),
		makeFleetRanking("ping_loss", "Ping 丢包 Top", "%", "higher_worse", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			return node.pingLoss, fmt.Sprintf("%d 个样本", node.pingSamples)
		}),
		makeFleetRanking("data_coverage", "数据覆盖不足 Top", "%", "lower_worse", metrics, topN, func(node fleetNodeMetrics) (float64, string) {
			return 100 - node.coverage*100, "覆盖率 " + formatPercent(node.coverage*100)
		}),
	}
}

func makeFleetRanking(key, title, unit, direction string, metrics []fleetNodeMetrics, topN int, valueFn func(fleetNodeMetrics) (float64, string)) FleetReportRanking {
	items := make([]FleetReportRankItem, 0, len(metrics))
	maxValue := 0.0
	for _, node := range metrics {
		value, detail := valueFn(node)
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			value = 0
		}
		if value > maxValue {
			maxValue = value
		}
		items = append(items, FleetReportRankItem{
			UUID:         node.client.UUID,
			Name:         fleetClientName(node.client),
			Group:        node.client.Group,
			Value:        value,
			DisplayValue: formatFleetRankValue(value, unit),
			Detail:       detail,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Value == items[j].Value {
			return items[i].Name < items[j].Name
		}
		return items[i].Value > items[j].Value
	})
	if topN > len(items) {
		topN = len(items)
	}
	items = items[:topN]
	if maxValue <= 0 {
		maxValue = 1
	}
	for i := range items {
		items[i].Rank = i + 1
		items[i].Percent = math.Min(100, items[i].Value/maxValue*100)
		items[i].Bar = textProgressBar(items[i].Percent, 10)
	}
	return FleetReportRanking{
		Key:       key,
		Title:     title,
		Unit:      unit,
		Direction: direction,
		Items:     items,
	}
}

func buildFleetReportRecommendations(metrics []fleetNodeMetrics, summary FleetReportSummary) []string {
	recommendations := make([]string, 0, 4)
	if summary.CriticalAnomalies > 0 {
		recommendations = append(recommendations, "优先处理红色异常节点，尤其是无监控样本、磁盘空间紧张和流量额度接近用尽。")
	}
	if summary.DataCoverage < 80 {
		recommendations = append(recommendations, "本周期数据覆盖率偏低，建议先确认 Agent 在线率、记录保留周期和任务调度是否正常。")
	}
	if summary.AvgPingLoss >= 1 || summary.AvgPingP95 >= 200 {
		recommendations = append(recommendations, "网络质量存在波动，建议在对比页按地区或线路进一步查看 Ping 任务趋势。")
	}
	if len(recommendations) == 0 && len(metrics) > 0 {
		recommendations = append(recommendations, "整体运行平稳，可继续观察流量额度、磁盘水位和高延迟节点的长期趋势。")
	}
	if len(metrics) == 0 {
		recommendations = append(recommendations, "当前没有可统计的 VPS 节点，请先添加节点或确认权限范围。")
	}
	return recommendations
}

func buildFleetReportText(data FleetReportData) string {
	lines := []string{
		"全局运维报告",
		"周期: " + data.CadenceLabel,
		"时区: " + data.Timezone,
		"时间范围: " + data.PeriodLabel,
		"健康分: " + fmt.Sprintf("%d/100 %s", data.Summary.HealthScore, textProgressBar(float64(data.Summary.HealthScore), 12)),
		"节点: " + fmt.Sprintf("%d 台 / 有数据 %d 台 / 无数据 %d 台", data.Summary.TotalNodes, data.Summary.ReportNodes, data.Summary.NoDataNodes),
		"异常: " + fmt.Sprintf("%d 台需关注，严重 %d，警告 %d", data.Summary.AnomalyNodes, data.Summary.CriticalAnomalies, data.Summary.WarningAnomalies),
		"数据覆盖: " + formatPercent(data.Summary.DataCoverage),
		"总流量: " + data.Summary.TotalTrafficText,
	}
	if data.Summary.AvgPingP95 > 0 || data.Summary.AvgPingLoss > 0 {
		lines = append(lines, "Ping: P95 均值 "+fmt.Sprintf("%.0f ms", data.Summary.AvgPingP95)+" / 丢包均值 "+formatPercent(data.Summary.AvgPingLoss))
	}
	for _, ranking := range data.Rankings {
		if len(ranking.Items) == 0 {
			continue
		}
		lines = append(lines, "", ranking.Title)
		for _, item := range ranking.Items {
			lines = append(lines, fmt.Sprintf("#%d %s %s %s", item.Rank, item.Name, item.DisplayValue, item.Bar))
		}
	}
	if len(data.Anomalies) > 0 {
		lines = append(lines, "", "异常摘要")
		limit := len(data.Anomalies)
		if limit > data.TopN {
			limit = data.TopN
		}
		for i := 0; i < limit; i++ {
			anomaly := data.Anomalies[i]
			lines = append(lines, fleetSeverityLabel(anomaly.Severity)+" "+anomaly.Name+" - "+anomaly.Title+": "+anomaly.Detail)
		}
	}
	if len(data.Recommendations) > 0 {
		lines = append(lines, "", "建议动作")
		for _, recommendation := range data.Recommendations {
			lines = append(lines, "- "+recommendation)
		}
	}
	return strings.Join(lines, "\n")
}

func summarizeFleetPing(records []models.PingRecord) (int, float64, float64, float64, float64) {
	if len(records) == 0 {
		return 0, 0, 0, 0, 0
	}
	values := make([]float64, 0, len(records))
	loss := 0
	for _, record := range records {
		if record.Value < 0 {
			loss++
			continue
		}
		values = append(values, float64(record.Value))
	}
	lossRate := float64(loss) / float64(len(records)) * 100
	return len(records), averageFloat64(values), percentileFloat64(values, 0.95), maxFloat64(values), lossRate
}

func estimateRecordCoverage(recs []models.Record, start, end time.Time) float64 {
	if len(recs) == 0 || !end.After(start) {
		return 0
	}
	times := make([]time.Time, 0, len(recs))
	for _, rec := range recs {
		ts := rec.Time.ToTime()
		if !ts.Before(start) && !ts.After(end) {
			times = append(times, ts)
		}
	}
	if len(times) == 0 {
		return 0
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	maxGap := 30 * time.Minute
	covered := 0.0
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		if gap <= 0 {
			continue
		}
		if gap > maxGap {
			gap = maxGap
		}
		covered += gap.Seconds()
	}
	if len(times) == 1 {
		covered = maxGap.Seconds()
	}
	return math.Min(1, covered/end.Sub(start).Seconds())
}

func computeFleetHealthScore(summary FleetReportSummary) int {
	score := 100
	score -= summary.CriticalAnomalies * 12
	score -= summary.WarningAnomalies * 5
	if summary.DataCoverage < 80 {
		score -= int(math.Round((80 - summary.DataCoverage) / 2))
	}
	if summary.NoDataNodes > 0 {
		score -= summary.NoDataNodes * 4
	}
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func sortFleetAnomalies(anomalies []FleetReportAnomaly) {
	weight := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(anomalies, func(i, j int) bool {
		left := weight[anomalies[i].Severity]
		right := weight[anomalies[j].Severity]
		if left == right {
			return anomalies[i].Name < anomalies[j].Name
		}
		return left < right
	})
}

func normalizeFleetReportTopN(topN int) int {
	if topN <= 0 {
		return 5
	}
	if topN > 20 {
		return 20
	}
	return topN
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func percentFromBytes(used, total int64) float64 {
	if total <= 0 {
		return -1
	}
	return float64(used) / float64(total) * 100
}

func averageFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func maxFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	maximum := values[0]
	for _, value := range values[1:] {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func percentileFloat64(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 1 {
		return sorted[len(sorted)-1]
	}
	pos := float64(len(sorted)-1) * percentile
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo] + (sorted[hi]-sorted[lo])*frac
}

func formatFleetRankValue(value float64, unit string) string {
	switch unit {
	case "bytes":
		return humanBytes(int64(math.Round(value)))
	case "ms":
		return fmt.Sprintf("%.0f ms", value)
	case "%":
		return formatPercent(value)
	default:
		return fmt.Sprintf("%.1f %s", value, unit)
	}
}

func formatFleetReportPeriodLabel(cadence trafficReportCadence, start, end time.Time, loc *time.Location) string {
	switch cadence {
	case trafficReportDaily:
		return start.In(loc).Format("2006-01-02")
	default:
		return formatTrafficReportTime(start, loc) + " - " + formatTrafficReportTime(end, loc)
	}
}

func textProgressBar(percent float64, width int) string {
	if width <= 0 {
		width = 10
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(math.Round(percent / 100 * float64(width)))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func fleetClientName(client models.Client) string {
	if strings.TrimSpace(client.Name) != "" {
		return strings.TrimSpace(client.Name)
	}
	return client.UUID
}

func fleetSeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "[严重]"
	case "warning":
		return "[警告]"
	default:
		return "[提示]"
	}
}
