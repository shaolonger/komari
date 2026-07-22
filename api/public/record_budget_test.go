package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

func TestGetRecordsByUUIDEnforcesBudgetAndProjection(t *testing.T) {
	db := dbcore.GetDBInstance()
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.Record{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.Client{}).Error; err != nil {
		t.Fatal(err)
	}
	client := models.Client{UUID: "budget-node", Token: "budget-node-token", Name: "Budget node"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.Record{}).Error
		_ = db.Delete(&models.Client{}, "uuid = ?", client.UUID).Error
	})
	now := time.Now()
	records := make([]models.Record, 101)
	for index := range records {
		records[index] = models.Record{
			Client: client.UUID, Time: models.FromTime(now.Add(-time.Duration(100-index) * time.Second)),
			Cpu: 10, Ram: 123, RamTotal: 456,
		}
	}
	records[57].Cpu = 100
	if err := db.Create(&records).Error; err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/records", GetRecordsByUUID)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/records?uuid=budget-node&hours=1&load_type=cpu&max_count=10", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Count   int                      `json:"count"`
			Records []map[string]interface{} `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Count != 10 || len(response.Data.Records) != 10 {
		t.Fatalf("bounded response=%+v", response.Data)
	}
	foundSpike := false
	for _, record := range response.Data.Records {
		if _, leaked := record["ram"]; leaked {
			t.Fatalf("cpu projection leaked ram: %+v", record)
		}
		if cpu, ok := record["cpu"].(float64); ok && cpu == 100 {
			foundSpike = true
		}
	}
	if !foundSpike {
		t.Fatalf("downsampling lost CPU spike: %+v", response.Data.Records)
	}
}

func TestGetRecordsByUUIDRejectsBudgetAndProjectionAbuse(t *testing.T) {
	router := gin.New()
	router.GET("/records", GetRecordsByUUID)
	for _, query := range []string{
		"uuid=node&hours=0",
		"uuid=node&hours=8785",
		"uuid=node&hours=1&max_count=20001",
		"uuid=node&hours=1&load_type=cpu%20FROM%20users",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/records?"+query, nil)
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("query=%q status=%d body=%s", query, recorder.Code, recorder.Body.String())
		}
	}
}
