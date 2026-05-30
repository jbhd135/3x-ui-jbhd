package service

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"gorm.io/gorm"
)

const customerRelayRemark = "__xui_customer_upstream_relay__"

type customerRelayRow struct {
	CustomerID         int
	CustomerName       string
	CustomerToken      string
	CustomerExpiryTime int64
	NodeID             int
	NodeName           string
	NodeProtocol       string
	NodeLink           string
	NodeClash          string
	NodeSourceType     string
	UpstreamID         int
	UpstreamSort       int
	NodeSort           int
}

type customerRelayNode struct {
	row      customerRelayRow
	email    string
	uuid     string
	outbound map[string]any
}

func (s *SubscriptionMarketService) SyncCustomerRelay() (bool, error) {
	db := database.GetDB()
	var changed bool
	err := db.Transaction(func(tx *gorm.DB) error {
		rows, err := s.activeCustomerRelayRows(tx, "")
		if err != nil {
			return err
		}

		nodes := make([]customerRelayNode, 0, len(rows))
		for _, row := range rows {
			outbound, err := buildUpstreamRelayOutbound(row)
			if err != nil {
				logger.Warningf("skip unsupported upstream relay node %d (%s): %v", row.NodeID, row.NodeName, err)
				continue
			}
			nodes = append(nodes, customerRelayNode{
				row:      row,
				email:    relayClientEmail(row.CustomerID, row.NodeID),
				uuid:     relayClientUUID(row.CustomerID, row.NodeID),
				outbound: outbound,
			})
		}

		inbound, created, err := ensureCustomerRelayInbound(tx)
		if err != nil {
			return err
		}
		if created {
			changed = true
		}

		settings := map[string]any{
			"clients": buildRelayClients(nodes),
		}
		settingsJSON, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return err
		}
		if inbound.Settings != string(settingsJSON) {
			inbound.Settings = string(settingsJSON)
			if err := tx.Save(inbound).Error; err != nil {
				return err
			}
			changed = true
		}

		currentEmailSet := make(map[string]struct{}, len(nodes))
		for _, node := range nodes {
			currentEmailSet[node.email] = struct{}{}
			stat := xray.ClientTraffic{
				InboundId:  inbound.Id,
				Email:      node.email,
				Total:      0,
				ExpiryTime: node.row.CustomerExpiryTime,
				Enable:     true,
				Reset:      0,
				LastOnline: 0,
			}
			var existing xray.ClientTraffic
			err := tx.Where("email = ?", node.email).First(&existing).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					if err := tx.Create(&stat).Error; err != nil {
						return err
					}
					changed = true
					continue
				}
				return err
			}
			updates := map[string]any{
				"inbound_id":  inbound.Id,
				"total":       int64(0),
				"expiry_time": node.row.CustomerExpiryTime,
				"enable":      true,
				"reset":       0,
			}
			if existing.InboundId != inbound.Id || existing.Total != 0 || existing.ExpiryTime != node.row.CustomerExpiryTime || !existing.Enable || existing.Reset != 0 {
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
					return err
				}
				changed = true
			}
		}

		var stale []xray.ClientTraffic
		if err := tx.Where("inbound_id = ?", inbound.Id).Find(&stale).Error; err != nil {
			return err
		}
		for _, stat := range stale {
			if _, ok := currentEmailSet[stat.Email]; ok {
				continue
			}
			if err := tx.Where("email = ?", stat.Email).Delete(&xray.ClientTraffic{}).Error; err != nil {
				return err
			}
			changed = true
		}

		return nil
	})
	return changed, err
}

func (s *SubscriptionMarketService) GetCustomerSubscription(token string, host string) (*CustomerSubscriptionContent, error) {
	token = strings.TrimSpace(token)
	var customer model.CustomerSubscription
	db := database.GetDB()
	if err := db.Where("token = ?", token).First(&customer).Error; err != nil {
		return nil, mapGormNotFound(err, ErrCustomerNotFound)
	}
	if !customer.Enable {
		return nil, ErrCustomerDisabled
	}
	if customer.ExpiryTime > 0 && time.Now().UnixMilli() > customer.ExpiryTime {
		return nil, ErrCustomerExpired
	}

	relayInbound, err := getCustomerRelayInbound(db)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if _, syncErr := s.SyncCustomerRelay(); syncErr != nil {
			return nil, syncErr
		}
		relayInbound, err = getCustomerRelayInbound(db)
		if err != nil {
			return nil, err
		}
	}
	rows, err := s.activeCustomerRelayRows(db, token)
	if err != nil {
		return nil, err
	}

	publicHost := hostWithoutPort(host)
	links := make([]string, 0, len(rows))
	clashProxies := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if _, err := buildUpstreamRelayOutbound(row); err != nil {
			continue
		}
		uuid := relayClientUUID(row.CustomerID, row.NodeID)
		name := customerRelayDisplayName(row)
		if name == "" {
			name = fmt.Sprintf("Node %d", row.NodeID)
		}
		links = append(links, buildRelayVmessLink(publicHost, relayInbound.Port, uuid, name))
		clashProxies = append(clashProxies, buildRelayClashProxy(publicHost, relayInbound.Port, uuid, name))
	}
	if len(links) == 0 && len(clashProxies) == 0 {
		return nil, ErrCustomerNoEnabledNodes
	}
	return &CustomerSubscriptionContent{
		Links:      links,
		ClashProxy: clashProxies,
		Customer:   customer,
	}, nil
}

