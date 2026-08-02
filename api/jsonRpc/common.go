package jsonRpc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/metrics"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/utils/rpc"
	"github.com/komari-monitor/komari/ws"

	cache "github.com/patrickmn/go-cache"
	"gorm.io/gorm"
)

// pingstats:<uuid>
var pingStatsCache = cache.New(1*time.Minute, 2*time.Minute)

type pingStat struct {
	Name   string  `json:"name"`
	Total  int64   `json:"total"`
	Lost   int64   `json:"lost"`
	Latest int     `json:"latest"`
	Avg    int     `json:"avg"`
	Tail   float64 `json:"tail"` // (P99-P50)/P50
	Loss   float64 `json:"loss"` // 丢包率 %
	Min    int     `json:"min"`
	Max    int     `json:"max"`
}

// getPingStatsForNode 计算并缓存节点最近 1 小时 ping 统计
func getPingStatsForNode(uuid string, pingTasks []models.PingTask) map[string]pingStat {
	if uuid == "" {
		return map[string]pingStat{}
	}
	stats := getPingStatsForNodes([]string{uuid}, pingTasks)[uuid]
	if stats == nil {
		return map[string]pingStat{}
	}
	return stats
}

// getPingStatsForNodes resolves all cache misses with one Ping query instead
// of issuing one query for every dashboard node.
func getPingStatsForNodes(uuids []string, pingTasks []models.PingTask) map[string]map[string]pingStat {
	results, _, _ := getPingStatsForNodesAt(context.Background(), dbcore.GetDBInstance(), uuids, pingTasks, time.Now())
	return results
}

