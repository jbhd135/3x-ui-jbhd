package service

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/util/json_util"
	"github.com/mhsanaei/3x-ui/v2/util/random"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"gorm.io/gorm"
)

const upstreamFetchTimeout = 20 * time.Second
const subscriptionInfoNodeUUID = "00000000-0000-0000-0000-000000000000"
const relayPortStart = 20000
const relayPortEnd = 29999
const relayTagPrefix = "xui-relay-node-"
const inboundRelayFastOutboundTag = "xui-relay-direct"
const directHysteria2NameMarker = "\u76f4\u8fde"

var upstreamFetchUserAgents = []string{
	"v2rayN/7.0",
	"V2Ray/5.0",
	"v2rayNG/1.9.0",
	"Shadowrocket/2.2.43",
	"3x-ui-subscription-market/1.0",
}

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
	ErrNodeNotFound              = errors.New("node not found")
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
	Tags         string `json:"tags"`
	Enable       bool   `json:"enable"`
	Emergency    bool   `json:"emergency"`
	RelayPort    int    `json:"relayPort"`
	RelayUp      int64  `json:"relayUp"`
	RelayDown    int64  `json:"relayDown"`
	Sort         int    `json:"sort"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

type UpstreamNodeConfigView struct {
	Id           int    `json:"id"`
	UpstreamId   int    `json:"upstreamId"`
	UpstreamName string `json:"upstreamName"`
	Name         string `json:"name"`
	Enable       bool   `json:"enable"`
	NodeIds      []int  `json:"nodeIds" gorm:"-"`
	NodeCount    int    `json:"nodeCount" gorm:"-"`
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

type InboundUpstreamTreeView struct {
	InboundId int                        `json:"inboundId"`
	Upstreams []InboundUpstreamGroupView `json:"upstreams"`
}

type InboundUpstreamGroupView struct {
	UpstreamId   int                         `json:"upstreamId"`
	UpstreamName string                      `json:"upstreamName"`
	Enable       bool                        `json:"enable"`
	NodeCount    int                         `json:"nodeCount"`
	Up           int64                       `json:"up"`
	Down         int64                       `json:"down"`
	AllTime      int64                       `json:"allTime"`
	Configs      []InboundUpstreamConfigTree `json:"configs"`
}

type InboundUpstreamConfigTree struct {
	ConfigId  int                           `json:"configId"`
	Name      string                        `json:"name"`
	NodeCount int                           `json:"nodeCount"`
	Up        int64                         `json:"up"`
	Down      int64                         `json:"down"`
	AllTime   int64                         `json:"allTime"`
	Nodes     []InboundUpstreamNodeTreeView `json:"nodes"`
}

type InboundUpstreamNodeTreeView struct {
	NodeId    int    `json:"nodeId"`
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Enable    bool   `json:"enable"`
	RelayPort int    `json:"relayPort"`
	RelayLink string `json:"relayLink"`
	Up        int64  `json:"up"`
	Down      int64  `json:"down"`
	AllTime   int64  `json:"allTime"`
}

type upstreamNodeContentRow struct {
	ID             int
	InboundID      int
	UpstreamSortID int
	Sort           int
	Name           string
	Protocol       string
	Link           string
	Clash          string
	RelayPort      int
	RelayUUID      string
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

func BuildSubscriptionExpiryInfoNode(expiryTime int64, address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		address = "127.0.0.1"
	}
	remark := subscriptionExpiryInfoRemark(expiryTime)
	obj := map[string]any{
		"v":    "2",
		"ps":   remark,
		"add":  address,
		"port": 80,
		"id":   subscriptionInfoNodeUUID,
		"aid":  "0",
		"scy":  "auto",
		"net":  "tcp",
		"type": "none",
		"host": "",
		"path": "",
		"tls":  "",
	}
	encoded, _ := json.Marshal(obj)
	return "vmess://" + base64.StdEncoding.EncodeToString(encoded)
}

func subscriptionExpiryInfoRemark(expiryTime int64) string {
	if expiryTime <= 0 {
		return "过期时间：永久有效"
	}
	expireAt := time.UnixMilli(expiryTime).In(time.FixedZone("CST", 8*3600))
	return fmt.Sprintf("过期时间：%s", expireAt.Format("2006-01-02"))
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
	var affectedInboundIDs []int
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		affectedInboundIDs, err = inboundIDsByUpstreamIDs(tx, []int{id})
		return err
	}); err != nil {
		return nil, err
	}
	upstream.Name = name
	upstream.Url = rawURL
	upstream.Enable = enable
	if err := db.Save(&upstream).Error; err != nil {
		return nil, err
	}
	if err := s.reloadInboundRelayRuntime(affectedInboundIDs); err != nil {
		return nil, err
	}
	return &upstream, nil
}

func (s *SubscriptionMarketService) DeleteUpstream(id int) error {
	db := database.GetDB()
	var affectedInboundIDs []int
	err := db.Transaction(func(tx *gorm.DB) error {
		var nodeIDs []int
		if err := tx.Model(model.UpstreamNode{}).Where("upstream_id = ?", id).Pluck("id", &nodeIDs).Error; err != nil {
			return err
		}
		var configIDs []int
		if err := tx.Model(model.UpstreamNodeConfig{}).Where("upstream_id = ?", id).Pluck("id", &configIDs).Error; err != nil {
			return err
		}
		var err error
		affectedInboundIDs, err = inboundIDsByUpstreamConfigIDs(tx, configIDs)
		if err != nil {
			return err
		}
		if len(nodeIDs) > 0 {
			if err := tx.Where("node_id IN ?", nodeIDs).Delete(&model.InboundSubscriptionNode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("node_id IN ?", nodeIDs).Delete(&model.UpstreamNodeConfigNode{}).Error; err != nil {
				return err
			}
		}
		if len(configIDs) > 0 {
			if err := tx.Where("config_id IN ?", configIDs).Delete(&model.InboundUpstreamConfig{}).Error; err != nil {
				return err
			}
			if err := tx.Where("config_id IN ?", configIDs).Delete(&model.UpstreamNodeConfigNode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", configIDs).Delete(&model.UpstreamNodeConfig{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("upstream_id = ?", id).Delete(&model.InboundEmergencyUpstream{}).Error; err != nil {
			return err
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
	if err != nil {
		return err
	}
	return s.reloadInboundRelayRuntime(affectedInboundIDs)
}

func (s *SubscriptionMarketService) SetUpstreamEnable(id int, enable bool) error {
	db := database.GetDB()
	var affectedInboundIDs []int
	err := db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.UpstreamSubscription{}).Where("id = ?", id).Update("enable", enable)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrSubscriptionNotFound
		}
		var err error
		affectedInboundIDs, err = inboundIDsByUpstreamIDs(tx, []int{id})
		return err
	})
	if err != nil {
		return err
	}
	return s.reloadInboundRelayRuntime(affectedInboundIDs)
}

func (s *SubscriptionMarketService) SetNodeEnable(id int, enable bool) error {
	db := database.GetDB()
	var affectedInboundIDs []int
	err := db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.UpstreamNode{}).Where("id = ?", id).Update("enable", enable)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrSubscriptionNotFound
		}
		var err error
		affectedInboundIDs, err = inboundIDsByUpstreamNodeIDs(tx, []int{id})
		return err
	})
	if err != nil {
		return err
	}
	return s.reloadInboundRelayRuntime(affectedInboundIDs)
}

func (s *SubscriptionMarketService) SetNodesEnable(nodeIDs []int, enable bool) error {
	nodeIDs = uniquePositiveInts(nodeIDs)
	if len(nodeIDs) == 0 {
		return nil
	}
	db := database.GetDB()
	var affectedInboundIDs []int
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.UpstreamNode{}).
			Where("id IN ?", nodeIDs).
			Update("enable", enable).Error; err != nil {
			return err
		}
		var err error
		affectedInboundIDs, err = inboundIDsByUpstreamNodeIDs(tx, nodeIDs)
		return err
	})
	if err != nil {
		return err
	}
	return s.reloadInboundRelayRuntime(affectedInboundIDs)
}

func (s *SubscriptionMarketService) UpdateNodesTag(nodeIDs []int, tag string, add bool) error {
	nodeIDs = uniquePositiveInts(nodeIDs)
	tag = normalizeNodeTag(tag)
	if len(nodeIDs) == 0 || tag == "" {
		return nil
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var nodes []model.UpstreamNode
		if err := tx.Where("id IN ?", nodeIDs).Find(&nodes).Error; err != nil {
			return err
		}
		for _, node := range nodes {
			tags := decodeNodeTags(node.Tags)
			if add {
				tags = append(tags, tag)
			} else {
				filtered := tags[:0]
				for _, item := range tags {
					if !strings.EqualFold(item, tag) {
						filtered = append(filtered, item)
					}
				}
				tags = filtered
			}
			if err := tx.Model(&model.UpstreamNode{}).
				Where("id = ?", node.Id).
				Update("tags", encodeNodeTags(tags)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *SubscriptionMarketService) GetUpstreamEmergencyNodeIDs(upstreamID int) ([]int, error) {
	var count int64
	if err := database.GetDB().Model(&model.UpstreamSubscription{}).Where("id = ?", upstreamID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrSubscriptionNotFound
	}
	var ids []int
	err := database.GetDB().
		Model(model.UpstreamNode{}).
		Where("upstream_id = ? AND emergency = ?", upstreamID, true).
		Order("sort asc, id asc").
		Pluck("id", &ids).Error
	return ids, err
}

func (s *SubscriptionMarketService) SetUpstreamEmergencyNodes(upstreamID int, nodeIDs []int) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.UpstreamSubscription{}).Where("id = ?", upstreamID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrSubscriptionNotFound
		}

		if err := tx.Model(&model.UpstreamNode{}).Where("upstream_id = ?", upstreamID).Update("emergency", false).Error; err != nil {
			return err
		}
		nodeIDs = uniquePositiveInts(nodeIDs)
		if len(nodeIDs) == 0 {
			return nil
		}
		return tx.Model(&model.UpstreamNode{}).
			Where("upstream_id = ? AND id IN ?", upstreamID, nodeIDs).
			Update("emergency", true).Error
	})
}

func (s *SubscriptionMarketService) GetAllUpstreamNodeConfigs() ([]UpstreamNodeConfigView, error) {
	db := database.GetDB()
	if err := db.Transaction(func(tx *gorm.DB) error {
		return s.ensureLegacyNodeConfigs(tx)
	}); err != nil {
		return nil, err
	}
	return s.getUpstreamNodeConfigs(0)
}

func (s *SubscriptionMarketService) GetUpstreamNodeConfigs(upstreamID int) ([]UpstreamNodeConfigView, error) {
	var count int64
	if err := database.GetDB().Model(&model.UpstreamSubscription{}).Where("id = ?", upstreamID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrSubscriptionNotFound
	}
	if err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		return s.ensureLegacyNodeConfigs(tx)
	}); err != nil {
		return nil, err
	}
	return s.getUpstreamNodeConfigs(upstreamID)
}

func (s *SubscriptionMarketService) CreateUpstreamNodeConfig(upstreamID int, name string, nodeIDs []int) (*UpstreamNodeConfigView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrSubscriptionNameRequired
	}
	var config model.UpstreamNodeConfig
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.UpstreamSubscription{}).Where("id = ?", upstreamID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrSubscriptionNotFound
		}
		config = model.UpstreamNodeConfig{
			UpstreamId: upstreamID,
			Name:       name,
		}
		if err := tx.Create(&config).Error; err != nil {
			return err
		}
		return s.replaceUpstreamNodeConfigNodes(tx, upstreamID, config.Id, nodeIDs)
	})
	if err != nil {
		return nil, err
	}
	return s.getUpstreamNodeConfig(config.Id)
}

func (s *SubscriptionMarketService) UpdateUpstreamNodeConfig(upstreamID int, configID int, name string, nodeIDs []int) (*UpstreamNodeConfigView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrSubscriptionNameRequired
	}
	var affectedInboundIDs []int
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		var config model.UpstreamNodeConfig
		if err := tx.Where("id = ? AND upstream_id = ?", configID, upstreamID).First(&config).Error; err != nil {
			return mapGormNotFound(err, ErrSubscriptionNotFound)
		}
		var err error
		affectedInboundIDs, err = inboundIDsByUpstreamConfigIDs(tx, []int{configID})
		if err != nil {
			return err
		}
		config.Name = name
		if err := tx.Save(&config).Error; err != nil {
			return err
		}
		return s.replaceUpstreamNodeConfigNodes(tx, upstreamID, configID, nodeIDs)
	})
	if err != nil {
		return nil, err
	}
	if err := s.reloadInboundRelayRuntime(affectedInboundIDs); err != nil {
		return nil, err
	}
	return s.getUpstreamNodeConfig(configID)
}

func (s *SubscriptionMarketService) DeleteUpstreamNodeConfig(upstreamID int, configID int) error {
	var affectedInboundIDs []int
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND upstream_id = ?", configID, upstreamID).Delete(&model.UpstreamNodeConfig{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrSubscriptionNotFound
		}
		var err error
		affectedInboundIDs, err = inboundIDsByUpstreamConfigIDs(tx, []int{configID})
		if err != nil {
			return err
		}
		if err := tx.Where("config_id = ?", configID).Delete(&model.UpstreamNodeConfigNode{}).Error; err != nil {
			return err
		}
		return tx.Where("config_id = ?", configID).Delete(&model.InboundUpstreamConfig{}).Error
	})
	if err != nil {
		return err
	}
	return s.reloadInboundRelayRuntime(affectedInboundIDs)
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

	var affectedInboundIDs []int
	err := db.Transaction(func(tx *gorm.DB) error {
		upstream.LastError = ""
		if err := tx.Save(&upstream).Error; err != nil {
			return err
		}
		var err error
		affectedInboundIDs, err = inboundIDsByUpstreamIDs(tx, []int{id})
		if err != nil {
			return err
		}

		var existing []model.UpstreamNode
		if err := tx.Where("upstream_id = ?", id).Find(&existing).Error; err != nil {
			return err
		}
		autoExtendConfigIDs, err := upstreamConfigIDsCoveringAllNodes(tx, id, upstreamNodeIDs(existing))
		if err != nil {
			return err
		}
		usedRelayPorts, err := collectUsedRelayPorts(tx)
		if err != nil {
			return err
		}
		byHash := make(map[string]model.UpstreamNode, len(existing))
		byIdentity := make(map[string]model.UpstreamNode, len(existing))
		for _, node := range existing {
			byHash[node.Hash] = node
			if key := upstreamNodeIdentityKey(id, node.SourceType, node.Protocol, node.Name); key != "" {
				byIdentity[key] = node
			}
		}

		seenHashes := make([]string, 0, len(nodes))
		replacedNodeIDs := make(map[int]int)
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
				if current.RelayPort <= 0 {
					port, err := nextRelayPort(usedRelayPorts)
					if err != nil {
						return err
					}
					current.RelayPort = port
				}
				if err := tx.Save(&current).Error; err != nil {
					return err
				}
				continue
			}
			var inherited *model.UpstreamNode
			if current, ok := byIdentity[upstreamNodeIdentityKey(id, parsed.SourceType, parsed.Protocol, parsed.Name)]; ok {
				inherited = &current
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
				RelayPort:  0,
				Sort:       parsed.Sort,
			}
			if inherited != nil {
				node.Tags = inherited.Tags
				node.Enable = inherited.Enable
				node.Emergency = inherited.Emergency
				node.RelayPort = inherited.RelayPort
			}
			if node.RelayPort <= 0 {
				port, err := nextRelayPort(usedRelayPorts)
				if err != nil {
					return err
				}
				node.RelayPort = port
			}
			if err := tx.Create(&node).Error; err != nil {
				return err
			}
			if inherited != nil {
				replacedNodeIDs[inherited.Id] = node.Id
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
			if err := transferNodeGrants(tx, replacedNodeIDs); err != nil {
				return err
			}
			if err := tx.Where("node_id IN ?", staleIDs).Delete(&model.InboundSubscriptionNode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("node_id IN ?", staleIDs).Delete(&model.UpstreamNodeConfigNode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", staleIDs).Delete(&model.UpstreamNode{}).Error; err != nil {
				return err
			}
		}
		if len(autoExtendConfigIDs) > 0 {
			var currentNodeIDs []int
			if err := tx.Model(model.UpstreamNode{}).
				Where("upstream_id = ?", id).
				Order("sort asc, id asc").
				Pluck("id", &currentNodeIDs).Error; err != nil {
				return err
			}
			for _, configID := range autoExtendConfigIDs {
				if err := s.replaceUpstreamNodeConfigNodes(tx, id, configID, currentNodeIDs); err != nil {
					return err
				}
			}
		}

		return nil
	})
	if err != nil {
		return &upstream, err
	}

	if err := s.reloadInboundRelayRuntime(affectedInboundIDs); err != nil {
		return &upstream, err
	}
	return s.getUpstream(id)
}

func (s *SubscriptionMarketService) GetNodes(enabledOnly bool) ([]UpstreamNodeView, error) {
	db := database.GetDB().
		Table("upstream_nodes").
		Select("upstream_nodes.id, upstream_nodes.upstream_id, upstream_subscriptions.name AS upstream_name, upstream_nodes.name, upstream_nodes.protocol, upstream_nodes.link, upstream_nodes.source_type, upstream_nodes.tags, upstream_nodes.enable, upstream_nodes.emergency, upstream_nodes.relay_port, upstream_nodes.relay_up, upstream_nodes.relay_down, upstream_nodes.sort, upstream_nodes.created_at, upstream_nodes.updated_at").
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

func (s *SubscriptionMarketService) SetInboundEmergencyEnable(inboundID int, enable bool) error {
	result := database.GetDB().
		Model(&model.Inbound{}).
		Where("id = ?", inboundID).
		Update("emergency_enable", enable)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrInboundNotFound
	}
	return s.reloadInboundRelayRuntime([]int{inboundID})
}

func (s *SubscriptionMarketService) GetInboundEmergencyUpstreamIDs(inboundID int) ([]int, error) {
	var count int64
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", inboundID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrInboundNotFound
	}
	var ids []int
	err := database.GetDB().
		Model(model.InboundEmergencyUpstream{}).
		Where("inbound_id = ?", inboundID).
		Order("upstream_id asc").
		Pluck("upstream_id", &ids).Error
	return ids, err
}

func (s *SubscriptionMarketService) SetInboundEmergencyUpstreams(inboundID int, upstreamIDs []int) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.Inbound{}).Where("id = ?", inboundID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrInboundNotFound
		}
		return s.replaceInboundEmergencyUpstreams(tx, inboundID, upstreamIDs)
	})
}

func (s *SubscriptionMarketService) GetInboundUpstreamConfigIDs(inboundID int) ([]int, error) {
	var count int64
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", inboundID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrInboundNotFound
	}
	if err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		return s.ensureLegacyNodeConfigs(tx)
	}); err != nil {
		return nil, err
	}
	var ids []int
	err := database.GetDB().
		Model(model.InboundUpstreamConfig{}).
		Where("inbound_id = ?", inboundID).
		Order("config_id asc").
		Pluck("config_id", &ids).Error
	return ids, err
}

func (s *SubscriptionMarketService) SetInboundUpstreamConfigs(inboundID int, configIDs []int) error {
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.Inbound{}).Where("id = ?", inboundID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrInboundNotFound
		}
		return s.replaceInboundUpstreamConfigs(tx, inboundID, configIDs)
	})
	if err != nil {
		return err
	}
	return s.reloadInboundRelayRuntime([]int{inboundID})
}

func (s *SubscriptionMarketService) GetInboundUpstreamTree(inboundID int, publicHost ...string) (*InboundUpstreamTreeView, error) {
	var count int64
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", inboundID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrInboundNotFound
	}
	if err := s.EnsureInboundRelayPorts([]int{inboundID}); err != nil {
		return nil, err
	}

	type treeRow struct {
		UpstreamId     int
		UpstreamName   string
		UpstreamEnable bool
		ConfigId       int
		ConfigName     string
		NodeId         int
		NodeName       string
		Protocol       string
		Link           string
		Clash          string
		NodeEnable     bool
		RelayPort      int
		RelayUUID      string
		Up             int64
		Down           int64
		AllTime        int64
	}
	var rows []treeRow
	err := database.GetDB().Table("inbound_upstream_configs").
		Select(`upstream_subscriptions.id AS upstream_id,
			upstream_subscriptions.name AS upstream_name,
			upstream_subscriptions.enable AS upstream_enable,
			upstream_node_configs.id AS config_id,
			upstream_node_configs.name AS config_name,
			upstream_nodes.id AS node_id,
			upstream_nodes.name AS node_name,
			upstream_nodes.protocol,
			upstream_nodes.link,
			upstream_nodes.clash,
			upstream_nodes.enable AS node_enable,
			inbound_upstream_relays.relay_port,
			inbound_upstream_relays.relay_uuid,
			COALESCE(inbound_upstream_relays.up, 0) AS up,
			COALESCE(inbound_upstream_relays.down, 0) AS down,
			COALESCE(inbound_upstream_relays.all_time, 0) AS all_time`).
		Joins("JOIN upstream_node_configs ON upstream_node_configs.id = inbound_upstream_configs.config_id").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_node_configs.upstream_id").
		Joins("JOIN upstream_node_config_nodes ON upstream_node_config_nodes.config_id = upstream_node_configs.id").
		Joins("JOIN upstream_nodes ON upstream_nodes.id = upstream_node_config_nodes.node_id").
		Joins("LEFT JOIN inbound_upstream_relays ON inbound_upstream_relays.inbound_id = inbound_upstream_configs.inbound_id AND inbound_upstream_relays.node_id = upstream_nodes.id").
		Where("inbound_upstream_configs.inbound_id = ?", inboundID).
		Order("upstream_subscriptions.id desc, upstream_node_configs.sort asc, upstream_node_configs.id asc, upstream_nodes.sort asc, upstream_nodes.id asc").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	view := &InboundUpstreamTreeView{InboundId: inboundID}
	relayHost := normalizeRelayPublicHost(firstString(publicHost...))
	groupIndex := make(map[int]int)
	configIndex := make(map[int]map[int]int)
	for _, row := range rows {
		groupPos, ok := groupIndex[row.UpstreamId]
		if !ok {
			groupPos = len(view.Upstreams)
			groupIndex[row.UpstreamId] = groupPos
			configIndex[row.UpstreamId] = make(map[int]int)
			view.Upstreams = append(view.Upstreams, InboundUpstreamGroupView{
				UpstreamId:   row.UpstreamId,
				UpstreamName: row.UpstreamName,
				Enable:       row.UpstreamEnable,
				Configs:      []InboundUpstreamConfigTree{},
			})
		}
		group := &view.Upstreams[groupPos]
		configPos, ok := configIndex[row.UpstreamId][row.ConfigId]
		if !ok {
			configPos = len(group.Configs)
			configIndex[row.UpstreamId][row.ConfigId] = configPos
			group.Configs = append(group.Configs, InboundUpstreamConfigTree{
				ConfigId: row.ConfigId,
				Name:     row.ConfigName,
				Nodes:    []InboundUpstreamNodeTreeView{},
			})
		}
		config := &group.Configs[configPos]
		relayLink := strings.TrimSpace(row.Link)
		if relayHost != "" && row.RelayPort > 0 {
			relayLink = ""
			if rewritten, ok := authenticatedRelayLink(row.Protocol, row.Link, row.NodeName, relayHost, row.RelayPort, row.RelayUUID); ok {
				relayLink = rewritten
			}
			if relayLink == "" && strings.TrimSpace(row.Clash) != "" {
				var proxy map[string]any
				if err := json.Unmarshal([]byte(row.Clash), &proxy); err == nil && len(proxy) > 0 {
					if rewriteAuthenticatedRelayClashProxy(proxy, row.Protocol, row.NodeName, relayHost, row.RelayPort, row.RelayUUID) {
						relayLink = clashProxyShareLink(proxy, row.Protocol, row.NodeName)
					}
				}
			}
		}
		config.Nodes = append(config.Nodes, InboundUpstreamNodeTreeView{
			NodeId:    row.NodeId,
			Name:      row.NodeName,
			Protocol:  row.Protocol,
			Enable:    row.UpstreamEnable && row.NodeEnable,
			RelayPort: row.RelayPort,
			RelayLink: relayLink,
			Up:        row.Up,
			Down:      row.Down,
			AllTime:   row.AllTime,
		})
		config.NodeCount++
		config.Up += row.Up
		config.Down += row.Down
		config.AllTime += row.AllTime
		group.NodeCount++
		group.Up += row.Up
		group.Down += row.Down
		group.AllTime += row.AllTime
	}
	return view, nil
}

func (s *SubscriptionMarketService) ResetInboundRelayNodeTraffic(inboundID, nodeID int) error {
	if inboundID <= 0 {
		return ErrInboundNotFound
	}
	if nodeID <= 0 {
		return ErrNodeNotFound
	}
	result := database.GetDB().Model(&model.InboundUpstreamRelay{}).
		Where("inbound_id = ? AND node_id = ?", inboundID, nodeID).
		Updates(map[string]any{
			"up":       int64(0),
			"down":     int64(0),
			"all_time": int64(0),
		})
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func (s *SubscriptionMarketService) GetInboundSubscriptionContent(subID string, publicHost ...string) (*InboundSubscriptionContent, error) {
	inboundIDs, err := s.inboundIDsBySubID(subID)
	if err != nil {
		return nil, err
	}
	if len(inboundIDs) == 0 {
		return &InboundSubscriptionContent{}, nil
	}
	if err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		return s.ensureLegacyNodeConfigs(tx)
	}); err != nil {
		return nil, err
	}
	if err := s.EnsureInboundRelayPorts(inboundIDs); err != nil {
		return nil, err
	}

	rows := make([]upstreamNodeContentRow, 0)

	var emergencyInboundIDs []int
	if err := database.GetDB().
		Model(model.Inbound{}).
		Where("id IN ? AND emergency_enable = ?", inboundIDs, true).
		Pluck("id", &emergencyInboundIDs).Error; err != nil {
		return nil, err
	}
	if len(emergencyInboundIDs) > 0 {
		var upstreamConfigIDs []int
		if err := database.GetDB().
			Model(model.InboundUpstreamConfig{}).
			Where("inbound_id IN ?", emergencyInboundIDs).
			Distinct().
			Pluck("config_id", &upstreamConfigIDs).Error; err != nil {
			return nil, err
		}
		upstreamConfigIDs = uniquePositiveInts(upstreamConfigIDs)
		if len(upstreamConfigIDs) == 0 {
			links, clashProxies := buildUpstreamNodeContent(rows, firstString(publicHost...))
			return &InboundSubscriptionContent{
				Links:      links,
				ClashProxy: clashProxies,
			}, nil
		}
		var configRows []upstreamNodeContentRow
		err = database.GetDB().Table("upstream_node_config_nodes").
			Select("DISTINCT inbound_upstream_configs.inbound_id, upstream_nodes.id, upstream_subscriptions.id AS upstream_sort_id, upstream_nodes.sort, upstream_nodes.name, upstream_nodes.protocol, upstream_nodes.link, upstream_nodes.clash, inbound_upstream_relays.relay_port, inbound_upstream_relays.relay_uuid").
			Joins("JOIN inbound_upstream_configs ON inbound_upstream_configs.config_id = upstream_node_config_nodes.config_id").
			Joins("JOIN upstream_node_configs ON upstream_node_configs.id = upstream_node_config_nodes.config_id").
			Joins("JOIN upstream_nodes ON upstream_nodes.id = upstream_node_config_nodes.node_id").
			Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
			Joins("JOIN inbound_upstream_relays ON inbound_upstream_relays.inbound_id = inbound_upstream_configs.inbound_id AND inbound_upstream_relays.node_id = upstream_nodes.id").
			Where("inbound_upstream_configs.inbound_id IN ?", emergencyInboundIDs).
			Where("upstream_node_config_nodes.config_id IN ?", upstreamConfigIDs).
			Where("upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true).
			Order("inbound_upstream_configs.inbound_id asc, upstream_subscriptions.id desc, upstream_nodes.sort asc, upstream_nodes.id asc").
			Scan(&configRows).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, configRows...)
	}

	links, clashProxies := buildUpstreamNodeContent(rows, firstString(publicHost...))
	return &InboundSubscriptionContent{
		Links:      links,
		ClashProxy: clashProxies,
	}, nil
}

func (s *SubscriptionMarketService) GetCustomerSubscription(token string) (*CustomerSubscriptionContent, error) {
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

	var rows []upstreamNodeContentRow
	err := db.Table("customer_subscription_nodes").
		Select("upstream_nodes.id, upstream_subscriptions.id AS upstream_sort_id, upstream_nodes.sort, upstream_nodes.name, upstream_nodes.protocol, upstream_nodes.link, upstream_nodes.clash").
		Joins("JOIN upstream_nodes ON upstream_nodes.id = customer_subscription_nodes.node_id").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
		Where("customer_subscription_nodes.customer_id = ?", customer.Id).
		Where("upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true).
		Order("upstream_subscriptions.id desc, upstream_nodes.sort asc, upstream_nodes.id asc").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	links, clashProxies := buildUpstreamNodeContent(rows, "")
	if len(links) == 0 && len(clashProxies) == 0 {
		return nil, ErrCustomerNoEnabledNodes
	}
	return &CustomerSubscriptionContent{
		Links:      links,
		ClashProxy: clashProxies,
		Customer:   customer,
	}, nil
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

func (s *SubscriptionMarketService) replaceInboundEmergencyUpstreams(tx *gorm.DB, inboundID int, upstreamIDs []int) error {
	if err := tx.Where("inbound_id = ?", inboundID).Delete(&model.InboundEmergencyUpstream{}).Error; err != nil {
		return err
	}
	upstreamIDs = uniquePositiveInts(upstreamIDs)
	if len(upstreamIDs) == 0 {
		return nil
	}
	var allowedIDs []int
	if err := tx.Model(&model.UpstreamSubscription{}).
		Where("id IN ?", upstreamIDs).
		Pluck("id", &allowedIDs).Error; err != nil {
		return err
	}
	sort.Ints(allowedIDs)
	for _, upstreamID := range allowedIDs {
		grant := model.InboundEmergencyUpstream{InboundId: inboundID, UpstreamId: upstreamID}
		if err := tx.Create(&grant).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *SubscriptionMarketService) replaceInboundUpstreamConfigs(tx *gorm.DB, inboundID int, configIDs []int) error {
	if err := tx.Where("inbound_id = ?", inboundID).Delete(&model.InboundUpstreamConfig{}).Error; err != nil {
		return err
	}
	configIDs = uniquePositiveInts(configIDs)
	if len(configIDs) == 0 {
		return nil
	}
	var allowedIDs []int
	if err := tx.Model(&model.UpstreamNodeConfig{}).
		Where("id IN ?", configIDs).
		Pluck("id", &allowedIDs).Error; err != nil {
		return err
	}
	sort.Ints(allowedIDs)
	for _, configID := range allowedIDs {
		grant := model.InboundUpstreamConfig{InboundId: inboundID, ConfigId: configID}
		if err := tx.Create(&grant).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *SubscriptionMarketService) replaceUpstreamNodeConfigNodes(tx *gorm.DB, upstreamID int, configID int, nodeIDs []int) error {
	if err := tx.Where("config_id = ?", configID).Delete(&model.UpstreamNodeConfigNode{}).Error; err != nil {
		return err
	}
	nodeIDs = uniquePositiveInts(nodeIDs)
	if len(nodeIDs) == 0 {
		return nil
	}
	var allowedIDs []int
	if err := tx.Model(&model.UpstreamNode{}).
		Where("upstream_id = ? AND id IN ?", upstreamID, nodeIDs).
		Pluck("id", &allowedIDs).Error; err != nil {
		return err
	}
	sort.Ints(allowedIDs)
	for _, nodeID := range allowedIDs {
		item := model.UpstreamNodeConfigNode{ConfigId: configID, NodeId: nodeID}
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
	}
	return nil
}

func upstreamNodeIDs(nodes []model.UpstreamNode) []int {
	ids := make([]int, 0, len(nodes))
	for _, node := range nodes {
		if node.Id > 0 {
			ids = append(ids, node.Id)
		}
	}
	return uniquePositiveInts(ids)
}

func upstreamConfigIDsCoveringAllNodes(tx *gorm.DB, upstreamID int, nodeIDs []int) ([]int, error) {
	nodeIDs = uniquePositiveInts(nodeIDs)
	if upstreamID <= 0 || len(nodeIDs) == 0 {
		return nil, nil
	}
	var configIDs []int
	if err := tx.Model(model.UpstreamNodeConfig{}).
		Where("upstream_id = ?", upstreamID).
		Pluck("id", &configIDs).Error; err != nil {
		return nil, err
	}
	configIDs = uniquePositiveInts(configIDs)
	if len(configIDs) == 0 {
		return nil, nil
	}

	type coverageRow struct {
		ConfigId int `gorm:"column:config_id"`
		Count    int `gorm:"column:count"`
	}
	var rows []coverageRow
	if err := tx.Table("upstream_node_config_nodes").
		Select("config_id, COUNT(DISTINCT node_id) AS count").
		Where("config_id IN ?", configIDs).
		Where("node_id IN ?", nodeIDs).
		Group("config_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	allCount := len(nodeIDs)
	coveredIDs := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.ConfigId > 0 && row.Count == allCount {
			coveredIDs = append(coveredIDs, row.ConfigId)
		}
	}
	return uniquePositiveInts(coveredIDs), nil
}

func inboundIDsByUpstreamConfigIDs(tx *gorm.DB, configIDs []int) ([]int, error) {
	configIDs = uniquePositiveInts(configIDs)
	if len(configIDs) == 0 {
		return nil, nil
	}
	var inboundIDs []int
	err := tx.Model(model.InboundUpstreamConfig{}).
		Where("config_id IN ?", configIDs).
		Distinct().
		Pluck("inbound_id", &inboundIDs).Error
	return uniquePositiveInts(inboundIDs), err
}

func inboundIDsByUpstreamIDs(tx *gorm.DB, upstreamIDs []int) ([]int, error) {
	upstreamIDs = uniquePositiveInts(upstreamIDs)
	if len(upstreamIDs) == 0 {
		return nil, nil
	}
	var inboundIDs []int
	err := tx.Table("inbound_upstream_configs").
		Joins("JOIN upstream_node_configs ON upstream_node_configs.id = inbound_upstream_configs.config_id").
		Where("upstream_node_configs.upstream_id IN ?", upstreamIDs).
		Distinct().
		Pluck("inbound_upstream_configs.inbound_id", &inboundIDs).Error
	return uniquePositiveInts(inboundIDs), err
}

func inboundIDsByUpstreamNodeIDs(tx *gorm.DB, nodeIDs []int) ([]int, error) {
	nodeIDs = uniquePositiveInts(nodeIDs)
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	var inboundIDs []int
	err := tx.Table("inbound_upstream_configs").
		Joins("JOIN upstream_node_config_nodes ON upstream_node_config_nodes.config_id = inbound_upstream_configs.config_id").
		Where("upstream_node_config_nodes.node_id IN ?", nodeIDs).
		Distinct().
		Pluck("inbound_upstream_configs.inbound_id", &inboundIDs).Error
	return uniquePositiveInts(inboundIDs), err
}

func (s *SubscriptionMarketService) reloadInboundRelayRuntime(inboundIDs []int) error {
	inboundIDs = uniquePositiveInts(inboundIDs)
	if len(inboundIDs) == 0 {
		return nil
	}
	if err := s.EnsureInboundRelayPorts(inboundIDs); err != nil {
		return err
	}
	(&XrayService{}).SetToNeedRestart()
	return nil
}

func (s *SubscriptionMarketService) getUpstreamNodeConfig(configID int) (*UpstreamNodeConfigView, error) {
	configs, err := s.getUpstreamNodeConfigsByIDs([]int{configID})
	if err != nil {
		return nil, err
	}
	if len(configs) == 0 {
		return nil, ErrSubscriptionNotFound
	}
	return &configs[0], nil
}

func (s *SubscriptionMarketService) getUpstreamNodeConfigs(upstreamID int) ([]UpstreamNodeConfigView, error) {
	db := database.GetDB().
		Table("upstream_node_configs").
		Select("upstream_node_configs.id, upstream_node_configs.upstream_id, upstream_subscriptions.name AS upstream_name, upstream_subscriptions.enable, upstream_node_configs.name, upstream_node_configs.created_at, upstream_node_configs.updated_at").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_node_configs.upstream_id").
		Order("upstream_node_configs.upstream_id desc, upstream_node_configs.id asc")
	if upstreamID > 0 {
		db = db.Where("upstream_node_configs.upstream_id = ?", upstreamID)
	}
	var configs []UpstreamNodeConfigView
	if err := db.Scan(&configs).Error; err != nil {
		return nil, err
	}
	return s.fillUpstreamNodeConfigNodeIDs(configs)
}

func (s *SubscriptionMarketService) getUpstreamNodeConfigsByIDs(configIDs []int) ([]UpstreamNodeConfigView, error) {
	configIDs = uniquePositiveInts(configIDs)
	if len(configIDs) == 0 {
		return nil, nil
	}
	var configs []UpstreamNodeConfigView
	if err := database.GetDB().
		Table("upstream_node_configs").
		Select("upstream_node_configs.id, upstream_node_configs.upstream_id, upstream_subscriptions.name AS upstream_name, upstream_subscriptions.enable, upstream_node_configs.name, upstream_node_configs.created_at, upstream_node_configs.updated_at").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_node_configs.upstream_id").
		Where("upstream_node_configs.id IN ?", configIDs).
		Order("upstream_node_configs.upstream_id desc, upstream_node_configs.id asc").
		Scan(&configs).Error; err != nil {
		return nil, err
	}
	return s.fillUpstreamNodeConfigNodeIDs(configs)
}

func (s *SubscriptionMarketService) fillUpstreamNodeConfigNodeIDs(configs []UpstreamNodeConfigView) ([]UpstreamNodeConfigView, error) {
	for index := range configs {
		var nodeIDs []int
		if err := database.GetDB().
			Model(model.UpstreamNodeConfigNode{}).
			Where("config_id = ?", configs[index].Id).
			Order("node_id asc").
			Pluck("node_id", &nodeIDs).Error; err != nil {
			return nil, err
		}
		configs[index].NodeIds = nodeIDs
		configs[index].NodeCount = len(nodeIDs)
	}
	return configs, nil
}

func (s *SubscriptionMarketService) ensureLegacyNodeConfigs(tx *gorm.DB) error {
	var upstreamIDs []int
	if err := tx.Model(model.UpstreamNode{}).
		Where("emergency = ?", true).
		Distinct().
		Pluck("upstream_id", &upstreamIDs).Error; err != nil {
		return err
	}
	legacyUpstreamIDs := uniquePositiveInts(upstreamIDs)
	for _, upstreamID := range legacyUpstreamIDs {
		var count int64
		if err := tx.Model(&model.UpstreamNodeConfig{}).Where("upstream_id = ?", upstreamID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		var nodeIDs []int
		if err := tx.Model(model.UpstreamNode{}).
			Where("upstream_id = ? AND emergency = ?", upstreamID, true).
			Order("sort asc, id asc").
			Pluck("id", &nodeIDs).Error; err != nil {
			return err
		}
		if len(nodeIDs) == 0 {
			continue
		}
		config := model.UpstreamNodeConfig{UpstreamId: upstreamID, Name: "默认配置"}
		if err := tx.Create(&config).Error; err != nil {
			return err
		}
		if err := s.replaceUpstreamNodeConfigNodes(tx, upstreamID, config.Id, nodeIDs); err != nil {
			return err
		}
	}
	if len(legacyUpstreamIDs) > 0 {
		if err := tx.Model(&model.UpstreamNode{}).
			Where("upstream_id IN ? AND emergency = ?", legacyUpstreamIDs, true).
			Update("emergency", false).Error; err != nil {
			return err
		}
	}

	var legacyGrants []model.InboundEmergencyUpstream
	if err := tx.Find(&legacyGrants).Error; err != nil {
		return err
	}
	for _, legacy := range legacyGrants {
		var config model.UpstreamNodeConfig
		if err := tx.Where("upstream_id = ?", legacy.UpstreamId).Order("id asc").First(&config).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		grant := model.InboundUpstreamConfig{InboundId: legacy.InboundId, ConfigId: config.Id}
		if err := tx.Where("inbound_id = ? AND config_id = ?", grant.InboundId, grant.ConfigId).
			FirstOrCreate(&grant).Error; err != nil {
			return err
		}
	}
	if len(legacyGrants) > 0 {
		if err := tx.Where("1 = 1").Delete(&model.InboundEmergencyUpstream{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *SubscriptionMarketService) inboundIDsBySubID(subID string) ([]int, error) {
	subID = strings.TrimSpace(subID)
	if subID == "" {
		return nil, nil
	}
	var rows []struct {
		ID int `gorm:"column:id"`
	}
	err := database.GetDB().Raw(`
		SELECT DISTINCT inbounds.id
		FROM inbounds,
			JSON_EACH(JSON_EXTRACT(inbounds.settings, '$.clients')) AS client
		WHERE
			protocol in ('vmess','vless','trojan','shadowsocks','hysteria','hysteria2')
			AND JSON_EXTRACT(client.value, '$.subId') = ?
			AND enable = ?`, subID, true).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.ID > 0 {
			ids = append(ids, row.ID)
		}
	}
	return ids, nil
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

func buildUpstreamNodeContent(rows []upstreamNodeContentRow, publicHost string) ([]string, []map[string]any) {
	links := make([]string, 0, len(rows))
	clashProxies := make([]map[string]any, 0)
	seenIDs := make(map[string]bool, len(rows))
	seenLinks := make(map[string]bool, len(rows))
	relayHost := normalizeRelayPublicHost(publicHost)
	for _, row := range rows {
		if row.ID > 0 {
			key := strconv.Itoa(row.ID)
			if row.InboundID > 0 {
				key = strconv.Itoa(row.InboundID) + ":" + key
			}
			if seenIDs[key] {
				continue
			}
			seenIDs[key] = true
		}
		link := strings.TrimSpace(row.Link)
		if relayHost != "" && row.RelayPort > 0 {
			link = ""
			if rewritten, ok := authenticatedRelayLink(row.Protocol, row.Link, row.Name, relayHost, row.RelayPort, row.RelayUUID); ok {
				link = rewritten
			}
		}
		if link != "" {
			if seenLinks[link] {
				continue
			}
			seenLinks[link] = true
			links = append(links, link)
		}
		if strings.TrimSpace(row.Clash) != "" {
			var proxy map[string]any
			if err := json.Unmarshal([]byte(row.Clash), &proxy); err == nil && len(proxy) > 0 {
				if relayHost != "" && row.RelayPort > 0 {
					if !rewriteAuthenticatedRelayClashProxy(proxy, row.Protocol, row.Name, relayHost, row.RelayPort, row.RelayUUID) {
						continue
					}
				}
				clashProxies = append(clashProxies, proxy)
			}
		}
	}
	return links, clashProxies
}

func (s *SubscriptionMarketService) EnsureInboundRelayPorts(inboundIDs []int) error {
	inboundIDs = uniquePositiveInts(inboundIDs)
	type relaySourceRow struct {
		InboundId int `gorm:"column:inbound_id"`
		NodeId    int `gorm:"column:node_id"`
	}

	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		query := tx.Table("inbound_upstream_configs").
			Select("DISTINCT inbound_upstream_configs.inbound_id, upstream_node_config_nodes.node_id").
			Joins("JOIN upstream_node_config_nodes ON upstream_node_config_nodes.config_id = inbound_upstream_configs.config_id").
			Joins("JOIN upstream_nodes ON upstream_nodes.id = upstream_node_config_nodes.node_id").
			Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
			Where("upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true)
		if len(inboundIDs) > 0 {
			query = query.Where("inbound_upstream_configs.inbound_id IN ?", inboundIDs)
		}

		var sources []relaySourceRow
		if err := query.Scan(&sources).Error; err != nil {
			return err
		}

		seen := make(map[string]relaySourceRow, len(sources))
		for _, source := range sources {
			if source.InboundId <= 0 || source.NodeId <= 0 {
				continue
			}
			seen[inboundRelayKey(source.InboundId, source.NodeId)] = source
		}

		var existing []model.InboundUpstreamRelay
		existingQuery := tx.Model(model.InboundUpstreamRelay{})
		if len(inboundIDs) > 0 {
			existingQuery = existingQuery.Where("inbound_id IN ?", inboundIDs)
		}
		if err := existingQuery.Find(&existing).Error; err != nil {
			return err
		}

		existingByKey := make(map[string]model.InboundUpstreamRelay, len(existing))
		for _, relay := range existing {
			key := inboundRelayKey(relay.InboundId, relay.NodeId)
			existingByKey[key] = relay
			if _, ok := seen[key]; !ok {
				if err := tx.Delete(&model.InboundUpstreamRelay{}, relay.Id).Error; err != nil {
					return err
				}
			} else if strings.TrimSpace(relay.RelayUUID) == "" {
				relay.RelayUUID = uuid.NewString()
				if err := tx.Model(&model.InboundUpstreamRelay{}).
					Where("id = ?", relay.Id).
					Update("relay_uuid", relay.RelayUUID).Error; err != nil {
					return err
				}
				existingByKey[key] = relay
			}
		}

		usedRelayPorts, err := collectUsedRelayPorts(tx)
		if err != nil {
			return err
		}
		for key, source := range seen {
			if relay, ok := existingByKey[key]; ok && relay.RelayPort > 0 {
				continue
			}
			port, err := nextRelayPort(usedRelayPorts)
			if err != nil {
				return err
			}
			relay := model.InboundUpstreamRelay{
				InboundId: source.InboundId,
				NodeId:    source.NodeId,
				RelayPort: port,
				RelayUUID: uuid.NewString(),
			}
			if err := tx.Create(&relay).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func inboundRelayKey(inboundID, nodeID int) string {
	return strconv.Itoa(inboundID) + ":" + strconv.Itoa(nodeID)
}

func relayInboundSettings(targetHost string, targetPort int) (string, error) {
	settings, err := json.Marshal(map[string]any{
		"address":        targetHost,
		"port":           targetPort,
		"network":        "tcp,udp",
		"followRedirect": false,
	})
	if err != nil {
		return "", err
	}
	return string(settings), nil
}

func relayInboundStreamSettings() json_util.RawMessage {
	settings, err := json.Marshal(map[string]any{
		"sockopt": map[string]any{
			"tcpFastOpen":          true,
			"tcpKeepAliveInterval": 15,
			"tcpKeepAliveIdle":     60,
			"tcpcongestion":        "bbr",
		},
	})
	if err != nil {
		return nil
	}
	return json_util.RawMessage(settings)
}

func relayDisabledSniffing() json_util.RawMessage {
	return json_util.RawMessage(`{"enabled":false}`)
}

func relayFastFreedomOutbound() map[string]any {
	return map[string]any{
		"tag":      inboundRelayFastOutboundTag,
		"protocol": "freedom",
		"settings": map[string]any{
			"domainStrategy": "AsIs",
			"redirect":       "",
			"noises":         []any{},
		},
		"streamSettings": map[string]any{
			"sockopt": map[string]any{
				"tcpFastOpen":          true,
				"tcpKeepAliveInterval": 15,
			},
		},
	}
}

type relayRuntimeRow struct {
	InboundId          int
	InboundTag         string
	NodeId             int
	RelayPort          int
	RelayUUID          string
	SocksProxyEnabled  bool
	SocksProxyHost     string
	SocksProxyPort     int
	SocksProxyUsername string
	SocksProxyPassword string
	Protocol           string
	Link               string
	Clash              string
}

func (s *SubscriptionMarketService) ApplyUpstreamRelayRuntime(xrayConfig *xray.Config) error {
	if xrayConfig == nil {
		return nil
	}
	db := database.GetDB()
	if err := s.EnsureInboundRelayPorts(nil); err != nil {
		return err
	}

	var rows []relayRuntimeRow
	err := db.Table("inbound_upstream_relays").
		Select("inbound_upstream_relays.inbound_id, inbounds.tag AS inbound_tag, inbound_upstream_relays.node_id, inbound_upstream_relays.relay_port, inbound_upstream_relays.relay_uuid, inbounds.socks_proxy_enabled, inbounds.socks_proxy_host, inbounds.socks_proxy_port, inbounds.socks_proxy_username, inbounds.socks_proxy_password, upstream_nodes.protocol, upstream_nodes.link, upstream_nodes.clash").
		Joins("JOIN inbounds ON inbounds.id = inbound_upstream_relays.inbound_id").
		Joins("JOIN upstream_nodes ON upstream_nodes.id = inbound_upstream_relays.node_id").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
		Where("inbounds.enable = ? AND inbounds.emergency_enable = ?", true, true).
		Where("upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true).
		Where("inbound_upstream_relays.relay_port > 0").
		Order("inbound_upstream_relays.inbound_id asc, upstream_nodes.sort asc, upstream_nodes.id asc").
		Scan(&rows).Error
	if err != nil {
		return err
	}

	outbounds := make([]any, 0)
	rules := make([]any, 0)
	for _, row := range rows {
		if inbound, outbound, rule, ok := buildAuthenticatedRelayRuntime(row); ok {
			xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, inbound)
			outbounds = append(outbounds, outbound)
			rules = append(rules, rule)
			continue
		}
	}
	return appendRelayRouting(xrayConfig, outbounds, rules)
}

func relayOutboundTagForRuntimeRow(row relayRuntimeRow) (string, bool) {
	if row.SocksProxyEnabled && strings.TrimSpace(row.SocksProxyHost) != "" && row.SocksProxyPort > 0 {
		inbound := model.Inbound{
			Id:                 row.InboundId,
			Tag:                row.InboundTag,
			SocksProxyEnabled:  row.SocksProxyEnabled,
			SocksProxyHost:     row.SocksProxyHost,
			SocksProxyPort:     row.SocksProxyPort,
			SocksProxyUsername: row.SocksProxyUsername,
			SocksProxyPassword: row.SocksProxyPassword,
		}
		return inboundSocksProxyTag(&inbound), false
	}
	return inboundRelayFastOutboundTag, true
}

func appendRelayRouting(xrayConfig *xray.Config, relayOutbounds []any, relayRules []any) error {
	if len(relayOutbounds) == 0 && len(relayRules) == 0 {
		return nil
	}
	var outbounds []any
	rawOutbounds := strings.TrimSpace(string(xrayConfig.OutboundConfigs))
	if rawOutbounds != "" && rawOutbounds != "null" {
		if err := json.Unmarshal(xrayConfig.OutboundConfigs, &outbounds); err != nil {
			return err
		}
	}
	outbounds = filterOutboundsByTags(outbounds, relayOutboundTags(relayOutbounds))
	outbounds = append(outbounds, relayOutbounds...)
	outboundBytes, err := json.MarshalIndent(outbounds, "", "  ")
	if err != nil {
		return err
	}
	xrayConfig.OutboundConfigs = outboundBytes

	routing := map[string]any{}
	rawRouting := strings.TrimSpace(string(xrayConfig.RouterConfig))
	if rawRouting != "" && rawRouting != "null" {
		if err := json.Unmarshal(xrayConfig.RouterConfig, &routing); err != nil {
			return err
		}
	}
	var rules []any
	if existing, ok := routing["rules"].([]any); ok {
		rules = existing
	}
	for index := len(relayRules) - 1; index >= 0; index-- {
		rule, ok := relayRules[index].(map[string]any)
		if !ok {
			continue
		}
		rules = prependAfterAPIRule(rules, rule)
	}
	routing["rules"] = rules
	routingBytes, err := json.MarshalIndent(routing, "", "  ")
	if err != nil {
		return err
	}
	xrayConfig.RouterConfig = routingBytes
	return nil
}

func relayOutboundTags(outbounds []any) map[string]bool {
	tags := make(map[string]bool)
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := outboundMap["tag"].(string)
		tag = strings.TrimSpace(tag)
		if tag != "" {
			tags[tag] = true
		}
	}
	return tags
}

func filterOutboundsByTags(outbounds []any, tags map[string]bool) []any {
	if len(tags) == 0 || len(outbounds) == 0 {
		return outbounds
	}
	filtered := make([]any, 0, len(outbounds))
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]any)
		if !ok {
			filtered = append(filtered, outbound)
			continue
		}
		tag, _ := outboundMap["tag"].(string)
		if !tags[strings.TrimSpace(tag)] {
			filtered = append(filtered, outbound)
		}
	}
	return filtered
}

func setStringIfNotEmpty(target map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		target[key] = value
	}
}

func setIntIfPositive(target map[string]any, key string, value int) {
	if value > 0 {
		target[key] = value
	}
}

func queryBool(values url.Values, key string) bool {
	raw := strings.TrimSpace(values.Get(key))
	if raw == "" {
		return false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func queryInt(values url.Values, key string) int {
	raw := strings.TrimSpace(values.Get(key))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func buildAuthenticatedRelayRuntime(row relayRuntimeRow) (xray.InboundConfig, map[string]any, map[string]any, bool) {
	if inbound, outbound, rule, ok := buildAuthenticatedHysteria2RelayRuntime(row); ok {
		return inbound, outbound, rule, true
	}
	return buildAuthenticatedVMessRelayRuntime(row)
}

type hysteria2RelayEndpoint struct {
	Address                 string
	Port                    int
	Auth                    string
	SNI                     string
	ALPN                    []string
	AllowInsecure           bool
	Fingerprint             string
	Congestion              string
	Up                      string
	Down                    string
	InitStreamWindow        int
	MaxStreamWindow         int
	InitConnectionWindow    int
	MaxConnectionWindow     int
	MaxIdleTimeout          int
	KeepAlivePeriod         int
	DisablePathMTUDiscovery bool
	Obfs                    string
	ObfsPassword            string
}

func buildAuthenticatedHysteria2RelayRuntime(row relayRuntimeRow) (xray.InboundConfig, map[string]any, map[string]any, bool) {
	if !isHysteria2Protocol(row.Protocol) || row.RelayPort <= 0 {
		return xray.InboundConfig{}, nil, nil, false
	}
	endpoint, ok := parseHysteria2RelayEndpoint(row.Link, row.Clash)
	if !ok || endpoint.Address == "" || endpoint.Port <= 0 || endpoint.Auth == "" {
		return xray.InboundConfig{}, nil, nil, false
	}
	relayAuth := strings.TrimSpace(row.RelayUUID)
	if relayAuth == "" {
		return xray.InboundConfig{}, nil, nil, false
	}

	inboundTag := inboundRelayTag(row.InboundId, row.NodeId)
	outboundTag := inboundTag + "-out"
	inboundSettings, err := json.Marshal(map[string]any{
		"version": 2,
		"clients": []any{
			map[string]any{
				"auth":  relayAuth,
				"email": relayCredentialEmail(row.InboundId, row.NodeId),
			},
		},
	})
	if err != nil {
		return xray.InboundConfig{}, nil, nil, false
	}

	outbound := map[string]any{
		"tag":      outboundTag,
		"protocol": string(model.Hysteria),
		"settings": map[string]any{
			"version": 2,
			"address": endpoint.Address,
			"port":    endpoint.Port,
		},
		"streamSettings": relayHysteria2OutboundStreamSettings(endpoint),
	}
	rule := map[string]any{
		"type":        "field",
		"inboundTag":  []string{inboundTag},
		"outboundTag": outboundTag,
	}
	inbound := xray.InboundConfig{
		Listen:         json_util.RawMessage(`"0.0.0.0"`),
		Port:           row.RelayPort,
		Protocol:       string(model.Hysteria),
		Settings:       json_util.RawMessage(inboundSettings),
		StreamSettings: relayHysteria2InboundStreamSettings(),
		Sniffing:       relayDisabledSniffing(),
		Tag:            inboundTag,
	}
	return inbound, outbound, rule, true
}

func isHysteria2Protocol(protocol string) bool {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	return protocol == "hysteria2" || protocol == "hy2"
}

func relayHysteria2InboundStreamSettings() json_util.RawMessage {
	if err := ensureHysteriaDefaultTLSCertificate(); err != nil {
		logger.Warning("failed to prepare default Hysteria2 relay certificate:", err)
	}
	stream := map[string]any{
		"network":  "hysteria",
		"security": "tls",
		"tlsSettings": map[string]any{
			"alpn":                    []any{"h3"},
			"enableSessionResumption": true,
			"certificates": []any{
				map[string]any{
					"certificateFile": hysteriaDefaultTLSCertFile(),
					"keyFile":         hysteriaDefaultTLSKeyFile(),
					"oneTimeLoading":  false,
					"usage":           "encipherment",
					"buildChain":      false,
				},
			},
		},
		"hysteriaSettings": map[string]any{
			"version":        2,
			"udpIdleTimeout": 60,
		},
		"finalmask": map[string]any{
			"quicParams": defaultRelayHysteria2QuicParams(),
		},
	}
	data, err := json.Marshal(stream)
	if err != nil {
		return json_util.RawMessage(`{"network":"hysteria","security":"tls","hysteriaSettings":{"version":2}}`)
	}
	return json_util.RawMessage(data)
}

func relayHysteria2OutboundStreamSettings(endpoint hysteria2RelayEndpoint) map[string]any {
	hysteriaSettings := map[string]any{
		"version": 2,
		"auth":    endpoint.Auth,
	}

	tlsSettings := map[string]any{
		"enableSessionResumption": true,
	}
	setStringIfNotEmpty(tlsSettings, "serverName", endpoint.SNI)
	setStringIfNotEmpty(tlsSettings, "fingerprint", endpoint.Fingerprint)
	if len(endpoint.ALPN) > 0 {
		alpn := make([]any, 0, len(endpoint.ALPN))
		for _, value := range endpoint.ALPN {
			if value = strings.TrimSpace(value); value != "" {
				alpn = append(alpn, value)
			}
		}
		if len(alpn) > 0 {
			tlsSettings["alpn"] = alpn
		}
	}
	if endpoint.AllowInsecure {
		tlsSettings["allowInsecure"] = true
	}

	stream := map[string]any{
		"network":          "hysteria",
		"security":         "tls",
		"hysteriaSettings": hysteriaSettings,
		"tlsSettings":      tlsSettings,
	}
	finalmask := map[string]any{
		"quicParams": relayHysteria2QuicParams(endpoint),
	}
	if strings.EqualFold(endpoint.Obfs, "salamander") && strings.TrimSpace(endpoint.ObfsPassword) != "" {
		finalmask["udp"] = []any{
			map[string]any{
				"type": "salamander",
				"settings": map[string]any{
					"password": endpoint.ObfsPassword,
				},
			},
		}
	}
	stream["finalmask"] = finalmask
	return stream
}

func defaultRelayHysteria2QuicParams() map[string]any {
	return map[string]any{
		"congestion":                  "bbr",
		"bbrProfile":                  "aggressive",
		"initStreamReceiveWindow":     32 * 1024 * 1024,
		"maxStreamReceiveWindow":      64 * 1024 * 1024,
		"initConnectionReceiveWindow": 64 * 1024 * 1024,
		"maxConnectionReceiveWindow":  128 * 1024 * 1024,
		"maxIdleTimeout":              30,
		"keepAlivePeriod":             10,
		"maxIncomingStreams":          1024,
	}
}

func relayHysteria2QuicParams(endpoint hysteria2RelayEndpoint) map[string]any {
	params := defaultRelayHysteria2QuicParams()
	congestion := strings.ToLower(strings.TrimSpace(endpoint.Congestion))
	if congestion != "" {
		if congestion == "force-brutal" && strings.TrimSpace(endpoint.Up) == "" {
			congestion = "bbr"
		}
		params["congestion"] = congestion
		if congestion != "bbr" {
			delete(params, "bbrProfile")
		}
	}
	setStringIfNotEmpty(params, "brutalUp", endpoint.Up)
	setStringIfNotEmpty(params, "brutalDown", endpoint.Down)
	setIntIfPositive(params, "initStreamReceiveWindow", endpoint.InitStreamWindow)
	setIntIfPositive(params, "maxStreamReceiveWindow", endpoint.MaxStreamWindow)
	setIntIfPositive(params, "initConnectionReceiveWindow", endpoint.InitConnectionWindow)
	setIntIfPositive(params, "maxConnectionReceiveWindow", endpoint.MaxConnectionWindow)
	setIntIfPositive(params, "maxIdleTimeout", endpoint.MaxIdleTimeout)
	setIntIfPositive(params, "keepAlivePeriod", endpoint.KeepAlivePeriod)
	if endpoint.DisablePathMTUDiscovery {
		params["disablePathMTUDiscovery"] = true
	}
	return params
}

func parseHysteria2RelayEndpoint(link, clash string) (hysteria2RelayEndpoint, bool) {
	if endpoint, ok := parseHysteria2RelayLink(link); ok {
		return endpoint, true
	}
	if strings.TrimSpace(clash) == "" {
		return hysteria2RelayEndpoint{}, false
	}
	var proxy map[string]any
	if err := json.Unmarshal([]byte(clash), &proxy); err != nil {
		return hysteria2RelayEndpoint{}, false
	}
	return parseHysteria2ClashProxy(proxy)
}

func parseHysteria2RelayLink(link string) (hysteria2RelayEndpoint, bool) {
	link = strings.TrimSpace(link)
	if !isHysteria2Protocol(uriProtocol(link)) {
		return hysteria2RelayEndpoint{}, false
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || u.User == nil {
		return hysteria2RelayEndpoint{}, false
	}
	host, port, ok := targetFromURL(u)
	if !ok {
		return hysteria2RelayEndpoint{}, false
	}
	auth := strings.TrimSpace(u.User.Username())
	if auth == "" {
		return hysteria2RelayEndpoint{}, false
	}
	q := u.Query()
	endpoint := hysteria2RelayEndpoint{
		Address:                 host,
		Port:                    port,
		Auth:                    auth,
		SNI:                     firstNonEmpty(q.Get("sni"), q.Get("peer"), q.Get("servername"), q.Get("serverName")),
		ALPN:                    splitCSV(q.Get("alpn")),
		AllowInsecure:           queryBool(q, "insecure") || queryBool(q, "allowInsecure") || queryBool(q, "skip-cert-verify"),
		Fingerprint:             firstNonEmpty(q.Get("fp"), q.Get("fingerprint")),
		Congestion:              q.Get("congestion"),
		Up:                      q.Get("up"),
		Down:                    q.Get("down"),
		InitStreamWindow:        queryInt(q, "initStreamReceiveWindow"),
		MaxStreamWindow:         queryInt(q, "maxStreamReceiveWindow"),
		InitConnectionWindow:    queryInt(q, "initConnectionReceiveWindow"),
		MaxConnectionWindow:     queryInt(q, "maxConnectionReceiveWindow"),
		MaxIdleTimeout:          queryInt(q, "maxIdleTimeout"),
		KeepAlivePeriod:         queryInt(q, "keepAlivePeriod"),
		DisablePathMTUDiscovery: queryBool(q, "disablePathMTUDiscovery"),
		Obfs:                    q.Get("obfs"),
		ObfsPassword:            firstNonEmpty(q.Get("obfs-password"), q.Get("obfs_password")),
	}
	return endpoint, true
}

func parseHysteria2ClashProxy(proxy map[string]any) (hysteria2RelayEndpoint, bool) {
	if !isHysteria2Protocol(normalizeClashProtocol(clashString(proxy, "type"))) {
		return hysteria2RelayEndpoint{}, false
	}
	server, port, ok := clashServerPort(proxy)
	auth := firstNonEmpty(clashString(proxy, "password"), clashString(proxy, "auth"))
	if !ok || auth == "" {
		return hysteria2RelayEndpoint{}, false
	}
	return hysteria2RelayEndpoint{
		Address:                 server,
		Port:                    port,
		Auth:                    auth,
		SNI:                     firstNonEmpty(clashString(proxy, "sni"), clashString(proxy, "servername"), clashString(proxy, "server-name")),
		ALPN:                    clashStringList(proxy, "alpn"),
		AllowInsecure:           clashBool(proxy, "skip-cert-verify") || clashBool(proxy, "allow-insecure") || clashBool(proxy, "insecure"),
		Fingerprint:             firstNonEmpty(clashString(proxy, "fp"), clashString(proxy, "fingerprint"), clashString(proxy, "client-fingerprint")),
		Congestion:              clashString(proxy, "congestion"),
		Up:                      clashString(proxy, "up"),
		Down:                    clashString(proxy, "down"),
		InitStreamWindow:        clashIntAny(firstClashValue(proxy, "initStreamReceiveWindow", "init-stream-receive-window")),
		MaxStreamWindow:         clashIntAny(firstClashValue(proxy, "maxStreamReceiveWindow", "max-stream-receive-window")),
		InitConnectionWindow:    clashIntAny(firstClashValue(proxy, "initConnectionReceiveWindow", "init-connection-receive-window")),
		MaxConnectionWindow:     clashIntAny(firstClashValue(proxy, "maxConnectionReceiveWindow", "max-connection-receive-window")),
		MaxIdleTimeout:          clashIntAny(firstClashValue(proxy, "maxIdleTimeout", "max-idle-timeout")),
		KeepAlivePeriod:         clashIntAny(firstClashValue(proxy, "keepAlivePeriod", "keep-alive-period")),
		DisablePathMTUDiscovery: clashBool(proxy, "disablePathMTUDiscovery") || clashBool(proxy, "disable-path-mtu-discovery"),
		Obfs:                    clashString(proxy, "obfs"),
		ObfsPassword:            firstNonEmpty(clashString(proxy, "obfs-password"), clashString(proxy, "obfs_password")),
	}, true
}

func buildAuthenticatedVMessRelayRuntime(row relayRuntimeRow) (xray.InboundConfig, map[string]any, map[string]any, bool) {
	if !strings.EqualFold(row.Protocol, "vmess") || row.RelayPort <= 0 {
		return xray.InboundConfig{}, nil, nil, false
	}
	if strings.TrimSpace(row.RelayUUID) == "" {
		return xray.InboundConfig{}, nil, nil, false
	}
	vmessData, ok := parseVMessLinkData(row.Link)
	if !ok {
		return xray.InboundConfig{}, nil, nil, false
	}

	address := firstNonEmpty(clashString(vmessData, "add"), clashString(vmessData, "address"), clashString(vmessData, "server"))
	port := clashIntAny(vmessData["port"])
	upstreamID := firstNonEmpty(clashString(vmessData, "id"), clashString(vmessData, "uuid"))
	if address == "" || port <= 0 || upstreamID == "" {
		return xray.InboundConfig{}, nil, nil, false
	}

	inboundTag := inboundRelayTag(row.InboundId, row.NodeId)
	outboundTag := inboundTag + "-out"
	inboundSettings := map[string]any{
		"clients": []any{
			map[string]any{
				"id":      row.RelayUUID,
				"alterId": 0,
				"email":   relayCredentialEmail(row.InboundId, row.NodeId),
			},
		},
		"disableInsecureEncryption": false,
	}
	settingsJSON, err := json.Marshal(inboundSettings)
	if err != nil {
		return xray.InboundConfig{}, nil, nil, false
	}

	outboundUser := map[string]any{
		"id":       upstreamID,
		"alterId":  clashIntAny(vmessData["aid"]),
		"security": firstNonEmpty(clashString(vmessData, "scy"), "auto"),
	}
	streamSettings := buildVMessOutboundStreamSettings(vmessData)
	addRelaySockopt(streamSettings)
	outbound := map[string]any{
		"tag":      outboundTag,
		"protocol": "vmess",
		"settings": map[string]any{
			"vnext": []any{
				map[string]any{
					"address": address,
					"port":    port,
					"users":   []any{outboundUser},
				},
			},
		},
		"streamSettings": streamSettings,
	}
	rule := map[string]any{
		"type":        "field",
		"inboundTag":  []string{inboundTag},
		"outboundTag": outboundTag,
	}
	inbound := xray.InboundConfig{
		Listen:         json_util.RawMessage(`"0.0.0.0"`),
		Port:           row.RelayPort,
		Protocol:       "vmess",
		Settings:       json_util.RawMessage(settingsJSON),
		StreamSettings: relayVMessInboundStreamSettings(),
		Sniffing:       relayDisabledSniffing(),
		Tag:            inboundTag,
	}
	return inbound, outbound, rule, true
}

func relayVMessInboundStreamSettings() json_util.RawMessage {
	settings := map[string]any{
		"network":  "tcp",
		"security": "none",
	}
	addRelaySockopt(settings)
	data, err := json.Marshal(settings)
	if err != nil {
		return json_util.RawMessage(`{"network":"tcp","security":"none"}`)
	}
	return json_util.RawMessage(data)
}

func addRelaySockopt(stream map[string]any) {
	if stream == nil {
		return
	}
	stream["sockopt"] = map[string]any{
		"tcpFastOpen":          true,
		"tcpKeepAliveInterval": 15,
		"tcpKeepAliveIdle":     60,
		"tcpcongestion":        "bbr",
	}
}

func parseVMessLinkData(link string) (map[string]any, bool) {
	if !strings.EqualFold(uriProtocol(link), "vmess") {
		return nil, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(link, "vmess://"))
	queryText := ""
	if basePayload, rest, ok := strings.Cut(payload, "?"); ok {
		payload = basePayload
		queryText, _, _ = strings.Cut(rest, "#")
	} else if basePayload, _, ok := strings.Cut(payload, "#"); ok {
		payload = basePayload
	}
	decoded, ok := decodeBase64Any(payload)
	if !ok {
		return nil, false
	}
	decoded = strings.TrimSpace(decoded)
	data := map[string]any{}
	if strings.HasPrefix(decoded, "{") {
		if err := json.Unmarshal([]byte(decoded), &data); err != nil {
			return nil, false
		}
		return data, true
	}
	return parseLegacyVMessData(decoded, queryText)
}

func parseLegacyVMessData(decoded string, queryText string) (map[string]any, bool) {
	at := strings.LastIndex(decoded, "@")
	if at <= 0 || at >= len(decoded)-1 {
		return nil, false
	}
	credential := strings.TrimSpace(decoded[:at])
	target := strings.TrimSpace(decoded[at+1:])
	if credential == "" || target == "" {
		return nil, false
	}

	security := "auto"
	upstreamID := credential
	if left, right, ok := strings.Cut(credential, ":"); ok {
		if strings.TrimSpace(left) != "" {
			security = strings.TrimSpace(left)
		}
		upstreamID = strings.TrimSpace(right)
	}
	host, port, ok := targetFromURL(&url.URL{Host: target})
	if !ok || upstreamID == "" {
		return nil, false
	}

	query, _ := url.ParseQuery(queryText)
	network := strings.ToLower(strings.TrimSpace(firstNonEmpty(query.Get("type"), query.Get("net"), query.Get("obfs"), "tcp")))
	switch network {
	case "", "none":
		network = "tcp"
	case "websocket":
		network = "ws"
	case "h2":
		network = "http"
	}
	securityMode := strings.TrimSpace(firstNonEmpty(query.Get("tls"), query.Get("security")))
	if strings.EqualFold(securityMode, "1") || strings.EqualFold(securityMode, "true") {
		securityMode = "tls"
	}
	if strings.EqualFold(securityMode, "0") || strings.EqualFold(securityMode, "false") {
		securityMode = ""
	}

	data := map[string]any{
		"add":  host,
		"port": port,
		"id":   upstreamID,
		"aid":  firstNonEmpty(query.Get("alterId"), query.Get("aid"), "0"),
		"scy":  security,
		"net":  network,
		"tls":  securityMode,
		"type": firstNonEmpty(query.Get("headerType"), query.Get("obfs"), "none"),
		"host": firstNonEmpty(query.Get("host"), query.Get("peer")),
		"path": query.Get("path"),
	}
	if name := subscriptionQueryName(queryText); name != "" {
		data["ps"] = name
	}
	return data, true
}

func buildVMessOutboundStreamSettings(data map[string]any) map[string]any {
	network := strings.ToLower(firstNonEmpty(clashString(data, "net"), "tcp"))
	if network == "h2" {
		network = "http"
	}
	stream := map[string]any{
		"network": network,
	}
	security := strings.ToLower(firstNonEmpty(clashString(data, "tls"), clashString(data, "security"), "none"))
	if security == "" {
		security = "none"
	}
	stream["security"] = security
	if security == "tls" || security == "reality" {
		tlsSettings := map[string]any{}
		if sni := firstNonEmpty(clashString(data, "sni"), clashString(data, "host")); sni != "" {
			tlsSettings["serverName"] = sni
		}
		if fingerprint := firstNonEmpty(clashString(data, "fp"), clashString(data, "fingerprint")); fingerprint != "" {
			tlsSettings["fingerprint"] = fingerprint
		}
		if alpn := splitCSV(clashString(data, "alpn")); len(alpn) > 0 {
			tlsSettings["alpn"] = alpn
		}
		if security == "reality" {
			stream["realitySettings"] = tlsSettings
		} else {
			stream["tlsSettings"] = tlsSettings
		}
	}

	switch network {
	case "ws":
		wsSettings := map[string]any{}
		if path := clashString(data, "path"); path != "" {
			wsSettings["path"] = path
		}
		if host := clashString(data, "host"); host != "" {
			wsSettings["headers"] = map[string]any{"Host": host}
		}
		stream["wsSettings"] = wsSettings
	case "grpc":
		grpcSettings := map[string]any{}
		if serviceName := firstNonEmpty(clashString(data, "path"), clashString(data, "serviceName"), clashString(data, "service-name")); serviceName != "" {
			grpcSettings["serviceName"] = serviceName
		}
		stream["grpcSettings"] = grpcSettings
	case "http":
		httpSettings := map[string]any{}
		if path := clashString(data, "path"); path != "" {
			httpSettings["path"] = []string{path}
		}
		if host := clashString(data, "host"); host != "" {
			httpSettings["host"] = []string{host}
		}
		stream["httpSettings"] = httpSettings
	default:
		headerType := firstNonEmpty(clashString(data, "type"), "none")
		if headerType != "none" {
			stream["tcpSettings"] = map[string]any{
				"header": map[string]any{"type": headerType},
			}
		}
	}
	return stream
}

func relayCredentialEmail(inboundID, nodeID int) string {
	return fmt.Sprintf("relay-in-%d-node-%d", inboundID, nodeID)
}

func splitCSV(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (s *SubscriptionMarketService) BuildRelayInbounds() ([]xray.InboundConfig, error) {
	db := database.GetDB()
	if err := s.EnsureInboundRelayPorts(nil); err != nil {
		return nil, err
	}

	type relayBuildRow struct {
		InboundId int
		NodeId    int
		RelayPort int
		Link      string
		Clash     string
	}
	var rows []relayBuildRow
	err := db.Table("inbound_upstream_relays").
		Select("inbound_upstream_relays.inbound_id, inbound_upstream_relays.node_id, inbound_upstream_relays.relay_port, upstream_nodes.link, upstream_nodes.clash").
		Joins("JOIN inbounds ON inbounds.id = inbound_upstream_relays.inbound_id").
		Joins("JOIN upstream_nodes ON upstream_nodes.id = inbound_upstream_relays.node_id").
		Joins("JOIN upstream_subscriptions ON upstream_subscriptions.id = upstream_nodes.upstream_id").
		Where("inbounds.enable = ? AND inbounds.emergency_enable = ?", true, true).
		Where("upstream_nodes.enable = ? AND upstream_subscriptions.enable = ?", true, true).
		Where("inbound_upstream_relays.relay_port > 0").
		Order("inbound_upstream_relays.inbound_id asc, upstream_nodes.sort asc, upstream_nodes.id asc").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	relayInbounds := make([]xray.InboundConfig, 0, len(rows))
	for _, row := range rows {
		node := model.UpstreamNode{Link: row.Link, Clash: row.Clash}
		targetHost, targetPort, ok := relayTargetFromNode(node)
		if !ok || row.RelayPort <= 0 {
			continue
		}
		settings, err := relayInboundSettings(targetHost, targetPort)
		if err != nil {
			return nil, err
		}
		relayInbounds = append(relayInbounds, xray.InboundConfig{
			Listen:         json_util.RawMessage(`"0.0.0.0"`),
			Port:           row.RelayPort,
			Protocol:       string(model.Tunnel),
			Settings:       json_util.RawMessage(settings),
			Tag:            inboundRelayTag(row.InboundId, row.NodeId),
			StreamSettings: relayInboundStreamSettings(),
			Sniffing:       relayDisabledSniffing(),
		})
	}
	return relayInbounds, nil
}

func (s *SubscriptionMarketService) AddRelayTraffic(traffics []*xray.Traffic) error {
	if len(traffics) == 0 {
		return nil
	}
	db := database.GetDB()
	for _, traffic := range traffics {
		if traffic == nil || !traffic.IsInbound {
			continue
		}
		if inboundID, nodeID, ok := inboundRelayIDs(traffic.Tag); ok {
			if err := db.Model(&model.InboundUpstreamRelay{}).
				Where("inbound_id = ? AND node_id = ?", inboundID, nodeID).
				Updates(map[string]any{
					"up":       gorm.Expr("up + ?", traffic.Up),
					"down":     gorm.Expr("down + ?", traffic.Down),
					"all_time": gorm.Expr("COALESCE(all_time, 0) + ?", traffic.Up+traffic.Down),
				}).Error; err != nil {
				return err
			}
			if err := db.Model(&model.Inbound{}).
				Where("id = ?", inboundID).
				Updates(map[string]any{
					"up":       gorm.Expr("up + ?", traffic.Up),
					"down":     gorm.Expr("down + ?", traffic.Down),
					"all_time": gorm.Expr("COALESCE(all_time, 0) + ?", traffic.Up+traffic.Down),
				}).Error; err != nil {
				return err
			}
			if err := db.Model(&model.UpstreamNode{}).
				Where("id = ?", nodeID).
				Updates(map[string]any{
					"relay_up":   gorm.Expr("relay_up + ?", traffic.Up),
					"relay_down": gorm.Expr("relay_down + ?", traffic.Down),
				}).Error; err != nil {
				return err
			}
			continue
		}
		nodeID, ok := upstreamRelayNodeID(traffic.Tag)
		if !ok {
			continue
		}
		if err := db.Model(&model.UpstreamNode{}).
			Where("id = ?", nodeID).
			Updates(map[string]any{
				"relay_up":   gorm.Expr("relay_up + ?", traffic.Up),
				"relay_down": gorm.Expr("relay_down + ?", traffic.Down),
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

func collectUsedRelayPorts(tx *gorm.DB) (map[int]bool, error) {
	used := make(map[int]bool)

	var inboundPorts []int
	if err := tx.Model(model.Inbound{}).Where("port > 0").Pluck("port", &inboundPorts).Error; err != nil {
		return nil, err
	}
	for _, port := range inboundPorts {
		used[port] = true
	}

	var relayPorts []int
	if err := tx.Model(model.UpstreamNode{}).Where("relay_port > 0").Pluck("relay_port", &relayPorts).Error; err != nil {
		return nil, err
	}
	for _, port := range relayPorts {
		used[port] = true
	}

	var inboundRelayPorts []int
	if err := tx.Model(model.InboundUpstreamRelay{}).Where("relay_port > 0").Pluck("relay_port", &inboundRelayPorts).Error; err != nil {
		return nil, err
	}
	for _, port := range inboundRelayPorts {
		used[port] = true
	}
	reserveSystemListeningPorts(used, relayPortStart, relayPortEnd)
	return used, nil
}

func reserveSystemListeningPorts(used map[int]bool, start, end int) {
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		isTCP := strings.Contains(path, "tcp")
		for index, line := range strings.Split(string(data), "\n") {
			if index == 0 {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			if isTCP && fields[3] != "0A" {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			portValue, err := strconv.ParseInt(portHex, 16, 32)
			if err != nil {
				continue
			}
			port := int(portValue)
			if port >= start && port <= end {
				used[port] = true
			}
		}
	}
	for _, port := range []int{8080, 8081, 9999} {
		if port >= start && port <= end {
			used[port] = true
		}
	}
}

func nextRelayPort(used map[int]bool) (int, error) {
	for port := relayPortStart; port <= relayPortEnd; port++ {
		if !used[port] {
			used[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available relay port in %d-%d", relayPortStart, relayPortEnd)
}

func upstreamRelayTag(nodeID int) string {
	return fmt.Sprintf("%s%d", relayTagPrefix, nodeID)
}

func upstreamRelayNodeID(tag string) (int, bool) {
	if !strings.HasPrefix(tag, relayTagPrefix) {
		return 0, false
	}
	id, err := strconv.Atoi(strings.TrimPrefix(tag, relayTagPrefix))
	return id, err == nil && id > 0
}

func inboundRelayTag(inboundID, nodeID int) string {
	return fmt.Sprintf("xui-relay-in-%d-node-%d", inboundID, nodeID)
}

func inboundRelayIDs(tag string) (int, int, bool) {
	const prefix = "xui-relay-in-"
	if !strings.HasPrefix(tag, prefix) {
		return 0, 0, false
	}
	rest := strings.TrimPrefix(tag, prefix)
	inboundText, nodeText, ok := strings.Cut(rest, "-node-")
	if !ok {
		return 0, 0, false
	}
	inboundID, inboundErr := strconv.Atoi(inboundText)
	nodeID, nodeErr := strconv.Atoi(nodeText)
	return inboundID, nodeID, inboundErr == nil && nodeErr == nil && inboundID > 0 && nodeID > 0
}

func firstString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func normalizeRelayPublicHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		raw = u.Host
	}
	raw = strings.Trim(raw, "[]")
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return strings.Trim(host, "[]")
	}
	if index := strings.LastIndex(raw, ":"); index > -1 && !strings.Contains(raw[:index], ":") {
		if _, err := strconv.Atoi(raw[index+1:]); err == nil {
			return raw[:index]
		}
	}
	return raw
}

func rewriteRelayLink(link, name, relayHost string, relayPort int) (string, bool) {
	link = strings.TrimSpace(link)
	if link == "" || relayHost == "" || relayPort <= 0 {
		return "", false
	}
	switch uriProtocol(link) {
	case "vmess":
		return rewriteRelayVMessLink(link, name, relayHost, relayPort)
	case "ss":
		if rewritten, ok := rewriteRelayURLLink(link, name, relayHost, relayPort); ok {
			return rewritten, true
		}
		return rewriteRelayLegacySSLink(link, name, relayHost, relayPort)
	case "vless", "trojan", "hysteria", "hysteria2", "hy2", "tuic", "wireguard":
		return rewriteRelayURLLink(link, name, relayHost, relayPort)
	default:
		return "", false
	}
}

func authenticatedRelayLink(protocol, sourceLink, name, relayHost string, relayPort int, relayUUID string) (string, bool) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "vmess":
		return authenticatedVMessRelayLink(sourceLink, name, relayHost, relayPort, relayUUID)
	case "hysteria2", "hy2":
		return authenticatedHysteria2RelayLink(sourceLink, name, relayHost, relayPort, relayUUID)
	default:
		return "", false
	}
}

func authenticatedVMessRelayLink(sourceLink, name, relayHost string, relayPort int, relayUUID string) (string, bool) {
	if _, ok := parseVMessLinkData(sourceLink); !ok {
		return "", false
	}
	relayUUID = strings.TrimSpace(relayUUID)
	if relayHost == "" || relayPort <= 0 || relayUUID == "" {
		return "", false
	}
	obj := map[string]any{
		"v":    "2",
		"ps":   strings.TrimSpace(name),
		"add":  relayHost,
		"port": relayPort,
		"id":   relayUUID,
		"aid":  "0",
		"scy":  "auto",
		"net":  "tcp",
		"type": "none",
		"host": "",
		"path": "",
		"tls":  "",
	}
	encoded, err := json.Marshal(obj)
	if err != nil {
		return "", false
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(encoded), true
}

func authenticatedHysteria2RelayLink(sourceLink, name, relayHost string, relayPort int, relayUUID string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(sourceLink))
	if err != nil || u.Host == "" || !isHysteria2Protocol(u.Scheme) {
		return "", false
	}
	relayUUID = strings.TrimSpace(relayUUID)
	if relayHost == "" || relayPort <= 0 || relayUUID == "" {
		return "", false
	}
	u.Scheme = "hysteria2"
	u.User = url.User(relayUUID)
	u.Host = formatClashHostPort(relayHost, relayPort)
	q := u.Query()
	q.Set("security", "tls")
	q.Set("sni", relayHost)
	q.Set("alpn", "h3")
	for _, key := range []string{
		"insecure", "allowInsecure", "obfs", "obfs-password", "obfs_password", "pinSHA256", "pin-sha256",
		"ca", "ca-str", "ech", "mport", "ports", "udphopPort", "udphopInterval",
		"udphopIntervalMin", "udphopIntervalMax",
	} {
		q.Del(key)
	}
	u.RawQuery = q.Encode()
	if strings.TrimSpace(name) != "" {
		setURLFragmentEncoded(u, strings.TrimSpace(name))
	}
	return u.String(), true
}

func rewriteRelayVMessLink(link, name, relayHost string, relayPort int) (string, bool) {
	data, ok := parseVMessLinkData(link)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(name) != "" {
		data["ps"] = strings.TrimSpace(name)
	}
	data["add"] = relayHost
	data["port"] = relayPort
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", false
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(encoded), true
}

func rewriteRelayURLLink(link, name, relayHost string, relayPort int) (string, bool) {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return "", false
	}
	u.Host = formatClashHostPort(relayHost, relayPort)
	if strings.TrimSpace(name) != "" {
		setURLFragmentEncoded(u, strings.TrimSpace(name))
	}
	return u.String(), true
}

func setURLFragmentEncoded(u *url.URL, fragment string) {
	u.Fragment = fragment
	u.RawFragment = strings.ReplaceAll(url.QueryEscape(fragment), "+", "%20")
}

func rewriteRelayLegacySSLink(link, name, relayHost string, relayPort int) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(link, "ss://"))
	if rest == "" {
		return "", false
	}
	fragment := ""
	if before, after, ok := strings.Cut(rest, "#"); ok {
		rest = before
		fragment = after
	}
	query := ""
	if before, after, ok := strings.Cut(rest, "?"); ok {
		rest = before
		query = after
	}
	decoded, ok := decodeBase64Any(rest)
	if !ok {
		return "", false
	}
	at := strings.LastIndex(decoded, "@")
	if at < 0 {
		return "", false
	}
	rewritten := decoded[:at+1] + formatClashHostPort(relayHost, relayPort)
	result := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(rewritten))
	if query != "" {
		result += "?" + query
	}
	if strings.TrimSpace(name) != "" {
		result += "#" + url.QueryEscape(strings.TrimSpace(name))
	} else if fragment != "" {
		result += "#" + fragment
	}
	return result, true
}

func rewriteRelayClashProxy(proxy map[string]any, name, relayHost string, relayPort int) {
	if strings.TrimSpace(name) != "" {
		proxy["name"] = strings.TrimSpace(name)
	}
	proxy["server"] = relayHost
	proxy["port"] = relayPort
}

func rewriteAuthenticatedRelayClashProxy(proxy map[string]any, protocol, name, relayHost string, relayPort int, relayUUID string) bool {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "vmess":
		return rewriteAuthenticatedVMessRelayClashProxy(proxy, name, relayHost, relayPort, relayUUID)
	case "hysteria2", "hy2":
		return rewriteAuthenticatedHysteria2RelayClashProxy(proxy, name, relayHost, relayPort, relayUUID)
	default:
		return false
	}
}

func rewriteAuthenticatedVMessRelayClashProxy(proxy map[string]any, name, relayHost string, relayPort int, relayUUID string) bool {
	relayUUID = strings.TrimSpace(relayUUID)
	if relayHost == "" || relayPort <= 0 || relayUUID == "" {
		return false
	}
	if strings.TrimSpace(name) != "" {
		proxy["name"] = strings.TrimSpace(name)
	}
	proxy["type"] = "vmess"
	proxy["server"] = relayHost
	proxy["port"] = relayPort
	proxy["uuid"] = relayUUID
	proxy["alterId"] = 0
	proxy["cipher"] = "auto"
	proxy["network"] = "tcp"
	proxy["tls"] = false
	delete(proxy, "servername")
	delete(proxy, "sni")
	delete(proxy, "ws-opts")
	delete(proxy, "grpc-opts")
	delete(proxy, "h2-opts")
	delete(proxy, "http-opts")
	return true
}

func rewriteAuthenticatedHysteria2RelayClashProxy(proxy map[string]any, name, relayHost string, relayPort int, relayUUID string) bool {
	relayUUID = strings.TrimSpace(relayUUID)
	if relayHost == "" || relayPort <= 0 || relayUUID == "" {
		return false
	}
	if strings.TrimSpace(name) != "" {
		proxy["name"] = strings.TrimSpace(name)
	}
	proxy["type"] = "hysteria2"
	proxy["server"] = relayHost
	proxy["port"] = relayPort
	proxy["password"] = relayUUID
	proxy["sni"] = relayHost
	proxy["alpn"] = []any{"h3"}
	proxy["skip-cert-verify"] = true
	for _, key := range []string{
		"auth", "obfs", "obfs-password", "obfs_password", "pinSHA256", "pin-sha256",
		"ca", "ca-str", "mport", "ports", "udp-hop", "udp-hop-interval",
	} {
		delete(proxy, key)
	}
	return true
}

func relayTargetFromNode(node model.UpstreamNode) (string, int, bool) {
	if strings.TrimSpace(node.Clash) != "" {
		var proxy map[string]any
		if err := json.Unmarshal([]byte(node.Clash), &proxy); err == nil {
			if server, port, ok := clashServerPort(proxy); ok {
				return server, port, true
			}
		}
	}
	return relayTargetFromLink(node.Link)
}

func relayTargetFromLink(link string) (string, int, bool) {
	link = strings.TrimSpace(link)
	switch uriProtocol(link) {
	case "vmess":
		return relayTargetFromVMessLink(link)
	case "ss":
		if host, port, ok := relayTargetFromURLLink(link); ok {
			return host, port, true
		}
		return relayTargetFromLegacySSLink(link)
	case "vless", "trojan", "hysteria", "hysteria2", "hy2", "tuic", "wireguard":
		return relayTargetFromURLLink(link)
	default:
		return "", 0, false
	}
}

func relayTargetFromVMessLink(link string) (string, int, bool) {
	data, ok := parseVMessLinkData(link)
	if !ok {
		return "", 0, false
	}
	host, _ := data["add"].(string)
	port, ok := anyToPositiveInt(data["port"])
	return strings.TrimSpace(host), port, strings.TrimSpace(host) != "" && ok
}

func relayTargetFromURLLink(link string) (string, int, bool) {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return "", 0, false
	}
	return targetFromURL(u)
}

func relayTargetFromLegacySSLink(link string) (string, int, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(link, "ss://"))
	if before, _, ok := strings.Cut(rest, "#"); ok {
		rest = before
	}
	if before, _, ok := strings.Cut(rest, "?"); ok {
		rest = before
	}
	decoded, ok := decodeBase64Any(rest)
	if !ok {
		return "", 0, false
	}
	at := strings.LastIndex(decoded, "@")
	if at < 0 {
		return "", 0, false
	}
	u, err := url.Parse("//" + decoded[at+1:])
	if err != nil {
		return "", 0, false
	}
	return targetFromURL(u)
}

func targetFromURL(u *url.URL) (string, int, bool) {
	host := strings.TrimSpace(u.Hostname())
	portText := strings.TrimSpace(u.Port())
	if host == "" || portText == "" {
		return "", 0, false
	}
	port, err := strconv.Atoi(portText)
	return host, port, err == nil && port > 0 && port <= 65535
}

func anyToPositiveInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, v > 0
	case int64:
		return int(v), v > 0
	case float64:
		port := int(v)
		return port, port > 0
	case string:
		port, err := strconv.Atoi(strings.TrimSpace(v))
		return port, err == nil && port > 0
	default:
		return 0, false
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

func fetchUpstreamSubscription(rawURL string) (string, upstreamTrafficInfo, error) {
	var firstErr error
	for _, userAgent := range upstreamFetchUserAgents {
		body, info, err := fetchUpstreamSubscriptionWithUserAgent(rawURL, userAgent)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return body, info, nil
	}
	if firstErr != nil {
		return "", upstreamTrafficInfo{}, firstErr
	}
	return "", upstreamTrafficInfo{}, ErrSubscriptionNoNodes
}

func fetchUpstreamSubscriptionWithUserAgent(rawURL, userAgent string) (string, upstreamTrafficInfo, error) {
	client := &http.Client{Timeout: upstreamFetchTimeout}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", upstreamTrafficInfo{}, err
	}
	req.Header.Set("User-Agent", userAgent)
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

func isSupportedUpstreamRelayProtocolName(protocol, name string) bool {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "vmess":
		return true
	case "hysteria2", "hy2":
		return strings.Contains(strings.TrimSpace(name), directHysteria2NameMarker)
	default:
		return false
	}
}

func isSupportedUpstreamRelayNode(node parsedUpstreamNode) bool {
	return isSupportedUpstreamRelayProtocolName(node.Protocol, node.Name)
}

func filterSupportedUpstreamRelayNodes(nodes []parsedUpstreamNode) []parsedUpstreamNode {
	if len(nodes) == 0 {
		return nil
	}
	filtered := make([]parsedUpstreamNode, 0, len(nodes))
	for _, node := range nodes {
		if isSupportedUpstreamRelayNode(node) {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func parseUpstreamNodes(body string) ([]parsedUpstreamNode, error) {
	candidates := []string{body}
	if decoded, ok := decodeBase64Subscription(body); ok {
		candidates = append([]string{decoded}, candidates...)
	}

	for _, candidate := range candidates {
		nodes := filterSupportedUpstreamRelayNodes(parseURINodes(candidate))
		if len(nodes) > 0 {
			return nodes, nil
		}
	}

	for _, candidate := range candidates {
		nodes := filterSupportedUpstreamRelayNodes(parseClashNodes(candidate))
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
		name := subscriptionURIName(link)
		if isSubscriptionInfoNodeName(name) {
			continue
		}
		nodes = append(nodes, parsedUpstreamNode{
			Name:       name,
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
		protocol = normalizeClashProtocol(protocol)
		if name == "" || protocol == "" {
			continue
		}
		if isSubscriptionInfoNodeName(name) {
			continue
		}
		link := clashProxyShareLink(proxy, protocol, name)
		encoded, err := json.Marshal(proxy)
		if err != nil {
			continue
		}
		nodes = append(nodes, parsedUpstreamNode{
			Name:       name,
			Protocol:   protocol,
			Link:       link,
			Clash:      string(encoded),
			SourceType: "clash",
		})
	}
	return nodes
}

func normalizeClashProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "shadowsocks" {
		return "ss"
	}
	if protocol == "hy2" {
		return "hysteria2"
	}
	return protocol
}

func clashProxyShareLink(proxy map[string]any, protocol, name string) string {
	switch protocol {
	case "vmess":
		return clashVMessShareLink(proxy, name)
	case "vless":
		return clashVLESSShareLink(proxy, name)
	case "trojan":
		return clashTrojanShareLink(proxy, name)
	case "ss":
		return clashSSShareLink(proxy, name)
	case "hysteria2", "hy2":
		return clashHysteria2ShareLink(proxy, name)
	default:
		return ""
	}
}

func clashVMessShareLink(proxy map[string]any, name string) string {
	server, port, ok := clashServerPort(proxy)
	uuid := clashString(proxy, "uuid")
	if !ok || uuid == "" {
		return ""
	}
	network := normalizeClashNetwork(clashString(proxy, "network"), "tcp")
	obj := map[string]any{
		"v":    "2",
		"ps":   name,
		"add":  server,
		"port": port,
		"id":   uuid,
		"aid":  clashIntAny(firstClashValue(proxy, "alterId", "alterID", "aid")),
		"scy":  firstNonEmpty(clashString(proxy, "cipher"), "auto"),
		"net":  vmessNetworkName(network),
		"type": "none",
		"host": "",
		"path": "",
		"tls":  "",
	}
	if clashTLSEnabled(proxy) {
		obj["tls"] = "tls"
	}
	applyClashVMessTransport(proxy, network, obj)
	applyClashVMessTLS(proxy, obj)
	encoded, _ := json.Marshal(obj)
	return "vmess://" + base64.StdEncoding.EncodeToString(encoded)
}

func clashVLESSShareLink(proxy map[string]any, name string) string {
	server, port, ok := clashServerPort(proxy)
	uuid := clashString(proxy, "uuid")
	if !ok || uuid == "" {
		return ""
	}
	params := url.Values{}
	params.Set("encryption", "none")
	applyClashShareParams(proxy, params)
	if flow := clashString(proxy, "flow"); flow != "" {
		params.Set("flow", flow)
	}
	return buildClashURI("vless", uuid, server, port, params, name)
}

func clashTrojanShareLink(proxy map[string]any, name string) string {
	server, port, ok := clashServerPort(proxy)
	password := clashString(proxy, "password")
	if !ok || password == "" {
		return ""
	}
	params := url.Values{}
	applyClashShareParams(proxy, params)
	return buildClashURI("trojan", password, server, port, params, name)
}

func clashSSShareLink(proxy map[string]any, name string) string {
	server, port, ok := clashServerPort(proxy)
	cipher := clashString(proxy, "cipher")
	password := clashString(proxy, "password")
	if !ok || cipher == "" || password == "" {
		return ""
	}
	userInfo := base64.RawURLEncoding.EncodeToString([]byte(cipher + ":" + password))
	params := url.Values{}
	if plugin := clashString(proxy, "plugin"); plugin != "" {
		pluginValue := plugin
		if opts := clashString(proxy, "plugin-opts"); opts != "" {
			pluginValue += ";" + opts
		}
		params.Set("plugin", pluginValue)
	}
	return buildClashURI("ss", userInfo, server, port, params, name)
}

func clashHysteria2ShareLink(proxy map[string]any, name string) string {
	server, port, ok := clashServerPort(proxy)
	password := firstNonEmpty(clashString(proxy, "password"), clashString(proxy, "auth"))
	if !ok || password == "" {
		return ""
	}
	params := url.Values{}
	params.Set("security", "tls")
	if sni := firstNonEmpty(clashString(proxy, "sni"), clashString(proxy, "servername"), clashString(proxy, "server-name")); sni != "" {
		params.Set("sni", sni)
	}
	if alpn := clashStringList(proxy, "alpn"); len(alpn) > 0 {
		params.Set("alpn", strings.Join(alpn, ","))
	}
	if clashBool(proxy, "skip-cert-verify") || clashBool(proxy, "allow-insecure") || clashBool(proxy, "insecure") {
		params.Set("insecure", "1")
	}
	if obfs := clashString(proxy, "obfs"); obfs != "" {
		params.Set("obfs", obfs)
	}
	if obfsPassword := firstNonEmpty(clashString(proxy, "obfs-password"), clashString(proxy, "obfs_password")); obfsPassword != "" {
		params.Set("obfs-password", obfsPassword)
	}
	if pin := firstNonEmpty(clashString(proxy, "pinSHA256"), clashString(proxy, "pin-sha256")); pin != "" {
		params.Set("pinSHA256", pin)
	}
	if up := clashString(proxy, "up"); up != "" {
		params.Set("up", up)
	}
	if down := clashString(proxy, "down"); down != "" {
		params.Set("down", down)
	}
	if congestion := clashString(proxy, "congestion"); congestion != "" {
		params.Set("congestion", congestion)
	}
	return buildClashURI("hysteria2", password, server, port, params, name)
}

func applyClashShareParams(proxy map[string]any, params url.Values) {
	network := normalizeClashNetwork(clashString(proxy, "network"), "tcp")
	params.Set("type", network)
	if clashTLSEnabled(proxy) {
		params.Set("security", "tls")
	} else {
		params.Set("security", "none")
	}
	applyClashShareTLS(proxy, params)
	applyClashShareTransport(proxy, network, params)
}

func applyClashShareTLS(proxy map[string]any, params url.Values) {
	if sni := firstNonEmpty(clashString(proxy, "servername"), clashString(proxy, "sni"), clashString(proxy, "server-name")); sni != "" {
		params.Set("sni", sni)
	}
	if fingerprint := firstNonEmpty(clashString(proxy, "client-fingerprint"), clashString(proxy, "fingerprint"), clashString(proxy, "fp")); fingerprint != "" {
		params.Set("fp", fingerprint)
	}
	if alpn := clashStringList(proxy, "alpn"); len(alpn) > 0 {
		params.Set("alpn", strings.Join(alpn, ","))
	}
	if clashBool(proxy, "skip-cert-verify") || clashBool(proxy, "allow-insecure") {
		params.Set("allowInsecure", "1")
	}
}

func applyClashShareTransport(proxy map[string]any, network string, params url.Values) {
	switch network {
	case "ws":
		ws := clashMap(proxy, "ws-opts")
		if path := firstNonEmpty(clashStringFromMap(ws, "path"), clashString(proxy, "path")); path != "" {
			params.Set("path", path)
		}
		if host := clashWSHost(proxy, ws); host != "" {
			params.Set("host", host)
		}
	case "grpc":
		grpc := clashMap(proxy, "grpc-opts")
		if serviceName := firstNonEmpty(
			clashStringFromMap(grpc, "grpc-service-name"),
			clashStringFromMap(grpc, "serviceName"),
			clashStringFromMap(grpc, "service-name"),
			clashString(proxy, "grpc-service-name"),
		); serviceName != "" {
			params.Set("serviceName", serviceName)
		}
		if mode := clashStringFromMap(grpc, "mode"); mode != "" {
			params.Set("mode", mode)
		}
	case "http", "h2":
		httpOpts := clashMap(proxy, "h2-opts")
		if len(httpOpts) == 0 {
			httpOpts = clashMap(proxy, "http-opts")
		}
		if path := firstNonEmpty(clashFirstStringFromMap(httpOpts, "path"), clashString(proxy, "path")); path != "" {
			params.Set("path", path)
		}
		if host := firstNonEmpty(clashFirstStringFromMap(httpOpts, "host"), clashString(proxy, "host")); host != "" {
			params.Set("host", host)
		}
	}
}

func applyClashVMessTransport(proxy map[string]any, network string, obj map[string]any) {
	switch network {
	case "ws":
		ws := clashMap(proxy, "ws-opts")
		if path := firstNonEmpty(clashStringFromMap(ws, "path"), clashString(proxy, "path")); path != "" {
			obj["path"] = path
		}
		if host := clashWSHost(proxy, ws); host != "" {
			obj["host"] = host
		}
	case "grpc":
		grpc := clashMap(proxy, "grpc-opts")
		if serviceName := firstNonEmpty(
			clashStringFromMap(grpc, "grpc-service-name"),
			clashStringFromMap(grpc, "serviceName"),
			clashStringFromMap(grpc, "service-name"),
			clashString(proxy, "grpc-service-name"),
		); serviceName != "" {
			obj["path"] = serviceName
		}
	case "http", "h2":
		httpOpts := clashMap(proxy, "h2-opts")
		if len(httpOpts) == 0 {
			httpOpts = clashMap(proxy, "http-opts")
		}
		if path := firstNonEmpty(clashFirstStringFromMap(httpOpts, "path"), clashString(proxy, "path")); path != "" {
			obj["path"] = path
		}
		if host := firstNonEmpty(clashFirstStringFromMap(httpOpts, "host"), clashString(proxy, "host")); host != "" {
			obj["host"] = host
		}
	}
}

func applyClashVMessTLS(proxy map[string]any, obj map[string]any) {
	if sni := firstNonEmpty(clashString(proxy, "servername"), clashString(proxy, "sni"), clashString(proxy, "server-name")); sni != "" {
		obj["sni"] = sni
	}
	if fingerprint := firstNonEmpty(clashString(proxy, "client-fingerprint"), clashString(proxy, "fingerprint"), clashString(proxy, "fp")); fingerprint != "" {
		obj["fp"] = fingerprint
	}
	if alpn := clashStringList(proxy, "alpn"); len(alpn) > 0 {
		obj["alpn"] = strings.Join(alpn, ",")
	}
	if clashBool(proxy, "skip-cert-verify") || clashBool(proxy, "allow-insecure") {
		obj["allowInsecure"] = "1"
	}
}

func buildClashURI(scheme, userInfo, server string, port int, params url.Values, name string) string {
	query := params.Encode()
	if query != "" {
		query = "?" + query
	}
	fragment := ""
	if strings.TrimSpace(name) != "" {
		fragment = "#" + url.QueryEscape(strings.TrimSpace(name))
	}
	return fmt.Sprintf("%s://%s@%s%s%s", scheme, url.User(userInfo).String(), formatClashHostPort(server, port), query, fragment)
}

func clashServerPort(proxy map[string]any) (string, int, bool) {
	server := firstNonEmpty(clashString(proxy, "server"), clashString(proxy, "add"), clashString(proxy, "address"))
	port := clashInt(proxy, "port")
	return server, port, server != "" && port > 0
}

func normalizeClashNetwork(network, fallback string) string {
	network = strings.ToLower(strings.TrimSpace(network))
	if network == "" {
		return fallback
	}
	if network == "h2" {
		return "http"
	}
	return network
}

func vmessNetworkName(network string) string {
	if network == "http" {
		return "h2"
	}
	return network
}

func clashTLSEnabled(proxy map[string]any) bool {
	return clashBool(proxy, "tls") || strings.EqualFold(clashString(proxy, "security"), "tls")
}

func clashWSHost(proxy map[string]any, ws map[string]any) string {
	if headers := clashMapFromMap(ws, "headers"); len(headers) > 0 {
		if host := firstNonEmpty(clashStringFromMap(headers, "Host"), clashStringFromMap(headers, "host")); host != "" {
			return host
		}
	}
	if headers := clashMap(proxy, "headers"); len(headers) > 0 {
		if host := firstNonEmpty(clashStringFromMap(headers, "Host"), clashStringFromMap(headers, "host")); host != "" {
			return host
		}
	}
	return firstNonEmpty(clashString(proxy, "host"), clashString(proxy, "servername"), clashString(proxy, "sni"))
}

func firstClashValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func clashString(values map[string]any, key string) string {
	return clashStringAny(values[key])
}

func clashStringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	return clashStringAny(values[key])
}

func clashStringAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func clashInt(values map[string]any, key string) int {
	return clashIntAny(values[key])
}

func clashIntAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint:
		return int(v)
	case uint64:
		return int(v)
	case uint32:
		return int(v)
	case float64:
		return int(v)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(v))
		return parsed
	default:
		return 0
	}
}

func clashBool(values map[string]any, key string) bool {
	switch v := values[key].(type) {
	case bool:
		return v
	case string:
		v = strings.ToLower(strings.TrimSpace(v))
		return v == "true" || v == "1" || v == "yes" || v == "tls"
	case int:
		return v != 0
	case float64:
		return v != 0
	default:
		return false
	}
}

func clashMap(values map[string]any, key string) map[string]any {
	return clashMapAny(values[key])
}

func clashMapFromMap(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	return clashMapAny(values[key])
}

func clashMapAny(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return nil
}

func clashStringList(values map[string]any, key string) []string {
	switch v := values[key].(type) {
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if text := clashStringAny(item); text != "" {
				result = append(result, text)
			}
		}
		return result
	case []string:
		return v
	case string:
		return splitNonEmpty(v, ",")
	default:
		return nil
	}
}

func clashFirstStringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	switch v := values[key].(type) {
	case []any:
		for _, item := range v {
			if text := clashStringAny(item); text != "" {
				return text
			}
		}
	case []string:
		for _, item := range v {
			if text := strings.TrimSpace(item); text != "" {
				return text
			}
		}
	default:
		return clashStringAny(v)
	}
	return ""
}

func splitNonEmpty(value, sep string) []string {
	parts := strings.Split(value, sep)
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func formatClashHostPort(host string, port int) string {
	host = strings.TrimSpace(host)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%d", host, port)
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
		if name := vmessURIName(link); name != "" {
			return name
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

func vmessURIName(link string) string {
	payload := strings.TrimSpace(link[len("vmess://"):])
	if basePayload, rest, ok := strings.Cut(payload, "?"); ok {
		payload = basePayload
		queryText, fragment, _ := strings.Cut(rest, "#")
		if name := subscriptionQueryName(queryText); name != "" {
			return name
		}
		if name := unescapeSubscriptionName(fragment); name != "" {
			return name
		}
	} else if basePayload, fragment, ok := strings.Cut(payload, "#"); ok {
		payload = basePayload
		if name := unescapeSubscriptionName(fragment); name != "" {
			return name
		}
	}
	if decoded, ok := decodeBase64Any(payload); ok {
		if strings.HasPrefix(strings.TrimSpace(decoded), "{") {
			var data map[string]any
			if err := json.Unmarshal([]byte(decoded), &data); err == nil {
				if name, _ := data["ps"].(string); strings.TrimSpace(name) != "" {
					return strings.TrimSpace(name)
				}
			}
		} else if _, address, ok := strings.Cut(strings.TrimSpace(decoded), "@"); ok {
			return strings.TrimSpace(address)
		}
	}
	return ""
}

func subscriptionQueryName(queryText string) string {
	query, err := url.ParseQuery(queryText)
	if err != nil {
		return ""
	}
	for _, key := range []string{"remarks", "remark", "ps", "name"} {
		if name := unescapeSubscriptionName(query.Get(key)); name != "" {
			return name
		}
	}
	return ""
}

func unescapeSubscriptionName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(value); err == nil {
		value = decoded
	}
	return strings.TrimSpace(value)
}

func isSubscriptionInfoNodeName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	infoPrefixes := []string{
		"订阅到期",
		"过期时间",
		"subscription expiry",
		"subscription expire",
		"剩余流量",
		"剩余容量",
		"流量剩余",
		"过期时间",
		"到期时间",
		"套餐到期",
		"expire",
		"expired",
		"remaining traffic",
		"traffic remaining",
		"remaining data",
	}
	for _, prefix := range infoPrefixes {
		if strings.HasPrefix(name, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
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
	link := node.Link
	if node.SourceType == "clash" {
		link = ""
	}
	payload := fmt.Sprintf("%d\n%s\n%s\n%s", upstreamID, node.SourceType, link, node.Clash)
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:])
}

func upstreamNodeIdentityKey(upstreamID int, sourceType, protocol, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return fmt.Sprintf("%d\n%s\n%s\n%s",
		upstreamID,
		strings.ToLower(strings.TrimSpace(sourceType)),
		strings.ToLower(strings.TrimSpace(protocol)),
		strings.ToLower(name),
	)
}

func transferNodeGrants(tx *gorm.DB, replacements map[int]int) error {
	for oldID, newID := range replacements {
		if oldID <= 0 || newID <= 0 || oldID == newID {
			continue
		}
		if err := tx.Model(&model.InboundSubscriptionNode{}).
			Where("node_id = ?", oldID).
			Update("node_id", newID).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.UpstreamNodeConfigNode{}).
			Where("node_id = ?", oldID).
			Update("node_id", newID).Error; err != nil {
			return err
		}
	}
	return nil
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

func normalizeNodeTag(tag string) string {
	tag = strings.TrimSpace(tag)
	tag = strings.Trim(tag, "#,，;；")
	if len([]rune(tag)) > 32 {
		runes := []rune(tag)
		tag = string(runes[:32])
	}
	return tag
}

func decodeNodeTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return normalizeNodeTags(parsed)
	}
	return normalizeNodeTags(strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\t'
	}))
}

func encodeNodeTags(tags []string) string {
	tags = normalizeNodeTags(tags)
	if len(tags) == 0 {
		return ""
	}
	data, err := json.Marshal(tags)
	if err != nil {
		return ""
	}
	return string(data)
}

func normalizeNodeTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = normalizeNodeTag(tag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, tag)
		if len(result) >= 24 {
			break
		}
	}
	return result
}

func mapGormNotFound(err error, replacement error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return replacement
	}
	return err
}
