package job

import (
	"regexp"
	"strconv"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

var remarkDateTokens = regexp.MustCompile(`\d+`)

// InboundRemarkExpiryJob deletes inbounds whose remark contains today's MMDD
// token. An 8-digit YYYYMMDD token is treated as its MMDD portion.
type InboundRemarkExpiryJob struct {
	inboundService service.InboundService
	settingService service.SettingService
	xrayService    *service.XrayService
}

func NewInboundRemarkExpiryJob(xrayService *service.XrayService) *InboundRemarkExpiryJob {
	return &InboundRemarkExpiryJob{xrayService: xrayService}
}

func (j *InboundRemarkExpiryJob) Run() {
	now := time.Now()
	if loc, err := j.settingService.GetTimeLocation(); err == nil && loc != nil {
		now = now.In(loc)
	} else if err != nil {
		logger.Warning("inbound remark expiry: get time location failed:", err)
	}
	inbounds, err := j.inboundService.GetAllInbounds()
	if err != nil {
		logger.Warning("inbound remark expiry: get inbounds failed:", err)
		return
	}

	deleted := 0
	needRestart := false
	for _, inbound := range inbounds {
		if inbound == nil || !remarkDateDue(inbound.Remark, now) {
			continue
		}
		restart, err := j.inboundService.DelInbound(inbound.Id)
		if err != nil {
			logger.Warningf("inbound remark expiry: delete inbound %d (%s) failed: %v", inbound.Id, inbound.Remark, err)
			continue
		}
		deleted++
		if restart {
			needRestart = true
		}
		logger.Infof("inbound remark expiry: deleted inbound %d with remark %q", inbound.Id, inbound.Remark)
	}

	if needRestart && j.xrayService != nil {
		j.xrayService.SetToNeedRestart()
	}
	if deleted > 0 {
		logger.Infof("inbound remark expiry: deleted %d inbound(s)", deleted)
	}
}

func remarkDateDue(remark string, now time.Time) bool {
	if remark == "" {
		return false
	}

	month := int(now.Month())
	day := now.Day()
	lastDay := lastDayOfMonth(now.Year(), now.Month(), now.Location())

	for _, token := range remarkDateTokens.FindAllString(remark, -1) {
		mmdd := token
		switch len(token) {
		case 4:
		case 8:
			mmdd = token[4:]
		default:
			continue
		}

		tokenMonth, err := strconv.Atoi(mmdd[:2])
		if err != nil || tokenMonth != month {
			continue
		}
		tokenDay, err := strconv.Atoi(mmdd[2:])
		if err != nil || tokenDay < 1 || tokenDay > 31 {
			continue
		}

		if day == lastDay {
			return tokenDay >= day
		}
		if tokenDay == day {
			return true
		}
	}
	return false
}

func lastDayOfMonth(year int, month time.Month, loc *time.Location) int {
	if loc == nil {
		loc = time.Local
	}
	return time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
}
