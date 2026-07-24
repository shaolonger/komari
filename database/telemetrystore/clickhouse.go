//go:build scale

// Package telemetrystore contains scale-oriented telemetry adapters.
package telemetrystore

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/storage"
)

const (
	defaultClickHouseMaxPoints = 100_000
	maxClickHouseBatchRows     = 100_000
)

var tablePrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

type ClickHouseConfig struct {
	Addresses            []string
	Database             string
	Username             string
	Password             string
	TablePrefix          string
	RequireTLS           bool
	TLSConfig            *tls.Config
	DialTimeout          time.Duration
	ReadTimeout          time.Duration
	MaxOpenConns         int
	MaxIdleConns         int
	ConnMaxLifetime      time.Duration
	MaxRetries           int
	RetryBackoff         time.Duration
	FreeBufOnConnRelease bool
}

type ClickHouse struct {
	conn         driver.Conn
	tablePrefix  string
	maxRetries   int
	retryBackoff time.Duration
}

var (
	_ storage.TelemetryStore    = (*ClickHouse)(nil)
	_ storage.TelemetryMigrator = (*ClickHouse)(nil)
)

func NewClickHouse(ctx context.Context, config ClickHouseConfig) (*ClickHouse, error) {
	options, prefix, retries, backoff, err := clickHouseOptions(config)
	if err != nil {
		return nil, err
	}
	conn, err := clickhouse.Open(options)
	if err != nil {
		return nil, errors.New("open ClickHouse telemetry pool")
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping ClickHouse telemetry store: %w", err)
	}
	return &ClickHouse{conn: conn, tablePrefix: prefix, maxRetries: retries, retryBackoff: backoff}, nil
}

func clickHouseOptions(config ClickHouseConfig) (*clickhouse.Options, string, int, time.Duration, error) {
	if len(config.Addresses) == 0 {
		return nil, "", 0, 0, errors.New("at least one ClickHouse address is required")
	}
	for _, address := range config.Addresses {
		if strings.TrimSpace(address) == "" || strings.ContainsAny(address, "/?#") {
			return nil, "", 0, 0, errors.New("ClickHouse addresses must be host:port values")
		}
	}
	if config.Database == "" {
		config.Database = "default"
	}
	if config.TablePrefix == "" {
		config.TablePrefix = "komari_"
	}
	if !tablePrefixPattern.MatchString(config.TablePrefix) {
		return nil, "", 0, 0, errors.New("ClickHouse table prefix must match [a-z][a-z0-9_]{0,31}")
	}
	if config.RequireTLS && config.TLSConfig == nil {
		return nil, "", 0, 0, errors.New("ClickHouse TLS is required but no TLS configuration was supplied")
	}
	if config.TLSConfig != nil {
		config.TLSConfig = config.TLSConfig.Clone()
		if config.TLSConfig.MinVersion < tls.VersionTLS12 {
			config.TLSConfig.MinVersion = tls.VersionTLS12
		}
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = 10 * time.Second
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = 30 * time.Second
	}
	if config.MaxOpenConns == 0 {
		config.MaxOpenConns = 32
	}
	if config.MaxOpenConns < 1 || config.MaxOpenConns > 256 {
		return nil, "", 0, 0, errors.New("ClickHouse max connections must be between 1 and 256")
	}
	if config.MaxIdleConns == 0 {
		config.MaxIdleConns = min(8, config.MaxOpenConns)
	}
	if config.MaxIdleConns < 0 || config.MaxIdleConns > config.MaxOpenConns {
		return nil, "", 0, 0, errors.New("ClickHouse idle connections must be between zero and max connections")
	}
	if config.ConnMaxLifetime <= 0 {
		config.ConnMaxLifetime = time.Hour
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 4
	}
	if config.MaxRetries < 0 || config.MaxRetries > 10 {
		return nil, "", 0, 0, errors.New("ClickHouse retries must be between zero and 10")
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = 25 * time.Millisecond
	}
	options := &clickhouse.Options{
		Addr: config.Addresses,
		Auth: clickhouse.Auth{
			Database: config.Database,
			Username: config.Username,
			Password: config.Password,
		},
		TLS:                  config.TLSConfig,
		DialTimeout:          config.DialTimeout,
		ReadTimeout:          config.ReadTimeout,
		MaxOpenConns:         config.MaxOpenConns,
		MaxIdleConns:         config.MaxIdleConns,
		ConnMaxLifetime:      config.ConnMaxLifetime,
		ConnOpenStrategy:     clickhouse.ConnOpenRoundRobin,
		Compression:          &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		FreeBufOnConnRelease: config.FreeBufOnConnRelease,
		Settings: clickhouse.Settings{
			"async_insert": 0,
		},
	}
	return options, config.TablePrefix, config.MaxRetries, config.RetryBackoff, nil
}

