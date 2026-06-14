package assetfx

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/komari-monitor/komari/config"
)

const (
	defaultBaseCode = "USD"
	defaultSource   = "open.er-api.com"
	defaultURL      = "https://open.er-api.com/v6/latest/USD"
)

type Snapshot struct {
	BaseCode  string             `json:"base_code"`
	Source    string             `json:"source"`
	UpdatedAt time.Time          `json:"updated_at"`
	Rates     map[string]float64 `json:"rates"`
	Stale     bool               `json:"stale"`
	Error     string             `json:"error,omitempty"`
}

func defaultSnapshot() Snapshot {
	return Snapshot{
		BaseCode: defaultBaseCode,
		Source:   "manual",
		Rates: map[string]float64{
			defaultBaseCode: 1,
		},
	}
}

func normalizeSnapshot(snapshot Snapshot) Snapshot {
	if strings.TrimSpace(snapshot.BaseCode) == "" {
		snapshot.BaseCode = defaultBaseCode
	}
	snapshot.BaseCode = strings.ToUpper(strings.TrimSpace(snapshot.BaseCode))
	if strings.TrimSpace(snapshot.Source) == "" {
		snapshot.Source = defaultSource
	}
	if snapshot.Rates == nil {
		snapshot.Rates = map[string]float64{}
	}
	snapshot.Rates[snapshot.BaseCode] = 1
	return snapshot
}

func GetSnapshot() (Snapshot, error) {
	snapshot, err := config.GetAs[Snapshot](config.AssetFxSnapshotKey, defaultSnapshot())
	if err != nil {
		return normalizeSnapshot(defaultSnapshot()), err
	}
	return normalizeSnapshot(snapshot), nil
}

func RefreshSnapshot() (Snapshot, error) {
	snapshot, err := fetchSnapshot()
	if err != nil {
		existing, existingErr := GetSnapshot()
		if existingErr != nil {
			existing = normalizeSnapshot(defaultSnapshot())
		}
		existing.Stale = true
		existing.Error = err.Error()
		return existing, err
	}

	if err := config.Set(config.AssetFxSnapshotKey, snapshot); err != nil {
		snapshot.Stale = true
		snapshot.Error = err.Error()
		return snapshot, err
	}
	return snapshot, nil
}

type erAPIPayload struct {
	Result             string             `json:"result"`
	Provider           string             `json:"provider"`
	TimeLastUpdateUnix int64              `json:"time_last_update_unix"`
	BaseCode           string             `json:"base_code"`
	Rates              map[string]float64 `json:"rates"`
	ErrorType          string             `json:"error-type"`
	Error              string             `json:"error"`
}

func fetchSnapshot() (Snapshot, error) {
	request, err := http.NewRequest(http.MethodGet, defaultURL, nil)
	if err != nil {
		return Snapshot{}, err
	}
	request.Header.Set("User-Agent", "komari-asset-fx/1.0")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Snapshot{}, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Snapshot{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Snapshot{}, fmt.Errorf("unexpected fx response status %d", response.StatusCode)
	}

	var payload erAPIPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Snapshot{}, err
	}
	if strings.ToLower(strings.TrimSpace(payload.Result)) != "success" {
		if payload.Error != "" {
			return Snapshot{}, fmt.Errorf("%s", payload.Error)
		}
		if payload.ErrorType != "" {
			return Snapshot{}, fmt.Errorf("%s", payload.ErrorType)
		}
		return Snapshot{}, fmt.Errorf("fx provider did not return success")
	}

	rates := make(map[string]float64, len(payload.Rates)+1)
	for key, value := range payload.Rates {
		code := strings.ToUpper(strings.TrimSpace(key))
		if code == "" || !isValidRate(value) {
			continue
		}
		rates[code] = value
	}
	baseCode := strings.ToUpper(strings.TrimSpace(payload.BaseCode))
	if baseCode == "" {
		baseCode = defaultBaseCode
	}
	rates[baseCode] = 1

	snapshot := Snapshot{
		BaseCode:  baseCode,
		Source:    strings.TrimSpace(payload.Provider),
		UpdatedAt: time.Unix(payload.TimeLastUpdateUnix, 0).UTC(),
		Rates:     rates,
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now().UTC()
	}
	return normalizeSnapshot(snapshot), nil
}

func isValidRate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}
