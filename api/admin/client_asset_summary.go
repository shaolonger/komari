package admin

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/ws"
)

type assetProviderSummary struct {
	Name            string  `json:"name"`
	AssetCount      int     `json:"asset_count"`
	BillableAssets  int     `json:"billable_assets"`
	HighRiskAssets  int     `json:"high_risk_assets"`
	UnderusedAssets int     `json:"underused_assets"`
	MonthlyCost     float64 `json:"monthly_cost"`
	AnnualizedCost  float64 `json:"annualized_cost"`
	RemainingValue  float64 `json:"remaining_value"`
}

type assetCurrencySummary struct {
	Key                string  `json:"key"`
	Label              string  `json:"label"`
	AssetCount         int     `json:"asset_count"`
	MonthlyCost        float64 `json:"monthly_cost"`
	AnnualizedCost     float64 `json:"annualized_cost"`
	RemainingValue     float64 `json:"remaining_value"`
	Renewal7dExposure  float64 `json:"renewal_7d_exposure"`
	Renewal30dExposure float64 `json:"renewal_30d_exposure"`
}

type assetLifecycleSummary struct {
	Expired     int `json:"expired"`
	Renewal7d   int `json:"renewal_7d"`
	Renewal30d  int `json:"renewal_30d"`
	Active      int `json:"active"`
	LongTerm    int `json:"long_term"`
	ManualRenew int `json:"manual_renew"`
	Ignored     int `json:"ignored"`
	MetadataGap int `json:"metadata_gap"`
	Underused   int `json:"underused"`
}

type assetQueueSummary struct {
	RenewalAttention int `json:"renewal_attention"`
	MetadataGap      int `json:"metadata_gap"`
	Underused        int `json:"underused"`
	HighRisk         int `json:"high_risk"`
}

type assetPortfolioSummary struct {
	GeneratedAt        time.Time              `json:"generated_at"`
	TotalAssets        int                    `json:"total_assets"`
	BillableAssets     int                    `json:"billable_assets"`
	IgnoredAssets      int                    `json:"ignored_assets"`
	HighRiskAssets     int                    `json:"high_risk_assets"`
	MonthlySpend       float64                `json:"monthly_spend"`
	AnnualizedSpend    float64                `json:"annualized_spend"`
	RemainingValue     float64                `json:"remaining_value"`
	Renewal7dExposure  float64                `json:"renewal_7d_exposure"`
	Renewal30dExposure float64                `json:"renewal_30d_exposure"`
	Lifecycle          assetLifecycleSummary  `json:"lifecycle"`
	Queue              assetQueueSummary      `json:"queue"`
	Providers          []assetProviderSummary `json:"providers"`
	IgnoredProviders   []assetProviderSummary `json:"ignored_providers"`
	Currencies         []assetCurrencySummary `json:"currencies"`
}

type assetAssessment struct {
	monthlyCost    float64
	annualizedCost float64
	remainingValue float64
	daysRemaining  int
	hasExpiry      bool
	metadataGap    bool
	underused      bool
	highRisk       bool
}

func GetClientAssetSummary(c *gin.Context) {
	allClients, err := clients.GetAllClientBasicInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	latest := ws.GetLatestReport()
	onlineSet := make(map[string]bool)
	for _, uuid := range ws.GetAllOnlineUUIDs() {
		onlineSet[uuid] = true
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   buildClientAssetSummary(allClients, latest, onlineSet, time.Now()),
	})
}

