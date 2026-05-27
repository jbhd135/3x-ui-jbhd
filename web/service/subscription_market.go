package service

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/goccy/go-yaml"
	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/random"

	"gorm.io/gorm"
)

const upstreamFetchTimeout = 20 * time.Second
const upstreamFetchUserAgent = "Shadowrocket/2026 3x-ui-subscription-market/1.0"

var (
	ErrSubscriptionURLRequired   = errors.New("subscription URL is required")
	ErrSubscriptionInvalidURL    = errors.New("subscription URL must be a valid http or https URL")
	ErrSubscriptionNameRequired  = errors.New("name is required")
	ErrSubscriptionNotFound      = errors.New("subscription not found")
	ErrSubscriptionNoNodes       = errors.New("no supported nodes found in subscription")
	ErrCustomerNotFound          = errors.New("customer subscription not found")
	ErrCustomerDisabled          = errors.New("customer subscription is disabled")
	ErrCustomerExpired           = errors.New("customer subscription is expired")
	ErrCustomerNoEnabledNodes    = errors.New("customer has no enabled nodes")
	ErrCustomerNoURIEnabledNodes = errors.New("customer has no URI nodes enabled")
	ErrInboundNotFound           = errors.New("inbound not found")
)

type SubscriptionMarketService struct{}

type UpstreamNodeView struct {
	Id           int    `json:"id"`
	UpstreamId   int    `json:"upstreamId"`
	UpstreamName string `json:"upstreamName"`
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	Link         string `json:"link"`
	SourceType   string `json:"sourceType"`
	Enable       bool   `json:"enable"`
	Sort         int    `json:"sort"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

type CustomerSubscriptionView struct {
	Id              int    `json:"id"`
	Name            string `json:"name"`
	Token           string `json:"token"`
	Enable          bool   `json:"enable"`
	ExpiryTime      int64  `json:"expiryTime"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
	NodeIds         []int  `json:"nodeIds"`
	NodeCount       int    `json:"nodeCount"`
	SubscriptionURL string `json:"subscriptionUrl,omitempty"`
}

type CustomerSubscriptionContent struct {
	Links      []string
	ClashProxy []map[string]any
	Customer   model.CustomerSubscription
}

type InboundSubscriptionContent struct {
	Links      []string
	ClashProxy []map[string]any
}

type parsedUpstreamNode struct {
	Name       string
	Protocol   string
	Link       string
	Clash      string
	SourceType string
	Sort       int
}

type upstreamTrafficInfo struct {
	Upload     int64
	Download   int64
	Total      int64
	ExpiryTime int64
}

func (s *SubscriptionMarketService) GetUpstreams() ([]model.UpstreamSubscription, error) {
	db := database.GetDB()
	var upstreams []model.UpstreamSubscription
	err := db.Model(model.UpstreamSubscription{}).
		Preload("Nodes", func(tx *gorm.DB) *gorm.DB {
			return tx.Order("sort asc, id asc")
		}).
		Order("id desc").
		Find(&upstreams).Error
	return upstreams, err
}

func (s *SubscriptionMarketService) CreateUpstream(name, rawURL string, enable bool) (*model.UpstreamSubscription, error) {
	name = strings.TrimSpace(name)
	rawURL, err := sanitizeHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, ErrSubscriptionNameRequired
	}
	upstream := &model.UpstreamSubscription{Name: name, Url: rawURL, Enable: enable}
	if err := database.GetDB().Create(upstream).Error; err != nil {
		return nil, err
	}
	return upstream, nil
}

func (s *SubscriptionMarketService) UpdateUpstream(id int, name, rawURL string, enable bool) (*model.UpstreamSubscription, error) {
	name = strings.TrimSpace(name)
	rawURL, err := sanitizeHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, ErrSubscriptionNameRequired
	}
	db := database.GetDB()
	var upstream model.UpstreamSubscription
	if err := db.First(&upstream, id).Error; err != nil {
		return nil, mapGormNotFound(err, ErrSubscriptionNotFound)
	}
	upstream.Name = name
	upstream.Url = rawURL
	upstream.Enable = enable
	if err := db.Save(&upstream).Error; err != nil {
		return nil, err
	}
	return &upstream, nil
}

