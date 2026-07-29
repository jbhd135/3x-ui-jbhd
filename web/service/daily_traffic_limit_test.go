package service

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	xuilogger "github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"
	"github.com/op/go-logging"
)

func TestDailyClientTrafficThreshold(t *testing.T) {
	if got, want := dailyClientTrafficThreshold(dailyClientTrafficLimitBytes, 0), int64(5*1024*1024*1024); got != want {
		t.Fatalf("default daily threshold = %d, want %d", got, want)
	}
	override := int64(8 * 1024 * 1024 * 1024)
	if got := dailyClientTrafficThreshold(dailyClientTrafficLimit10GBBytes, override); got != override {
		t.Fatalf("manual override threshold = %d, want %d", got, override)
	}
	if got := dailyClientTrafficThreshold(0, 0); got != 0 {
		t.Fatalf("unlimited daily threshold = %d, want 0", got)
	}
}

func TestDailyClientTrafficLimitReached(t *testing.T) {
	limit := dailyClientTrafficLimitBytes
	if dailyClientTrafficLimitReached(limit-1, limit, 0) {
		t.Fatal("traffic below the default limit should remain enabled")
	}
	if !dailyClientTrafficLimitReached(limit, limit, 0) {
		t.Fatal("traffic at the default limit should be blocked")
	}
	override := limit * 2
	if dailyClientTrafficLimitReached(limit, limit, override) {
		t.Fatal("traffic below a manual override should remain enabled")
	}
	if !dailyClientTrafficLimitReached(override, limit, override) {
		t.Fatal("traffic at a manual override should be blocked")
	}
	if dailyClientTrafficLimitReached(override, 0, 0) {
		t.Fatal("unlimited daily traffic should never be blocked")
	}
}

func TestStaleDailyOverrideDoesNotCarryAcrossDays(t *testing.T) {
	setupDailyTrafficTestDB(t)

	today := (&InboundService{}).monitorTrafficDate()
	inbound := &model.Inbound{
		Tag:               "stale-daily-override-inbound",
		Port:              23459,
		Protocol:          model.VMESS,
		Enable:            true,
		DailyTrafficLimit: dailyClientTrafficLimit10GBBytes,
		Settings:          `{"clients":[]}`,
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	traffic := &xray.ClientTraffic{
		InboundId:          inbound.Id,
		Email:              "stale-daily-override-client",
		Enable:             true,
		DailyOverrideLimit: 40 * 1024 * 1024 * 1024,
		// Empty is the legacy value after AutoMigrate adds the date column.
		DailyOverrideDate: "",
	}
	if err := database.GetDB().Create(traffic).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.DailyClientTraffic{
		Date:        today,
		InboundId:   inbound.Id,
		ClientEmail: traffic.Email,
		Down:        11 * 1024 * 1024 * 1024,
	}).Error; err != nil {
		t.Fatal(err)
	}

	service := &InboundService{}
	if _, err := service.ResetDailyClientTrafficLimits(); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(traffic, traffic.Id).Error; err != nil {
		t.Fatal(err)
	}
	if traffic.DailyOverrideLimit != 0 || traffic.DailyOverrideDate != "" {
		t.Fatalf("stale override was not cleared: limit=%d date=%q", traffic.DailyOverrideLimit, traffic.DailyOverrideDate)
	}
	if _, count, err := service.disableDailyLimitInbounds(database.GetDB()); err != nil {
		t.Fatal(err)
	} else if count != 1 {
		t.Fatalf("disabled inbound count = %d, want 1", count)
	}
}

func setupDailyTrafficTestDB(t *testing.T) {
	t.Helper()
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	t.Setenv("XUI_LOG_FOLDER", dbDir)
	xuilogger.InitLogger(logging.ERROR)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("database.InitDB failed: %v", err)
	}
	t.Cleanup(func() {
		if err := database.CloseDB(); err != nil {
			t.Logf("database.CloseDB warning: %v", err)
		}
	})
}