func (store *ClickHouse) table(suffix string) string {
	return store.tablePrefix + suffix
}

func (store *ClickHouse) Migrate(ctx context.Context) error {
	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			component LowCardinality(String),
			version UInt32,
			applied_at DateTime64(3, 'UTC')
		) ENGINE=ReplacingMergeTree(applied_at)
		ORDER BY component`, store.table("schema_migrations")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			batch_id String,
			client String,
			time DateTime64(3, 'UTC'),
			cpu Float32,
			gpu Float32,
			ram Int64,
			ram_total Int64,
			swap Int64,
			swap_total Int64,
			load Float32,
			temp Float32,
			disk Int64,
			disk_total Int64,
			net_in Int64,
			net_out Int64,
			net_total_up Int64,
			net_total_down Int64,
			process Int32,
			connections Int32,
			connections_udp Int32
		) ENGINE=ReplacingMergeTree
		PARTITION BY toYYYYMM(time)
		ORDER BY (client, time, batch_id)
		SETTINGS non_replicated_deduplication_window=1000`, store.table("records")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			batch_id String,
			client String,
			time DateTime64(3, 'UTC'),
			device_index Int32,
			device_name String,
			mem_total Int64,
			mem_used Int64,
			utilization Float32,
			temperature Int32
		) ENGINE=ReplacingMergeTree
		PARTITION BY toYYYYMM(time)
		ORDER BY (client, device_index, time, batch_id)
		SETTINGS non_replicated_deduplication_window=1000`, store.table("gpu_records")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			batch_id String,
			client String,
			task_id UInt32,
			time DateTime64(3, 'UTC'),
			value Int32
		) ENGINE=ReplacingMergeTree
		PARTITION BY toYYYYMM(time)
		ORDER BY (client, task_id, time, batch_id)
		SETTINGS non_replicated_deduplication_window=1000`, store.table("ping_records")),
	}
	for _, statement := range statements {
		if err := store.conn.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return store.conn.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s (component,version,applied_at) VALUES ('telemetry',1,now64(3))",
		store.table("schema_migrations"),
	))
}

