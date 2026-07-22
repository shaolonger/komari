package public

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/historycache"
)

const maxCachedHistoryPoints = 1_000

type historyGPUDevice struct {
	DeviceIndex int                `json:"device_index"`
	DeviceName  string             `json:"device_name"`
	Records     []models.GPURecord `json:"records"`
}

type recordHistoryPayload struct {
	records      []models.Record
	flatRecords  []gin.H
	filtered     bool
	loadType     string
	gpuDevices   map[string]*historyGPUDevice
	includeGPU   bool
	hasGPUData   bool
	totalGPUData int
}

func (payload recordHistoryPayload) count() int {
	if payload.filtered {
		return len(payload.flatRecords)
	}
	return len(payload.records)
}

func (payload recordHistoryPayload) cacheable() bool {
	return payload.count()+payload.totalGPUData <= maxCachedHistoryPoints
}

func (payload recordHistoryPayload) data() gin.H {
	data := gin.H{"count": payload.count()}
	if payload.filtered {
		data["records"] = payload.flatRecords
		data["load_type"] = payload.loadType
	} else {
		data["records"] = payload.records
	}
	if payload.includeGPU {
		data["has_gpu_data"] = payload.hasGPUData
		if payload.hasGPUData {
			data["gpu_devices"] = payload.gpuDevices
		}
	}
	return data
}

func historyRecordCacheKey(permission, uuid string, hours int, loadType string, maxPoints int) string {
	encoded, _ := json.Marshal(struct {
		Version    int    `json:"version"`
		Permission string `json:"permission"`
		UUID       string `json:"uuid"`
		Hours      int    `json:"hours"`
		LoadType   string `json:"load_type"`
		MaxPoints  int    `json:"max_points"`
		Minute     int64  `json:"minute"`
	}{1, permission, uuid, hours, loadType, maxPoints, time.Now().Unix() / 60})
	return string(encoded)
}

func writeCachedHistory(c *gin.Context, payload []byte) {
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Komari-History-Cache", "hit")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(payload)
}

func respondRecordHistory(c *gin.Context, payload recordHistoryPayload, cacheKey string, generation uint64) error {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Komari-History-Cache", "miss")
	if payload.cacheable() {
		encoded, err := json.Marshal(api.Response{Status: "success", Message: "", Data: payload.data()})
		if err != nil {
			return err
		}
		historycache.PutIfGeneration(cacheKey, encoded, generation)
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.Status(http.StatusOK)
		_, err = c.Writer.Write(encoded)
		return err
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(http.StatusOK)
	return streamRecordHistory(c.Request.Context(), c.Writer, payload)
}

// streamRecordHistory emits each history point independently. The largest
// transient JSON buffer is one Record/GPURecord, regardless of response size.
func streamRecordHistory(ctx context.Context, writer io.Writer, payload recordHistoryPayload) error {
	buffered := bufio.NewWriterSize(writer, 32<<10)
	write := func(value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := buffered.Write(value); err != nil {
			return err
		}
		return ctx.Err()
	}
	marshalWrite := func(value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return write(encoded)
	}
	if err := write([]byte(`{"status":"success","message":"","data":{"records":[`)); err != nil {
		return err
	}
	count := payload.count()
	for index := 0; index < count; index++ {
		if index > 0 {
			if err := write([]byte(",")); err != nil {
				return err
			}
		}
		if payload.filtered {
			if err := marshalWrite(payload.flatRecords[index]); err != nil {
				return err
			}
		} else if err := marshalWrite(payload.records[index]); err != nil {
			return err
		}
	}
	if err := write([]byte(`],"count":` + strconv.Itoa(count))); err != nil {
		return err
	}
	if payload.filtered {
		if err := write([]byte(`,"load_type":`)); err != nil {
			return err
		}
		if err := marshalWrite(payload.loadType); err != nil {
			return err
		}
	}
	if payload.includeGPU {
		if payload.hasGPUData {
			if err := write([]byte(`,"gpu_devices":{`)); err != nil {
				return err
			}
			firstDevice := true
			for key, device := range payload.gpuDevices {
				if !firstDevice {
					if err := write([]byte(",")); err != nil {
						return err
					}
				}
				firstDevice = false
				if err := marshalWrite(key); err != nil {
					return err
				}
				if err := write([]byte(`:{"device_index":` + strconv.Itoa(device.DeviceIndex) + `,"device_name":`)); err != nil {
					return err
				}
				if err := marshalWrite(device.DeviceName); err != nil {
					return err
				}
				if err := write([]byte(`,"records":[`)); err != nil {
					return err
				}
				for index, record := range device.Records {
					if index > 0 {
						if err := write([]byte(",")); err != nil {
							return err
						}
					}
					if err := marshalWrite(record); err != nil {
						return err
					}
				}
				if err := write([]byte("]}")); err != nil {
					return err
				}
			}
			if err := write([]byte("}")); err != nil {
				return err
			}
		}
		if err := write([]byte(`,"has_gpu_data":` + strconv.FormatBool(payload.hasGPUData))); err != nil {
			return err
		}
	}
	if err := write([]byte("}}")); err != nil {
		return fmt.Errorf("finish history response: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return buffered.Flush()
}