func applyCustomerRelayRoutes(xrayConfig *xray.Config) error {
	if xrayConfig == nil {
		return nil
	}
	s := &SubscriptionMarketService{}
	db := database.GetDB()
	relayInbound, err := getCustomerRelayInbound(db)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	rows, err := s.activeCustomerRelayRows(db, "")
	if err != nil {
		return err
	}

	outbounds, err := xrayOutbounds(xrayConfig)
	if err != nil {
		return err
	}
	rules, routing, err := xrayRoutingRules(xrayConfig)
	if err != nil {
		return err
	}

	outboundByTag := make(map[string]struct{}, len(outbounds))
	for _, outbound := range outbounds {
		if m, ok := outbound.(map[string]any); ok {
			if tag, _ := m["tag"].(string); tag != "" {
				outboundByTag[tag] = struct{}{}
			}
		}
	}

	insertedRules := buildCustomerRelayDirectRules(relayInbound.Tag)
	for _, row := range rows {
		outbound, err := buildUpstreamRelayOutbound(row)
		if err != nil {
			logger.Warningf("skip unsupported upstream relay route %d (%s): %v", row.NodeID, row.NodeName, err)
			continue
		}
		tag, _ := outbound["tag"].(string)
		if _, exists := outboundByTag[tag]; !exists {
			outbounds = append(outbounds, outbound)
			outboundByTag[tag] = struct{}{}
		}
		insertedRules = append(insertedRules, map[string]any{
			"type":        "field",
			"inboundTag":  []string{relayInbound.Tag},
			"user":        []string{relayClientEmail(row.CustomerID, row.NodeID)},
			"outboundTag": tag,
		})
	}
	if len(insertedRules) == 2 {
		return nil
	}

	outboundBytes, err := json.MarshalIndent(outbounds, "", "  ")
	if err != nil {
		return err
	}
	xrayConfig.OutboundConfigs = outboundBytes

	routing["rules"] = prependAfterAPIRule(rules, insertedRules...)
	routingBytes, err := json.MarshalIndent(routing, "", "  ")
	if err != nil {
		return err
	}
	xrayConfig.RouterConfig = routingBytes
	return nil
}

func applyInboundUpstreamRelayRoutes(xrayConfig *xray.Config) error {
	if xrayConfig == nil {
		return nil
	}
	db := database.GetDB()
	rows, err := inboundUpstreamRelayRows(db)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	outbounds, err := xrayOutbounds(xrayConfig)
	if err != nil {
		return err
	}
	rules, routing, err := xrayRoutingRules(xrayConfig)
	if err != nil {
		return err
	}
	outboundByTag := make(map[string]struct{}, len(outbounds))
	for _, outbound := range outbounds {
		if m, ok := outbound.(map[string]any); ok {
			if tag, _ := m["tag"].(string); tag != "" {
				outboundByTag[tag] = struct{}{}
			}
		}
	}

	insertedRules := make([]map[string]any, 0, len(rows)*3)
	seenInbound := map[string]struct{}{}
	for _, row := range rows {
		outbound, err := buildUpstreamRelayOutbound(row)
		if err != nil {
			logger.Warningf("skip unsupported inbound upstream relay node %d (%s): %v", row.NodeID, row.NodeName, err)
			continue
		}
		tag, _ := outbound["tag"].(string)
		if _, exists := outboundByTag[tag]; !exists {
			outbounds = append(outbounds, outbound)
			outboundByTag[tag] = struct{}{}
		}
		inboundTag := row.CustomerToken
		if _, ok := seenInbound[inboundTag]; !ok {
			insertedRules = append(insertedRules, buildCustomerRelayDirectRules(inboundTag)...)
			seenInbound[inboundTag] = struct{}{}
		}
		insertedRules = append(insertedRules, map[string]any{
			"type":        "field",
			"inboundTag":  []string{inboundTag},
			"outboundTag": tag,
		})
	}
	if len(insertedRules) == 0 {
		return nil
	}

	outboundBytes, err := json.MarshalIndent(outbounds, "", "  ")
	if err != nil {
		return err
	}
	xrayConfig.OutboundConfigs = outboundBytes
	routing["rules"] = prependAfterAPIRule(rules, insertedRules...)
	routingBytes, err := json.MarshalIndent(routing, "", "  ")
	if err != nil {
		return err
	}
	xrayConfig.RouterConfig = routingBytes
	return nil
}

