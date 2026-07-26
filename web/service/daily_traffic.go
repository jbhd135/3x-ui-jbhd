package service

import (
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"

	"gorm.io/gorm"
)

// monitorTrafficDate returns the panel-local calendar date used by daily
// traffic accounting and daily limit enforcement.
func (s *InboundService) monitorTrafficDate() string {
	location := time.Local
	if loc, err := (&SettingService{}).GetTimeLocation(); err == nil && loc != nil {
		location = loc
	}
	return time.Now().In(location).Format("2006-01-02")
}

func (s *InboundService) addDailyClientTrafficDelta(tx *gorm.DB, date string, inboundID int, email string, up, down int64) error {
	var row model.DailyClientTraffic
	err := tx.Where("date = ? AND inbound_id = ? AND client_email = ?", date, inboundID, email).First(&row).Error
	if database.IsNotFound(err) {
		return tx.Create(&model.DailyClientTraffic{
			Date:        date,
			InboundId:   inboundID,
			ClientEmail: email,
			Up:          up,
			Down:        down,
		}).Error
	}
	if err != nil {
		return err
	}

	row.Up += up
	row.Down += down
	return tx.Save(&row).Error
}
