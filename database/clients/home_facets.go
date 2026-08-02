package clients

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

type HomeFacetValues map[string][]string

type HomeFacetClientRow struct {
	UUID         string          `json:"uuid"`
	Name         string          `json:"name"`
	Group        string          `json:"group"`
	Region       string          `json:"region"`
	Provider     string          `json:"provider"`
	BusinessRole string          `json:"business_role"`
	Tags         string          `json:"tags"`
	HomeFacets   HomeFacetValues `json:"home_facets"`
}

var homeFacetIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,47}$`)
var homeFacetSplitPattern = regexp.MustCompile(`[;,\n，、]+`)

func normalizeHomeFacetValues(value interface{}) ([]string, error) {
	var rawValues []interface{}
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		parts := homeFacetSplitPattern.Split(typed, -1)
		rawValues = make([]interface{}, 0, len(parts))
		for _, part := range parts {
			rawValues = append(rawValues, part)
		}
	case []string:
		rawValues = make([]interface{}, 0, len(typed))
		for _, item := range typed {
			rawValues = append(rawValues, item)
		}
	case []interface{}:
		rawValues = typed
	default:
		return nil, fmt.Errorf("facet values must be a string or array")
	}

	seen := make(map[string]struct{}, len(rawValues))
	result := make([]string, 0, len(rawValues))
	for _, raw := range rawValues {
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("facet value must be a string")
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(value) > 100 {
			return nil, fmt.Errorf("facet value exceeds max length 100")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result, nil
}

func NormalizeHomeFacets(value interface{}) (HomeFacetValues, error) {
	if value == nil {
		return HomeFacetValues{}, nil
	}

	raw, ok := value.(map[string]interface{})
	if !ok {
		if typed, ok := value.(HomeFacetValues); ok {
			raw = make(map[string]interface{}, len(typed))
			for key, values := range typed {
				raw[key] = values
			}
		} else {
			return nil, fmt.Errorf("home_facets must be an object")
		}
	}

	result := HomeFacetValues{}
	for key, rawValues := range raw {
		id := strings.TrimSpace(key)
		if id == "" {
			continue
		}
		if !homeFacetIDPattern.MatchString(id) {
			return nil, fmt.Errorf("invalid facet dimension: %s", id)
		}
		values, err := normalizeHomeFacetValues(rawValues)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		if len(values) > 0 {
			result[id] = values
		}
	}

	return result, nil
}

func EncodeHomeFacets(facets HomeFacetValues) (string, error) {
	normalized, err := NormalizeHomeFacets(facets)
	if err != nil {
		return "", err
	}
	if len(normalized) == 0 {
		return "", nil
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func DecodeHomeFacets(payload string) HomeFacetValues {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return HomeFacetValues{}
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return HomeFacetValues{}
	}
	facets, err := NormalizeHomeFacets(raw)
	if err != nil {
		return HomeFacetValues{}
	}
	return facets
}

func SaveClientHomeFacets(clientUUID string, facets HomeFacetValues) error {
	if strings.TrimSpace(clientUUID) == "" {
		return fmt.Errorf("invalid client UUID")
	}
	encoded, err := EncodeHomeFacets(facets)
	if err != nil {
		return err
	}
	return dbcore.GetDBInstance().
		Model(&models.Client{}).
		Where("uuid = ?", clientUUID).
		Updates(map[string]interface{}{
			"home_facets": encoded,
			"updated_at":  time.Now(),
		}).Error
}

func BatchSaveClientHomeFacets(items map[string]HomeFacetValues) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	uuids := make([]string, 0, len(items))
	for uuid := range items {
		uuids = append(uuids, uuid)
	}
	sort.Strings(uuids)

	updated := 0
	db := dbcore.GetDBInstance()
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, uuid := range uuids {
			encoded, err := EncodeHomeFacets(items[uuid])
			if err != nil {
				return err
			}
			if err := tx.Model(&models.Client{}).Where("uuid = ?", uuid).Updates(map[string]interface{}{
				"home_facets": encoded,
				"updated_at":  time.Now(),
			}).Error; err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	return updated, err
}

func GetAllClientHomeFacetRows() ([]HomeFacetClientRow, error) {
	var clients []models.Client
	if err := dbcore.GetDBInstance().Find(&clients).Error; err != nil {
		return nil, err
	}
	rows := make([]HomeFacetClientRow, 0, len(clients))
	for _, client := range clients {
		rows = append(rows, HomeFacetClientRow{
			UUID:         client.UUID,
			Name:         client.Name,
			Group:        client.Group,
			Region:       client.Region,
			Provider:     client.Provider,
			BusinessRole: client.BusinessRole,
			Tags:         client.Tags,
			HomeFacets:   DecodeHomeFacets(client.HomeFacets),
		})
	}
	return rows, nil
}

func GetClientHomeFacetRow(clientUUID string) (HomeFacetClientRow, error) {
	client, err := GetClientByUUID(clientUUID)
	if err != nil {
		return HomeFacetClientRow{}, err
	}
	return HomeFacetClientRow{
		UUID:         client.UUID,
		Name:         client.Name,
		Group:        client.Group,
		Region:       client.Region,
		Provider:     client.Provider,
		BusinessRole: client.BusinessRole,
		Tags:         client.Tags,
		HomeFacets:   DecodeHomeFacets(client.HomeFacets),
	}, nil
}