func (s *SubscriptionMarketService) activeCustomerRelayRows(db *gorm.DB, token string) ([]customerRelayRow, error) {
	now := time.Now().UnixMilli()
	query := db.Table("customer_subscription_nodes").
		Select(`customer_subscriptions.id AS customer_id,
			customer_subscriptions.name AS customer_name,
			customer_subscriptions.token AS customer_token,
			customer_subscriptions.expiry_time AS customer_expiry_time,
			upstream_nodes.id AS node_id,
			upstream_nodes.name AS node_name,
			upstream_nodes.protocol AS node_protocol,
			upstream_nodes.link AS node_link,
			upstream_nodes.clash AS node_clash,
			upstream_nodes.source_type AS node_source_type,
			upstream_subscriptions.id AS upstream_id,
			upstream_subscriptions.id AS upstream_sort,
			upstream_nodes.sort AS node_sort`).
		Joins("JOIN customer_subscriptions ON customer_subscriptions.id = customer_subscription_nodes.customer_id").
		Joins("JOIN upstream_nodes ON upstream_nodes.id = customer_subscription_nodes.node_id").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
		Where("customer_subscriptions.enable = ? AND upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true, true).
		Where("(customer_subscriptions.expiry_time = 0 OR customer_subscriptions.expiry_time > ?)", now)
	if strings.TrimSpace(token) != "" {
		query = query.Where("customer_subscriptions.token = ?", strings.TrimSpace(token))
	}
	query = query.Order("upstream_subscriptions.id desc, upstream_nodes.sort asc, upstream_nodes.id asc")
	var rows []customerRelayRow
	return rows, query.Scan(&rows).Error
}

func inboundUpstreamRelayRows(db *gorm.DB) ([]customerRelayRow, error) {
	var rows []customerRelayRow
	err := db.Table("inbound_subscription_nodes").
		Select(`inbounds.id AS customer_id,
			inbounds.remark AS customer_name,
			inbounds.tag AS customer_token,
			inbounds.expiry_time AS customer_expiry_time,
			upstream_nodes.id AS node_id,
			upstream_nodes.name AS node_name,
			upstream_nodes.protocol AS node_protocol,
			upstream_nodes.link AS node_link,
			upstream_nodes.clash AS node_clash,
			upstream_nodes.source_type AS node_source_type,
			upstream_subscriptions.id AS upstream_id,
			upstream_subscriptions.id AS upstream_sort,
			upstream_nodes.sort AS node_sort`).
		Joins("JOIN inbounds ON inbounds.id = inbound_subscription_nodes.inbound_id").
		Joins("JOIN upstream_nodes ON upstream_nodes.id = inbound_subscription_nodes.node_id").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
		Where("inbounds.enable = ? AND upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true, true).
		Order("inbounds.id asc, upstream_subscriptions.id desc, upstream_nodes.sort asc, upstream_nodes.id asc").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	firstByInbound := make(map[int]customerRelayRow)
	inboundIDs := make([]int, 0)
	for _, row := range rows {
		if _, ok := firstByInbound[row.CustomerID]; ok {
			continue
		}
		firstByInbound[row.CustomerID] = row
		inboundIDs = append(inboundIDs, row.CustomerID)
	}
	sort.Ints(inboundIDs)
	result := make([]customerRelayRow, 0, len(inboundIDs))
	for _, id := range inboundIDs {
		result = append(result, firstByInbound[id])
	}
	return result, nil
}

func ensureCustomerRelayInbound(tx *gorm.DB) (*model.Inbound, bool, error) {
	inbound, err := getCustomerRelayInbound(tx)
	if err == nil {
		changed := false
		if inbound.Protocol != model.VMESS {
			inbound.Protocol = model.VMESS
			changed = true
		}
		if !inbound.Enable {
			inbound.Enable = true
			changed = true
		}
		if inbound.Port <= 0 {
			port, err := findCustomerRelayPort(tx)
			if err != nil {
				return nil, false, err
			}
			inbound.Port = port
			changed = true
		}
		if strings.TrimSpace(inbound.Tag) == "" {
			inbound.Tag = fmt.Sprintf("inbound-%d", inbound.Port)
			changed = true
		}
		streamSettings := defaultCustomerRelayStreamSettings()
		if strings.TrimSpace(inbound.StreamSettings) != strings.TrimSpace(streamSettings) {
			inbound.StreamSettings = streamSettings
			changed = true
		}
		if strings.TrimSpace(inbound.Sniffing) == "" {
			inbound.Sniffing = `{"enabled":true,"destOverride":["http","tls","quic"],"metadataOnly":false,"routeOnly":false}`
			changed = true
		}
		if changed {
			if err := tx.Save(inbound).Error; err != nil {
				return nil, false, err
			}
		}
		return inbound, changed, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	port, err := findCustomerRelayPort(tx)
	if err != nil {
		return nil, false, err
	}
	inbound = &model.Inbound{
		UserId:         1,
		Up:             0,
		Down:           0,
		Total:          0,
		Remark:         customerRelayRemark,
		Enable:         true,
		ExpiryTime:     0,
		DeviceLimit:    0,
		TrafficReset:   "never",
		Listen:         "",
		Port:           port,
		Protocol:       model.VMESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: defaultCustomerRelayStreamSettings(),
		Tag:            fmt.Sprintf("inbound-%d", port),
		Sniffing:       `{"enabled":true,"destOverride":["http","tls","quic"],"metadataOnly":false,"routeOnly":false}`,
	}
	if err := tx.Create(inbound).Error; err != nil {
		return nil, false, err
	}
	return inbound, true, nil
}

func getCustomerRelayInbound(db *gorm.DB) (*model.Inbound, error) {
	var inbound model.Inbound
	err := db.Where("remark = ?", customerRelayRemark).First(&inbound).Error
	if err != nil {
		return nil, err
	}
	return &inbound, nil
}

func findCustomerRelayPort(db *gorm.DB) (int, error) {
	var used []int
	if err := db.Model(&model.Inbound{}).Pluck("port", &used).Error; err != nil {
		return 0, err
	}
	usedMap := make(map[int]struct{}, len(used))
	for _, port := range used {
		usedMap[port] = struct{}{}
	}
	for port := 30000; port <= 60999; port++ {
		if _, exists := usedMap[port]; exists {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no free relay port available")
}

func buildRelayClients(nodes []customerRelayNode) []map[string]any {
	clients := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		clients = append(clients, map[string]any{
			"id":         node.uuid,
			"security":   "auto",
			"email":      node.email,
			"limitIp":    0,
			"totalGB":    0,
			"expiryTime": node.row.CustomerExpiryTime,
			"enable":     true,
			"tgId":       "",
			"subId":      "",
			"comment":    node.row.NodeName,
			"reset":      0,
		})
	}
	return clients
}

func defaultCustomerRelayStreamSettings() string {
	settings := map[string]any{
		"network":  "tcp",
		"security": "none",
		"tcpSettings": map[string]any{
			"acceptProxyProtocol": false,
			"header": map[string]any{
				"type": "http",
				"request": map[string]any{
					"version": "1.1",
					"method":  "GET",
					"path":    []string{"/"},
					"headers": map[string]any{},
				},
				"response": map[string]any{
					"version": "1.1",
					"status":  "200",
					"reason":  "OK",
					"headers": map[string]any{},
				},
			},
		},
	}
	b, _ := json.MarshalIndent(settings, "", "  ")
	return string(b)
}

func buildCustomerRelayDirectRules(inboundTag string) []map[string]any {
	return []map[string]any{
		{
			"type":        "field",
			"inboundTag":  []string{inboundTag},
			"domain":      []string{"geosite:cn"},
			"outboundTag": "direct",
		},
		{
			"type":        "field",
			"inboundTag":  []string{inboundTag},
			"ip":          []string{"geoip:cn", "geoip:private"},
			"outboundTag": "direct",
		},
	}
}

func relayClientEmail(customerID, nodeID int) string {
	return fmt.Sprintf("xui-relay-c%d-n%d", customerID, nodeID)
}

func relayClientUUID(customerID, nodeID int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("xui-relay-client:%d:%d", customerID, nodeID)))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func upstreamRelayOutboundTag(nodeID int) string {
	return fmt.Sprintf("xui-upstream-node-%d", nodeID)
}

func buildRelayVmessLink(host string, port int, uuid string, name string) string {
	obj := map[string]any{
		"v":    "2",
		"ps":   name,
		"add":  host,
		"port": port,
		"id":   uuid,
		"aid":  "0",
		"scy":  "auto",
		"net":  "tcp",
		"type": "http",
		"host": "",
		"path": "/",
		"tls":  "none",
	}
	b, _ := json.MarshalIndent(obj, "", "  ")
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

func buildRelayClashProxy(host string, port int, uuid string, name string) map[string]any {
	return map[string]any{
		"name":       name,
		"type":       "vmess",
		"server":     host,
		"port":       port,
		"uuid":       uuid,
		"alterId":    0,
		"cipher":     "auto",
		"udp":        true,
		"network":    "http",
		"servername": "",
		"http-opts": map[string]any{
			"method": "GET",
			"path":   []string{"/"},
		},
	}
}

func customerRelayDisplayName(row customerRelayRow) string {
	if row.CustomerExpiryTime > 0 {
		t := time.UnixMilli(row.CustomerExpiryTime)
		return fmt.Sprintf("过期日期:%d年%d月%d日", t.Year(), int(t.Month()), t.Day())
	}
	return strings.TrimSpace(row.NodeName)
}

func hostWithoutPort(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	if strings.Count(host, ":") == 1 {
		if h, _, ok := strings.Cut(host, ":"); ok {
			return h
		}
	}
	return strings.Trim(host, "[]")
}

func xrayOutbounds(xrayConfig *xray.Config) ([]any, error) {
	var outbounds []any
	rawOutbounds := strings.TrimSpace(string(xrayConfig.OutboundConfigs))
	if rawOutbounds != "" && rawOutbounds != "null" {
		if err := json.Unmarshal(xrayConfig.OutboundConfigs, &outbounds); err != nil {
			return nil, err
		}
	}
	return outbounds, nil
}

func xrayRoutingRules(xrayConfig *xray.Config) ([]any, map[string]any, error) {
	routing := map[string]any{}
	rawRouting := strings.TrimSpace(string(xrayConfig.RouterConfig))
	if rawRouting != "" && rawRouting != "null" {
		if err := json.Unmarshal(xrayConfig.RouterConfig, &routing); err != nil {
			return nil, nil, err
		}
	}
	var rules []any
	if existing, ok := routing["rules"].([]any); ok {
		rules = existing
	}
	return rules, routing, nil
}

func buildUpstreamRelayOutbound(row customerRelayRow) (map[string]any, error) {
	tag := upstreamRelayOutboundTag(row.NodeID)
	if strings.TrimSpace(row.NodeLink) != "" {
		return buildUpstreamURIOutbound(tag, row.NodeLink)
	}
	if strings.TrimSpace(row.NodeClash) != "" {
		return buildUpstreamClashOutbound(tag, row.NodeClash)
	}
	return nil, fmt.Errorf("node has no link or clash config")
}

func buildUpstreamURIOutbound(tag string, link string) (map[string]any, error) {
	scheme := strings.ToLower(strings.TrimSpace(strings.SplitN(link, "://", 2)[0]))
	switch scheme {
	case "vmess":
		return buildVmessOutboundFromURI(tag, link)
	case "vless":
		return buildVLESSOutboundFromURI(tag, link)
	case "trojan":
		return buildTrojanOutboundFromURI(tag, link)
	case "ss":
		return buildSSOutboundFromURI(tag, link)
	case "hysteria2", "hy2":
		return buildHysteria2OutboundFromURI(tag, link)
	default:
		return nil, fmt.Errorf("unsupported uri protocol %q", scheme)
	}
}

func buildVmessOutboundFromURI(tag string, link string) (map[string]any, error) {
	payload := strings.TrimSpace(strings.TrimPrefix(link, "vmess://"))
	decoded, ok := decodeBase64Any(payload)
	if !ok {
		return nil, fmt.Errorf("invalid vmess payload")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(decoded), &data); err != nil {
		return nil, err
	}
	address := stringValue(data["add"])
	port := intValue(data["port"])
	uuid := stringValue(data["id"])
	if address == "" || port <= 0 || uuid == "" {
		return nil, fmt.Errorf("vmess node misses address, port, or id")
	}
	security := stringValue(data["scy"])
	if security == "" {
		security = "auto"
	}
	return map[string]any{
		"tag":      tag,
		"protocol": "vmess",
		"settings": map[string]any{
			"vnext": []any{
				map[string]any{
					"address": address,
					"port":    port,
					"users": []any{
						map[string]any{
							"id":       uuid,
							"alterId":  intValue(data["aid"]),
							"security": security,
						},
					},
				},
			},
		},
		"streamSettings": streamSettingsFromVMess(data),
	}, nil
}

func buildVLESSOutboundFromURI(tag string, link string) (map[string]any, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	port := intValue(u.Port())
	uuid := u.User.Username()
	if u.Hostname() == "" || port <= 0 || uuid == "" {
		return nil, fmt.Errorf("vless node misses host, port, or uuid")
	}
	q := u.Query()
	encryption := q.Get("encryption")
	if encryption == "" {
		encryption = "none"
	}
	user := map[string]any{
		"id":         uuid,
		"encryption": encryption,
	}
	if flow := q.Get("flow"); flow != "" {
		user["flow"] = flow
	}
	return map[string]any{
		"tag":      tag,
		"protocol": "vless",
		"settings": map[string]any{
			"vnext": []any{
				map[string]any{
					"address": u.Hostname(),
					"port":    port,
					"users":   []any{user},
				},
			},
		},
		"streamSettings": streamSettingsFromURL(q),
	}, nil
}

func buildTrojanOutboundFromURI(tag string, link string) (map[string]any, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	port := intValue(u.Port())
	password := u.User.Username()
	if u.Hostname() == "" || port <= 0 || password == "" {
		return nil, fmt.Errorf("trojan node misses host, port, or password")
	}
	return map[string]any{
		"tag":      tag,
		"protocol": "trojan",
		"settings": map[string]any{
			"servers": []any{
				map[string]any{
					"address":  u.Hostname(),
					"port":     port,
					"password": password,
				},
			},
		},
		"streamSettings": streamSettingsFromURL(u.Query()),
	}, nil
}

func buildSSOutboundFromURI(tag string, link string) (map[string]any, error) {
	method, password, host, port, err := parseSSURI(link)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"tag":      tag,
		"protocol": "shadowsocks",
		"settings": map[string]any{
			"servers": []any{
				map[string]any{
					"address":  host,
					"port":     port,
					"method":   method,
					"password": password,
				},
			},
		},
	}, nil
}

func buildHysteria2OutboundFromURI(tag string, link string) (map[string]any, error) {
	u, err := url.Parse(strings.Replace(link, "hy2://", "hysteria2://", 1))
	if err != nil {
		return nil, err
	}
	port := intValue(u.Port())
	password := u.User.Username()
	if u.Hostname() == "" || port <= 0 || password == "" {
		return nil, fmt.Errorf("hysteria2 node misses host, port, or password")
	}
	return hysteria2Outbound(tag, u.Hostname(), port, password, u.Query(), nil), nil
}

func buildUpstreamClashOutbound(tag string, raw string) (map[string]any, error) {
	var proxy map[string]any
	if err := json.Unmarshal([]byte(raw), &proxy); err != nil {
		return nil, err
	}
	proxyType := strings.ToLower(stringValue(proxy["type"]))
	switch proxyType {
	case "vmess":
		return clashVMessOutbound(tag, proxy)
	case "vless":
		return clashVLESSOutbound(tag, proxy)
	case "trojan":
		return clashTrojanOutbound(tag, proxy)
	case "ss", "shadowsocks":
		return clashSSOutbound(tag, proxy)
	case "hysteria2", "hy2":
		return clashHysteria2Outbound(tag, proxy)
	default:
		return nil, fmt.Errorf("unsupported clash protocol %q", proxyType)
	}
}

func clashVMessOutbound(tag string, proxy map[string]any) (map[string]any, error) {
	address := stringValue(proxy["server"])
	port := intValue(proxy["port"])
	uuid := stringValue(proxy["uuid"])
	if address == "" || port <= 0 || uuid == "" {
		return nil, fmt.Errorf("vmess clash node misses server, port, or uuid")
	}
	cipher := stringValue(proxy["cipher"])
	if cipher == "" {
		cipher = "auto"
	}
	return map[string]any{
		"tag":      tag,
		"protocol": "vmess",
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": address,
				"port":    port,
				"users": []any{map[string]any{
					"id":       uuid,
					"alterId":  intValue(proxy["alterId"]),
					"security": cipher,
				}},
			}},
		},
		"streamSettings": streamSettingsFromClash(proxy),
	}, nil
}

