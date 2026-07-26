package job

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
	"github.com/mhsanaei/3x-ui/v2/web/websocket"
	"github.com/mhsanaei/3x-ui/v2/xray"
	"gorm.io/gorm"
)

const (
	deviceLimitActiveTTL = 30 * time.Second
	deviceLimitGrace     = 3 * time.Minute
)

var relayTrafficActivity = struct {
	sync.Mutex
	seen map[string]time.Time
}{
	seen: make(map[string]time.Time),
}

type deviceLimitInfo struct {
	Limit    int
	Port     int
	Ports    []int
	Tag      string
	Protocol model.Protocol
	Settings string
}

type deviceIPState struct {
	FirstSeen time.Time
	LastSeen  time.Time
}

// CheckDeviceLimitJob tracks active client IPs and temporarily invalidates
// users that exceed the inbound-level device limit.
type CheckDeviceLimitJob struct {
	runLock            sync.Mutex
	xrayService        *service.XrayService
	lastPosition       int64
	activeClientIPs    map[string]map[string]time.Time
	activeInboundIPs   map[int]map[string]deviceIPState
	activeRelayNodeIPs map[string]map[string]deviceIPState
	bannedInboundIPs   map[int]map[string][]int
}

func NewCheckDeviceLimitJob(xrayService *service.XrayService) *CheckDeviceLimitJob {
	return &CheckDeviceLimitJob{
		xrayService:        xrayService,
		activeClientIPs:    make(map[string]map[string]time.Time),
		activeInboundIPs:   make(map[int]map[string]deviceIPState),
		activeRelayNodeIPs: make(map[string]map[string]deviceIPState),
		bannedInboundIPs:   make(map[int]map[string][]int),
	}
}

func (j *CheckDeviceLimitJob) Run() {
	if !j.runLock.TryLock() {
		return
	}
	defer j.runLock.Unlock()

	if j.xrayService == nil || !j.xrayService.IsXrayRunning() {
		return
	}

	j.parseAccessLog()
	j.refreshRelayActivityFromTraffic()
	j.cleanupExpiredIPs()
	j.checkInboundDeviceLimits()
	j.broadcastActiveInboundIPs()
}

func markRelayTrafficActivity(traffics []*xray.Traffic) {
	if len(traffics) == 0 {
		return
	}
	now := time.Now()
	relayTrafficActivity.Lock()
	defer relayTrafficActivity.Unlock()
	if relayTrafficActivity.seen == nil {
		relayTrafficActivity.seen = make(map[string]time.Time)
	}
	for _, traffic := range traffics {
		if traffic == nil || !traffic.IsInbound || traffic.Down == 0 {
			continue
		}
		key := relayNodeKeyFromTag(traffic.Tag)
		if key != "" {
			relayTrafficActivity.seen[key] = now
		}
	}
	for key, seenAt := range relayTrafficActivity.seen {
		if now.Sub(seenAt) > deviceLimitActiveTTL {
			delete(relayTrafficActivity.seen, key)
		}
	}
}

func relayTrafficActivityKeys(now time.Time) map[string]bool {
	relayTrafficActivity.Lock()
	defer relayTrafficActivity.Unlock()
	keys := make(map[string]bool)
	for key, seenAt := range relayTrafficActivity.seen {
		if now.Sub(seenAt) > deviceLimitActiveTTL {
			delete(relayTrafficActivity.seen, key)
			continue
		}
		keys[key] = true
	}
	return keys
}

func (j *CheckDeviceLimitJob) refreshRelayActivityFromTraffic() {
	now := time.Now()
	for relayKey := range relayTrafficActivityKeys(now) {
		ips := j.activeRelayNodeIPs[relayKey]
		if len(ips) == 0 {
			continue
		}
		inboundID, _, ok := relayIDsFromKey(relayKey)
		for ip, state := range ips {
			if state.FirstSeen.IsZero() {
				state.FirstSeen = now
			}
			state.LastSeen = now
			ips[ip] = state

			if !ok || inboundID <= 0 {
				continue
			}
			if _, exists := j.activeInboundIPs[inboundID]; !exists {
				j.activeInboundIPs[inboundID] = make(map[string]deviceIPState)
			}
			inboundState := j.activeInboundIPs[inboundID][ip]
			if inboundState.FirstSeen.IsZero() {
				inboundState.FirstSeen = state.FirstSeen
			}
			inboundState.LastSeen = now
			j.activeInboundIPs[inboundID][ip] = inboundState
		}
	}
}

