package clients

import (
	"errors"
	"log"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/internal/historycache"
	"github.com/komari-monitor/komari/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"fmt"

	"github.com/google/uuid"
)

var currencyCodePattern = regexp.MustCompile(`^[A-Z]{3}$`)
var capabilityBooleanFields = []string{
	"capability_ping",
	"capability_terminal",
	"capability_remote_exec",
	"capability_remote_control",
	"capability_gpu",
	"capability_auto_update",
	"capability_private_ping_targets",
}

func normalizeGovernanceStatusValue(value string) string {
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

type ClientTokenStatus struct {
	Token     string           `json:"token"`
	IssuedAt  models.LocalTime `json:"issued_at"`
	ExpiresAt models.LocalTime `json:"expires_at"`
	RevokedAt models.LocalTime `json:"revoked_at"`
	Active    bool             `json:"active"`
}

func clientTokenExpiryFromHours(expiresInHours int64, now time.Time) (models.LocalTime, error) {
	if expiresInHours < 0 {
		return models.LocalTime(time.Time{}), errors.New("expires_in_hours must be zero or positive")
	}
	if expiresInHours == 0 {
		return models.LocalTime(time.Time{}), nil
	}
	return models.FromTime(now.Add(time.Duration(expiresInHours) * time.Hour)), nil
}

func buildClientTokenStatus(client models.Client) ClientTokenStatus {
	return ClientTokenStatus{
		Token:     client.Token,
		IssuedAt:  client.TokenIssuedAt,
		ExpiresAt: client.TokenExpiresAt,
		RevokedAt: client.TokenRevokedAt,
		Active:    validateClientTokenState(client, time.Now()) == nil,
	}
}

func zeroLocalTime() models.LocalTime {
	return models.LocalTime(time.Time{})
}

func writeClientTokenLifecycle(clientUUID, token string, issuedAt, expiresAt, revokedAt models.LocalTime) (ClientTokenStatus, error) {
	db := dbcore.GetDBInstance()
	updates := map[string]interface{}{
		"token":            token,
		"token_issued_at":  issuedAt,
		"token_expires_at": expiresAt,
		"token_revoked_at": revokedAt,
		"updated_at":       time.Now(),
	}
	var oldToken string
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Client{}).Select("token").Where("uuid = ?", clientUUID).Scan(&oldToken).Error; err != nil {
			return err
		}
		result := tx.Model(&models.Client{}).Where("uuid = ?", clientUUID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}); err != nil {
		return ClientTokenStatus{}, err
	}
	invalidateClientCredential(oldToken, token)
	return GetClientTokenStatusByUUID(clientUUID)
}

// Deprecated: DeleteClientConfig is deprecated and will be removed in a future release. Use DeleteClient instead.
func DeleteClientConfig(clientUuid string) error {
	db := dbcore.GetDBInstance()
	err := db.Delete(&common.ClientConfig{ClientUUID: clientUuid}).Error
	if err != nil {
		return err
	}
	return nil
}
func DeleteClient(clientUuid string) error {
	db := dbcore.GetDBInstance()
	var oldToken string
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Client{}).Select("token").Where("uuid = ?", clientUuid).Scan(&oldToken).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Client{}, "uuid = ?", clientUuid).Error
	})
	if err != nil {
		return err
	}
	invalidateClientCredential(oldToken)
	historycache.Invalidate()
	return nil
}