func clashVLESSOutbound(tag string, proxy map[string]any) (map[string]any, error) {
	address := stringValue(proxy["server"])
	port := intValue(proxy["port"])
	uuid := stringValue(proxy["uuid"])
	if address == "" || port <= 0 || uuid == "" {
		return nil, fmt.Errorf("vless clash node misses server, port, or uuid")
	}
	user := map[string]any{"id": uuid, "encryption": "none"}
	if flow := stringValue(proxy["flow"]); flow != "" {
		user["flow"] = flow
	}
	return map[string]any{
		"tag":      tag,
		"protocol": "vless",
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": address,
				"port":    port,
				"users":   []any{user},
			}},
		},
		"streamSettings": streamSettingsFromClash(proxy),
	}, nil
}

func clashTrojanOutbound(tag string, proxy map[string]any) (map[string]any, error) {
	address := stringValue(proxy["server"])
	port := intValue(proxy["port"])
	password := stringValue(proxy["password"])
	if address == "" || port <= 0 || password == "" {
		return nil, fmt.Errorf("trojan clash node misses server, port, or password")
	}
	return map[string]any{
		"tag":      tag,
		"protocol": "trojan",
		"settings": map[string]any{
			"servers": []any{map[string]any{
				"address":  address,
				"port":     port,
				"password": password,
			}},
		},
		"streamSettings": streamSettingsFromClash(proxy),
	}, nil
}