func (store *ClickHouse) WriteBatch(ctx context.Context, batch storage.TelemetryBatch) error {
	if batch.Rows() == 0 {
		return nil
	}
	if batch.Rows() > maxClickHouseBatchRows {
		return fmt.Errorf("telemetry batch has %d rows, maximum is %d", batch.Rows(), maxClickHouseBatchRows)
	}
	id, err := storage.BatchID(batch)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err = store.writeBatchOnce(ctx, id, batch)
		if err == nil || attempt >= store.maxRetries {
			return err
		}
		timer := time.NewTimer(store.retryBackoff << attempt)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (store *ClickHouse) writeBatchOnce(ctx context.Context, id string, batch storage.TelemetryBatch) error {
	if len(batch.Records) > 0 {
		writeCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
			"insert_deduplication_token": id + ":records",
		}))
		writer, err := store.conn.PrepareBatch(writeCtx, fmt.Sprintf(
			`INSERT INTO %s (
				batch_id,client,time,cpu,gpu,ram,ram_total,swap,swap_total,load,temp,
				disk,disk_total,net_in,net_out,net_total_up,net_total_down,process,connections,connections_udp
			)`, store.table("records")))
		if err != nil {
			return err
		}
		defer writer.Close()
		for _, record := range batch.Records {
			if err := writer.Append(
				id, record.Client, record.Time.ToTime().UTC(), record.Cpu, record.Gpu,
				record.Ram, record.RamTotal, record.Swap, record.SwapTotal, record.Load, record.Temp,
				record.Disk, record.DiskTotal, record.NetIn, record.NetOut, record.NetTotalUp,
				record.NetTotalDown, int32(record.Process), int32(record.Connections), int32(record.ConnectionsUdp),
			); err != nil {
				return err
			}
		}
		if err := writer.Send(); err != nil {
			return err
		}
	}
	if len(batch.GPURecords) > 0 {
		writeCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
			"insert_deduplication_token": id + ":gpu",
		}))
		writer, err := store.conn.PrepareBatch(writeCtx, fmt.Sprintf(
			`INSERT INTO %s (
				batch_id,client,time,device_index,device_name,mem_total,mem_used,utilization,temperature
			)`, store.table("gpu_records")))
		if err != nil {
			return err
		}
		defer writer.Close()
		for _, record := range batch.GPURecords {
			if err := writer.Append(
				id, record.Client, record.Time.ToTime().UTC(), int32(record.DeviceIndex),
				record.DeviceName, record.MemTotal, record.MemUsed, record.Utilization, int32(record.Temperature),
			); err != nil {
				return err
			}
		}
		if err := writer.Send(); err != nil {
			return err
		}
	}
	if len(batch.PingRecords) > 0 {
		writeCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
			"insert_deduplication_token": id + ":ping",
		}))
		writer, err := store.conn.PrepareBatch(writeCtx, fmt.Sprintf(
			`INSERT INTO %s (batch_id,client,task_id,time,value)`, store.table("ping_records")))
		if err != nil {
			return err
		}
		defer writer.Close()
		for _, record := range batch.PingRecords {
			if err := writer.Append(
				id, record.Client, uint32(record.TaskId), record.Time.ToTime().UTC(), int32(record.Value),
			); err != nil {
				return err
			}
		}
		if err := writer.Send(); err != nil {
			return err
		}
	}
	return nil
}

func (store *ClickHouse) QueryRecords(ctx context.Context, query storage.RecordRange) ([]models.Record, error) {
	limit, err := validateRange(query.Start, query.End, query.MaxPoints)
	if err != nil {
		return nil, err
	}
	if !validLoadType(query.LoadType) {
		return nil, errors.New("invalid record load type")
	}
	if query.Client != "" && query.End.Sub(query.Start) > time.Duration(limit)*time.Minute {
		nanos := (query.End.Sub(query.Start).Nanoseconds() + int64(limit) - 1) / int64(limit)
		seconds := (nanos + int64(time.Second) - 1) / int64(time.Second)
		resolution := max(time.Second, time.Duration(seconds)*time.Second)
		return store.AggregateRecords(ctx, storage.AggregateQuery{Range: query, Resolution: resolution})
	}
	sqlQuery := fmt.Sprintf(`SELECT
		client,time,cpu,gpu,ram,ram_total,swap,swap_total,load,temp,disk,disk_total,
		net_in,net_out,net_total_up,net_total_down,process,connections,connections_udp
		FROM %s FINAL
		WHERE time >= fromUnixTimestamp64Milli(?) AND time < fromUnixTimestamp64Milli(?)`,
		store.table("records"))
	args := []any{query.Start.UnixMilli(), query.End.UnixMilli()}
	if query.Client != "" {
		sqlQuery += " AND client = ?"
		args = append(args, query.Client)
	}
	sqlQuery += fmt.Sprintf(" ORDER BY time, client LIMIT %d", limit+1)
	rows, err := store.conn.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.Record, 0, min(limit, 1024))
	for rows.Next() {
		var record models.Record
		var at time.Time
		var process, connections, udp int32
		if err := rows.Scan(
			&record.Client, &at, &record.Cpu, &record.Gpu, &record.Ram, &record.RamTotal,
			&record.Swap, &record.SwapTotal, &record.Load, &record.Temp, &record.Disk,
			&record.DiskTotal, &record.NetIn, &record.NetOut, &record.NetTotalUp,
			&record.NetTotalDown, &process, &connections, &udp,
		); err != nil {
			return nil, err
		}
		record.Time = models.FromTime(at)
		record.Process, record.Connections, record.ConnectionsUdp = int(process), int(connections), int(udp)
		result = append(result, record)
		if len(result) > limit {
			return nil, storage.ErrQueryLimit
		}
	}
	return result, rows.Err()
}

