package admin

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/ws"
)

type assetInventoryFilters struct {
	Provider       string `json:"provider,omitempty"`
	Currency       string `json:"currency,omitempty"`
	Role           string `json:"role,omitempty"`
	IncludeIgnored bool   `json:"include_ignored"`
	Filter         string `json:"filter"`
	Sort           string `json:"sort"`
	Order          string `json:"order"`
	Limit          int    `json:"limit"`
}

type assetInventoryItem struct {
	UUID                  string   `json:"uuid"`
	Name                  string   `json:"name"`
	Provider              string   `json:"provider"`
	Role                  string   `json:"role"`
	Group                 string   `json:"group"`
	Currency              string   `json:"currency"`
	CurrencyLabel         string   `json:"currency_label"`
	Price                 float64  `json:"price"`
	BillingCycle          int      `json:"billing_cycle"`
	AutoRenewal           bool     `json:"auto_renewal"`
	AssetIgnored          bool     `json:"asset_ignored"`
	Online                bool     `json:"online"`
	CPUUsage              float64  `json:"cpu_usage"`
	MemoryUsage           float64  `json:"memory_usage"`
	TrafficPercentage     float64  `json:"traffic_percentage"`
	MonthlyCost           float64  `json:"monthly_cost"`
	AnnualizedCost        float64  `json:"annualized_cost"`
	RemainingValue        float64  `json:"remaining_value"`
	EfficiencyScore       float64  `json:"efficiency_score"`
	DaysRemaining         *int     `json:"days_remaining,omitempty"`
	MetadataMissingFields []string `json:"metadata_missing_fields,omitempty"`
	RiskReasons           []string `json:"risk_reasons"`
	RiskScore             int      `json:"risk_score"`
	HighRisk              bool     `json:"high_risk"`
	Underused             bool     `json:"underused"`
}

type assetInventoryResponse struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Filters     assetInventoryFilters `json:"filters"`
	Total       int                   `json:"total"`
	Items       []assetInventoryItem  `json:"items"`
}

func GetClientAssetInventory(c *gin.Context) {
	allClients, err := clients.GetAllClientBasicInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	includeIgnored := true
	if raw := strings.TrimSpace(c.Query("include_ignored")); raw != "" {
		includeIgnored = raw != "false" && raw != "0"
	}

	filters := assetInventoryFilters{
		Provider:       strings.TrimSpace(c.Query("provider")),
		Currency:       strings.TrimSpace(c.Query("currency")),
		Role:           strings.TrimSpace(c.Query("role")),
		IncludeIgnored: includeIgnored,
		Filter:         normalizedInventoryFilter(c.Query("filter")),
		Sort:           normalizedInventorySort(c.Query("sort")),
		Order:          normalizedInventoryOrder(c.Query("order")),
		Limit:          limit,
	}

	allClients = filterAssetSummaryClients(
		allClients,
		filters.Provider,
		filters.Currency,
		filters.Role,
		filters.IncludeIgnored,
	)

	latest := ws.GetLatestReport()
	onlineSet := make(map[string]bool)
	for _, uuid := range ws.GetAllOnlineUUIDs() {
		onlineSet[uuid] = true
	}

	response := buildClientAssetInventory(allClients, latest, onlineSet, time.Now().UTC(), filters)
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   response,
	})
}

func buildClientAssetInventory(
	allClients []models.Client,
	latest map[string]*common.Report,
	onlineSet map[string]bool,
	now time.Time,
	filters assetInventoryFilters,
) assetInventoryResponse {
	items := make([]assetInventoryItem, 0, len(allClients))
	for _, client := range allClients {
		report := latest[client.UUID]
		online := onlineSet[client.UUID]
		assessment := assessClientAsset(client, report, online, now)
		item := buildAssetInventoryItem(client, report, online, assessment)
		if !matchesInventoryFilter(item, filters.Filter) {
			continue
		}
		items = append(items, item)
	}

	sortAssetInventoryItems(items, filters.Sort, filters.Order)
	total := len(items)
	if filters.Limit > 0 && len(items) > filters.Limit {
		items = items[:filters.Limit]
	}

	return assetInventoryResponse{
		GeneratedAt: now,
		Filters:     filters,
		Total:       total,
		Items:       items,
	}
}

func buildAssetInventoryItem(
	client models.Client,
	report *common.Report,
	online bool,
	assessment assetAssessment,
) assetInventoryItem {
	var daysRemaining *int
	if assessment.hasExpiry {
		value := assessment.daysRemaining
		daysRemaining = &value
	}

	return assetInventoryItem{
		UUID:                  client.UUID,
		Name:                  client.Name,
		Provider:              providerLabel(client),
		Role:                  roleLabel(client),
		Group:                 client.Group,
		Currency:              currencyKey(client),
		CurrencyLabel:         currencyLabel(client),
		Price:                 client.Price,
		BillingCycle:          client.BillingCycle,
		AutoRenewal:           client.AutoRenewal,
		AssetIgnored:          client.AssetIgnored,
		Online:                online,
		CPUUsage:              assessment.cpuUsage,
		MemoryUsage:           assessment.memoryUsage,
		TrafficPercentage:     assessment.trafficPct,
		MonthlyCost:           assessment.monthlyCost,
		AnnualizedCost:        assessment.annualizedCost,
		RemainingValue:        assessment.remainingValue,
		EfficiencyScore:       assessment.efficiencyScore,
		DaysRemaining:         daysRemaining,
		MetadataMissingFields: metadataMissingFields(client),
		RiskReasons:           buildAssetIssueReasons(client, report, online, assessment),
		RiskScore:             assessment.riskScore,
		HighRisk:              assessment.highRisk,
		Underused:             assessment.underused,
	}
}

func matchesInventoryFilter(item assetInventoryItem, filter string) bool {
	switch filter {
	case "high":
		return item.HighRisk
	case "expiring":
		return item.DaysRemaining != nil && *item.DaysRemaining <= 30
	case "manual":
		return item.Price > 0 && !item.AutoRenewal && item.DaysRemaining != nil && *item.DaysRemaining <= 30
	case "ignored":
		return item.AssetIgnored
	case "metadata":
		return len(item.MetadataMissingFields) > 0
	case "underused":
		return item.Underused
	case "all":
		fallthrough
	default:
		return true
	}
}

func sortAssetInventoryItems(items []assetInventoryItem, sortMode string, order string) {
	sort.Slice(items, func(i, j int) bool {
		less := false
		switch sortMode {
		case "monthly":
			less = items[i].MonthlyCost < items[j].MonthlyCost
		case "remaining":
			less = items[i].RemainingValue < items[j].RemainingValue
		case "expiry":
			less = daysRemainingValue(items[i].DaysRemaining) < daysRemainingValue(items[j].DaysRemaining)
		case "efficiency":
			less = items[i].EfficiencyScore < items[j].EfficiencyScore
		case "name":
			less = items[i].Name < items[j].Name
		case "risk":
			fallthrough
		default:
			if items[i].RiskScore == items[j].RiskScore {
				less = items[i].MonthlyCost < items[j].MonthlyCost
			} else {
				less = items[i].RiskScore < items[j].RiskScore
			}
		}

		if order == "asc" {
			return less
		}
		return !less
	})
}

func normalizedInventoryFilter(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high", "expiring", "manual", "ignored", "metadata", "underused":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "all"
	}
}

func normalizedInventorySort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "monthly", "remaining", "expiry", "efficiency", "name":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "risk"
	}
}

func normalizedInventoryOrder(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "asc") {
		return "asc"
	}
	return "desc"
}