func clashSSOutbound(tag string, proxy map[string]any) (map[string]any, error) {
	address := stringValue(proxy["server"])
	port := intValue(proxy["port"])
	method := stringValue(proxy["cipher"])
	password := stringValue(proxy["password"])
	if address == "" || port <= 0 || method == "" || password == "" {
		return nil, fmt.Errorf("ss clash node misses server, port, cipher, or password")
	}
	return map[string]any{
		"tag":      tag,
		"protocol": "shadowsocks",
		"settings": map[string]any{
			"servers": []any{map[string]any{
				"address":  address,
				"port":     port,
				"method":   method,
				"password": password,
			}},
		},
	}, nil
}

func clashHysteria2Outbound(tag string, proxy map[string]any) (map[string]any, error) {
	address := stringValue(proxy["server"])
	port := intValue(proxy["port"])
	password := stringValue(proxy["password"])
	if address == "" || port <= 0 || password == "" {
		return nil, fmt.Errorf("hysteria2 clash node misses server, port, or password")
	}
	return hysteria2Outbound(tag, address, port, password, nil, proxy), nil
}

func hysteria2Outbound(tag string, address string, port int, password string, query url.Values, proxy map[string]any) map[string]any {
	return map[string]any{
		"tag":      tag,
		"protocol": "hysteria",
		"settings": map[string]any{
			"version": 2,
			"address": address,
			"port":    port,
		},
		"streamSettings": hysteria2StreamSettings(password, query, proxy),
	}
}

