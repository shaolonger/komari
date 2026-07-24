//go:build scale

package telemetrystore

import (
	"crypto/tls"
	"testing"
	"time"
)

func TestClickHouseOptionsSecureDefaultsAndBounds(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS10}
	options, prefix, retries, backoff, err := clickHouseOptions(ClickHouseConfig{
		Addresses: []string{"clickhouse.example:9440"},
		Database:  "komari", Username: "user", Password: "secret",
		RequireTLS: true, TLSConfig: tlsConfig,
		MaxOpenConns: 16, MaxIdleConns: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.TLS == tlsConfig {
		t.Fatal("caller TLS config was not cloned")
	}
	if options.TLS.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS min version=%x", options.TLS.MinVersion)
	}
	if prefix != "komari_" || retries != 4 || backoff != 25*time.Millisecond {
		t.Fatalf("prefix=%q retries=%d backoff=%s", prefix, retries, backoff)
	}
	if options.MaxOpenConns != 16 || options.MaxIdleConns != 4 {
		t.Fatalf("pool max=%d idle=%d", options.MaxOpenConns, options.MaxIdleConns)
	}
}

func TestClickHouseOptionsRejectUnsafeOrUnboundedConfiguration(t *testing.T) {
	tests := []ClickHouseConfig{
		{},
		{Addresses: []string{"localhost:9000/path"}},
		{Addresses: []string{"localhost:9000"}, RequireTLS: true},
		{Addresses: []string{"localhost:9000"}, TablePrefix: "bad-prefix"},
		{Addresses: []string{"localhost:9000"}, MaxOpenConns: 257},
		{Addresses: []string{"localhost:9000"}, MaxOpenConns: 2, MaxIdleConns: 3},
		{Addresses: []string{"localhost:9000"}, MaxRetries: 11},
	}
	for index, config := range tests {
		if _, _, _, _, err := clickHouseOptions(config); err == nil {
			t.Fatalf("case %d accepted: %+v", index, config)
		}
	}
}

func TestClickHouseQueryValidation(t *testing.T) {
	now := time.Now()
	if _, err := validateRange(now, now, 1); err == nil {
		t.Fatal("zero range accepted")
	}
	if _, err := validateRange(now, now.Add(time.Hour), defaultClickHouseMaxPoints+1); err == nil {
		t.Fatal("oversized point limit accepted")
	}
	if validLoadType("cpu; DROP TABLE records") {
		t.Fatal("injected load type accepted")
	}
	tests := map[string]string{
		clickHouseAggregateExpression("cpu", "cpu"):                     "max(cpu)",
		clickHouseAggregateExpression("ram", "ram"):                     "argMax(ram,if(ram_total>0,ram/ram_total,0))",
		clickHouseAggregateExpression("net_out", "network"):             "argMax(net_out,net_in+net_out)",
		clickHouseAggregateExpression("connections_udp", "connections"): "argMax(connections_udp,connections+connections_udp)",
		clickHouseAggregateExpression("disk", "cpu"):                    "avg(disk)",
	}
	for got, want := range tests {
		if got != want {
			t.Fatalf("expression=%q want=%q", got, want)
		}
	}
}