// Deprecated: UpdateOrInsertBasicInfo is deprecated and will be removed in a future release. Use SaveClientInfo instead.
func UpdateOrInsertBasicInfo(cbi common.ClientInfo) error {
	db := dbcore.GetDBInstance()
	updates := make(map[string]interface{})

	if cbi.Name != "" {
		updates["name"] = cbi.Name
	}
	if cbi.CpuName != "" {
		updates["cpu_name"] = cbi.CpuName
	}
	if cbi.Arch != "" {
		updates["arch"] = cbi.Arch
	}
	if cbi.CpuCores > 0 || cbi.CpuCores < math.MaxInt-1 {
		updates["cpu_cores"] = cbi.CpuCores
	}
	if cbi.OS != "" {
		updates["os"] = cbi.OS
	}
	if cbi.GpuName != "" {
		updates["gpu_name"] = cbi.GpuName
	}
	if cbi.IPv4 != "" {
		updates["ipv4"] = cbi.IPv4
	}
	if cbi.IPv6 != "" {
		updates["ipv6"] = cbi.IPv6
	}
	if cbi.Region != "" {
		updates["region"] = cbi.Region
	}
	if cbi.Remark != "" {
		updates["remark"] = cbi.Remark
	}
	updates["mem_total"] = cbi.MemTotal
	updates["swap_total"] = cbi.SwapTotal
	updates["disk_total"] = cbi.DiskTotal
	updates["version"] = cbi.Version
	updates["updated_at"] = time.Now()

	// 转换为更新Client表
	client := models.Client{
		UUID: cbi.UUID,
	}

	err := db.Model(&client).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "uuid"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(map[string]interface{}{
		"uuid":       cbi.UUID,
		"name":       cbi.Name,
		"cpu_name":   cbi.CpuName,
		"arch":       cbi.Arch,
		"cpu_cores":  cbi.CpuCores,
		"os":         cbi.OS,
		"gpu_name":   cbi.GpuName,
		"ipv4":       cbi.IPv4,
		"ipv6":       cbi.IPv6,
		"region":     cbi.Region,
		"remark":     cbi.Remark,
		"mem_total":  cbi.MemTotal,
		"swap_total": cbi.SwapTotal,
		"disk_total": cbi.DiskTotal,
		"version":    cbi.Version,
		"updated_at": time.Now(),
	}).Error

	if err != nil {
		return err
	}
	historycache.Invalidate()
	return nil
}
func SaveClientInfo(update map[string]interface{}) error {
	db := dbcore.GetDBInstance()
	clientUUID, ok := update["uuid"].(string)
	if !ok || clientUUID == "" {
		return fmt.Errorf("invalid client UUID")
	}

	// 确保更新的字段不为空
	if len(update) == 0 {
		return fmt.Errorf("no fields to update")
	}

	update["updated_at"] = time.Now()

	checkInt64 := func(name string, val float64) error {
		if val < 0 {
			return fmt.Errorf("%s must be non-negative, got %d", name, int64(val))
		}
		if val > math.MaxInt64-1 {
			return fmt.Errorf("%s exceeds int64 max limit: %d", name, int64(val))
		}
		return nil
	}

	verify := func(update map[string]interface{}) error {
		if update["cpu_cores"].(float64) < 0 || update["cpu_cores"].(float64) > math.MaxInt-1 {
			return fmt.Errorf("Cpu.Cores be not a valid int64 number: %d", update["cpu_cores"])
		}
		if err := checkInt64("Ram.Total", update["mem_total"].(float64)); err != nil {
			return err
		}
		if err := checkInt64("Swap.Total", update["swap_total"].(float64)); err != nil {
			return err
		}
		if err := checkInt64("Disk.Total", update["disk_total"].(float64)); err != nil {
			return err
		}
		return nil
	}

	if err := verify(update); err != nil {
		return err
	}
	if err := normalizeCapabilityMetadata(update); err != nil {
		return err
	}

	err := db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Updates(update).Error
	if err != nil {
		return err
	}
	historycache.Invalidate()
	return nil
}

// 更新客户端设置
func UpdateClientConfig(config common.ClientConfig) error {
	db := dbcore.GetDBInstance()
	err := db.Save(&config).Error
	if err != nil {
		return err
	}
	historycache.Invalidate()
	return nil
}

func EditClientName(clientUUID, clientName string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Update("name", clientName).Error
	if err != nil {
		return err
	}
	historycache.Invalidate()
	return nil
}