func buildClientAssetSummary(
	allClients []models.Client,
	latest map[string]*common.Report,
	onlineSet map[string]bool,
	now time.Time,
) assetPortfolioSummary {
	summary := assetPortfolioSummary{
		GeneratedAt: now,
	}

	providers := make(map[string]*assetProviderSummary)
	ignoredProviders := make(map[string]*assetProviderSummary)
	currencies := make(map[string]*assetCurrencySummary)

	for _, client := range allClients {
		report := latest[client.UUID]
		online := onlineSet[client.UUID]
		assessment := assessClientAsset(client, report, online, now)

		summary.TotalAssets++
		if client.AssetIgnored {
			summary.IgnoredAssets++
			summary.Lifecycle.Ignored++
		}
		if assessment.highRisk {
			summary.HighRiskAssets++
			summary.Queue.HighRisk++
		}
		if assessment.metadataGap {
			summary.Lifecycle.MetadataGap++
			summary.Queue.MetadataGap++
		}
		if assessment.underused {
			summary.Lifecycle.Underused++
			summary.Queue.Underused++
		}
		if client.Price > 0 && !client.AutoRenewal {
			summary.Lifecycle.ManualRenew++
		}
		if assessment.hasExpiry {
			switch {
			case assessment.daysRemaining <= 0:
				summary.Lifecycle.Expired++
			case assessment.daysRemaining <= 7:
				summary.Lifecycle.Renewal7d++
				if !client.AssetIgnored {
					summary.Queue.RenewalAttention++
				}
			case assessment.daysRemaining <= 30:
				summary.Lifecycle.Renewal30d++
			case assessment.daysRemaining > 365:
				summary.Lifecycle.LongTerm++
			default:
				summary.Lifecycle.Active++
			}
		}

		providerName := providerLabel(client)
		targetProviders := providers
		if client.AssetIgnored {
			targetProviders = ignoredProviders
		}
		provider := targetProviders[providerName]
		if provider == nil {
			provider = &assetProviderSummary{Name: providerName}
			targetProviders[providerName] = provider
		}
		provider.AssetCount++
		if client.Price > 0 && !client.AssetIgnored {
			provider.BillableAssets++
		}
		if assessment.highRisk {
			provider.HighRiskAssets++
		}
		if assessment.underused {
			provider.UnderusedAssets++
		}
		provider.MonthlyCost += assessment.monthlyCost
		provider.AnnualizedCost += assessment.annualizedCost
		provider.RemainingValue += assessment.remainingValue

		if client.AssetIgnored {
			continue
		}

		if client.Price > 0 {
			summary.BillableAssets++
		}
		summary.MonthlySpend += assessment.monthlyCost
		summary.AnnualizedSpend += assessment.annualizedCost
		summary.RemainingValue += assessment.remainingValue
		if assessment.hasExpiry && assessment.daysRemaining > 0 && assessment.daysRemaining <= 7 {
			summary.Renewal7dExposure += client.Price
		}
		if assessment.hasExpiry && assessment.daysRemaining > 0 && assessment.daysRemaining <= 30 {
			summary.Renewal30dExposure += client.Price
		}

		currencyKey := currencyKey(client)
		currency := currencies[currencyKey]
		if currency == nil {
			currency = &assetCurrencySummary{
				Key:   currencyKey,
				Label: currencyLabel(client),
			}
			currencies[currencyKey] = currency
		}
		currency.AssetCount++
		currency.MonthlyCost += assessment.monthlyCost
		currency.AnnualizedCost += assessment.annualizedCost
		currency.RemainingValue += assessment.remainingValue
		if assessment.hasExpiry && assessment.daysRemaining > 0 && assessment.daysRemaining <= 7 {
			currency.Renewal7dExposure += client.Price
		}
		if assessment.hasExpiry && assessment.daysRemaining > 0 && assessment.daysRemaining <= 30 {
			currency.Renewal30dExposure += client.Price
		}
	}

	summary.Providers = sortProviderSummaries(providers)
	summary.IgnoredProviders = sortProviderSummaries(ignoredProviders)
	summary.Currencies = sortCurrencySummaries(currencies)

	return summary
}

func assessClientAsset(
	client models.Client,
	report *common.Report,
	online bool,
	now time.Time,
) assetAssessment {
	monthlyCost := monthlyCost(client)
	assessment := assetAssessment{
		monthlyCost:    monthlyCost,
		annualizedCost: monthlyCost * 12,
		remainingValue: remainingValue(client, now),
	}
	daysRemaining, hasExpiry := daysUntilExpiry(client.ExpiredAt, now)
	assessment.daysRemaining = daysRemaining
	assessment.hasExpiry = hasExpiry

	metadataGap := len(metadataMissingFields(client)) > 0
	assessment.metadataGap = metadataGap

	cpuUsage := 0.0
	memoryUsage := 0.0
	trafficPct := 0.0
	if report != nil {
		cpuUsage = report.CPU.Usage
		if client.MemTotal > 0 {
			memoryUsage = float64(report.Ram.Used) / float64(client.MemTotal) * 100
		}
		trafficPct = trafficPercentage(client, report)
	}

	assessment.underused =
		online &&
			client.Price > 0 &&
			!client.AssetIgnored &&
			hasExpiry &&
			daysRemaining > 30 &&
			cpuUsage < 10 &&
			memoryUsage < 25 &&
			trafficPct < 15

	riskScore := 0
	if !online {
		riskScore += 4
	}
	if hasExpiry && daysRemaining <= 7 {
		riskScore += 4
	} else if hasExpiry && daysRemaining <= 30 {
		riskScore += 2
	}
	if hasExpiry && daysRemaining <= 30 && !client.AutoRenewal && client.Price > 0 {
		riskScore += 3
	}
	if trafficPct >= 90 {
		riskScore += 3
	} else if trafficPct >= 75 {
		riskScore += 1
	}
	if metadataGap {
		riskScore += 1
	}
	if !client.CapabilityPing {
		riskScore += 1
	}
	if !client.CapabilityTerminal && !client.CapabilityRemoteExec {
		riskScore += 2
	}
	if !client.CapabilityAutoUpdate {
		riskScore += 1
	}
	if assessment.underused {
		riskScore += 2
	}

	assessment.highRisk = riskScore >= 5
	return assessment
}