func getPingStatsForNodesAt(ctx context.Context, db *gorm.DB, uuids []string, pingTasks []models.PingTask, end time.Time) (map[string]map[string]pingStat, int, error) {
	results := make(map[string]map[string]pingStat, len(uuids))
	assignedByClient := make(map[string][]models.PingTask, len(uuids))
	queryClients := make([]string, 0, len(uuids))
	seen := make(map[string]struct{}, len(uuids))
	for _, uuid := range uuids {
		if uuid == "" {
			continue
		}
		if _, ok := seen[uuid]; ok {
			continue
		}
		seen[uuid] = struct{}{}
		key := fmt.Sprintf("pingstats:%s", uuid)
		if value, ok := pingStatsCache.Get(key); ok {
			if cached, valid := value.(map[string]pingStat); valid {
				results[uuid] = cached
				continue
			}
		}
		for _, task := range pingTasks {
			if task.AppliesToClient(uuid) {
				assignedByClient[uuid] = append(assignedByClient[uuid], task)
			}
		}
		if len(assignedByClient[uuid]) == 0 {
			empty := map[string]pingStat{}
			results[uuid] = empty
			pingStatsCache.Set(key, empty, cache.DefaultExpiration)
			continue
		}
		queryClients = append(queryClients, uuid)
	}
	if len(queryClients) == 0 {
		return results, 0, nil
	}
	type aggregateRow struct {
		Client      string
		TaskID      uint `gorm:"column:task_id"`
		SampleCount int64
		LossCount   int64
		Average     int
		Minimum     int
		Maximum     int
		Latest      int
	}
	var rows []aggregateRow
	start := end.Add(-time.Hour)
	var err error
	if metrics.RollupsReady(ctx, db) {
		err = db.WithContext(ctx).Table("ping_rollups AS pr").
			Select(`pr.client, pr.task_id,
			SUM(pr.sample_count) AS sample_count,
			SUM(pr.loss_count) AS loss_count,
			COALESCE(CAST(SUM(pr.sum_value) / NULLIF(SUM(pr.valid_count), 0) AS INTEGER), 0) AS average,
			COALESCE(MIN(CASE WHEN pr.valid_count > 0 THEN pr.min_value END), 0) AS minimum,
			COALESCE(MAX(CASE WHEN pr.valid_count > 0 THEN pr.max_value END), 0) AS maximum,
			COALESCE((SELECT latest.value FROM ping_records AS latest
				WHERE latest.client = pr.client AND latest.task_id = pr.task_id
				AND latest.value >= 0 AND latest.time >= ? AND latest.time <= ?
				ORDER BY latest.time DESC LIMIT 1), -1) AS latest`, models.FromTime(start), models.FromTime(end)).
			Where("pr.resolution_seconds = ?", 60).
			Where("pr.client IN ?", queryClients).
			Where("pr.bucket_time >= ? AND pr.bucket_time <= ?", models.FromTime(start.Truncate(time.Minute)), models.FromTime(end)).
			Group("pr.client, pr.task_id").
			Scan(&rows).Error
	} else {
		err = db.WithContext(ctx).Table("ping_records AS pr").
			Select(`pr.client, pr.task_id,
			COUNT(*) AS sample_count,
			SUM(CASE WHEN pr.value < 0 THEN 1 ELSE 0 END) AS loss_count,
			COALESCE(CAST(AVG(CASE WHEN pr.value >= 0 THEN pr.value END) AS INTEGER), 0) AS average,
			COALESCE(MIN(CASE WHEN pr.value >= 0 THEN pr.value END), 0) AS minimum,
			COALESCE(MAX(CASE WHEN pr.value >= 0 THEN pr.value END), 0) AS maximum,
			COALESCE((SELECT latest.value FROM ping_records AS latest
				WHERE latest.client = pr.client AND latest.task_id = pr.task_id
				AND latest.value >= 0 AND latest.time >= ? AND latest.time <= ?
				ORDER BY latest.time DESC LIMIT 1), -1) AS latest`, models.FromTime(start), models.FromTime(end)).
			Where("pr.client IN ?", queryClients).
			Where("pr.time >= ? AND pr.time <= ?", models.FromTime(start), models.FromTime(end)).
			Group("pr.client, pr.task_id").
			Scan(&rows).Error
	}
	if err != nil {
		// Do not turn a storage failure into a cached empty result: callers that
		// can report errors must distinguish an outage from a real no-data window.
		return results, 1, err
	}
	assignedIDs := make(map[string]map[uint]struct{}, len(queryClients))
	taskNames := make(map[uint]string, len(pingTasks))
	for _, task := range pingTasks {
		taskNames[task.Id] = task.Name
	}
	for _, uuid := range queryClients {
		assignedIDs[uuid] = make(map[uint]struct{}, len(assignedByClient[uuid]))
		for _, task := range assignedByClient[uuid] {
			assignedIDs[uuid][task.Id] = struct{}{}
		}
	}
	for _, row := range rows {
		if _, ok := assignedIDs[row.Client][row.TaskID]; !ok {
			continue
		}
		if results[row.Client] == nil {
			results[row.Client] = make(map[string]pingStat)
		}
		loss := 0.0
		if row.SampleCount > 0 {
			loss = float64(row.LossCount) / float64(row.SampleCount) * 100
		}
		tail := 0.0
		if row.Average > 0 && row.Maximum >= row.Average {
			tail = float64(row.Maximum-row.Average) / float64(row.Average)
		}
		results[row.Client][fmt.Sprintf("%d", row.TaskID)] = pingStat{
			Name: taskNames[row.TaskID], Total: row.SampleCount, Lost: row.LossCount,
			Latest: row.Latest, Avg: row.Average,
			Tail: tail, Loss: loss, Min: row.Minimum, Max: row.Maximum,
		}
	}
	for _, uuid := range queryClients {
		if results[uuid] == nil {
			results[uuid] = map[string]pingStat{}
		}
		pingStatsCache.Set(fmt.Sprintf("pingstats:%s", uuid), results[uuid], cache.DefaultExpiration)
	}
	return results, 1, nil
}

func summarizeNodePingStats(assigned []models.PingTask, grouped map[uint][]models.PingRecord) map[string]pingStat {
	result := make(map[string]pingStat, len(grouped))
	for _, t := range assigned {
		records := grouped[t.Id]
		if len(records) == 0 {
			continue
		}
		latest := -1
		var latestTs time.Time
		values := make([]int, 0, len(records))
		sum := 0
		valid := 0
		total := 0
		lossCount := 0
		minLat := 0
		maxLat := 0
		for _, r := range records {
			total++
			if r.Value < 0 { // 丢包
				lossCount++
				continue
			}
			values = append(values, r.Value)
			sum += r.Value
			valid++
			if minLat == 0 || r.Value < minLat {
				minLat = r.Value
			}
			if r.Value > maxLat {
				maxLat = r.Value
			}
			ts := r.Time.ToTime()
			if latestTs.IsZero() || ts.After(latestTs) {
				latestTs = ts
				latest = r.Value
			}
		}
		avg := 0
		if valid > 0 {
			avg = sum / valid
		}
		p50, p99 := 0, 0
		if len(values) > 0 {
			sort.Ints(values)
			percentile := func(vals []int, pct float64) int {
				if len(vals) == 0 {
					return 0
				}
				if pct <= 0 {
					return vals[0]
				}
				if pct >= 1 {
					return vals[len(vals)-1]
				}
				pos := (float64(len(vals) - 1)) * pct
				lo := int(math.Floor(pos))
				hi := int(math.Ceil(pos))
				if lo == hi {
					return vals[lo]
				}
				frac := pos - float64(lo)
				v := float64(vals[lo]) + (float64(vals[hi])-float64(vals[lo]))*frac
				return int(math.Round(v))
			}
			p50 = percentile(values, 0.50)
			p99 = percentile(values, 0.99)
		}
		tail := 0.0
		if p50 > 0 && p99 >= p50 {
			tail = float64(p99-p50) / float64(p50)
		}
		lossRate := 0.0
		if total > 0 {
			lossRate = float64(lossCount) / float64(total) * 100
		}
		result[fmt.Sprintf("%d", t.Id)] = pingStat{
			Name:   t.Name,
			Latest: latest,
			Avg:    avg,
			Tail:   tail,
			Loss:   lossRate,
			Min:    minLat,
			Max:    maxLat,
		}
	}
	return result
}

