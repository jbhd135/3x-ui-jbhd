package job

import (
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// DailyClientTrafficLimitJob restores clients that were automatically
// disabled by the daily traffic allowance after the panel's day rolls over.
type DailyClientTrafficLimitJob struct {
	inboundService service.InboundService
	xrayService    *service.XrayService
}

func NewDailyClientTrafficLimitJob(xrayService *service.XrayService) *DailyClientTrafficLimitJob {
	return &DailyClientTrafficLimitJob{xrayService: xrayService}
}

func (j *DailyClientTrafficLimitJob) Run() {
	resetCount, err := j.inboundService.ResetDailyClientTrafficLimits()
	if err != nil {
		logger.Warning("daily client traffic reset failed:", err)
		return
	}
	if resetCount == 0 {
		return
	}
	logger.Infof("Daily client traffic reset completed: %d inbound(s) re-enabled", resetCount)
	if j.xrayService != nil {
		j.xrayService.SetToNeedRestart()
	}
}