func TestTerminateInboundTCPConnections(t *testing.T) {
	previous := runSocketCommand
	t.Cleanup(func() {
		runSocketCommand = previous
	})

	var command string
	var args []string
	runSocketCommand = func(name string, commandArgs ...string) ([]byte, error) {
		command = name
		args = append([]string(nil), commandArgs...)
		return nil, nil
	}

	if err := terminateInboundTCPConnections(23456); err != nil {
		t.Fatalf("terminateInboundTCPConnections failed: %v", err)
	}
	if command != "ss" {
		t.Fatalf("command = %q, want ss", command)
	}
	wantArgs := []string{"-K", "state", "established", "sport = :23456"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestTerminateInboundTCPConnectionsRejectsInvalidPort(t *testing.T) {
	if err := terminateInboundTCPConnections(0); err == nil {
		t.Fatal("invalid port should be rejected")
	}
}

func TestDisableInvalidClientsByDailyTrafficLimit(t *testing.T) {
	setupDailyTrafficTestDB(t)

	settings, err := json.Marshal(map[string]any{
		"clients": []map[string]any{{"email": "daily-limit-test", "enable": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{
		Tag:               "daily-limit-test-inbound",
		Port:              23456,
		Protocol:          model.VMESS,
		Enable:            true,
		DailyTrafficLimit: dailyClientTrafficLimitBytes,
		Settings:          string(settings),
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{
		InboundId: inbound.Id,
		Email:     "daily-limit-test",
		Enable:    true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(time.Local).Format("2006-01-02")
	if err := database.GetDB().Create(&model.DailyClientTraffic{
		Date:        today,
		InboundId:   inbound.Id,
		ClientEmail: "daily-limit-test",
		Down:        dailyClientTrafficLimitBytes,
	}).Error; err != nil {
		t.Fatal(err)
	}

	needRestart, globalRestart, err := (&InboundService{}).AddTraffic(nil, nil)
	if err != nil {
		t.Fatalf("AddTraffic failed: %v", err)
	}
	if needRestart {
		t.Fatal("no Xray API restart should be required in the database-only test")
	}
	if globalRestart {
		t.Fatal("a successful daily-limit disable must not request a global Xray restart")
	}

	var traffic xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", "daily-limit-test").First(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	if !traffic.Enable {
		t.Fatal("client should remain enabled when the inbound is disabled")
	}
	var savedInbound model.Inbound
	if err := database.GetDB().First(&savedInbound, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if savedInbound.Enable {
		t.Fatal("inbound should be disabled after the daily limit is reached")
	}
	if savedInbound.DailyTrafficBlockedDate != today {
		t.Fatalf("daily blocked date = %q, want %q", savedInbound.DailyTrafficBlockedDate, today)
	}

	if err := database.GetDB().Model(&savedInbound).Updates(map[string]any{
		"daily_traffic_blocked_date": "2000-01-01",
		"enable":                     false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	resetCount, err := (&InboundService{}).ResetDailyClientTrafficLimits()
	if err != nil {
		t.Fatalf("ResetDailyClientTrafficLimits failed: %v", err)
	}
	if resetCount != 1 {
		t.Fatalf("reset count = %d, want 1", resetCount)
	}
	if err := database.GetDB().First(&savedInbound, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !savedInbound.Enable {
		t.Fatal("inbound should be re-enabled after the day changes")
	}
}

func TestUpdateInboundDailyTrafficLimitRestoresAndBlocks(t *testing.T) {
	setupDailyTrafficTestDB(t)

	today := time.Now().In(time.Local).Format("2006-01-02")
	inbound := &model.Inbound{
		Tag:                     "daily-limit-selection-test",
		Port:                    23457,
		Protocol:                model.VMESS,
		Enable:                  false,
		DailyTrafficLimit:       dailyClientTrafficLimitBytes,
		DailyTrafficBlockedDate: today,
		Settings:                `{"clients":[]}`,
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{
		InboundId: inbound.Id,
		Email:     "daily-limit-selection-client",
		Enable:    true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.DailyClientTraffic{
		Date:        today,
		InboundId:   inbound.Id,
		ClientEmail: "daily-limit-selection-client",
		Down:        6 * 1024 * 1024 * 1024,
	}).Error; err != nil {
		t.Fatal(err)
	}

	service := &InboundService{}
	updated, needRestart, err := service.UpdateInboundDailyTrafficLimit(inbound.Id, dailyClientTrafficLimit10GBBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !needRestart || !updated.Enable || updated.DailyTrafficBlockedDate != "" {
		t.Fatalf("10 GB selection should restore the inbound: restart=%v enable=%v blocked=%q", needRestart, updated.Enable, updated.DailyTrafficBlockedDate)
	}

	updated, needRestart, err = service.UpdateInboundDailyTrafficLimit(inbound.Id, dailyClientTrafficLimitBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !needRestart || updated.Enable || updated.DailyTrafficBlockedDate != today {
		t.Fatalf("5 GB selection should block the inbound: restart=%v enable=%v blocked=%q", needRestart, updated.Enable, updated.DailyTrafficBlockedDate)
	}

	updated, needRestart, err = service.UpdateInboundDailyTrafficLimit(inbound.Id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !needRestart || !updated.Enable || updated.DailyTrafficLimit != 0 {
		t.Fatalf("unlimited selection should restore the inbound: restart=%v enable=%v limit=%d", needRestart, updated.Enable, updated.DailyTrafficLimit)
	}

	if _, _, err := service.UpdateInboundDailyTrafficLimit(inbound.Id, 7*1024*1024*1024); err == nil {
		t.Fatal("unsupported daily traffic limit should be rejected")
	}
}

func TestGetInboundsIncludesTodayTraffic(t *testing.T) {
	setupDailyTrafficTestDB(t)

	inbound := &model.Inbound{
		UserId:            7,
		Tag:               "today-traffic-test-inbound",
		Port:              23458,
		Protocol:          model.VMESS,
		Enable:            true,
		DailyTrafficLimit: dailyClientTrafficLimit10GBBytes,
		Settings:          `{"clients":[]}`,
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}

	service := &InboundService{}
	today := service.monitorTrafficDate()
	rows := []model.DailyClientTraffic{
		{Date: today, InboundId: inbound.Id, ClientEmail: "today-a", Up: 100, Down: 200},
		{Date: today, InboundId: inbound.Id, ClientEmail: "today-b", Up: 300, Down: 400},
		{Date: "2000-01-01", InboundId: inbound.Id, ClientEmail: "old", Up: 5000, Down: 5000},
	}
	if err := database.GetDB().Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	inbounds, err := service.GetInbounds(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbounds) != 1 {
		t.Fatalf("inbound count = %d, want 1", len(inbounds))
	}
	if got, want := inbounds[0].TodayTraffic, int64(1000); got != want {
		t.Fatalf("today traffic = %d, want %d", got, want)
	}
}