func (store *ClickHouse) QueryGPURecords(ctx context.Context, query storage.GPURange) ([]models.GPURecord, error) {
	limit, err := validateRange(query.Start, query.End, query.MaxPoints)
	if err != nil {
		return nil, err
	}
	sqlQuery := fmt.Sprintf(`SELECT
		client,time,device_index,device_name,mem_total,mem_used,utilization,temperature
		FROM %s FINAL
		WHERE time >= fromUnixTimestamp64Milli(?) AND time < fromUnixTimestamp64Milli(?)`,
		store.table("gpu_records"))
	args := []any{query.Start.UnixMilli(), query.End.UnixMilli()}
	if query.Client != "" {
		sqlQuery += " AND client = ?"
		args = append(args, query.Client)
	}
	sqlQuery += fmt.Sprintf(" ORDER BY time, client, device_index LIMIT %d", limit+1)
	rows, err := store.conn.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.GPURecord, 0, min(limit, 1024))
	for rows.Next() {
		var record models.GPURecord
		var at time.Time
		var device, temperature int32
		if err := rows.Scan(
			&record.Client, &at, &device, &record.DeviceName, &record.MemTotal,
			&record.MemUsed, &record.Utilization, &temperature,
		); err != nil {
			return nil, err
		}
		record.Time = models.FromTime(at)
		record.DeviceIndex, record.Temperature = int(device), int(temperature)
		result = append(result, record)
		if len(result) > limit {
			return nil, storage.ErrQueryLimit
		}
	}
	return result, rows.Err()
}

func (store *ClickHouse) AggregateRecords(ctx context.Context, query storage.AggregateQuery) ([]models.Record, error) {
	limit, err := validateRange(query.Range.Start, query.Range.End, query.Range.MaxPoints)
	if err != nil {
		return nil, err
	}
	if !validLoadType(query.Range.LoadType) {
		return nil, errors.New("invalid record load type")
	}
	if query.Resolution < time.Second || query.Resolution > 31*24*time.Hour {
		return nil, errors.New("aggregate resolution must be between one second and 31 days")
	}
	seconds := int64(query.Resolution / time.Second)
	expression := func(column string) string { return clickHouseAggregateExpression(column, query.Range.LoadType) }
	sqlQuery := fmt.Sprintf(`SELECT
		client,
		toStartOfInterval(time, INTERVAL %d SECOND) AS bucket,
		toFloat32(%s),toFloat32(%s),
		toInt64(%s),toInt64(%s),
		toInt64(%s),toInt64(%s),
		toFloat32(%s),toFloat32(%s),
		toInt64(%s),toInt64(%s),
		toInt64(%s),toInt64(%s),
		argMax(net_total_up,time),argMax(net_total_down,time),
		toInt32(%s),toInt32(%s),toInt32(%s)
		FROM %s FINAL
		WHERE time >= fromUnixTimestamp64Milli(?) AND time < fromUnixTimestamp64Milli(?)`,
		seconds, expression("cpu"), expression("gpu"),
		expression("ram"), expression("ram_total"), expression("swap"), expression("swap_total"),
		expression("load"), expression("temp"), expression("disk"), expression("disk_total"),
		expression("net_in"), expression("net_out"), expression("process"),
		expression("connections"), expression("connections_udp"), store.table("records"))
	args := []any{query.Range.Start.UnixMilli(), query.Range.End.UnixMilli()}
	if query.Range.Client != "" {
		sqlQuery += " AND client = ?"
		args = append(args, query.Range.Client)
	}
	sqlQuery += fmt.Sprintf(" GROUP BY client,bucket ORDER BY bucket,client LIMIT %d", limit+1)
	rows, err := store.conn.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.Record, 0, min(limit, 1024))
	for rows.Next() {
		var record models.Record
		var at time.Time
		var process, connections, udp int32
		if err := rows.Scan(
			&record.Client, &at, &record.Cpu, &record.Gpu, &record.Ram, &record.RamTotal,
			&record.Swap, &record.SwapTotal, &record.Load, &record.Temp, &record.Disk,
			&record.DiskTotal, &record.NetIn, &record.NetOut, &record.NetTotalUp,
			&record.NetTotalDown, &process, &connections, &udp,
		); err != nil {
			return nil, err
		}
		record.Time = models.FromTime(at)
		record.Process, record.Connections, record.ConnectionsUdp = int(process), int(connections), int(udp)
		result = append(result, record)
		if len(result) > limit {
			return nil, storage.ErrQueryLimit
		}
	}
	return result, rows.Err()
}

