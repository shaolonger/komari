package admin

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
)

const (
	assetObservationFresh   = "fresh"
	assetObservationPartial = "partial"
	assetObservationStale   = "stale"
	assetObservationMissing = "missing"

	assetTokenActive   = "active"
	assetTokenExpiring = "expiring"
	assetTokenExpired  = "expired"
	assetTokenRevoked  = "revoked"
)

type assetScoreFactor struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Points int    `json:"points"`
	Detail string `json:"detail,omitempty"`
}

type assetGovernanceSummary struct {
	ServerVersion                    string `json:"server_version"`
	TargetAgentVersion               string `json:"target_agent_version"`
	NotificationChannelEnabled       bool   `json:"notification_channel_enabled"`
	ExpireNotificationEnabled        bool   `json:"expire_notification_enabled"`
	CapabilityGapAssets              int    `json:"capability_gap_assets"`
	VersionDriftAssets               int    `json:"version_drift_assets"`
	ObservationPartialAssets         int    `json:"observation_partial_assets"`
	ObservationStaleAssets           int    `json:"observation_stale_assets"`
	ObservationMissingAssets         int    `json:"observation_missing_assets"`
	TokenExpiringAssets              int    `json:"token_expiring_assets"`
	TokenExpiredAssets               int    `json:"token_expired_assets"`
	TokenRevokedAssets               int    `json:"token_revoked_assets"`
	OfflineNotificationCoveredAssets int    `json:"offline_notification_covered_assets"`
	OfflineNotificationMissingAssets int    `json:"offline_notification_missing_assets"`
	LoadNotificationCoveredAssets    int    `json:"load_notification_covered_assets"`
	LoadNotificationMissingAssets    int    `json:"load_notification_missing_assets"`
	RecentTaskFailureAssets          int    `json:"recent_task_failure_assets"`
	GovernanceManagedAssets          int    `json:"governance_managed_assets"`
	GovernanceObserveAssets          int    `json:"governance_observe_assets"`
	GovernanceIgnoredAssets          int    `json:"governance_ignored_assets"`
}

type assetEvaluationContext struct {
	targetAgentVersion          string
	offlineNotificationCoverage map[string]bool
	loadNotificationCoverage    map[string]bool
	recentTaskFailures          map[string]bool
	notificationChannelEnabled  bool
	expireNotificationEnabled   bool
}

func defaultAssetEvaluationContext(allClients []models.Client) assetEvaluationContext {
	return assetEvaluationContext{
		targetAgentVersion: detectTargetAgentVersion(allClients),
	}
}

func loadAssetEvaluationContext(allClients []models.Client, now time.Time) assetEvaluationContext {
	ctx := defaultAssetEvaluationContext(allClients)
	db := dbcore.GetDBInstance()
	if db == nil {
		return ctx
	}

	if config.Ready() {
		enabled, _ := config.GetAs[bool](config.NotificationEnabledKey, false)
		method, _ := config.GetAs[string](config.NotificationMethodKey, "none")
		ctx.notificationChannelEnabled = enabled && strings.TrimSpace(strings.ToLower(method)) != "" && strings.TrimSpace(strings.ToLower(method)) != "none"
		ctx.expireNotificationEnabled, _ = config.GetAs[bool](config.ExpireNotificationEnabledKey, false)
	}

	var offlineNotifications []models.OfflineNotification
	if err := db.Find(&offlineNotifications).Error; err == nil {
		ctx.offlineNotificationCoverage = make(map[string]bool, len(offlineNotifications))
		for _, item := range offlineNotifications {
			if item.Client != "" && item.Enable {
				ctx.offlineNotificationCoverage[item.Client] = true
			}
		}
	}

	var loadNotifications []models.LoadNotification
	if err := db.Find(&loadNotifications).Error; err == nil {
		ctx.loadNotificationCoverage = make(map[string]bool, len(loadNotifications))
		for _, item := range loadNotifications {
			for _, clientUUID := range item.Clients {
				if trimmed := strings.TrimSpace(clientUUID); trimmed != "" {
					ctx.loadNotificationCoverage[trimmed] = true
				}
			}
		}
	}

	var taskResults []models.TaskResult
	if err := db.Where("created_at >= ?", models.FromTime(now.Add(-7*24*time.Hour))).Find(&taskResults).Error; err == nil {
		ctx.recentTaskFailures = make(map[string]bool)
		for _, item := range taskResults {
			if item.Client == "" || item.ExitCode == nil || *item.ExitCode == 0 {
				continue
			}
			ctx.recentTaskFailures[item.Client] = true
		}
	}

	return ctx
}

func detectTargetAgentVersion(allClients []models.Client) string {
	best := strings.TrimSpace(utils.CurrentVersion)
	for _, client := range allClients {
		current := strings.TrimSpace(client.Version)
		if compareVersionLike(current, best) > 0 {
			best = current
		}
	}
	return best
}

func compareVersionLike(left string, right string) int {
	if left == right {
		return 0
	}
	leftParts, leftOK := parseVersionLike(left)
	rightParts, rightOK := parseVersionLike(right)
	switch {
	case leftOK && !rightOK:
		return 1
	case !leftOK && rightOK:
		return -1
	case !leftOK && !rightOK:
		return strings.Compare(left, right)
	}

	maxLen := len(leftParts)
	if len(rightParts) > maxLen {
		maxLen = len(rightParts)
	}
	for index := 0; index < maxLen; index++ {
		leftValue := 0
		rightValue := 0
		if index < len(leftParts) {
			leftValue = leftParts[index]
		}
		if index < len(rightParts) {
			rightValue = rightParts[index]
		}
		if leftValue > rightValue {
			return 1
		}
		if leftValue < rightValue {
			return -1
		}
	}
	return strings.Compare(left, right)
}