func streamSettingsFromVMess(data map[string]any) map[string]any {
	q := url.Values{}
	q.Set("type", firstNonEmpty(stringValue(data["net"]), "tcp"))
	q.Set("security", securityFromVMess(stringValue(data["tls"])))
	if v := stringValue(data["type"]); v != "" {
		q.Set("headerType", v)
	}
	for _, pair := range [][2]string{
		{"host", "host"},
		{"path", "path"},
		{"sni", "sni"},
		{"fp", "fp"},
		{"alpn", "alpn"},
		{"serviceName", "path"},
		{"authority", "authority"},
		{"mode", "type"},
	} {
		if value := stringValue(data[pair[1]]); value != "" {
			q.Set(pair[0], value)
		}
	}
	return streamSettingsFromURL(q)
}

func securityFromVMess(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "tls", "reality":
		return value
	default:
		return "none"
	}
}

func streamSettingsFromURL(q url.Values) map[string]any {
	network := firstNonEmpty(q.Get("type"), "tcp")
	if network == "none" {
		network = "tcp"
	}
	security := firstNonEmpty(q.Get("security"), "none")
	stream := map[string]any{
		"network":  network,
		"security": security,
	}
	switch network {
	case "tcp":
		headerType := firstNonEmpty(q.Get("headerType"), "none")
		tcp := map[string]any{
			"header": map[string]any{
				"type": headerType,
			},
		}
		if headerType == "http" {
			tcp["header"].(map[string]any)["request"] = map[string]any{
				"path":    splitList(firstNonEmpty(q.Get("path"), "/")),
				"headers": hostHeaders(q.Get("host")),
			}
		}
		stream["tcpSettings"] = tcp
	case "ws":
		stream["wsSettings"] = map[string]any{
			"path":    firstNonEmpty(q.Get("path"), "/"),
			"headers": hostHeaderMap(q.Get("host")),
		}
	case "grpc":
		stream["grpcSettings"] = map[string]any{
			"serviceName": q.Get("serviceName"),
			"authority":   q.Get("authority"),
			"multiMode":   q.Get("mode") == "multi",
		}
	case "httpupgrade":
		stream["httpupgradeSettings"] = map[string]any{
			"path": firstNonEmpty(q.Get("path"), "/"),
			"host": q.Get("host"),
		}
	}
	if security == "tls" {
		stream["tlsSettings"] = map[string]any{
			"serverName":    firstNonEmpty(q.Get("sni"), q.Get("peer")),
			"fingerprint":   q.Get("fp"),
			"alpn":          splitList(q.Get("alpn")),
			"allowInsecure": truthy(q.Get("allowInsecure")),
		}
	}
	if security == "reality" {
		stream["realitySettings"] = map[string]any{
			"serverName":  q.Get("sni"),
			"fingerprint": q.Get("fp"),
			"publicKey":   q.Get("pbk"),
			"shortId":     q.Get("sid"),
			"spiderX":     q.Get("spx"),
		}
	}
	return stream
}

