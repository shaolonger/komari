package config

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
)

type adminSettingKind uint8

const (
	adminBool adminSettingKind = iota
	adminString
	adminInteger
	adminNumber
)

type adminSettingSpec struct {
	kind      adminSettingKind
	secret    bool
	maxLength int
	min       float64
	max       float64
	validate  func(any) error
}

var safeTablePrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

// adminSettingSpecs is deliberately explicit. The generic config store also
// contains internal state (migration checkpoints, cached snapshots and share
// tokens) that must never become writable or readable through /admin/settings.
var adminSettingSpecs = map[string]adminSettingSpec{
	SitenameKey:                   {kind: adminString, maxLength: 256},
	DescriptionKey:                {kind: adminString, maxLength: 4096},
	AllowCorsKey:                  {kind: adminBool},
	ThemeKey:                      {kind: adminString, maxLength: 128},
	PrivateSiteKey:                {kind: adminBool},
	ApiKeyKey:                     {kind: adminString, secret: true, maxLength: 4096},
	AutoDiscoveryKeyKey:           {kind: adminString, secret: true, maxLength: 4096},
	ScriptDomainKey:               {kind: adminString, maxLength: 2048},
	SendIpAddrToGuestKey:          {kind: adminBool},
	EulaAcceptedKey:               {kind: adminBool},
	BaseScriptsURLKey:             {kind: adminString, maxLength: 2048},
	GeoIpEnabledKey:               {kind: adminBool},
	GeoIpProviderKey:              {kind: adminString, maxLength: 64},
	NezhaCompatEnabledKey:         {kind: adminBool},
	NezhaCompatListenKey:          {kind: adminString, maxLength: 512},
	OAuthEnabledKey:               {kind: adminBool},
	OAuthProviderKey:              {kind: adminString, maxLength: 128},
	"o_auth_client_id":            {kind: adminString, maxLength: 4096},
	"o_auth_client_secret":        {kind: adminString, secret: true, maxLength: 4096},
	DisablePasswordLoginKey:       {kind: adminBool},
	CloudflareTunnelTokenKey:      {kind: adminString, secret: true, maxLength: 16384},
	CustomHeadKey:                 {kind: adminString, maxLength: 0},
	CustomBodyKey:                 {kind: adminString, maxLength: 0},
	NotificationEnabledKey:        {kind: adminBool},
	NotificationMethodKey:         {kind: adminString, maxLength: 128},
	NotificationTemplateKey:       {kind: adminString, maxLength: 65536},
	NotificationTimezoneKey:       {kind: adminString, maxLength: 128},
	NotificationReportSendHourKey: {kind: adminInteger, min: 0, max: 23},
	ExpireNotificationEnabledKey:  {kind: adminBool},
	ExpireNotificationLeadDaysKey: {kind: adminInteger, min: 0, max: 3650},
	LoginNotificationKey:          {kind: adminBool},
	TrafficLimitPercentageKey:     {kind: adminNumber, min: 0, max: 100},
	RecordEnabledKey:              {kind: adminBool},
	RecordPreserveTimeKey:         {kind: adminInteger, min: 1, max: 876000},
	PingRecordPreserveTimeKey:     {kind: adminInteger, min: 1, max: 876000},
	"cors_origin_check_enabled":   {kind: adminBool},
	"cors_allowed_origins":        {kind: adminString, maxLength: 32768},
	"ws_origin_check_enabled":     {kind: adminBool},
	"ws_allowed_origins":          {kind: adminString, maxLength: 32768},
	"low_resource_mode":           {kind: adminBool},
	"metric_db_dsn":               {kind: adminString, secret: true, maxLength: 16384},
	"metric_retention_days":       {kind: adminInteger, min: 1, max: 3650},
	"metric_table_prefix": {kind: adminString, maxLength: 32, validate: func(value any) error {
		if !safeTablePrefix.MatchString(value.(string)) {
			return fmt.Errorf("must start with a letter and contain only letters, digits, or underscores")
		}
		return nil
	}},
	"metric_max_open_conns": {kind: adminInteger, min: 1, max: 64},
	"metric_max_idle_conns": {kind: adminInteger, min: 0, max: 64},
}

// AdminSettings returns only UI-owned settings. Secrets are omitted, rather
// than replaced by a sentinel, so clients that merge and resubmit the response
// cannot accidentally erase or disclose an existing credential.
func AdminSettings(source map[string]any) map[string]any {
	result := make(map[string]any, len(adminSettingSpecs))
	for key, spec := range adminSettingSpecs {
		if spec.secret {
			continue
		}
		if key == CustomHeadKey || key == CustomBodyKey {
			result[key] = ""
			continue
		}
		if value, ok := source[key]; ok {
			result[key] = cloneConfigValue(value)
		}
	}
	return result
}

// ValidateAdminSettings creates a safe copy suitable for SetMany. It rejects
// unknown keys before any write, validates JSON types and enforces bounded
// values so a typo or hostile client cannot create hidden configuration state.
func ValidateAdminSettings(source map[string]any) (map[string]any, error) {
	validated := make(map[string]any, len(source))
	for key, value := range source {
		spec, ok := adminSettingSpecs[key]
		if !ok {
			return nil, fmt.Errorf("unsupported setting %q", key)
		}
		if err := validateAdminSetting(key, value, spec); err != nil {
			return nil, err
		}
		if key == CustomHeadKey || key == CustomBodyKey {
			validated[key] = ""
			continue
		}
		validated[key] = cloneConfigValue(value)
	}
	return validated, nil
}

func validateAdminSetting(key string, value any, spec adminSettingSpec) error {
	switch spec.kind {
	case adminBool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("setting %q must be a boolean", key)
		}
	case adminString:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("setting %q must be a string", key)
		}
		if spec.maxLength == 0 && text != "" && key != CustomHeadKey && key != CustomBodyKey {
			return fmt.Errorf("setting %q must be empty", key)
		}
		if spec.maxLength > 0 && len(text) > spec.maxLength {
			return fmt.Errorf("setting %q exceeds %d bytes", key, spec.maxLength)
		}
	case adminInteger, adminNumber:
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("setting %q must be a finite number", key)
		}
		if spec.kind == adminInteger && math.Trunc(number) != number {
			return fmt.Errorf("setting %q must be an integer", key)
		}
		if number < spec.min || number > spec.max {
			return fmt.Errorf("setting %q must be between %s and %s", key, formatBound(spec.min), formatBound(spec.max))
		}
	default:
		return fmt.Errorf("setting %q has an invalid schema", key)
	}
	if spec.validate != nil {
		if err := spec.validate(value); err != nil {
			return fmt.Errorf("invalid setting %q: %w", key, err)
		}
	}
	return nil
}

func formatBound(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