func parseVersionLike(version string) ([]int, bool) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(version), "v"))
	if trimmed == "" {
		return nil, false
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '.' || r == '-' || r == '+' || r == '_'
	})
	if len(parts) == 0 {
		return nil, false
	}

	values := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			break
		}
		values = append(values, number)
	}
	if len(values) == 0 {
		return nil, false
	}
	return values, true
}

func classifyObservationQuality(reportUpdatedAt time.Time, reportPresent bool, reportPartial bool, now time.Time) (string, *time.Time, *int) {
	if !reportPresent {
		return assetObservationMissing, nil, nil
	}

	if reportUpdatedAt.IsZero() {
		reportUpdatedAt = now
	}
	reportAge := int(now.Sub(reportUpdatedAt).Minutes())
	if reportAge < 0 {
		reportAge = 0
	}

	if reportAge >= 15 {
		return assetObservationStale, &reportUpdatedAt, &reportAge
	}
	if reportPartial {
		return assetObservationPartial, &reportUpdatedAt, &reportAge
	}
	return assetObservationFresh, &reportUpdatedAt, &reportAge
}

func isPartialReport(client models.Client, reportPresent bool, cpuUsage float64, memoryUsage float64, trafficPct float64) bool {
	if !reportPresent {
		return false
	}
	if client.MemTotal <= 0 && client.DiskTotal <= 0 {
		return false
	}
	return cpuUsage <= 0 && memoryUsage <= 0 && trafficPct <= 0
}

func classifyTokenStatus(client models.Client, now time.Time) string {
	if !client.TokenRevokedAt.ToTime().IsZero() {
		return assetTokenRevoked
	}
	if !client.TokenExpiresAt.ToTime().IsZero() {
		expiry := client.TokenExpiresAt.ToTime()
		if !now.Before(expiry) {
			return assetTokenExpired
		}
		if expiry.Sub(now) <= 7*24*time.Hour {
			return assetTokenExpiring
		}
	}
	return assetTokenActive
}

func normalizeGovernanceStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "observe":
		return "observe"
	case "ignored", "ignore":
		return "ignored"
	case "resolved":
		return "resolved"
	default:
		return "none"
	}
}

func buildAssetValueScore(client models.Client, assessment assetAssessment) (int, []assetScoreFactor) {
	factors := make([]assetScoreFactor, 0, 8)
	score := 0

	addFactor := func(key string, points int, label string, detail string) {
		if points == 0 {
			return
		}
		score += points
		factors = append(factors, assetScoreFactor{
			Key:    key,
			Label:  label,
			Points: points,
			Detail: detail,
		})
	}

	switch {
	case assessment.monthlyCost >= 50:
		addFactor("billing_commitment", 28, "Billing commitment", "Monthly normalized cost is at least 50")
	case assessment.monthlyCost >= 20:
		addFactor("billing_commitment", 22, "Billing commitment", "Monthly normalized cost is at least 20")
	case assessment.monthlyCost >= 10:
		addFactor("billing_commitment", 18, "Billing commitment", "Monthly normalized cost is at least 10")
	case assessment.monthlyCost > 0:
		addFactor("billing_commitment", 12, "Billing commitment", "Monthly normalized cost is above zero")
	}

	switch {
	case assessment.remainingValue >= 100:
		addFactor("remaining_exposure", 18, "Remaining exposure", "Remaining paid value is at least 100")
	case assessment.remainingValue >= 50:
		addFactor("remaining_exposure", 14, "Remaining exposure", "Remaining paid value is at least 50")
	case assessment.remainingValue > 0:
		addFactor("remaining_exposure", 10, "Remaining exposure", "Remaining paid value is above zero")
	}

	if strings.TrimSpace(client.Provider) != "" {
		addFactor("provider_context", 6, "Provider context", "Provider metadata is maintained")
	}
	if strings.TrimSpace(client.BusinessRole) != "" {
		addFactor("business_context", 8, "Business context", "Business role is maintained")
	}
	if strings.TrimSpace(client.Group) != "" || strings.TrimSpace(client.PublicRemark) != "" {
		addFactor("portfolio_context", 4, "Portfolio context", "Group or public remark provides extra context")
	}
	if client.CapabilityPing {
		addFactor("capability_ping", 5, "Ping capability", "Ping diagnostics are available")
	}
	if client.CapabilityTerminal || client.CapabilityRemoteExec {
		addFactor("capability_remediation", 7, "Remediation path", "Terminal or remote execution is available")
	}
	if client.CapabilityAutoUpdate {
		addFactor("capability_update", 4, "Auto update", "Auto-update is available")
	}
	if client.AutoRenewal && client.Price > 0 {
		addFactor("renewal_continuity", 5, "Renewal continuity", "Billing is configured for automatic renewal")
	}
	if assessment.hasExpiry && assessment.daysRemaining > 30 {
		addFactor("runway", 4, "Runway", "Asset has more than 30 days of runway")
	}
	if assessment.online {
		addFactor("availability", 5, "Availability", "Asset is currently online")
	}
	if client.AssetIgnored {
		addFactor("ignored_adjustment", -20, "Ignored asset", "Ignored assets are discounted in value ranking")
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	sort.SliceStable(factors, func(i, j int) bool {
		if factors[i].Points == factors[j].Points {
			return factors[i].Label < factors[j].Label
		}
		return factors[i].Points > factors[j].Points
	})
	return score, factors
}
