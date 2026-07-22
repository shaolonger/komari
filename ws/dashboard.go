package ws

import (
	"sort"
	"time"

	"github.com/komari-monitor/komari/common"
)

const dashboardChangeCapacity = 16_384

type dashboardChangeKind uint8

const (
	dashboardReportChange dashboardChangeKind = iota + 1
	dashboardOnlineChange
)

type dashboardChange struct {
	sequence uint64
	uuid     string
	kind     dashboardChangeKind
}

// DashboardUpdate owns all returned maps and reports. Snapshot is true for an
// initial state or when Since fell outside the bounded change journal.
type DashboardUpdate struct {
	Sequence uint64
	Snapshot bool
	Resync   bool
	Reports  map[string]common.Report
	Removed  []string
	Online   []string
	Offline  []string
}

var (
	dashboardSequence uint64
	dashboardChanges  []dashboardChange
	dashboardStart    int
	dashboardNotify   = make(chan struct{})
)

func appendDashboardChangeLocked(uuid string, kind dashboardChangeKind) {
	dashboardSequence++
	change := dashboardChange{sequence: dashboardSequence, uuid: uuid, kind: kind}
	if len(dashboardChanges) == dashboardChangeCapacity {
		dashboardChanges[dashboardStart] = change
		dashboardStart = (dashboardStart + 1) % dashboardChangeCapacity
	} else {
		dashboardChanges = append(dashboardChanges, change)
	}
	close(dashboardNotify)
	dashboardNotify = make(chan struct{})
}

// DashboardStateSince returns a consistent state and the notification channel
// for the next sequence. Capturing both under the same lock prevents lost
// wakeups between reading state and waiting for a change.
func DashboardStateSince(since uint64) (DashboardUpdate, <-chan struct{}) {
	mu.RLock()
	defer mu.RUnlock()

	update := DashboardUpdate{Sequence: dashboardSequence}
	resync := since > dashboardSequence
	if !resync && since > 0 && len(dashboardChanges) > 0 && since+1 < dashboardChanges[dashboardStart].sequence {
		resync = true
	}
	if since == 0 || resync {
		update.Snapshot = true
		update.Resync = resync
		update.Reports = cloneLatestReportsLocked()
		update.Online = onlineUUIDsLocked(time.Now())
		return update, dashboardNotify
	}

	reportKeys := make(map[string]struct{})
	onlineKeys := make(map[string]struct{})
	startOffset := 0
	if len(dashboardChanges) > 0 {
		oldestSequence := dashboardChanges[dashboardStart].sequence
		if since >= oldestSequence {
			startOffset = int(min(since-oldestSequence+1, uint64(len(dashboardChanges))))
		}
	}
	for offset := startOffset; offset < len(dashboardChanges); offset++ {
		change := dashboardChanges[(dashboardStart+offset)%len(dashboardChanges)]
		if change.sequence <= since {
			continue
		}
		switch change.kind {
		case dashboardReportChange:
			reportKeys[change.uuid] = struct{}{}
		case dashboardOnlineChange:
			onlineKeys[change.uuid] = struct{}{}
		}
	}
	if len(reportKeys) > 0 {
		update.Reports = make(map[string]common.Report, len(reportKeys))
		for uuid := range reportKeys {
			if report, exists := latestReport[uuid]; exists {
				update.Reports[uuid] = cloneReport(report)
			} else {
				update.Removed = append(update.Removed, uuid)
			}
		}
	}
	now := time.Now()
	for uuid := range onlineKeys {
		if isOnlineLocked(uuid, now) {
			update.Online = append(update.Online, uuid)
		} else {
			update.Offline = append(update.Offline, uuid)
		}
	}
	sort.Strings(update.Removed)
	sort.Strings(update.Online)
	sort.Strings(update.Offline)
	return update, dashboardNotify
}

func cloneLatestReportsLocked() map[string]common.Report {
	reports := make(map[string]common.Report, len(latestReport))
	for uuid, report := range latestReport {
		reports[uuid] = cloneReport(report)
	}
	return reports
}

func cloneReport(report common.Report) common.Report {
	cloned := report
	if report.GPU != nil {
		gpu := *report.GPU
		gpu.DetailedInfo = append([]common.GPUDeviceInfo(nil), report.GPU.DetailedInfo...)
		cloned.GPU = &gpu
	}
	return cloned
}

func isOnlineLocked(uuid string, now time.Time) bool {
	if _, exists := connectedClients[uuid]; exists {
		return true
	}
	presence, exists := presenceOnly[uuid]
	return exists && presence.expire.After(now)
}

func onlineUUIDsLocked(now time.Time) []string {
	online := make([]string, 0, len(connectedClients)+len(presenceOnly))
	seen := make(map[string]struct{}, len(connectedClients)+len(presenceOnly))
	for uuid := range connectedClients {
		seen[uuid] = struct{}{}
		online = append(online, uuid)
	}
	for uuid, presence := range presenceOnly {
		if presence.expire.After(now) {
			if _, exists := seen[uuid]; !exists {
				online = append(online, uuid)
			}
		}
	}
	sort.Strings(online)
	return online
}
