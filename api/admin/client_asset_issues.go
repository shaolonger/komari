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

type assetIssueFilters struct {
	Provider       string `json:"provider,omitempty"`
	Currency       string `json:"currency,omitempty"`
	Role           string `json:"role,omitempty"`
	IncludeIgnored bool   `json:"include_ignored"`
	Limit          int    `json:"limit"`
}

type assetIssueItem struct {
	UUID                  string   `json:"uuid"`
	Name                  string   `json:"name"`
	Provider              string   `json:"provider"`
	Role                  string   `json:"role"`
	Group                 string   `json:"group"`
	Currency              string   `json:"currency"`
	CurrencyLabel         string   `json:"currency_label"`
	AssetIgnored          bool     `json:"asset_ignored"`
	Online                bool     `json:"online"`
	MonthlyCost           float64  `json:"monthly_cost"`
	AnnualizedCost        float64  `json:"annualized_cost"`
	RemainingValue        float64  `json:"remaining_value"`
	DaysRemaining         *int     `json:"days_remaining,omitempty"`
	MetadataMissingFields []string `json:"metadata_missing_fields,omitempty"`
	IssueReasons          []string `json:"issue_reasons"`
	RiskScore             int      `json:"risk_score"`
	HighRisk              bool     `json:"high_risk"`
	Underused             bool     `json:"underused"`
	ManualRenew           bool     `json:"manual_renew"`
	CapabilityPing        bool     `json:"capability_ping"`
	CapabilityTerminal    bool     `json:"capability_terminal"`
	CapabilityRemoteExec  bool     `json:"capability_remote_exec"`
	CapabilityAutoUpdate  bool     `json:"capability_auto_update"`
}

type assetIssuesResponse struct {
	GeneratedAt      time.Time         `json:"generated_at"`
	Filters          assetIssueFilters `json:"filters"`
	Counts           assetQueueSummary `json:"counts"`
	RenewalAttention []assetIssueItem  `json:"renewal_attention"`
	MetadataGap      []assetIssueItem  `json:"metadata_gap"`
	Underused        []assetIssueItem  `json:"underused"`
	HighRisk         []assetIssueItem  `json:"high_risk"`
}

func GetClientAssetIssues(c *gin.Context) {
	allClients, err := clients.GetAllClientBasicInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			if parsed > 0 && parsed <= 200 {
				limit = parsed
			}
		}
	}

	includeIgnored := true
	if raw := strings.TrimSpace(c.Query("include_ignored")); raw != "" {
		includeIgnored = raw != "false" && raw != "0"
	}

	provider := strings.TrimSpace(c.Query("provider"))
	currency := strings.TrimSpace(c.Query("currency"))
	role := strings.TrimSpace(c.Query("role"))
	allClients = filterAssetSummaryClients(
		allClients,
		provider,
		currency,
		role,
		includeIgnored,
	)

	latest := ws.GetLatestReport()
	onlineSet := make(map[string]bool)
	for _, uuid := range ws.GetAllOnlineUUIDs() {
		onlineSet[uuid] = true
	}

	response := buildClientAssetIssues(
		allClients,
		latest,
		onlineSet,
		time.Now().UTC(),
		assetIssueFilters{
			Provider:       provider,
			Currency:       currency,
			Role:           role,
			IncludeIgnored: includeIgnored,
			Limit:          limit,
		},
	)

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   response,
	})
}

func buildClientAssetIssues(
	allClients []models.Client,
	latest map[string]*common.Report,
	onlineSet map[string]bool,
	now time.Time,
	filters assetIssueFilters,
) assetIssuesResponse {
	response := assetIssuesResponse{
		GeneratedAt: now,
		Filters:     filters,
	}

	for _, client := range allClients {
		report := latest[client.UUID]
		online := onlineSet[client.UUID]
		assessment := assessClientAsset(client, report, online, now)
		item := buildAssetIssueItem(client, report, online, assessment)

		if assessment.hasExpiry && assessment.daysRemaining > 0 && assessment.daysRemaining <= 7 && !client.AssetIgnored {
			response.Counts.RenewalAttention++
			response.RenewalAttention = append(response.RenewalAttention, item)
		}
		if len(item.MetadataMissingFields) > 0 {
			response.Counts.MetadataGap++
			response.MetadataGap = append(response.MetadataGap, item)
		}
		if item.Underused {
			response.Counts.Underused++
			response.Underused = append(response.Underused, item)
		}
		if item.HighRisk {
			response.Counts.HighRisk++
			response.HighRisk = append(response.HighRisk, item)
		}
	}

	sortAssetIssueItems(response.RenewalAttention, "renewal")
	sortAssetIssueItems(response.MetadataGap, "metadata")
	sortAssetIssueItems(response.Underused, "underused")
	sortAssetIssueItems(response.HighRisk, "risk")

	if filters.Limit > 0 {
		response.RenewalAttention = truncateAssetIssueItems(response.RenewalAttention, filters.Limit)
		response.MetadataGap = truncateAssetIssueItems(response.MetadataGap, filters.Limit)
		response.Underused = truncateAssetIssueItems(response.Underused, filters.Limit)
		response.HighRisk = truncateAssetIssueItems(response.HighRisk, filters.Limit)
	}

	return response
}