func metadataMissingFields(client models.Client) []string {
	fields := make([]string, 0, 4)
	if strings.TrimSpace(client.Provider) == "" {
		fields = append(fields, "provider")
	}
	if strings.TrimSpace(client.BusinessRole) == "" {
		fields = append(fields, "business_role")
	}
	if strings.TrimSpace(client.CurrencyCode) == "" {
		fields = append(fields, "currency_code")
	}
	if client.ExpiredAt.ToTime().IsZero() {
		fields = append(fields, "expired_at")
	}
	return fields
}

func providerLabel(client models.Client) string {
	if value := strings.TrimSpace(client.Provider); value != "" {
		return value
	}
	if value := strings.TrimSpace(client.Group); value != "" {
		return value
	}
	return "Unassigned"
}

func currencyKey(client models.Client) string {
	if value := strings.TrimSpace(client.CurrencyCode); value != "" {
		return strings.ToUpper(value)
	}
	if value := strings.TrimSpace(client.Currency); value != "" {
		return value
	}
	return "?"
}

func currencyLabel(client models.Client) string {
	if value := strings.TrimSpace(client.Currency); value != "" {
		return value
	}
	if value := strings.TrimSpace(client.CurrencyCode); value != "" {
		return strings.ToUpper(value)
	}
	return "?"
}

func monthlyCost(client models.Client) float64 {
	if client.Price <= 0 || client.BillingCycle <= 0 {
		return 0
	}
	return client.Price * 30 / float64(client.BillingCycle)
}

func remainingValue(client models.Client, now time.Time) float64 {
	if client.Price <= 0 || client.BillingCycle <= 0 {
		return 0
	}
	daysRemaining, ok := daysUntilExpiry(client.ExpiredAt, now)
	if !ok || daysRemaining <= 0 {
		return 0
	}
	return client.Price * float64(daysRemaining) / float64(client.BillingCycle)
}

func daysUntilExpiry(expiredAt models.LocalTime, now time.Time) (int, bool) {
	expiry := expiredAt.ToTime()
	if expiry.IsZero() {
		return 0, false
	}
	return int(math.Ceil(expiry.Sub(now).Hours() / 24)), true
}

func trafficPercentage(client models.Client, report *common.Report) float64 {
	if report == nil || client.TrafficLimit <= 0 {
		return 0
	}
	used := trafficUsedByType(
		strings.ToLower(client.TrafficLimitType),
		report.Network.TotalUp,
		report.Network.TotalDown,
	)
	if used <= 0 {
		return 0
	}
	return float64(used) / float64(client.TrafficLimit) * 100
}

func trafficUsedByType(limitType string, up, down int64) int64 {
	switch limitType {
	case "up":
		return up
	case "down":
		return down
	case "sum":
		return up + down
	case "min":
		if up < down {
			return up
		}
		return down
	case "max":
		fallthrough
	default:
		if up > down {
			return up
		}
		return down
	}
}

func sortProviderSummaries(source map[string]*assetProviderSummary) []assetProviderSummary {
	items := make([]assetProviderSummary, 0, len(source))
	for _, item := range source {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].MonthlyCost == items[j].MonthlyCost {
			return items[i].Name < items[j].Name
		}
		return items[i].MonthlyCost > items[j].MonthlyCost
	})
	return items
}

func sortCurrencySummaries(source map[string]*assetCurrencySummary) []assetCurrencySummary {
	items := make([]assetCurrencySummary, 0, len(source))
	for _, item := range source {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].MonthlyCost == items[j].MonthlyCost {
			return items[i].Key < items[j].Key
		}
		return items[i].MonthlyCost > items[j].MonthlyCost
	})
	return items
}
