package notifier

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	"github.com/komari-monitor/komari/utils/messageSender"
	"github.com/komari-monitor/komari/utils/renewal"
)

func CheckExpireScheduledWork() {
	CheckExpireScheduledWorkContext(context.Background())
}

func CheckExpireScheduledWorkContext(ctx context.Context) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location()) // UTC 9AM = CST 17PM
		if now.After(next) {
			next = next.Add(24 * time.Hour)
		}
		duration := next.Sub(now)
		timer := time.NewTimer(duration)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}

		cfg, err := config.GetMany(map[string]any{
			config.ExpireNotificationEnabledKey:  false,
			config.ExpireNotificationLeadDaysKey: 7,
		})
		if err != nil {
			if !waitNotificationContext(ctx, time.Second) {
				return
			}
			continue
		}

		clients_all, err := clients.GetAllClientBasicInfo()
		if err != nil {
			if !waitNotificationContext(ctx, time.Second) {
				return
			}
			continue
		}

		checkTime := time.Now()

		// 过期提醒检查（仅当启用过期通知时）
		if cfg[config.ExpireNotificationEnabledKey].(bool) {
			notificationLeadDays := int(cfg[config.ExpireNotificationLeadDaysKey].(float64)) // Json unmarshal 会将数字解析为 float64

			type clientToExpireInfo struct {
				Name     string
				DaysLeft int
			}

			var clientLeadToExpire []clientToExpireInfo

			for _, client := range clients_all {
				clientExpireTime := client.ExpiredAt.ToTime()

				if clientExpireTime.Before(checkTime) {
					continue
				}

				notificationThreshold := checkTime.Add(time.Duration(notificationLeadDays) * 24 * time.Hour)

				if clientExpireTime.Before(notificationThreshold) || clientExpireTime.Equal(notificationThreshold) {
					remainingDuration := clientExpireTime.Sub(checkTime)
					daysLeft := int(math.Ceil(remainingDuration.Hours() / 24))

					clientLeadToExpire = append(clientLeadToExpire, clientToExpireInfo{
						Name:     client.Name,
						DaysLeft: daysLeft,
					})
				}
			}

			if len(clientLeadToExpire) > 0 {
				message := ""
				for _, clientInfo := range clientLeadToExpire {
					message += fmt.Sprintf("• %s (%dd)\n", clientInfo.Name, clientInfo.DaysLeft)
				}
				messageSender.SendEvent(models.EventMessage{
					Event:   messageevent.Expire,
					Time:    time.Now(),
					Message: message,
					Emoji:   "⏳",
				})
			}
		}

		// 等待1秒，防止多次触发
		if !waitNotificationContext(ctx, time.Second) {
			return
		}
		for _, client := range clients_all {
			if ctx.Err() != nil {
				return
			}
			renewal.CheckAndAutoRenewal(client)
		}
	}

}

func waitNotificationContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