func init() {
	RegisterWithGroupAndMeta("getNodes", "common",
		func(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
			return getNodes(ctx, req)
		},
		&rpc.MethodMeta{
			Name:    "getNodes",
			Summary: "Get all nodes",
			Params: []rpc.ParamMeta{
				{
					Name:        "uuid",
					Description: "Specify the UUID of the node",
					Required:    false,
					Type:        "string",
				},
			},
			Returns: "Client | { [uuid]: Client }",
		},
	)
	RegisterWithGroupAndMeta("getNodesLatestStatus", "common",
		func(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
			return getNodesLatestStatus(ctx, req)
		},
		&rpc.MethodMeta{
			Name:    "getNodesLatestStatus",
			Summary: "Get latest status reports (single node or map).",
			Params: []rpc.ParamMeta{
				{
					Name:        "uuid",
					Description: "Specify the UUID of the node (optional)",
					Required:    false,
					Type:        "string",
				},
				{
					Name:        "uuids",
					Description: "Specify multiple UUIDs (array) to get subset (ignored if uuid provided)",
					Required:    false,
					Type:        "string[]",
				},
			},
			Returns: "Record | { [uuid]: Record }",
		},
	)
	Register("getMe", func(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
		return getMe(ctx, req)
	})
	Register("getPublicInfo", getPublicInfo)
	Register("getVersion", getVersion)
	Register("getNodeRecentStatus", getNodeRecentStatus)
}

func getNodes(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID string `json:"uuid"`
	}
	req.BindParams(&params)
	cinfo, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get client info", cinfo)
	}
	meta := rpc.MetaFromContext(ctx)

	SendIpAddrToGuest, _ := config.GetAs[bool](config.SendIpAddrToGuestKey)
	if meta.Permission != "admin" {
		// 过滤 Hidden 节点并隐藏敏感字段
		filtered := make([]models.Client, 0, len(cinfo))
		for _, node := range cinfo {
			if node.Hidden { // 非 admin 不显示隐藏节点
				continue
			}
			if SendIpAddrToGuest {
				if node.IPv4 != "" {
					node.IPv4 = strings.Split(node.IPv4, ".")[0] + ".*.*.*"
				}
				if node.IPv6 != "" {
					node.IPv6 = strings.Split(node.IPv6, ":")[0] + ":*:*:*:*:*:*:*"
				}
			} else {
				node.IPv4 = ""
				node.IPv6 = ""
			}

			node.Remark = ""
			node.Version = ""
			node.Token = ""
			filtered = append(filtered, node)
		}
		cinfo = filtered
	}
	if params.UUID != "" {
		for _, node := range cinfo {
			if node.UUID == params.UUID {
				return node, nil
			}
		}
		return nil, rpc.MakeError(rpc.InvalidParams, "Node not found", params.UUID)
	}

	nodesMap := make(map[string]models.Client, len(cinfo))
	for _, node := range cinfo {
		nodesMap[node.UUID] = node
	}
	return nodesMap, nil
}

func getPublicInfo(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	info, err := database.GetPublicInfo()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get public info", err.Error())
	}
	return info, nil
}