func (s *SubscriptionMarketService) DeleteUpstream(id int) error {
	db := database.GetDB()
	return db.Transaction(func(tx *gorm.DB) error {
		var nodeIDs []int
		if err := tx.Model(model.UpstreamNode{}).Where("upstream_id = ?", id).Pluck("id", &nodeIDs).Error; err != nil {
			return err
		}
		if len(nodeIDs) > 0 {
			if err := tx.Where("node_id IN ?", nodeIDs).Delete(&model.CustomerSubscriptionNode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("node_id IN ?", nodeIDs).Delete(&model.InboundSubscriptionNode{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("upstream_id = ?", id).Delete(&model.UpstreamNode{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&model.UpstreamSubscription{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrSubscriptionNotFound
		}
		return nil
	})
}

func (s *SubscriptionMarketService) SetUpstreamEnable(id int, enable bool) error {
	result := database.GetDB().Model(&model.UpstreamSubscription{}).Where("id = ?", id).Update("enable", enable)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

func (s *SubscriptionMarketService) SetNodeEnable(id int, enable bool) error {
	result := database.GetDB().Model(&model.UpstreamNode{}).Where("id = ?", id).Update("enable", enable)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

func (s *SubscriptionMarketService) SyncUpstream(id int) (*model.UpstreamSubscription, error) {
	db := database.GetDB()
	var upstream model.UpstreamSubscription
	if err := db.First(&upstream, id).Error; err != nil {
		return nil, mapGormNotFound(err, ErrSubscriptionNotFound)
	}

	body, info, fetchErr := fetchUpstreamSubscription(upstream.Url)
	now := time.Now().Unix()
	upstream.LastFetchedAt = now
	upstream.Upload = info.Upload
	upstream.Download = info.Download
	upstream.Total = info.Total
	upstream.ExpiryTime = info.ExpiryTime

	if fetchErr != nil {
		upstream.LastError = fetchErr.Error()
		_ = db.Save(&upstream).Error
		return &upstream, fetchErr
	}

	nodes, parseErr := parseUpstreamNodes(body)
	if parseErr != nil {
		upstream.LastError = parseErr.Error()
		_ = db.Save(&upstream).Error
		return &upstream, parseErr
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		upstream.LastError = ""
		if err := tx.Save(&upstream).Error; err != nil {
			return err
		}

		var existing []model.UpstreamNode
		if err := tx.Where("upstream_id = ?", id).Find(&existing).Error; err != nil {
			return err
		}
		byHash := make(map[string]model.UpstreamNode, len(existing))
		for _, node := range existing {
			byHash[node.Hash] = node
		}

		seenHashes := make([]string, 0, len(nodes))
		for index, parsed := range nodes {
			parsed.Sort = index
			hash := upstreamNodeHash(id, parsed)
			seenHashes = append(seenHashes, hash)
			if current, ok := byHash[hash]; ok {
				current.Name = parsed.Name
				current.Protocol = parsed.Protocol
				current.Link = parsed.Link
				current.Clash = parsed.Clash
				current.SourceType = parsed.SourceType
				current.Sort = parsed.Sort
				if err := tx.Save(&current).Error; err != nil {
					return err
				}
				continue
			}
			node := model.UpstreamNode{
				UpstreamId: id,
				Name:       parsed.Name,
				Protocol:   parsed.Protocol,
				Link:       parsed.Link,
				Clash:      parsed.Clash,
				SourceType: parsed.SourceType,
				Hash:       hash,
				Enable:     true,
				Sort:       parsed.Sort,
			}
			if err := tx.Create(&node).Error; err != nil {
				return err
			}
		}

		staleQuery := tx.Model(model.UpstreamNode{}).Where("upstream_id = ?", id)
		if len(seenHashes) > 0 {
			staleQuery = staleQuery.Where("hash NOT IN ?", seenHashes)
		}
		var staleIDs []int
		if err := staleQuery.Pluck("id", &staleIDs).Error; err != nil {
			return err
		}
		if len(staleIDs) > 0 {
			if err := tx.Where("node_id IN ?", staleIDs).Delete(&model.CustomerSubscriptionNode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("node_id IN ?", staleIDs).Delete(&model.InboundSubscriptionNode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", staleIDs).Delete(&model.UpstreamNode{}).Error; err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return &upstream, err
	}

	return s.getUpstream(id)
}

func (s *SubscriptionMarketService) GetNodes(enabledOnly bool) ([]UpstreamNodeView, error) {
	db := database.GetDB().
		Table("upstream_nodes").
		Select("upstream_nodes.id, upstream_nodes.upstream_id, upstream_subscriptions.name AS upstream_name, upstream_nodes.name, upstream_nodes.protocol, upstream_nodes.link, upstream_nodes.source_type, upstream_nodes.enable, upstream_nodes.sort, upstream_nodes.created_at, upstream_nodes.updated_at").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
		Order("upstream_subscriptions.id desc, upstream_nodes.sort asc, upstream_nodes.id asc")
	if enabledOnly {
		db = db.Where("upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true)
	}
	var nodes []UpstreamNodeView
	return nodes, db.Scan(&nodes).Error
}

func (s *SubscriptionMarketService) GetCustomers() ([]CustomerSubscriptionView, error) {
	db := database.GetDB()
	var customers []model.CustomerSubscription
	if err := db.Order("id desc").Find(&customers).Error; err != nil {
		return nil, err
	}
	result := make([]CustomerSubscriptionView, 0, len(customers))
	for _, customer := range customers {
		nodeIDs, err := s.customerNodeIDs(customer.Id)
		if err != nil {
			return nil, err
		}
		result = append(result, customerView(customer, nodeIDs))
	}
	return result, nil
}

func (s *SubscriptionMarketService) CreateCustomer(name string, enable bool, expiryTime int64, nodeIDs []int) (*CustomerSubscriptionView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrSubscriptionNameRequired
	}
	customer := model.CustomerSubscription{
		Name:       name,
		Token:      s.newCustomerToken(),
		Enable:     enable,
		ExpiryTime: expiryTime,
	}
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&customer).Error; err != nil {
			return err
		}
		return s.replaceCustomerNodes(tx, customer.Id, nodeIDs)
	})
	if err != nil {
		return nil, err
	}
	ids, err := s.customerNodeIDs(customer.Id)
	if err != nil {
		return nil, err
	}
	view := customerView(customer, ids)
	return &view, nil
}

func (s *SubscriptionMarketService) UpdateCustomer(id int, name string, enable bool, expiryTime int64, nodeIDs []int) (*CustomerSubscriptionView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrSubscriptionNameRequired
	}
	db := database.GetDB()
	var customer model.CustomerSubscription
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&customer, id).Error; err != nil {
			return mapGormNotFound(err, ErrCustomerNotFound)
		}
		customer.Name = name
		customer.Enable = enable
		customer.ExpiryTime = expiryTime
		if err := tx.Save(&customer).Error; err != nil {
			return err
		}
		return s.replaceCustomerNodes(tx, id, nodeIDs)
	})
	if err != nil {
		return nil, err
	}
	ids, err := s.customerNodeIDs(customer.Id)
	if err != nil {
		return nil, err
	}
	view := customerView(customer, ids)
	return &view, nil
}

func (s *SubscriptionMarketService) SetCustomerEnable(id int, enable bool) error {
	result := database.GetDB().Model(&model.CustomerSubscription{}).Where("id = ?", id).Update("enable", enable)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrCustomerNotFound
	}
	return nil
}

func (s *SubscriptionMarketService) DeleteCustomer(id int) error {
	db := database.GetDB()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("customer_id = ?", id).Delete(&model.CustomerSubscriptionNode{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&model.CustomerSubscription{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrCustomerNotFound
		}
		return nil
	})
}

func (s *SubscriptionMarketService) GetInboundNodeIDs(inboundID int) ([]int, error) {
	var count int64
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", inboundID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrInboundNotFound
	}
	var ids []int
	err := database.GetDB().
		Model(model.InboundSubscriptionNode{}).
		Where("inbound_id = ?", inboundID).
		Order("node_id asc").
		Pluck("node_id", &ids).Error
	return ids, err
}

func (s *SubscriptionMarketService) SetInboundNodes(inboundID int, nodeIDs []int) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.Inbound{}).Where("id = ?", inboundID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrInboundNotFound
		}
		return s.replaceInboundNodes(tx, inboundID, nodeIDs)
	})
}

