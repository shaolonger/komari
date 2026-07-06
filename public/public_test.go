package public

import (
	"bytes"
	"testing"
)

func TestInjectAdminFleetReportSettingsLink(t *testing.T) {
	html := []byte(`<!doctype html><html><body><div id="root"></div></body></html>`)
	injected := injectAdminFleetReportSettingsLink(html)

	if !bytes.Contains(injected, []byte("/admin/notification/fleet-report-settings")) {
		t.Fatalf("injected HTML does not include fleet report route: %s", string(injected))
	}
	if count := bytes.Count(injected, []byte("fleet-report-settings-nav-injector")); count != 1 {
		t.Fatalf("injector count = %d, want 1", count)
	}

	second := injectAdminFleetReportSettingsLink(injected)
	if count := bytes.Count(second, []byte("fleet-report-settings-nav-injector")); count != 1 {
		t.Fatalf("injector count after second injection = %d, want 1", count)
	}
}

func TestInjectAdminFleetReportSettingsLinkSkipsBodylessHTML(t *testing.T) {
	html := []byte(`<div id="root"></div>`)
	injected := injectAdminFleetReportSettingsLink(html)
	if !bytes.Equal(injected, html) {
		t.Fatalf("bodyless HTML changed: %s", string(injected))
	}
}