func getNodesLatestStatus(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID  string   `json:"uuid"`
		UUIDs []string `json:"uuids"`
	}
	req.BindParams(&params)

	meta := rpc.MetaFromContext(ctx)
	latest := ws.GetLatestReport() // map[string]*common.Report (copy)
	onlineUUIDs := ws.GetAllOnlineUUIDs()
	onlineSet := make(map[string]bool, len(onlineUUIDs))
	for _, uuid := range onlineUUIDs {
		onlineSet[uuid] = true
	}

	// Hidden 过滤
	if meta.Permission != "admin" {
		cinfo, err := clients.GetAllClientBasicInfo()
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to get client info", err.Error())
		}
		hidden := make(map[string]bool, len(cinfo))
		for _, c := range cinfo {
			if c.Hidden {
				hidden[c.UUID] = true
			}
		}
		for uuid := range latest {
			if hidden[uuid] {
				delete(latest, uuid)
			}
		}
	}

	// 如果指定 uuid 但找不到，直接返回 not found
	if params.UUID != "" {
		if _, ok := latest[params.UUID]; !ok {
			return nil, rpc.MakeError(rpc.InvalidParams, "Node not found", params.UUID)
		}
	}

	type recordLike struct {
		Client         string              `json:"client"`
		Time           models.LocalTime    `json:"time"`
		Cpu            float32             `json:"cpu"`
		Gpu            float32             `json:"gpu"`
		Ram            int64               `json:"ram"`
		RamTotal       int64               `json:"ram_total"`
		Swap           int64               `json:"swap"`
		SwapTotal      int64               `json:"swap_total"`
		Load           float32             `json:"load"`
		Load5          float32             `json:"load5"`
		Load15         float32             `json:"load15"`
		Temp           float32             `json:"temp"`
		Disk           int64               `json:"disk"`
		DiskTotal      int64               `json:"disk_total"`
		NetIn          int64               `json:"net_in"`
		NetOut         int64               `json:"net_out"`
		NetTotalUp     int64               `json:"net_total_up"`
		NetTotalDown   int64               `json:"net_total_down"`
		Process        int                 `json:"process"`
		Connections    int                 `json:"connections"`
		ConnectionsUdp int                 `json:"connections_udp"`
		Online         bool                `json:"online"`
		Uptime         int64               `json:"uptime"`
		Ping           map[string]pingStat `json:"ping"`
	}

	respMap := make(map[string]recordLike, len(latest))

	// 预取所有 ping 任务
	pingTasks, _ := tasks.GetAllPingTasks()
	pingClientIDs := make([]string, 0, len(latest))
	for uuid := range latest {
		pingClientIDs = append(pingClientIDs, uuid)
	}
	pingStatsByClient := getPingStatsForNodes(pingClientIDs, pingTasks)

	appendOne := func(uuid string, rep *common.Report) {
		if rep == nil {
			return
		}
		stats := pingStatsByClient[uuid]
		rl := recordLike{
			Client:         uuid,
			Time:           models.FromTime(rep.UpdatedAt),
			Cpu:            float32(rep.CPU.Usage),
			Gpu:            0,
			Ram:            rep.Ram.Used,
			RamTotal:       rep.Ram.Total,
			Swap:           rep.Swap.Used,
			SwapTotal:      rep.Swap.Total,
			Load:           float32(rep.Load.Load1),
			Load5:          float32(rep.Load.Load5),
			Load15:         float32(rep.Load.Load15),
			Temp:           0,
			Disk:           rep.Disk.Used,
			DiskTotal:      rep.Disk.Total,
			NetIn:          rep.Network.Down,
			NetOut:         rep.Network.Up,
			NetTotalUp:     rep.Network.TotalUp,
			NetTotalDown:   rep.Network.TotalDown,
			Process:        rep.Process,
			Connections:    rep.Connections.TCP + rep.Connections.UDP,
			ConnectionsUdp: rep.Connections.UDP,
			Online:         onlineSet[uuid],
			Uptime:         rep.Uptime,
			Ping:           stats,
		}
		respMap[uuid] = rl
	}

	// 选择逻辑
	if params.UUID != "" { // 单个
		appendOne(params.UUID, latest[params.UUID])
		return respMap[params.UUID], nil
	}
	selected := map[string]bool{}
	if len(params.UUIDs) > 0 {
		for _, id := range params.UUIDs {
			selected[id] = true
		}
		for uuid, rep := range latest {
			if selected[uuid] {
				appendOne(uuid, rep)
			}
		}
		return respMap, nil
	}
	for uuid, rep := range latest {
		appendOne(uuid, rep)
	}
	return respMap, nil
}