func streamSettingsFromClash(proxy map[string]any) map[string]any {
	q := url.Values{}
	network := stringValue(proxy["network"])
	if network == "http" {
		q.Set("type", "tcp")
		q.Set("headerType", "http")
	} else if network != "" {
		q.Set("type", network)
	}
	if truthyAny(proxy["tls"]) {
		q.Set("security", "tls")
	}
	if sni := firstNonEmpty(stringValue(proxy["servername"]), stringValue(proxy["sni"])); sni != "" {
		q.Set("sni", sni)
	}
	if truthyAny(proxy["skip-cert-verify"]) {
		q.Set("allowInsecure", "true")
	}
	if wsOpts, ok := proxy["ws-opts"].(map[string]any); ok {
		q.Set("type", "ws")
		if path := stringValue(wsOpts["path"]); path != "" {
			q.Set("path", path)
		}
		if headers, ok := wsOpts["headers"].(map[string]any); ok {
			q.Set("host", stringValue(headers["Host"]))
		}
	}
	if grpcOpts, ok := proxy["grpc-opts"].(map[string]any); ok {
		q.Set("type", "grpc")
		q.Set("serviceName", stringValue(grpcOpts["grpc-service-name"]))
	}
	if httpOpts, ok := proxy["http-opts"].(map[string]any); ok {
		q.Set("type", "tcp")
		q.Set("headerType", "http")
		if path := stringSliceValue(httpOpts["path"]); len(path) > 0 {
			q.Set("path", strings.Join(path, ","))
		}
	}
	return streamSettingsFromURL(q)
}