func (j *CheckDeviceLimitJob) cleanupExpiredIPs() {
	now := time.Now()
	for email, ips := range j.activeClientIPs {
		for ip, lastSeen := range ips {
			if now.Sub(lastSeen) > deviceLimitActiveTTL {
				delete(ips, ip)
			}
		}
		if len(ips) == 0 {
			delete(j.activeClientIPs, email)
		}
	}
	for inboundID, ips := range j.activeInboundIPs {
		for ip, state := range ips {
			if now.Sub(state.LastSeen) > deviceLimitActiveTTL {
				delete(ips, ip)
				j.unbanInboundIP(inboundID, ip)
			}
		}
		if len(ips) == 0 {
			delete(j.activeInboundIPs, inboundID)
		}
	}
	for relayKey, ips := range j.activeRelayNodeIPs {
		for ip, state := range ips {
			if now.Sub(state.LastSeen) > deviceLimitActiveTTL {
				delete(ips, ip)
			}
		}
		if len(ips) == 0 {
			delete(j.activeRelayNodeIPs, relayKey)
		}
	}
}

func (j *CheckDeviceLimitJob) parseAccessLog() {
	logPath, err := xray.GetAccessLogPath()
	if err != nil || logPath == "" || logPath == "none" {
		return
	}

	file, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer file.Close()

	if stat, err := file.Stat(); err == nil && stat.Size() < j.lastPosition {
		j.lastPosition = 0
	}
	if _, err := file.Seek(j.lastPosition, 0); err != nil {
		j.lastPosition = 0
		return
	}

	tagToInboundID, emailToInboundID := j.deviceLimitLookupMaps()
	emailRegex := regexp.MustCompile(`email: ([^ ]+)`)
	ipRegex := regexp.MustCompile(`(?:from\s+)?(?:tcp:|udp:)?\[?([0-9a-fA-F\.:]+)\]?:\d+\s+accepted`)

	now := time.Now()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		emailMatch := emailRegex.FindStringSubmatch(line)
		ipMatch := ipRegex.FindStringSubmatch(line)
		if len(ipMatch) < 2 {
			continue
		}

		email := ""
		if len(emailMatch) >= 2 {
			email = emailMatch[1]
		}
		ip := ipMatch[1]
		if ip == "127.0.0.1" || ip == "::1" {
			continue
		}

		if email != "" && !isRelayCredentialEmail(email) {
			if _, ok := j.activeClientIPs[email]; !ok {
				j.activeClientIPs[email] = make(map[string]time.Time)
			}
			j.activeClientIPs[email][ip] = now
		}

		inboundID := j.inboundIDFromAccessLine(line, email, tagToInboundID, emailToInboundID)
		if inboundID > 0 {
			if _, ok := j.activeInboundIPs[inboundID]; !ok {
				j.activeInboundIPs[inboundID] = make(map[string]deviceIPState)
			}
			state, exists := j.activeInboundIPs[inboundID][ip]
			if !exists {
				state.FirstSeen = now
			}
			state.LastSeen = now
			j.activeInboundIPs[inboundID][ip] = state
		}

		relayKey := relayNodeKeyFromAccessLine(line)
		if relayKey != "" {
			if _, ok := j.activeRelayNodeIPs[relayKey]; !ok {
				j.activeRelayNodeIPs[relayKey] = make(map[string]deviceIPState)
			}
			state, exists := j.activeRelayNodeIPs[relayKey][ip]
			if !exists {
				state.FirstSeen = now
			}
			state.LastSeen = now
			j.activeRelayNodeIPs[relayKey][ip] = state
		}
	}

	if pos, err := file.Seek(0, io.SeekCurrent); err == nil {
		j.lastPosition = pos
	}
}

