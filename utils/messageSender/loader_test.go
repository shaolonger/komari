package messageSender

import (
	"testing"

	"github.com/komari-monitor/komari/utils/messageSender/factory"
)

func Test(t *testing.T) {
	senders := factory.GetAllMessageSenders()
	if len(senders) == 0 {
		t.Error("No message senders found")
		return
	}
	cfg := factory.GetSenderConfigs()
	if len(cfg) == 0 {
		t.Error("No sender configs found")
		return
	}
	if err := LoadProvider("email", `{"host":"smtp.example.com","port":587,"username":"user","password":"pass"}`); err != nil {
		t.Fatalf("LoadProvider(email) error = %v", err)
	}
	cp := CurrentProvider
	if cp() == nil {
		t.Error("Current provider is nil")
		return
	}
}

func TestLoadProviderReturnsInitError(t *testing.T) {
	if err := LoadProvider("Javascript", `{"script":""}`); err == nil {
		t.Fatal("LoadProvider(Javascript) error = nil, want initialization error")
	}
}