func hysteria2StreamSettings(password string, query url.Values, proxy map[string]any) map[string]any {
	sni := ""
	allowInsecure := false
	fingerprint := ""
	up := ""
	down := ""
	congestion := ""
	obfs := ""
	obfsPassword := ""
	if query != nil {
		sni = firstNonEmpty(query.Get("sni"), query.Get("peer"))
		allowInsecure = truthy(query.Get("insecure")) || truthy(query.Get("allowInsecure"))
		fingerprint = query.Get("fp")
		up = query.Get("up")
		down = query.Get("down")
		congestion = query.Get("congestion")
		obfs = query.Get("obfs")
		obfsPassword = firstNonEmpty(query.Get("obfs-password"), query.Get("obfs_password"))
	}
	if proxy != nil {
		sni = firstNonEmpty(stringValue(proxy["sni"]), stringValue(proxy["servername"]), sni)
		allowInsecure = truthyAny(proxy["skip-cert-verify"])
		fingerprint = firstNonEmpty(stringValue(proxy["fingerprint"]), fingerprint)
		up = firstNonEmpty(stringValue(proxy["up"]), up)
		down = firstNonEmpty(stringValue(proxy["down"]), down)
		congestion = firstNonEmpty(stringValue(proxy["congestion"]), congestion)
		obfs = firstNonEmpty(stringValue(proxy["obfs"]), obfs)
		obfsPassword = firstNonEmpty(stringValue(proxy["obfs-password"]), stringValue(proxy["obfs_password"]), obfsPassword)
	}
	stream := map[string]any{
		"network":  "hysteria",
		"security": "tls",
		"tlsSettings": map[string]any{
			"serverName":    sni,
			"fingerprint":   firstNonEmpty(fingerprint, "chrome"),
			"alpn":          []string{"h3"},
			"allowInsecure": allowInsecure,
		},
		"hysteriaSettings": map[string]any{
			"version": 2,
			"auth":    password,
		},
	}
	hysteriaSettings := stream["hysteriaSettings"].(map[string]any)
	if up != "" {
		hysteriaSettings["up"] = up
	}
	if down != "" {
		hysteriaSettings["down"] = down
	}
	if congestion != "" {
		hysteriaSettings["congestion"] = congestion
	}
	if obfs != "" {
		stream["udpmasks"] = []map[string]any{
			{
				"type": obfs,
				"settings": map[string]any{
					"password": obfsPassword,
				},
			},
		}
	}
	return stream
}

func parseSSURI(link string) (string, string, string, int, error) {
	raw := strings.TrimPrefix(link, "ss://")
	if idx := strings.Index(raw, "#"); idx >= 0 {
		raw = raw[:idx]
	}
	if idx := strings.Index(raw, "?"); idx >= 0 {
		raw = raw[:idx]
	}
	if strings.Contains(raw, "@") {
		left, right, _ := strings.Cut(raw, "@")
		if decoded, ok := decodeBase64Any(left); ok && strings.Contains(decoded, ":") {
			left = decoded
		}
		method, password, ok := strings.Cut(left, ":")
		if !ok {
			return "", "", "", 0, fmt.Errorf("invalid ss user info")
		}
		host, port, err := splitHostPortLoose(right)
		if err != nil {
			return "", "", "", 0, err
		}
		return method, password, host, port, nil
	}
	decoded, ok := decodeBase64Any(raw)
	if !ok {
		return "", "", "", 0, fmt.Errorf("invalid ss payload")
	}
	user, address, ok := strings.Cut(decoded, "@")
	if !ok {
		return "", "", "", 0, fmt.Errorf("invalid ss decoded payload")
	}
	method, password, ok := strings.Cut(user, ":")
	if !ok {
		return "", "", "", 0, fmt.Errorf("invalid ss decoded user")
	}
	host, port, err := splitHostPortLoose(address)
	if err != nil {
		return "", "", "", 0, err
	}
	return method, password, host, port, nil
}

func splitHostPortLoose(value string) (string, int, error) {
	u, err := url.Parse("//" + value)
	if err == nil && u.Hostname() != "" {
		port := intValue(u.Port())
		if port > 0 {
			return u.Hostname(), port, nil
		}
	}
	host, portText, ok := strings.Cut(value, ":")
	if !ok {
		return "", 0, fmt.Errorf("missing port")
	}
	port := intValue(portText)
	if strings.TrimSpace(host) == "" || port <= 0 {
		return "", 0, fmt.Errorf("invalid host or port")
	}
	return host, port, nil
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := strconv.Atoi(string(v))
		return i
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(v))
		return i
	default:
		return 0
	}
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func stringSliceValue(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s := stringValue(item); s != "" {
				result = append(result, s)
			}
		}
		return result
	case string:
		return splitList(v)
	default:
		return nil
	}
}

func splitList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if s := strings.TrimSpace(part); s != "" {
			result = append(result, s)
		}
	}
	return result
}

func hostHeaderMap(host string) map[string]any {
	host = strings.TrimSpace(host)
	if host == "" {
		return map[string]any{}
	}
	return map[string]any{"Host": host}
}

func hostHeaders(host string) map[string]any {
	host = strings.TrimSpace(host)
	if host == "" {
		return map[string]any{}
	}
	return map[string]any{"Host": splitList(host)}
}

func truthy(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "1" || value == "true" || value == "yes"
}

func truthyAny(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return truthy(v)
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