func getMe(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var resp struct {
		TwoFAEnabled bool   `json:"2fa_enabled"`
		LoggedIn     bool   `json:"logged_in"`
		SSOId        string `json:"sso_id"`
		SSOType      string `json:"sso_type"`
		Username     string `json:"username"`
		UUID         string `json:"uuid"`
	}

	meta := rpc.MetaFromContext(ctx)

	switch meta.Permission {
	case "admin":
		resp.TwoFAEnabled = meta.User.TwoFactor != ""
		resp.LoggedIn = true
		resp.SSOId = meta.User.SSOID
		resp.SSOType = meta.User.SSOType
		resp.Username = meta.User.Username
		resp.UUID = meta.User.UUID
		return resp, nil
	case "guest":
		resp.LoggedIn = false
		return resp, nil
	case "client":
		resp.LoggedIn = true
		resp.SSOId = "client"
		resp.SSOType = "client"
		resp.Username = "client"
		resp.UUID = meta.ClientToken
		client, err := clients.GetClientUUIDByToken(meta.ClientToken)
		if err != nil {
			resp.UUID = client
		}
		return resp, nil
	default:
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid user role", meta.Permission)
	}
}

func getVersion(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return struct {
		Version string `json:"version"`
		Hash    string `json:"hash"`
	}{
		Version: utils.CurrentVersion,
		Hash:    utils.VersionHash,
	}, nil
}

func getNodeRecentStatus(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID string `json:"uuid"`
	}
	req.BindParams(&params)
	if params.UUID == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "UUID is required", params)
	}
	meta := rpc.MetaFromContext(ctx)
	// 登录状态检查
	isLogin := false
	if meta.Permission == "admin" {
		isLogin = true
	}

	// 仅在未登录时需要 Hidden 信息做过滤
	hiddenMap := map[string]bool{}
	if !isLogin {
		var hiddenClients []models.Client
		db := dbcore.GetDBInstance()
		_ = db.Select("uuid").Where("hidden = ?", true).Find(&hiddenClients).Error
		for _, cli := range hiddenClients {
			hiddenMap[cli.UUID] = true
		}

		if hiddenMap[params.UUID] {
			return nil, rpc.MakeError(rpc.InvalidParams, "UUID is required", params) //防止未登录用户获取隐藏客户端数据
		}
	}

	reports := api.Telemetry.Recent(params.UUID)

	// 扁平化为 { count, records: [] }
	type flatRecord struct {
		Client         string           `json:"client"`
		Time           models.LocalTime `json:"time"`
		Cpu            float32          `json:"cpu"`
		Gpu            float32          `json:"gpu"`
		Ram            int64            `json:"ram"`
		RamTotal       int64            `json:"ram_total"`
		Swap           int64            `json:"swap"`
		SwapTotal      int64            `json:"swap_total"`
		Load           float32          `json:"load"`
		Temp           float32          `json:"temp"`
		Disk           int64            `json:"disk"`
		DiskTotal      int64            `json:"disk_total"`
		NetIn          int64            `json:"net_in"`
		NetOut         int64            `json:"net_out"`
		NetTotalUp     int64            `json:"net_total_up"`
		NetTotalDown   int64            `json:"net_total_down"`
		Process        int              `json:"process"`
		Connections    int              `json:"connections"`
		ConnectionsUdp int              `json:"connections_udp"`
	}

	resp := struct {
		Count   int          `json:"count"`
		Records []flatRecord `json:"records"`
	}{
		Count:   0,
		Records: []flatRecord{},
	}

	if len(reports) == 0 {
		return resp, nil
	}

	resp.Records = make([]flatRecord, 0, len(reports))
	for _, r := range reports {
		fr := flatRecord{
			Client:         params.UUID,
			Time:           models.FromTime(r.UpdatedAt),
			Cpu:            float32(r.CPU.Usage),
			Gpu:            0,
			Ram:            r.Ram.Used,
			RamTotal:       r.Ram.Total,
			Swap:           r.Swap.Used,
			SwapTotal:      r.Swap.Total,
			Load:           float32(r.Load.Load1),
			Temp:           0,
			Disk:           r.Disk.Used,
			DiskTotal:      r.Disk.Total,
			NetIn:          r.Network.Down,
			NetOut:         r.Network.Up,
			NetTotalUp:     r.Network.TotalUp,
			NetTotalDown:   r.Network.TotalDown,
			Process:        r.Process,
			Connections:    r.Connections.TCP + r.Connections.UDP,
			ConnectionsUdp: r.Connections.UDP,
		}
		resp.Records = append(resp.Records, fr)
	}
	resp.Count = len(resp.Records)
	return resp, nil
}