func (store *ClickHouse) ApplyRetention(ctx context.Context, policy storage.RetentionPolicy) (storage.RetentionResult, error) {
	now := policy.Now
	if now.IsZero() {
		now = time.Now()
	}
	if policy.FinalCutoff.IsZero() || policy.FinalCutoff.After(now) {
		return storage.RetentionResult{}, errors.New("retention cutoff must be set and cannot be in the future")
	}
	mutationCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"mutations_sync": 2}))
	for _, suffix := range []string{"records", "gpu_records", "ping_records"} {
		if err := store.conn.Exec(mutationCtx,
			fmt.Sprintf("ALTER TABLE %s DELETE WHERE time < fromUnixTimestamp64Milli(?)", store.table(suffix)),
			policy.FinalCutoff.UnixMilli(),
		); err != nil {
			return storage.RetentionResult{}, err
		}
	}
	return storage.RetentionResult{FinalCutoff: policy.FinalCutoff, CompletedAt: time.Now()}, nil
}

func (store *ClickHouse) Health(ctx context.Context) (storage.Health, error) {
	started := time.Now()
	health := storage.Health{Backend: "clickhouse", CheckedAt: started}
	var value uint8
	err := store.conn.QueryRow(ctx, "SELECT toUInt8(1)").Scan(&value)
	health.Latency = time.Since(started)
	health.Ready = err == nil && value == 1
	if err != nil {
		return health, err
	}
	if !health.Ready {
		return health, errors.New("ClickHouse telemetry health query returned an unexpected value")
	}
	return health, nil
}

func (store *ClickHouse) Close(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- store.conn.Close() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validateRange(start, end time.Time, maxPoints int) (int, error) {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return 0, errors.New("invalid telemetry query range")
	}
	if maxPoints <= 0 {
		maxPoints = defaultClickHouseMaxPoints
	}
	if maxPoints > defaultClickHouseMaxPoints {
		return 0, fmt.Errorf("query point limit cannot exceed %d", defaultClickHouseMaxPoints)
	}
	return maxPoints, nil
}

func validLoadType(loadType string) bool {
	switch loadType {
	case "", "all", "cpu", "gpu", "ram", "swap", "load", "temp", "disk", "network", "process", "connections":
		return true
	default:
		return false
	}
}

func clickHouseAggregateExpression(column, loadType string) string {
	direct := map[string]string{
		"cpu": "cpu", "gpu": "gpu", "load": "load", "temp": "temp", "process": "process",
	}
	if direct[loadType] == column {
		return "max(" + column + ")"
	}
	switch loadType {
	case "ram":
		if column == "ram" || column == "ram_total" {
			return "argMax(" + column + ",if(ram_total>0,ram/ram_total,0))"
		}
	case "swap":
		if column == "swap" || column == "swap_total" {
			return "argMax(" + column + ",if(swap_total>0,swap/swap_total,0))"
		}
	case "disk":
		if column == "disk" || column == "disk_total" {
			return "argMax(" + column + ",if(disk_total>0,disk/disk_total,0))"
		}
	case "network":
		if column == "net_in" || column == "net_out" {
			return "argMax(" + column + ",net_in+net_out)"
		}
	case "connections":
		if column == "connections" || column == "connections_udp" {
			return "argMax(" + column + ",connections+connections_udp)"
		}
	}
	return "avg(" + column + ")"
}
