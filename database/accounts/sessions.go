package accounts

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/utils/geoip"
	"github.com/komari-monitor/komari/utils/messageSender"
	"gorm.io/gorm"
)

var ErrSessionExpired = errors.New("session expired")

// GetAllSessions 获取所有会话
func GetAllSessions() (sessions []models.Session, err error) {
	db := dbcore.GetDBInstance()
	err = db.Find(&sessions).Error
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

// CreateSession 创建新会话
func CreateSession(uuid string, expires int, userAgent, ip, login_method string) (string, error) {
	db := dbcore.GetDBInstance()
	session := utils.GenerateRandomString(32)

	sessionRecord := models.Session{
		UUID:         uuid,
		Session:      session,
		Expires:      models.FromTime(time.Now().Add(time.Duration(expires) * time.Second)),
		UserAgent:    userAgent,
		Ip:           ip,
		LoginMethod:  login_method,
		LatestOnline: models.FromTime(time.Now()),
	}
	notifyLogin := func() {
		LoginNotification, _ := config.GetAs[bool](config.LoginNotificationKey, false)
		if LoginNotification {
			var ipinfo *geoip.GeoInfo
			if geoEnabled, _ := config.GetAs[bool](config.GeoIpEnabledKey, false); geoEnabled {
				ipAddr := net.ParseIP(ip)
				ipinfo, _ = geoip.GetGeoInfo(ipAddr)
			}
			loc := "unknown"
			if ipinfo != nil && ipinfo.Name != "" {
				loc = ipinfo.Name
			}
			messageSender.SendEvent(models.EventMessage{
				Event:   messageevent.Login,
				Time:    time.Now(),
				Message: fmt.Sprintf("%s: %s (%s)\n%s", login_method, ip, loc, userAgent),
				Emoji:   "🔑",
			})
		}
	}

	err := db.Create(&sessionRecord).Error
	if err != nil {
		return "", err
	}
	invalidateSessionCredential(session)
	go notifyLogin()
	return session, nil
}

// GetSession 根据会话 ID 获取 UUID
func GetSession(session string) (uuid string, err error) {
	if session == "" {
		return "", gorm.ErrRecordNotFound
	}
	now := time.Now()
	if entry, ok := cachedSessionCredential(session, now); ok {
		if !entry.Found {
			return "", gorm.ErrRecordNotFound
		}
		if !entry.CredentialExpiresAt.IsZero() && !now.Before(entry.CredentialExpiresAt) {
			_ = DeleteSession(session)
			return "", ErrSessionExpired
		}
		return entry.Value.UUID, nil
	}
	generation := sessionCredentialGeneration()

	db := dbcore.GetDBInstance()
	var sessionRecord models.Session
	err = db.Select("uuid", "expires", "created_at").Where("session = ?", session).First(&sessionRecord).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			cacheMissingSessionCredential(session, now, generation)
		}
		return "", err
	}

	cacheSessionCredential(session, sessionRecord, now, generation)
	if !now.Before(sessionRecord.Expires.ToTime()) {
		// 会话已过期，删除它
		_ = DeleteSession(session)
		return "", ErrSessionExpired
	}

	return sessionRecord.UUID, nil
}

func GetUserBySession(session string) (models.User, error) {
	uuid, err := GetSession(session)
	if err != nil {
		return models.User{}, err
	}
	return GetUserByUUID(uuid)
}

// DeleteSession 删除指定会话
func DeleteSession(session string) (err error) {
	db := dbcore.GetDBInstance()
	result := db.Where("session = ?", session).Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	invalidateSessionCredential(session)
	return nil
}

func DeleteAllSessions() error {
	db := dbcore.GetDBInstance()
	result := db.Where("1 = 1").Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	clearSessionCredentials()
	return nil
}

func DeleteSessionsByUUID(uuid string) error {
	db := dbcore.GetDBInstance()
	result := db.Where("uuid = ?", uuid).Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	// The cache deliberately has no plaintext secondary index. Clearing this
	// bounded cache makes account-level revocation immediate without retaining
	// session identifiers or building a high-cardinality UUID index.
	clearSessionCredentials()
	return nil
}

func UpdateLatestOnline(session string) error {
	db := dbcore.GetDBInstance()
	return db.Model(&models.Session{}).Where("session = ?", session).Update("latest_online", time.Now()).Error
}

func UpdateLatestUserAgent(session, userAgent string) error {
	db := dbcore.GetDBInstance()
	return db.Model(&models.Session{}).Where("session = ?", session).Update("latest_user_agent", userAgent).Error
}
func UpdateLatestIp(session, ip string) error {
	db := dbcore.GetDBInstance()
	return db.Model(&models.Session{}).Where("session = ?", session).Update("latest_ip", ip).Error
}

func UpdateLatest(session, useragent, ip string) error {
	db := dbcore.GetDBInstance()
	return db.Model(&models.Session{}).Where("session = ?", session).Updates(map[string]interface{}{
		"latest_online":     time.Now(),
		"latest_user_agent": useragent,
		"latest_ip":         ip,
	}).Error
}

func RemoveExpiredSessions() error {
	db := dbcore.GetDBInstance()
	result := db.Where("expires < ?", time.Now()).Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	clearSessionCredentials()
	return nil
}