/*
// UpdateClientByUUID 更新指定 UUID 的客户端配置

	func UpdateClientByUUID(config common.ClientConfig) error {
		db := dbcore.GetDBInstance()
		result := db.Model(&common.ClientConfig{}).Where("client_uuid = ?", config.ClientUUID).Updates(config)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}
*/
func EditClientToken(clientUUID, token string) error {
	_, err := writeClientTokenLifecycle(clientUUID, token, models.FromTime(time.Now()), zeroLocalTime(), zeroLocalTime())
	return err
}

// CreateClient 创建新客户端
func CreateClient() (clientUUID, token string, err error) {
	db := dbcore.GetDBInstance()
	token = utils.GenerateToken()
	clientUUID = uuid.New().String()

	client := models.Client{
		UUID:          clientUUID,
		Token:         token,
		TokenIssuedAt: models.FromTime(time.Now()),
		Name:          "client_" + clientUUID[0:8],
		CreatedAt:     models.FromTime(time.Now()),
		UpdatedAt:     models.FromTime(time.Now()),
	}

	err = db.Create(&client).Error
	if err != nil {
		return "", "", err
	}
	invalidateClientCredential(token)
	historycache.Invalidate()
	if err := tasks.AddDefaultOnClientUUID(clientUUID); err != nil {
		log.Println("Failed to apply default-on ping tasks to new client:", err)
	}
	return clientUUID, token, nil
}

func CreateClientWithName(name string) (clientUUID, token string, err error) {
	if name == "" {
		return CreateClient()
	}
	db := dbcore.GetDBInstance()
	token = utils.GenerateToken()
	clientUUID = uuid.New().String()
	client := models.Client{
		UUID:          clientUUID,
		Token:         token,
		TokenIssuedAt: models.FromTime(time.Now()),
		Name:          name,
		CreatedAt:     models.FromTime(time.Now()),
		UpdatedAt:     models.FromTime(time.Now()),
	}

	err = db.Create(&client).Error
	if err != nil {
		return "", "", err
	}
	invalidateClientCredential(token)
	historycache.Invalidate()
	if err := tasks.AddDefaultOnClientUUID(clientUUID); err != nil {
		log.Println("Failed to apply default-on ping tasks to new client:", err)
	}
	return clientUUID, token, nil
}

/*
// GetAllClients 获取所有客户端配置

	func getAllClients() (clients []models.Client, err error) {
		db := dbcore.GetDBInstance()
		err = db.Find(&clients).Error
		if err != nil {
			return nil, err
		}
		return clients, nil
	}
*/
func GetClientByUUID(uuid string) (client models.Client, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&client).Error
	if err != nil {
		return models.Client{}, err
	}
	return client, nil
}

// GetClientBasicInfo 获取指定 UUID 的客户端基本信息
func GetClientBasicInfo(uuid string) (client models.Client, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&client).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.Client{}, fmt.Errorf("客户端不存在: %s", uuid)
		}
		return models.Client{}, err
	}
	return client, nil
}

func GetClientTokenByUUID(uuid string) (token string, err error) {
	db := dbcore.GetDBInstance()
	var client models.Client
	err = db.Where("uuid = ?", uuid).First(&client).Error
	if err != nil {
		return "", err
	}
	return client.Token, nil
}

func GetClientTokenStatusByUUID(uuid string) (ClientTokenStatus, error) {
	client, err := GetClientByUUID(uuid)
	if err != nil {
		return ClientTokenStatus{}, err
	}
	return buildClientTokenStatus(client), nil
}

func RotateClientToken(clientUUID string, expiresInHours int64) (ClientTokenStatus, error) {
	now := time.Now()
	expiresAt, err := clientTokenExpiryFromHours(expiresInHours, now)
	if err != nil {
		return ClientTokenStatus{}, err
	}
	return writeClientTokenLifecycle(clientUUID, utils.GenerateToken(), models.FromTime(now), expiresAt, zeroLocalTime())
}

func ReissueClientToken(clientUUID string, expiresInHours int64) (ClientTokenStatus, error) {
	return RotateClientToken(clientUUID, expiresInHours)
}

