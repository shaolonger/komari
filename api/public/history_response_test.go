package public

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/historycache"
	"gorm.io/gorm"
)

func TestRecordHistoryCacheSeparatesPermissionsAndInvalidatesVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	historycache.Invalidate()
	db := dbcore.GetDBInstance()
	_ = accounts.DeleteAccountByUsername("history-cache-admin")
	_ = accounts.DeleteAllSessions()
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.Record{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.Client{}).Error; err != nil {
		t.Fatal(err)
	}
	client := models.Client{UUID: "history-cache-node", Token: "history-cache-token", Name: "cache node"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Record{Client: client.UUID, Time: models.FromTime(time.Now().Add(-time.Minute)), Cpu: 42}).Error; err != nil {
		t.Fatal(err)
	}
	user, err := accounts.CreateAccount("history-cache-admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	session, err := accounts.CreateSession(user.UUID, 3_600, "test", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		historycache.Invalidate()
		_ = accounts.DeleteAccountByUsername("history-cache-admin")
		_ = accounts.DeleteAllSessions()
		_ = db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.Record{}).Error
		_ = db.Delete(&models.Client{}, "uuid = ?", client.UUID).Error
	})
	router := gin.New()
	router.GET("/records", GetRecordsByUUID)
	path := "/records?uuid=" + client.UUID + "&hours=1&load_type=cpu&max_count=10"
	request := func(sessionToken string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if sessionToken != "" {
			req.AddCookie(&http.Cookie{Name: "session_token", Value: sessionToken})
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}

	if response := request(""); response.Code != http.StatusOK || response.Header().Get("X-Komari-History-Cache") != "miss" {
		t.Fatalf("first public response status=%d cache=%q body=%s", response.Code, response.Header().Get("X-Komari-History-Cache"), response.Body.String())
	}
	if response := request(""); response.Header().Get("X-Komari-History-Cache") != "hit" {
		t.Fatalf("second public response cache=%q", response.Header().Get("X-Komari-History-Cache"))
	}
	if response := request(session); response.Code != http.StatusOK || response.Header().Get("X-Komari-History-Cache") != "miss" {
		t.Fatalf("admin reused public cache: status=%d cache=%q", response.Code, response.Header().Get("X-Komari-History-Cache"))
	}
	if response := request(session); response.Header().Get("X-Komari-History-Cache") != "hit" {
		t.Fatalf("second admin response cache=%q", response.Header().Get("X-Komari-History-Cache"))
	}

	if err := clients.SaveClient(map[string]interface{}{"uuid": client.UUID, "hidden": true}); err != nil {
		t.Fatal(err)
	}
	if response := request(""); response.Code != http.StatusBadRequest {
		t.Fatalf("hidden node served cached public history: status=%d body=%s", response.Code, response.Body.String())
	}
	if err := clients.SaveClient(map[string]interface{}{"uuid": client.UUID, "hidden": false}); err != nil {
		t.Fatal(err)
	}
	if response := request(""); response.Code != http.StatusOK || response.Header().Get("X-Komari-History-Cache") != "miss" {
		t.Fatalf("unhidden node did not use new generation: status=%d cache=%q", response.Code, response.Header().Get("X-Komari-History-Cache"))
	}
}

func TestStreamRecordHistoryIsValidAndUsesBoundedChunks(t *testing.T) {
	const points = 2_000
	payload := recordHistoryPayload{records: make([]models.Record, points)}
	now := time.Now().UTC()
	for index := range payload.records {
		payload.records[index] = models.Record{Client: "stream-node", Time: models.FromTime(now.Add(time.Duration(index) * time.Second)), Cpu: float32(index % 100)}
	}
	writer := &boundedWriteBuffer{maxChunk: 64 << 10}
	if err := streamRecordHistory(context.Background(), writer, payload); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status string `json:"status"`
		Data   struct {
			Count   int               `json:"count"`
			Records []json.RawMessage `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(writer.data, &response); err != nil {
		t.Fatalf("invalid streamed JSON: %v", err)
	}
	if response.Status != "success" || response.Data.Count != points || len(response.Data.Records) != points {
		t.Fatalf("streamed response status=%q count=%d records=%d", response.Status, response.Data.Count, len(response.Data.Records))
	}
	if writer.largest > writer.maxChunk {
		t.Fatalf("largest write=%d exceeds bound=%d", writer.largest, writer.maxChunk)
	}
}

func TestStreamRecordHistoryCancelsSlowClient(t *testing.T) {
	payload := recordHistoryPayload{records: make([]models.Record, 10_000)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := streamRecordHistory(ctx, slowHistoryWriter{delay: 2 * time.Millisecond}, payload)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow client error=%v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("stream cancellation took %s", elapsed)
	}
}

func BenchmarkStreamRecordHistory100000Points(b *testing.B) {
	payload := benchmarkHistoryPayload()
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if err := streamRecordHistory(context.Background(), io.Discard, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalRecordHistory100000Points(b *testing.B) {
	payload := benchmarkHistoryPayload()
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		encoded, err := json.Marshal(api.Response{Status: "success", Data: payload.data()})
		if err != nil || len(encoded) == 0 {
			b.Fatal(err)
		}
	}
}

func benchmarkHistoryPayload() recordHistoryPayload {
	payload := recordHistoryPayload{records: make([]models.Record, 100_000)}
	now := time.Now().UTC()
	for index := range payload.records {
		payload.records[index] = models.Record{
			Client: "benchmark-node", Time: models.FromTime(now.Add(time.Duration(index) * time.Second)), Cpu: float32(index % 100), Ram: int64(index), RamTotal: 1 << 30,
		}
	}
	return payload
}

type boundedWriteBuffer struct {
	data     []byte
	maxChunk int
	largest  int
}

func (writer *boundedWriteBuffer) Write(value []byte) (int, error) {
	if len(value) > writer.maxChunk {
		return 0, fmt.Errorf("write chunk %d exceeds %d", len(value), writer.maxChunk)
	}
	writer.largest = max(writer.largest, len(value))
	writer.data = append(writer.data, value...)
	return len(value), nil
}

type slowHistoryWriter struct{ delay time.Duration }

func (writer slowHistoryWriter) Write(value []byte) (int, error) {
	time.Sleep(writer.delay)
	return len(value), nil
}
