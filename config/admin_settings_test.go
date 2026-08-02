package config

import (
	"strings"
	"testing"
)

func TestAdminSettingsFiltersInternalStateAndSecrets(t *testing.T) {
	got := AdminSettings(map[string]any{
		SitenameKey:            "Komari",
		ApiKeyKey:              "top-secret",
		"o_auth_client_secret": "oauth-secret",
		"metric_db_dsn":        "user:password@example",
		AssetFxSnapshotKey:     map[string]any{"internal": true},
		"migration_checkpoint": 42,
		CustomHeadKey:          "<script>alert(1)</script>",
	})

	if got[SitenameKey] != "Komari" {
		t.Fatalf("sitename = %#v", got[SitenameKey])
	}
	for _, key := range []string{ApiKeyKey, "o_auth_client_secret", "metric_db_dsn", AssetFxSnapshotKey, "migration_checkpoint"} {
		if _, ok := got[key]; ok {
			t.Fatalf("sensitive/internal key %q leaked", key)
		}
	}
	if got[CustomHeadKey] != "" || got[CustomBodyKey] != "" {
		t.Fatalf("custom script fields must be forced empty: %#v", got)
	}
}

func TestValidateAdminSettingsRejectsUnknownAndInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"unknown", map[string]any{"metric_retention_day": float64(7)}, "unsupported setting"},
		{"wrong type", map[string]any{"low_resource_mode": "true"}, "must be a boolean"},
		{"fractional integer", map[string]any{"metric_retention_days": 1.5}, "must be an integer"},
		{"out of range", map[string]any{"metric_max_open_conns": float64(65)}, "between 1 and 64"},
		{"unsafe table", map[string]any{"metric_table_prefix": "metric_; DROP TABLE"}, "contain only"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateAdminSettings(test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateAdminSettingsAllowsSupportedSecretsAndClearsScripts(t *testing.T) {
	got, err := ValidateAdminSettings(map[string]any{
		"metric_db_dsn":         "file:metrics.db?_busy_timeout=5000",
		"metric_retention_days": float64(7),
		"metric_table_prefix":   "metric_v2_",
		"metric_max_open_conns": float64(2),
		"metric_max_idle_conns": float64(1),
		"cors_allowed_origins":  "https://example.com",
		CustomHeadKey:           "<script>alert(1)</script>",
		CustomBodyKey:           "<iframe />",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["metric_db_dsn"] == "" || got["metric_table_prefix"] != "metric_v2_" {
		t.Fatalf("validated settings = %#v", got)
	}
	if got[CustomHeadKey] != "" || got[CustomBodyKey] != "" {
		t.Fatalf("custom scripts were not cleared: %#v", got)
	}
}
