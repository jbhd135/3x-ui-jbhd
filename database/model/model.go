// Package model defines the database models and data structures used by the 3x-ui panel.
package model

import (
	"fmt"

	"github.com/mhsanaei/3x-ui/v2/util/json_util"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// Protocol represents the protocol type for Xray inbounds.
type Protocol string

// Protocol constants for different Xray inbound protocols
const (
	VMESS       Protocol = "vmess"
	VLESS       Protocol = "vless"
	Tunnel      Protocol = "tunnel"
	HTTP        Protocol = "http"
	Trojan      Protocol = "trojan"
	Shadowsocks Protocol = "shadowsocks"
	Mixed       Protocol = "mixed"
	WireGuard   Protocol = "wireguard"
	// UI stores Hysteria v1 and v2 both as "hysteria" and uses
	// settings.version to discriminate. Imports from outside the panel
	// can carry the literal "hysteria2" string, so IsHysteria below
	// accepts both.
	Hysteria  Protocol = "hysteria"
	Hysteria2 Protocol = "hysteria2"
)

// IsHysteria returns true for both "hysteria" and "hysteria2".
// Use instead of a bare ==model.Hysteria check: a v2 inbound stored
// with the literal v2 string would otherwise fall through (#4081).
func IsHysteria(p Protocol) bool {
	return p == Hysteria || p == Hysteria2
}

// User represents a user account in the 3x-ui panel.
type User struct {
	Id       int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Inbound represents an Xray inbound configuration with traffic statistics and settings.
type Inbound struct {
	Id                      int                  `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`                                                     // Unique identifier
	UserId                  int                  `json:"-"`                                                                                                // Associated user ID
	Up                      int64                `json:"up" form:"up"`                                                                                     // Upload traffic in bytes
	Down                    int64                `json:"down" form:"down"`                                                                                 // Download traffic in bytes
	Total                   int64                `json:"total" form:"total"`                                                                               // Total traffic limit in bytes
	AllTime                 int64                `json:"allTime" form:"allTime" gorm:"default:0"`                                                          // All-time traffic usage
	Remark                  string               `json:"remark" form:"remark"`                                                                             // Human-readable remark
	Enable                  bool                 `json:"enable" form:"enable" gorm:"index:idx_enable_traffic_reset,priority:1"`                            // Whether the inbound is enabled
	ExpiryTime              int64                `json:"expiryTime" form:"expiryTime"`                                                                     // Expiration timestamp
	DeviceLimit             int                  `json:"deviceLimit" form:"deviceLimit" gorm:"column:device_limit;default:0"`                              // Maximum active client IPs per inbound; 0 means unlimited
	EmergencyEnable         bool                 `json:"emergencyEnable" form:"emergencyEnable" gorm:"column:emergency_enable;default:false"`              // Whether upstream relay nodes are enabled for this inbound
	TrafficReset            string               `json:"trafficReset" form:"trafficReset" gorm:"default:never;index:idx_enable_traffic_reset,priority:2"`  // Traffic reset schedule
	LastTrafficResetTime    int64                `json:"lastTrafficResetTime" form:"lastTrafficResetTime" gorm:"default:0"`                                // Last traffic reset timestamp
	DailyTrafficBlockedDate string               `json:"dailyTrafficBlockedDate" gorm:"column:daily_traffic_blocked_date;default:''"`                      // Date when the inbound was auto-disabled by the daily client limit
	DailyTrafficLimit       int64                `json:"dailyTrafficLimit" form:"dailyTrafficLimit" gorm:"column:daily_traffic_limit;default:10737418240"` // Per-client daily traffic allowance in bytes; 0 means unlimited
	TodayTraffic            int64                `json:"todayTraffic" gorm:"-"`                                                                            // Aggregate upload and download traffic for the current panel-local day
	SocksProxyEnabled       bool                 `json:"socksProxyEnabled" form:"socksProxyEnabled" gorm:"column:socks_proxy_enabled;default:false"`       // Route this inbound through a dedicated SOCKS5 outbound
	SocksProxyHost          string               `json:"socksProxyHost" form:"socksProxyHost" gorm:"column:socks_proxy_host"`                              // SOCKS5 outbound server address
	SocksProxyPort          int                  `json:"socksProxyPort" form:"socksProxyPort" gorm:"column:socks_proxy_port;default:0"`                    // SOCKS5 outbound server port
	SocksProxyUsername      string               `json:"socksProxyUsername" form:"socksProxyUsername" gorm:"column:socks_proxy_username"`                  // SOCKS5 outbound username
	SocksProxyPassword      string               `json:"socksProxyPassword" form:"socksProxyPassword" gorm:"column:socks_proxy_password"`                  // SOCKS5 outbound password
	ClientStats             []xray.ClientTraffic `gorm:"foreignKey:InboundId;references:Id" json:"clientStats" form:"clientStats"`                         // Client traffic statistics

	// Xray configuration fields
	Listen         string   `json:"listen" form:"listen"`
	Port           int      `json:"port" form:"port"`
	Protocol       Protocol `json:"protocol" form:"protocol"`
	Settings       string   `json:"settings" form:"settings"`
	StreamSettings string   `json:"streamSettings" form:"streamSettings"`
	Tag            string   `json:"tag" form:"tag" gorm:"unique"`
	Sniffing       string   `json:"sniffing" form:"sniffing"`
}

// OutboundTraffics tracks traffic statistics for Xray outbound connections.
type OutboundTraffics struct {
	Id    int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Tag   string `json:"tag" form:"tag" gorm:"unique"`
	Up    int64  `json:"up" form:"up" gorm:"default:0"`
	Down  int64  `json:"down" form:"down" gorm:"default:0"`
	Total int64  `json:"total" form:"total" gorm:"default:0"`
}

// InboundClientIps stores IP addresses associated with inbound clients for access control.
type InboundClientIps struct {
	Id          int    `json:"id" gorm:"primaryKey;autoIncrement"`
	ClientEmail string `json:"clientEmail" form:"clientEmail" gorm:"unique"`
	Ips         string `json:"ips" form:"ips"`
}

// DailyClientTraffic stores per-day traffic deltas for dashboard monitoring.
type DailyClientTraffic struct {
	Id          int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Date        string `json:"date" gorm:"uniqueIndex:idx_daily_client_traffic,priority:1;size:10"`
	InboundId   int    `json:"inboundId" gorm:"uniqueIndex:idx_daily_client_traffic,priority:2;column:inbound_id;index"`
	ClientEmail string `json:"clientEmail" gorm:"uniqueIndex:idx_daily_client_traffic,priority:3;column:client_email;index"`
	Up          int64  `json:"up" gorm:"default:0"`
	Down        int64  `json:"down" gorm:"default:0"`
	CreatedAt   int64  `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt   int64  `json:"updatedAt" gorm:"autoUpdateTime;column:updated_at"`
}

// HistoryOfSeeders tracks which database seeders have been executed to prevent re-running.
type HistoryOfSeeders struct {
	Id         int    `json:"id" gorm:"primaryKey;autoIncrement"`
	SeederName string `json:"seederName"`
}

// GenXrayInboundConfig generates an Xray inbound configuration from the Inbound model.
func (i *Inbound) GenXrayInboundConfig() *xray.InboundConfig {
	listen := i.Listen
	// Default to 0.0.0.0 (all interfaces) when listen is empty
	// This ensures proper dual-stack IPv4/IPv6 binding in systems where bindv6only=0
	if listen == "" {
		listen = "0.0.0.0"
	}
	listen = fmt.Sprintf("\"%v\"", listen)
	return &xray.InboundConfig{
		Listen:         json_util.RawMessage(listen),
		Port:           i.Port,
		Protocol:       string(i.Protocol),
		Settings:       json_util.RawMessage(i.Settings),
		StreamSettings: json_util.RawMessage(i.StreamSettings),
		Tag:            i.Tag,
		Sniffing:       json_util.RawMessage(i.Sniffing),
	}
}

// Setting stores key-value configuration settings for the 3x-ui panel.
type Setting struct {
	Id    int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Key   string `json:"key" form:"key"`
	Value string `json:"value" form:"value"`
}

type CustomGeoResource struct {
	Id            int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Type          string `json:"type" gorm:"not null;uniqueIndex:idx_custom_geo_type_alias;column:geo_type"`
	Alias         string `json:"alias" gorm:"not null;uniqueIndex:idx_custom_geo_type_alias"`
	Url           string `json:"url" gorm:"not null"`
	LocalPath     string `json:"localPath" gorm:"column:local_path"`
	LastUpdatedAt int64  `json:"lastUpdatedAt" gorm:"default:0;column:last_updated_at"`
	LastModified  string `json:"lastModified" gorm:"column:last_modified"`
	CreatedAt     int64  `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt     int64  `json:"updatedAt" gorm:"autoUpdateTime;column:updated_at"`
}

// UpstreamSubscription stores a remote provider subscription URL whose nodes
// can be filtered and re-published to downstream customers and inbounds.
type UpstreamSubscription struct {
	Id            int            `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Name          string         `json:"name" form:"name" gorm:"not null"`
	Url           string         `json:"url" form:"url" gorm:"not null"`
	Enable        bool           `json:"enable" form:"enable" gorm:"default:true"`
	Upload        int64          `json:"upload" form:"upload" gorm:"default:0"`
	Download      int64          `json:"download" form:"download" gorm:"default:0"`
	Total         int64          `json:"total" form:"total" gorm:"default:0"`
	ExpiryTime    int64          `json:"expiryTime" form:"expiryTime" gorm:"default:0"`
	LastFetchedAt int64          `json:"lastFetchedAt" form:"lastFetchedAt" gorm:"default:0;column:last_fetched_at"`
	LastError     string         `json:"lastError" form:"lastError" gorm:"column:last_error"`
	CreatedAt     int64          `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt     int64          `json:"updatedAt" gorm:"autoUpdateTime;column:updated_at"`
	Nodes         []UpstreamNode `json:"nodes" gorm:"foreignKey:UpstreamId;references:Id"`
}

// UpstreamNode is one parsed node from an upstream subscription.
type UpstreamNode struct {
	Id         int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	UpstreamId int    `json:"upstreamId" form:"upstreamId" gorm:"index;column:upstream_id"`
	Name       string `json:"name" form:"name"`
	Protocol   string `json:"protocol" form:"protocol" gorm:"index"`
	Link       string `json:"link" form:"link" gorm:"type:text"`
	Clash      string `json:"clash" form:"clash" gorm:"type:text"`
	SourceType string `json:"sourceType" form:"sourceType" gorm:"default:uri;column:source_type"`
	Tags       string `json:"tags" form:"tags" gorm:"type:text"`
	Hash       string `json:"hash" form:"hash" gorm:"uniqueIndex:idx_upstream_node_hash"`
	Enable     bool   `json:"enable" form:"enable" gorm:"default:true"`
	Emergency  bool   `json:"emergency" form:"emergency" gorm:"default:false"`
	RelayPort  int    `json:"relayPort" form:"relayPort" gorm:"index;column:relay_port;default:0"`
	RelayUp    int64  `json:"relayUp" form:"relayUp" gorm:"column:relay_up;default:0"`
	RelayDown  int64  `json:"relayDown" form:"relayDown" gorm:"column:relay_down;default:0"`
	Sort       int    `json:"sort" form:"sort" gorm:"default:0"`
	CreatedAt  int64  `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt  int64  `json:"updatedAt" gorm:"autoUpdateTime;column:updated_at"`
}

// UpstreamNodeConfig is a named reusable node selection under one upstream
// subscription. Inbounds can enable one or more of these configurations.
type UpstreamNodeConfig struct {
	Id         int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	UpstreamId int    `json:"upstreamId" form:"upstreamId" gorm:"index;column:upstream_id"`
	Name       string `json:"name" form:"name" gorm:"not null"`
	Sort       int    `json:"sort" form:"sort" gorm:"default:0"`
	CreatedAt  int64  `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt  int64  `json:"updatedAt" gorm:"autoUpdateTime;column:updated_at"`
}

// UpstreamNodeConfigNode grants one node to one named upstream node config.
type UpstreamNodeConfigNode struct {
	Id        int   `json:"id" gorm:"primaryKey;autoIncrement"`
	ConfigId  int   `json:"configId" form:"configId" gorm:"uniqueIndex:idx_upstream_node_config_node;column:config_id"`
	NodeId    int   `json:"nodeId" form:"nodeId" gorm:"uniqueIndex:idx_upstream_node_config_node;column:node_id"`
	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
}

// CustomerSubscription represents a downstream customer subscription token.
type CustomerSubscription struct {
	Id         int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Name       string `json:"name" form:"name" gorm:"not null"`
	Token      string `json:"token" form:"token" gorm:"uniqueIndex;not null"`
	Enable     bool   `json:"enable" form:"enable" gorm:"default:true"`
	ExpiryTime int64  `json:"expiryTime" form:"expiryTime" gorm:"default:0"`
	CreatedAt  int64  `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt  int64  `json:"updatedAt" gorm:"autoUpdateTime;column:updated_at"`
}

// CustomerSubscriptionNode grants one customer access to one upstream node.
type CustomerSubscriptionNode struct {
	Id         int   `json:"id" gorm:"primaryKey;autoIncrement"`
	CustomerId int   `json:"customerId" form:"customerId" gorm:"uniqueIndex:idx_customer_node;column:customer_id"`
	NodeId     int   `json:"nodeId" form:"nodeId" gorm:"uniqueIndex:idx_customer_node;column:node_id"`
	CreatedAt  int64 `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
}

// InboundSubscriptionNode grants every client subscription under one inbound
// access to one upstream node.
type InboundSubscriptionNode struct {
	Id        int   `json:"id" gorm:"primaryKey;autoIncrement"`
	InboundId int   `json:"inboundId" form:"inboundId" gorm:"uniqueIndex:idx_inbound_node;column:inbound_id"`
	NodeId    int   `json:"nodeId" form:"nodeId" gorm:"uniqueIndex:idx_inbound_node;column:node_id"`
	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
}

// InboundEmergencyUpstream limits which upstream subscriptions can contribute
// emergency nodes for one inbound when emergency mode is enabled.
type InboundEmergencyUpstream struct {
	Id         int   `json:"id" gorm:"primaryKey;autoIncrement"`
	InboundId  int   `json:"inboundId" form:"inboundId" gorm:"uniqueIndex:idx_inbound_emergency_upstream;column:inbound_id"`
	UpstreamId int   `json:"upstreamId" form:"upstreamId" gorm:"uniqueIndex:idx_inbound_emergency_upstream;column:upstream_id"`
	CreatedAt  int64 `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
}

// InboundUpstreamConfig enables a named upstream node configuration for one
// inbound when the inbound upstream switch is on.
type InboundUpstreamConfig struct {
	Id        int   `json:"id" gorm:"primaryKey;autoIncrement"`
	InboundId int   `json:"inboundId" form:"inboundId" gorm:"uniqueIndex:idx_inbound_upstream_config;column:inbound_id"`
	ConfigId  int   `json:"configId" form:"configId" gorm:"uniqueIndex:idx_inbound_upstream_config;column:config_id"`
	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
}

// InboundUpstreamRelay stores the per-inbound relay port and traffic counters
// for one upstream node.
type InboundUpstreamRelay struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	InboundId int    `json:"inboundId" form:"inboundId" gorm:"uniqueIndex:idx_inbound_upstream_relay;index;column:inbound_id"`
	NodeId    int    `json:"nodeId" form:"nodeId" gorm:"uniqueIndex:idx_inbound_upstream_relay;index;column:node_id"`
	RelayPort int    `json:"relayPort" form:"relayPort" gorm:"uniqueIndex;column:relay_port;default:0"`
	RelayUUID string `json:"relayUuid" form:"relayUuid" gorm:"column:relay_uuid"`
	Up        int64  `json:"up" form:"up" gorm:"default:0"`
	Down      int64  `json:"down" form:"down" gorm:"default:0"`
	AllTime   int64  `json:"allTime" form:"allTime" gorm:"column:all_time;default:0"`
	CreatedAt int64  `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt int64  `json:"updatedAt" gorm:"autoUpdateTime;column:updated_at"`
}

type ClientReverse struct {
	Tag string `json:"tag"`
}

// Client represents a client configuration for Xray inbounds with traffic limits and settings.
type Client struct {
	ID         string         `json:"id,omitempty"`                 // Unique client identifier
	Security   string         `json:"security"`                     // Security method (e.g., "auto", "aes-128-gcm")
	Password   string         `json:"password,omitempty"`           // Client password
	Flow       string         `json:"flow,omitempty"`               // Flow control (XTLS)
	Reverse    *ClientReverse `json:"reverse,omitempty"`            // VLESS simple reverse proxy settings
	Auth       string         `json:"auth,omitempty"`               // Auth password (Hysteria)
	Email      string         `json:"email"`                        // Client email identifier
	LimitIP    int            `json:"limitIp"`                      // IP limit for this client
	TotalGB    int64          `json:"totalGB" form:"totalGB"`       // Total traffic limit in GB
	ExpiryTime int64          `json:"expiryTime" form:"expiryTime"` // Expiration timestamp
	Enable     bool           `json:"enable" form:"enable"`         // Whether the client is enabled
	TgID       int64          `json:"tgId" form:"tgId"`             // Telegram user ID for notifications
	SubID      string         `json:"subId" form:"subId"`           // Subscription identifier
	Comment    string         `json:"comment" form:"comment"`       // Client comment
	Reset      int            `json:"reset" form:"reset"`           // Reset period in days
	CreatedAt  int64          `json:"created_at,omitempty"`         // Creation timestamp
	UpdatedAt  int64          `json:"updated_at,omitempty"`         // Last update timestamp
}