func (s *SubscriptionMarketService) GetInboundSubscriptionContent(subID string) (*InboundSubscriptionContent, error) {
	return &InboundSubscriptionContent{}, nil
}

func (s *SubscriptionMarketService) BuildClashSubscription(content *CustomerSubscriptionContent) (string, error) {
	if content == nil || len(content.ClashProxy) == 0 {
		return "", ErrCustomerNoEnabledNodes
	}
	names := make([]string, 0, len(content.ClashProxy))
	seen := make(map[string]int)
	proxies := make([]map[string]any, 0, len(content.ClashProxy))
	for _, proxy := range content.ClashProxy {
		name, _ := proxy["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			name = fmt.Sprintf("Node %d", len(names)+1)
		}
		if count := seen[name]; count > 0 {
			seen[name] = count + 1
			name = fmt.Sprintf("%s %d", name, count+1)
		} else {
			seen[name] = 1
		}
		proxy["name"] = name
		proxies = append(proxies, proxy)
		names = append(names, name)
	}
	doc := map[string]any{
		"port":       7890,
		"socks-port": 7891,
		"allow-lan":  false,
		"mode":       "rule",
		"log-level":  "info",
		"proxies":    proxies,
		"proxy-groups": []map[string]any{
			{
				"name":    "Proxy",
				"type":    "select",
				"proxies": names,
			},
		},
		"rules": []string{"MATCH,Proxy"},
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (s *SubscriptionMarketService) getUpstream(id int) (*model.UpstreamSubscription, error) {
	var upstream model.UpstreamSubscription
	err := database.GetDB().
		Preload("Nodes", func(tx *gorm.DB) *gorm.DB {
			return tx.Order("sort asc, id asc")
		}).
		First(&upstream, id).Error
	if err != nil {
		return nil, mapGormNotFound(err, ErrSubscriptionNotFound)
	}
	return &upstream, nil
}

func (s *SubscriptionMarketService) replaceCustomerNodes(tx *gorm.DB, customerID int, nodeIDs []int) error {
	if err := tx.Where("customer_id = ?", customerID).Delete(&model.CustomerSubscriptionNode{}).Error; err != nil {
		return err
	}
	nodeIDs = uniquePositiveInts(nodeIDs)
	if len(nodeIDs) == 0 {
		return nil
	}
	var allowedIDs []int
	if err := tx.Table("upstream_nodes").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
		Where("upstream_nodes.id IN ?", nodeIDs).
		Where("upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true).
		Pluck("upstream_nodes.id", &allowedIDs).Error; err != nil {
		return err
	}
	sort.Ints(allowedIDs)
	for _, nodeID := range allowedIDs {
		grant := model.CustomerSubscriptionNode{CustomerId: customerID, NodeId: nodeID}
		if err := tx.Create(&grant).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *SubscriptionMarketService) replaceInboundNodes(tx *gorm.DB, inboundID int, nodeIDs []int) error {
	if err := tx.Where("inbound_id = ?", inboundID).Delete(&model.InboundSubscriptionNode{}).Error; err != nil {
		return err
	}
	nodeIDs = uniquePositiveInts(nodeIDs)
	if len(nodeIDs) == 0 {
		return nil
	}
	var allowedIDs []int
	if err := tx.Table("upstream_nodes").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
		Where("upstream_nodes.id IN ?", nodeIDs).
		Where("upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true).
		Pluck("upstream_nodes.id", &allowedIDs).Error; err != nil {
		return err
	}
	sort.Ints(allowedIDs)
	for _, nodeID := range allowedIDs {
		grant := model.InboundSubscriptionNode{InboundId: inboundID, NodeId: nodeID}
		if err := tx.Create(&grant).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *SubscriptionMarketService) customerNodeIDs(customerID int) ([]int, error) {
	var ids []int
	err := database.GetDB().
		Model(model.CustomerSubscriptionNode{}).
		Where("customer_id = ?", customerID).
		Order("node_id asc").
		Pluck("node_id", &ids).Error
	return ids, err
}

func (s *SubscriptionMarketService) newCustomerToken() string {
	for {
		token := random.Seq(24)
		var count int64
		database.GetDB().Model(model.CustomerSubscription{}).Where("token = ?", token).Count(&count)
		if count == 0 {
			return token
		}
	}
}

func customerView(customer model.CustomerSubscription, nodeIDs []int) CustomerSubscriptionView {
	return CustomerSubscriptionView{
		Id:         customer.Id,
		Name:       customer.Name,
		Token:      customer.Token,
		Enable:     customer.Enable,
		ExpiryTime: customer.ExpiryTime,
		CreatedAt:  customer.CreatedAt,
		UpdatedAt:  customer.UpdatedAt,
		NodeIds:    nodeIDs,
		NodeCount:  len(nodeIDs),
	}
}

func sanitizeHTTPURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrSubscriptionURLRequired
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrSubscriptionInvalidURL
	}
	clean := &url.URL{
		Scheme:   u.Scheme,
		Host:     u.Host,
		Path:     u.Path,
		RawPath:  u.RawPath,
		RawQuery: u.RawQuery,
		Fragment: u.Fragment,
	}
	return clean.String(), nil
}

func fetchUpstreamSubscription(rawURL string) (string, upstreamTrafficInfo, error) {
	client := &http.Client{Timeout: upstreamFetchTimeout}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", upstreamTrafficInfo{}, err
	}
	req.Header.Set("User-Agent", upstreamFetchUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", upstreamTrafficInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", parseSubscriptionUserInfo(resp.Header.Get("Subscription-Userinfo")), fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", parseSubscriptionUserInfo(resp.Header.Get("Subscription-Userinfo")), err
	}
	return string(body), parseSubscriptionUserInfo(resp.Header.Get("Subscription-Userinfo")), nil
}

func parseSubscriptionUserInfo(header string) upstreamTrafficInfo {
	var info upstreamTrafficInfo
	for _, part := range strings.Split(header, ";") {
		piece := strings.TrimSpace(part)
		if piece == "" {
			continue
		}
		key, value, ok := strings.Cut(piece, "=")
		if !ok {
			continue
		}
		num, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "upload":
			info.Upload = num
		case "download":
			info.Download = num
		case "total":
			info.Total = num
		case "expire":
			if num > 0 {
				info.ExpiryTime = num * 1000
			}
		}
	}
	return info
}

func parseUpstreamNodes(body string) ([]parsedUpstreamNode, error) {
	candidates := []string{body}
	if decoded, ok := decodeBase64Subscription(body); ok {
		candidates = append([]string{decoded}, candidates...)
	}

	for _, candidate := range candidates {
		nodes := parseURINodes(candidate)
		if len(nodes) > 0 {
			return nodes, nil
		}
	}

	nodes := parseClashNodes(body)
	if len(nodes) > 0 {
		return nodes, nil
	}
	if decoded, ok := decodeBase64Subscription(body); ok {
		nodes = parseClashNodes(decoded)
		if len(nodes) > 0 {
			return nodes, nil
		}
	}
	return nil, ErrSubscriptionNoNodes
}

func parseURINodes(content string) []parsedUpstreamNode {
	lines := strings.FieldsFunc(content, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	nodes := make([]parsedUpstreamNode, 0, len(lines))
	for _, line := range lines {
		link := strings.TrimSpace(line)
		if link == "" || strings.HasPrefix(link, "#") {
			continue
		}
		protocol := uriProtocol(link)
		if protocol == "" {
			continue
		}
		nodes = append(nodes, parsedUpstreamNode{
			Name:       subscriptionURIName(link),
			Protocol:   protocol,
			Link:       link,
			SourceType: "uri",
		})
	}
	return nodes
}

func parseClashNodes(content string) []parsedUpstreamNode {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil
	}
	rawProxies, ok := doc["proxies"].([]any)
	if !ok || len(rawProxies) == 0 {
		return nil
	}
	nodes := make([]parsedUpstreamNode, 0, len(rawProxies))
	for _, raw := range rawProxies {
		proxy, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := proxy["name"].(string)
		protocol, _ := proxy["type"].(string)
		name = strings.TrimSpace(name)
		protocol = strings.TrimSpace(protocol)
		if name == "" || protocol == "" {
			continue
		}
		encoded, err := json.Marshal(proxy)
		if err != nil {
			continue
		}
		nodes = append(nodes, parsedUpstreamNode{
			Name:       name,
			Protocol:   protocol,
			Clash:      string(encoded),
			SourceType: "clash",
		})
	}
	return nodes
}

func decodeBase64Subscription(content string) (string, bool) {
	compact := strings.Join(strings.Fields(content), "")
	if compact == "" {
		return "", false
	}
	decoders := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, decoder := range decoders {
		data, err := decoder.DecodeString(compact)
		if err == nil && looksLikeSubscriptionText(string(data)) {
			return string(data), true
		}
	}
	if pad := len(compact) % 4; pad != 0 {
		padded := compact + strings.Repeat("=", 4-pad)
		for _, decoder := range decoders {
			data, err := decoder.DecodeString(padded)
			if err == nil && looksLikeSubscriptionText(string(data)) {
				return string(data), true
			}
		}
	}
	return "", false
}

func looksLikeSubscriptionText(text string) bool {
	return strings.Contains(text, "://") || strings.Contains(text, "proxies:")
}

func uriProtocol(link string) string {
	scheme, _, ok := strings.Cut(link, "://")
	if !ok {
		return ""
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	switch scheme {
	case "vmess", "vless", "trojan", "ss", "ssr", "hysteria", "hysteria2", "hy2", "tuic", "wireguard":
		return scheme
	default:
		return ""
	}
}

func subscriptionURIName(link string) string {
	if strings.HasPrefix(strings.ToLower(link), "vmess://") {
		payload := strings.TrimSpace(link[len("vmess://"):])
		if decoded, ok := decodeBase64Any(payload); ok {
			var data map[string]any
			if err := json.Unmarshal([]byte(decoded), &data); err == nil {
				if name, _ := data["ps"].(string); strings.TrimSpace(name) != "" {
					return strings.TrimSpace(name)
				}
			}
		}
	}
	u, err := url.Parse(link)
	if err == nil {
		if u.Fragment != "" {
			if decoded, err := url.QueryUnescape(u.Fragment); err == nil && strings.TrimSpace(decoded) != "" {
				return strings.TrimSpace(decoded)
			}
			return strings.TrimSpace(u.Fragment)
		}
		if u.Host != "" {
			return u.Host
		}
	}
	protocol := uriProtocol(link)
	if protocol == "" {
		protocol = "node"
	}
	return strings.ToUpper(protocol) + " node"
}

func decodeBase64Any(content string) (string, bool) {
	compact := strings.TrimSpace(content)
	decoders := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, decoder := range decoders {
		data, err := decoder.DecodeString(compact)
		if err == nil {
			return string(data), true
		}
	}
	if pad := len(compact) % 4; pad != 0 {
		padded := compact + strings.Repeat("=", 4-pad)
		for _, decoder := range decoders {
			data, err := decoder.DecodeString(padded)
			if err == nil {
				return string(data), true
			}
		}
	}
	return "", false
}

func upstreamNodeHash(upstreamID int, node parsedUpstreamNode) string {
	payload := fmt.Sprintf("%d\n%s\n%s\n%s", upstreamID, node.SourceType, node.Link, node.Clash)
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:])
}

func uniquePositiveInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func mapGormNotFound(err error, replacement error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return replacement
	}
	return err
}