func RevokeClientToken(clientUUID string) (ClientTokenStatus, error) {
	status, err := GetClientTokenStatusByUUID(clientUUID)
	if err != nil {
		return ClientTokenStatus{}, err
	}
	return writeClientTokenLifecycle(clientUUID, status.Token, status.IssuedAt, status.ExpiresAt, models.FromTime(time.Now()))
}

func GetAllClientBasicInfo() (clients []models.Client, err error) {
	db := dbcore.GetDBInstance()
	err = db.Find(&clients).Error
	if err != nil {
		return nil, err
	}
	return clients, nil
}

func normalizeStringField(updates map[string]interface{}, key string, maxLen int) error {
	value, exists := updates[key]
	if !exists {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", key)
	}
	text = strings.TrimSpace(text)
	if maxLen > 0 && len(text) > maxLen {
		return fmt.Errorf("%s exceeds max length %d", key, maxLen)
	}
	updates[key] = text
	return nil
}

func normalizeAssetMetadata(updates map[string]interface{}) error {
	if err := normalizeStringField(updates, "provider", 100); err != nil {
		return err
	}
	if err := normalizeStringField(updates, "business_role", 100); err != nil {
		return err
	}
	if err := normalizeStringField(updates, "currency", 20); err != nil {
		return err
	}
	if err := normalizeStringField(updates, "currency_code", 10); err != nil {
		return err
	}
	if value, exists := updates["currency_code"]; exists {
		code := strings.ToUpper(value.(string))
		if code != "" && !currencyCodePattern.MatchString(code) {
			return fmt.Errorf("currency_code must be a 3-letter ISO code")
		}
		updates["currency_code"] = code
	}
	if value, exists := updates["asset_ignored"]; exists {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("asset_ignored must be a boolean")
		}
	}
	if err := normalizeStringField(updates, "governance_status", 20); err != nil {
		return err
	}
	if value, exists := updates["governance_status"]; exists {
		updates["governance_status"] = normalizeGovernanceStatusValue(value.(string))
	}
	if err := normalizeStringField(updates, "governance_note", 2000); err != nil {
		return err
	}
	return nil
}

func normalizeBooleanField(updates map[string]interface{}, key string) error {
	value, exists := updates[key]
	if !exists {
		return nil
	}
	booleanValue, ok := value.(bool)
	if !ok {
		return fmt.Errorf("%s must be a boolean", key)
	}
	updates[key] = booleanValue
	return nil
}

func normalizeCapabilityMetadata(updates map[string]interface{}) error {
	for _, key := range capabilityBooleanFields {
		if err := normalizeBooleanField(updates, key); err != nil {
			return err
		}
	}
	return nil
}

func SaveClient(updates map[string]interface{}) error {
	db := dbcore.GetDBInstance()
	clientUUID, ok := updates["uuid"].(string)
	if !ok || clientUUID == "" {
		return fmt.Errorf("invalid client UUID")
	}

	// 确保更新的字段不为空
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	if v, exists := updates["traffic_limit"]; exists {
		if val, ok := v.(float64); ok {
			if val < 0 || val > math.MaxInt64-1 {
				return fmt.Errorf("traffic_limit must be a valid non-negative int64 value, got %v", val)
			}
		}
	}
	if err := normalizeAssetMetadata(updates); err != nil {
		return err
	}

	updates["updated_at"] = time.Now()

	newToken, tokenChanged := updates["token"].(string)
	if _, exists := updates["token"]; exists && !tokenChanged {
		return fmt.Errorf("token must be a string")
	}
	var oldToken string
	var err error
	if tokenChanged {
		err = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.Client{}).Select("token").Where("uuid = ?", clientUUID).Scan(&oldToken).Error; err != nil {
				return err
			}
			return tx.Model(&models.Client{}).Where("uuid = ?", clientUUID).Updates(updates).Error
		})
	} else {
		err = db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Updates(updates).Error
	}
	if err != nil {
		return err
	}
	if tokenChanged {
		invalidateClientCredential(oldToken, newToken)
	}
	historycache.Invalidate()
	return nil
}