func (j *CheckDeviceLimitJob) deviceLimitLookupMaps() (map[string]int, map[string]int) {
	tagToInboundID := make(map[string]int)
	emailToInboundID := make(map[string]int)

	var inbounds []*model.Inbound
	if err := database.GetDB().Where("enable = ? AND device_limit > ?", true, 0).Find(&inbounds).Error; err != nil {
		return tagToInboundID, emailToInboundID
	}
	for _, inbound := range inbounds {
		if inbound == nil {
			continue
		}
		tagToInboundID[inbound.Tag] = inbound.Id
		var settings map[string][]model.Client
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			continue
		}
		for _, client := range settings["clients"] {
			if client.Email != "" {
				emailToInboundID[client.Email] = inbound.Id
			}
		}
	}

	var relayRows []struct {
		InboundId int `gorm:"column:inbound_id"`
		NodeId    int `gorm:"column:node_id"`
	}
	if err := database.GetDB().Table("inbound_upstream_relays").
		Select("inbound_upstream_relays.inbound_id, inbound_upstream_relays.node_id").
		Joins("JOIN inbounds ON inbounds.id = inbound_upstream_relays.inbound_id").
		Where("inbounds.enable = ? AND inbounds.emergency_enable = ? AND inbounds.device_limit > ?", true, true, 0).
		Scan(&relayRows).Error; err == nil {
		for _, row := range relayRows {
			if row.InboundId > 0 && row.NodeId > 0 {
				tagToInboundID[inboundRelayTag(row.InboundId, row.NodeId)] = row.InboundId
			}
		}
	}
	return tagToInboundID, emailToInboundID
}

func (j *CheckDeviceLimitJob) inboundIDFromAccessLine(line, email string, tagToInboundID map[string]int, emailToInboundID map[string]int) int {
	for tag, inboundID := range tagToInboundID {
		if tag != "" && strings.Contains(line, tag) {
			return inboundID
		}
	}
	if email != "" {
		return emailToInboundID[email]
	}
	return 0
}

func inboundRelayTag(inboundID, nodeID int) string {
	return fmt.Sprintf("xui-relay-in-%d-node-%d", inboundID, nodeID)
}

var inboundRelayTagRegex = regexp.MustCompile(`xui-relay-in-(\d+)-node-(\d+)`)
var relayCredentialEmailRegex = regexp.MustCompile(`^relay-in-\d+-node-\d+$`)

func isRelayCredentialEmail(email string) bool {
	return relayCredentialEmailRegex.MatchString(strings.TrimSpace(email))
}

func relayNodeKeyFromAccessLine(line string) string {
	return relayNodeKeyFromTag(line)
}

func relayNodeKeyFromTag(tag string) string {
	matches := inboundRelayTagRegex.FindStringSubmatch(tag)
	if len(matches) < 3 {
		return ""
	}
	inboundID, err1 := strconv.Atoi(matches[1])
	nodeID, err2 := strconv.Atoi(matches[2])
	if err1 != nil || err2 != nil || inboundID <= 0 || nodeID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", inboundID, nodeID)
}

func relayIDsFromKey(key string) (int, int, bool) {
	inboundText, nodeText, ok := strings.Cut(key, ":")
	if !ok {
		return 0, 0, false
	}
	inboundID, inboundErr := strconv.Atoi(inboundText)
	nodeID, nodeErr := strconv.Atoi(nodeText)
	return inboundID, nodeID, inboundErr == nil && nodeErr == nil && inboundID > 0 && nodeID > 0
}

