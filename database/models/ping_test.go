package models

import (
	"encoding/json"
	"strings"
	"testing"
	"unsafe"
)

func TestPingRecordStaysCompactAndOmitsRelationships(t *testing.T) {
	if size := unsafe.Sizeof(PingRecord{}); size > 80 {
		t.Fatalf("PingRecord size = %d bytes, want <= 80", size)
	}
	encoded, err := json.Marshal(PingRecord{Client: "node", TaskId: 7, Value: 12})
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{"client_info", `"task"`} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("compact Ping payload %s contains %q", payload, forbidden)
		}
	}
}
