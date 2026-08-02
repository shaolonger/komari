package javascript

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestJavaScriptSenderSendsTextMessage(t *testing.T) {
	sender := &JavaScriptSender{
		Addition: Addition{
			Script: `
				function sendMessage(message, title) {
					return message === "hello" && title === "greeting";
				}
			`,
		},
	}

	if err := sender.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := sender.SendTextMessage("hello", "greeting"); err != nil {
		t.Fatalf("SendTextMessage() error = %v", err)
	}
}

func TestJavaScriptSenderSendsEventWithJSONFields(t *testing.T) {
	sender := &JavaScriptSender{
		Addition: Addition{
			Script: `
				function sendMessage() {
					return false;
				}
				function sendEvent(event) {
					return event.event === "Test" &&
						event.message === "payload" &&
						event.clients.length === 1 &&
						event.clients[0].name === "node-a";
				}
			`,
		},
	}

	if err := sender.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	err := sender.SendEvent(models.EventMessage{
		Event:   "Test",
		Message: "payload",
		Time:    time.Now(),
		Clients: []models.Client{{UUID: "node-a-uuid", Name: "node-a"}},
	})
	if err != nil {
		t.Fatalf("SendEvent() error = %v", err)
	}
}

func TestJavaScriptSenderRejectsMissingSendMessage(t *testing.T) {
	sender := &JavaScriptSender{
		Addition: Addition{Script: `function noop() { return true; }`},
	}

	if err := sender.Init(); err == nil {
		t.Fatal("Init() error = nil, want missing sendMessage error")
	}
}

func TestJavaScriptFetchBlocksLocalhost(t *testing.T) {
	sender := &JavaScriptSender{
		Addition: Addition{
			Script: `
				async function sendMessage() {
					try {
						await fetch("http://127.0.0.1/test");
						return false;
					} catch (error) {
						return String(error).indexOf("not permitted") >= 0 ||
							String(error).indexOf("not allowed") >= 0;
					}
				}
			`,
		},
	}

	if err := sender.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := sender.SendTextMessage("hello", "greeting"); err != nil {
		t.Fatalf("SendTextMessage() error = %v", err)
	}
}