func (j *CheckDeviceLimitJob) checkInboundDeviceLimits() {
	db := database.GetDB()
	var inbounds []model.Inbound
	if err := db.Where("enable = ? AND device_limit > ?", true, 0).Find(&inbounds).Error; err != nil || len(inbounds) == 0 {
		return
	}

	inboundIDs := make([]int, 0, len(inbounds))
	for _, inbound := range inbounds {
		inboundIDs = append(inboundIDs, inbound.Id)
	}
	relayPorts := relayPortsByInboundID(db, inboundIDs)
	infoByID := make(map[int]deviceLimitInfo, len(inbounds))
	for _, inbound := range inbounds {
		ports := []int{}
		if inbound.Port > 0 {
			ports = append(ports, inbound.Port)
		}
		ports = append(ports, relayPorts[inbound.Id]...)
		infoByID[inbound.Id] = deviceLimitInfo{
			Limit:    inbound.DeviceLimit,
			Port:     inbound.Port,
			Ports:    ports,
			Tag:      inbound.Tag,
			Protocol: inbound.Protocol,
			Settings: inbound.Settings,
		}
	}

	for inboundID, ips := range j.activeInboundIPs {
		info, ok := infoByID[inboundID]
		if !ok || info.Limit <= 0 {
			continue
		}

		active := make([]struct {
			IP    string
			State deviceIPState
		}, 0, len(ips))
		for ip, state := range ips {
			active = append(active, struct {
				IP    string
				State deviceIPState
			}{IP: ip, State: state})
		}
		sort.Slice(active, func(i, k int) bool {
			return active[i].State.FirstSeen.Before(active[k].State.FirstSeen)
		})

		allowed := make(map[string]bool, info.Limit)
		for index, entry := range active {
			if index < info.Limit {
				allowed[entry.IP] = true
				continue
			}
			if !j.isInboundIPBanned(inboundID, entry.IP) {
				j.banInboundIP(inboundID, entry.IP, &info, len(active))
			}
		}

		for ip := range j.bannedInboundIPs[inboundID] {
			if allowed[ip] || ips[ip].LastSeen.IsZero() {
				j.unbanInboundIP(inboundID, ip)
			}
		}
	}
}

func relayPortsByInboundID(db *gorm.DB, inboundIDs []int) map[int][]int {
	result := make(map[int][]int)
	if len(inboundIDs) == 0 {
		return result
	}
	var rows []struct {
		InboundId int `gorm:"column:inbound_id"`
		RelayPort int `gorm:"column:relay_port"`
	}
	if err := db.Table("inbound_upstream_relays").
		Select("inbound_id, relay_port").
		Where("inbound_id IN ? AND relay_port > 0", inboundIDs).
		Scan(&rows).Error; err != nil {
		return result
	}
	for _, row := range rows {
		if row.InboundId > 0 && row.RelayPort > 0 {
			result[row.InboundId] = append(result[row.InboundId], row.RelayPort)
		}
	}
	return result
}

func (j *CheckDeviceLimitJob) broadcastActiveInboundIPs() {
	if !websocket.HasClients() {
		return
	}

	clientsByIP := make(map[string][]string)
	for email, ips := range j.activeClientIPs {
		if strings.TrimSpace(email) == "" {
			continue
		}
		for ip := range ips {
			clientsByIP[ip] = append(clientsByIP[ip], email)
		}
	}
	for ip := range clientsByIP {
		sort.Strings(clientsByIP[ip])
	}

	payload := make(map[int][]map[string]any, len(j.activeInboundIPs))
	for inboundID, ips := range j.activeInboundIPs {
		if len(ips) == 0 {
			continue
		}

		items := make([]struct {
			IP    string
			State deviceIPState
		}, 0, len(ips))
		for ip, state := range ips {
			items = append(items, struct {
				IP    string
				State deviceIPState
			}{IP: ip, State: state})
		}
		sort.Slice(items, func(i, k int) bool {
			return items[i].State.FirstSeen.Before(items[k].State.FirstSeen)
		})

		rows := make([]map[string]any, 0, len(items))
		for _, item := range items {
			rows = append(rows, map[string]any{
				"ip":        item.IP,
				"firstSeen": item.State.FirstSeen.UnixMilli(),
				"lastSeen":  item.State.LastSeen.UnixMilli(),
				"banned":    j.isInboundIPBanned(inboundID, item.IP),
				"clients":   clientsByIP[item.IP],
			})
		}
		payload[inboundID] = rows
	}

	relayPayload := make(map[string][]map[string]any, len(j.activeRelayNodeIPs))
	for relayKey, ips := range j.activeRelayNodeIPs {
		if len(ips) == 0 {
			continue
		}

		items := make([]struct {
			IP    string
			State deviceIPState
		}, 0, len(ips))
		for ip, state := range ips {
			items = append(items, struct {
				IP    string
				State deviceIPState
			}{IP: ip, State: state})
		}
		sort.Slice(items, func(i, k int) bool {
			return items[i].State.FirstSeen.Before(items[k].State.FirstSeen)
		})

		rows := make([]map[string]any, 0, len(items))
		for _, item := range items {
			rows = append(rows, map[string]any{
				"ip":        item.IP,
				"firstSeen": item.State.FirstSeen.UnixMilli(),
				"lastSeen":  item.State.LastSeen.UnixMilli(),
				"clients":   clientsByIP[item.IP],
			})
		}
		relayPayload[relayKey] = rows
	}

	websocket.BroadcastTraffic(map[string]any{
		"deviceLimitIPs": payload,
		"relayNodeIPs":   relayPayload,
	})
}