func buildAssetIssueItem(
	client models.Client,
	report *common.Report,
	online bool,
	assessment assetAssessment,
) assetIssueItem {
	var daysRemaining *int
	if assessment.hasExpiry {
		value := assessment.daysRemaining
		daysRemaining = &value
	}

	return assetIssueItem{
		UUID:                  client.UUID,
		Name:                  client.Name,
		Provider:              providerLabel(client),
		Role:                  roleLabel(client),
		Group:                 client.Group,
		Currency:              currencyKey(client),
		CurrencyLabel:         currencyLabel(client),
		AssetIgnored:          client.AssetIgnored,
		Online:                online,
		MonthlyCost:           assessment.monthlyCost,
		AnnualizedCost:        assessment.annualizedCost,
		RemainingValue:        assessment.remainingValue,
		DaysRemaining:         daysRemaining,
		MetadataMissingFields: metadataMissingFields(client),
		IssueReasons:          buildAssetIssueReasons(client, report, online, assessment),
		RiskScore:             assessment.riskScore,
		HighRisk:              assessment.highRisk,
		Underused:             assessment.underused,
		ManualRenew:           client.Price > 0 && !client.AutoRenewal,
		CapabilityPing:        client.CapabilityPing,
		CapabilityTerminal:    client.CapabilityTerminal,
		CapabilityRemoteExec:  client.CapabilityRemoteExec,
		CapabilityAutoUpdate:  client.CapabilityAutoUpdate,
	}
}

func buildAssetIssueReasons(
	client models.Client,
	report *common.Report,
	online bool,
	assessment assetAssessment,
) []string {
	reasons := make([]string, 0, 8)

	if !online {
		reasons = append(reasons, "offline_or_stale")
	}
	if assessment.hasExpiry && assessment.daysRemaining <= 7 {
		reasons = append(reasons, "renewal_due_7d")
	} else if assessment.hasExpiry && assessment.daysRemaining <= 30 {
		reasons = append(reasons, "renewal_due_30d")
	}
	if assessment.hasExpiry && assessment.daysRemaining <= 30 && client.Price > 0 && !client.AutoRenewal {
		reasons = append(reasons, "manual_renewal")
	}

	trafficPct := trafficPercentage(client, report)
	if trafficPct >= 90 {
		reasons = append(reasons, "traffic_above_90pct")
	} else if trafficPct >= 75 {
		reasons = append(reasons, "traffic_above_75pct")
	}

	if len(metadataMissingFields(client)) > 0 {
		reasons = append(reasons, "metadata_gap")
	}
	if !client.CapabilityPing {
		reasons = append(reasons, "capability_ping_disabled")
	}
	if !client.CapabilityTerminal && !client.CapabilityRemoteExec {
		reasons = append(reasons, "no_remediation_path")
	}
	if !client.CapabilityAutoUpdate {
		reasons = append(reasons, "capability_auto_update_disabled")
	}
	if assessment.underused {
		reasons = append(reasons, "underused_spend")
	}

	return reasons
}

func sortAssetIssueItems(items []assetIssueItem, mode string) {
	sort.Slice(items, func(i, j int) bool {
		switch mode {
		case "renewal":
			iDays := daysRemainingValue(items[i].DaysRemaining)
			jDays := daysRemainingValue(items[j].DaysRemaining)
			if iDays == jDays {
				return items[i].MonthlyCost > items[j].MonthlyCost
			}
			return iDays < jDays
		case "metadata":
			if len(items[i].MetadataMissingFields) == len(items[j].MetadataMissingFields) {
				return items[i].MonthlyCost > items[j].MonthlyCost
			}
			return len(items[i].MetadataMissingFields) > len(items[j].MetadataMissingFields)
		case "underused":
			if items[i].MonthlyCost == items[j].MonthlyCost {
				return items[i].Name < items[j].Name
			}
			return items[i].MonthlyCost > items[j].MonthlyCost
		case "risk":
			fallthrough
		default:
			if items[i].RiskScore == items[j].RiskScore {
				return items[i].MonthlyCost > items[j].MonthlyCost
			}
			return items[i].RiskScore > items[j].RiskScore
		}
	})
}

func truncateAssetIssueItems(items []assetIssueItem, limit int) []assetIssueItem {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
}

func daysRemainingValue(value *int) int {
	if value == nil {
		return int(^uint(0) >> 1)
	}
	return *value
}