func (j *CheckDeviceLimitJob) isInboundIPBanned(inboundID int, ip string) bool {
	if j.bannedInboundIPs[inboundID] == nil {
		return false
	}
	_, ok := j.bannedInboundIPs[inboundID][ip]
	return ok
}

func (j *CheckDeviceLimitJob) banInboundIP(inboundID int, ip string, info *deviceLimitInfo, activeIPCount int) {
	if ip == "" || info == nil {
		return
	}
	if j.bannedInboundIPs[inboundID] == nil {
		j.bannedInboundIPs[inboundID] = make(map[string][]int)
	}
	ports := uniquePositiveInts(info.Ports)
	if len(ports) == 0 && info.Port > 0 {
		ports = []int{info.Port}
	}
	if len(ports) == 0 {
		return
	}
	for _, port := range ports {
		if err := setInboundIPDropRule(ip, port, true); err != nil {
			logger.Warningf("[DEVICE_LIMIT] Failed to block IP %s on inbound %s port %d: %v", ip, info.Tag, port, err)
			return
		}
	}
	logger.Warningf("[DEVICE_LIMIT] Blocking new device IP %s on inbound %s ports %v: active=%d limit=%d", ip, info.Tag, ports, activeIPCount, info.Limit)
	j.bannedInboundIPs[inboundID][ip] = ports
}

func (j *CheckDeviceLimitJob) unbanInboundIP(inboundID int, ip string) {
	if ip == "" || j.bannedInboundIPs[inboundID] == nil {
		return
	}
	ports, ok := j.bannedInboundIPs[inboundID][ip]
	if !ok {
		return
	}
	for _, port := range ports {
		_ = setInboundIPDropRule(ip, port, false)
	}
	delete(j.bannedInboundIPs[inboundID], ip)
	if len(j.bannedInboundIPs[inboundID]) == 0 {
		delete(j.bannedInboundIPs, inboundID)
	}
}

func uniquePositiveInts(values []int) []int {
	seen := make(map[int]bool, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func setInboundIPDropRule(ip string, port int, ban bool) error {
	if ip == "" || port <= 0 {
		return nil
	}
	binary := "iptables"
	if strings.Contains(ip, ":") {
		binary = "ip6tables"
	}
	portArg := strconv.Itoa(port)
	var firstErr error
	for _, proto := range []string{"tcp", "udp"} {
		ruleArgs := []string{"INPUT", "-p", proto, "-s", ip, "--dport", portArg, "-j", "DROP"}
		if ban {
			checkArgs := append([]string{"-w", "-C"}, ruleArgs...)
			if err := exec.Command(binary, checkArgs...).Run(); err == nil {
				continue
			}
			insertArgs := append([]string{"-w", "-I"}, ruleArgs...)
			if err := exec.Command(binary, insertArgs...).Run(); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		deleteArgs := append([]string{"-w", "-D"}, ruleArgs...)
		for {
			if err := exec.Command(binary, deleteArgs...).Run(); err != nil {
				break
			}
		}
	}
	return firstErr
}
