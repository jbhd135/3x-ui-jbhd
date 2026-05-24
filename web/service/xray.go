package service

import (
	"encoding/json"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"go.uber.org/atomic"
)

var (
	p                 *xray.Process
	lock              sync.Mutex
	isNeedXrayRestart atomic.Bool // Indicates that restart was requested for Xray
	isManuallyStopped atomic.Bool // Indicates that Xray was stopped manually from the panel
	result            string
)

// XrayService provides business logic for Xray process management.
// It handles starting, stopping, restarting Xray, and managing its configuration.
type XrayService struct {
	inboundService InboundService
	settingService SettingService
	xrayAPI        xray.XrayAPI
}

// IsXrayRunning checks if the Xray process is currently running.
func (s *XrayService) IsXrayRunning() bool {
	return p != nil && p.IsRunning()
}

// GetXrayErr returns the error from the Xray process, if any.
func (s *XrayService) GetXrayErr() error {
	if p == nil {
		return nil
	}

	err := p.GetErr()
	if err == nil {
		return nil
	}

	if runtime.GOOS == "windows" && err.Error() == "exit status 1" {
		// exit status 1 on Windows means that Xray process was killed
		// as we kill process to stop in on Windows, this is not an error
		return nil
	}

	return err
}

// GetXrayResult returns the result string from the Xray process.
func (s *XrayService) GetXrayResult() string {
	if result != "" {
		return result
	}
	if s.IsXrayRunning() {
		return ""
	}
	if p == nil {
		return ""
	}

	result = p.GetResult()

	if runtime.GOOS == "windows" && result == "exit status 1" {
		// exit status 1 on Windows means that Xray process was killed
		// as we kill process to stop in on Windows, this is not an error
		return ""
	}

	return result
}

// GetXrayVersion returns the version of the running Xray process.
func (s *XrayService) GetXrayVersion() string {
	if p == nil {
		return "Unknown"
	}
	return p.GetVersion()
}

// RemoveIndex removes an element at the specified index from a slice.
// Returns a new slice with the element removed.
func RemoveIndex(s []any, index int) []any {
	return append(s[:index], s[index+1:]...)
}

// GetXrayConfig retrieves and builds the Xray configuration from settings and inbounds.
func (s *XrayService) GetXrayConfig() (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	err = json.Unmarshal([]byte(templateConfig), xrayConfig)
	if err != nil {
		return nil, err
	}

	_, _, _ = s.inboundService.AddTraffic(nil, nil)

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		// get settings clients
		settings := map[string]any{}
		json.Unmarshal([]byte(inbound.Settings), &settings)
		clients, ok := settings["clients"].([]any)
		if ok {
			// Fast O(N) lookup map for client traffic enablement
			clientStats := inbound.ClientStats
			enableMap := make(map[string]bool, len(clientStats))
			for _, clientTraffic := range clientStats {
				enableMap[clientTraffic.Email] = clientTraffic.Enable
			}

			// filter and clean clients
			var final_clients []any
			for _, client := range clients {
				c, ok := client.(map[string]any)
				if !ok {
					continue
				}

				email, _ := c["email"].(string)

				// check users active or not via stats
				if enable, exists := enableMap[email]; exists && !enable {
					logger.Infof("Remove Inbound User %s due to expiration or traffic limit", email)
					continue
				}

				// check manual disabled flag
				if manualEnable, ok := c["enable"].(bool); ok && !manualEnable {
					continue
				}

				// clear client config for additional parameters
				for key := range c {
					if key != "email" && key != "id" && key != "password" && key != "flow" && key != "method" && key != "auth" {
						delete(c, key)
					}
					if flow, ok := c["flow"].(string); ok && flow == "xtls-rprx-vision-udp443" {
						c["flow"] = "xtls-rprx-vision"
					}
				}
				final_clients = append(final_clients, any(c))
			}

			settings["clients"] = final_clients
			modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
			if err != nil {
				return nil, err
			}

			inbound.Settings = string(modifiedSettings)
		}

		if len(inbound.StreamSettings) > 0 {
			// Unmarshal stream JSON
			var stream map[string]any
			json.Unmarshal([]byte(inbound.StreamSettings), &stream)

			// Remove the "settings" field under "tlsSettings" and "realitySettings"
			tlsSettings, ok1 := stream["tlsSettings"].(map[string]any)
			realitySettings, ok2 := stream["realitySettings"].(map[string]any)
			if ok1 || ok2 {
				if ok1 {
					delete(tlsSettings, "settings")
				} else if ok2 {
					delete(realitySettings, "settings")
				}
			}

			delete(stream, "externalProxy")

			newStream, err := json.MarshalIndent(stream, "", "  ")
			if err != nil {
				return nil, err
			}
			inbound.StreamSettings = string(newStream)
		}

		inboundConfig := inbound.GenXrayInboundConfig()
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
		if err := applyInboundSocksProxy(xrayConfig, inbound); err != nil {
			return nil, err
		}
	}
	return xrayConfig, nil
}

func applyInboundSocksProxy(xrayConfig *xray.Config, inbound *model.Inbound) error {
	if xrayConfig == nil || inbound == nil || !inbound.SocksProxyEnabled {
		return nil
	}
	if strings.TrimSpace(inbound.SocksProxyHost) == "" || inbound.SocksProxyPort <= 0 {
		return nil
	}

	outboundTag := inboundSocksProxyTag(inbound)
	outbound := map[string]any{
		"tag":      outboundTag,
		"protocol": "socks",
		"settings": map[string]any{
			"servers": []any{
				buildSocksProxyServer(inbound),
			},
		},
	}

	var outbounds []any
	rawOutbounds := strings.TrimSpace(string(xrayConfig.OutboundConfigs))
	if rawOutbounds != "" && rawOutbounds != "null" {
		if err := json.Unmarshal(xrayConfig.OutboundConfigs, &outbounds); err != nil {
			return err
		}
	}
	outbounds = append(outbounds, outbound)
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
	routing["rules"] = prependAfterAPIRule(rules, buildSocksProxyRoutingRules(inbound.Tag, outboundTag)...)
	routingBytes, err := json.MarshalIndent(routing, "", "  ")
	if err != nil {
		return err
	}
	xrayConfig.RouterConfig = routingBytes
	return nil
}

func buildSocksProxyServer(inbound *model.Inbound) map[string]any {
	server := map[string]any{
		"address": strings.TrimSpace(inbound.SocksProxyHost),
		"port":    inbound.SocksProxyPort,
	}
	user := strings.TrimSpace(inbound.SocksProxyUsername)
	pass := inbound.SocksProxyPassword
	if user != "" || pass != "" {
		server["users"] = []any{
			map[string]any{
				"user": user,
				"pass": pass,
			},
		}
	}
	return server
}

func buildSocksProxyRoutingRules(inboundTag string, socksOutboundTag string) []map[string]any {
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
		{
			"type":        "field",
			"inboundTag":  []string{inboundTag},
			"outboundTag": socksOutboundTag,
		},
	}
}

func inboundSocksProxyTag(inbound *model.Inbound) string {
	if inbound.Id > 0 {
		return "xui-socks-inbound-" + strconv.Itoa(inbound.Id)
	}
	return "xui-socks-" + inbound.Tag
}

func prependAfterAPIRule(rules []any, insertedRules ...map[string]any) []any {
	newRules := make([]any, 0, len(rules)+len(insertedRules))
	for _, rule := range insertedRules {
		if rule != nil {
			newRules = append(newRules, rule)
		}
	}
	if len(rules) == 0 {
		return newRules
	}
	if isAPIRoutingRule(rules[0]) {
		apiRule := rules[0]
		newRules = append([]any{apiRule}, newRules...)
		return append(newRules, rules[1:]...)
	}
	return append(newRules, rules...)
}

func isAPIRoutingRule(rule any) bool {
	ruleMap, ok := rule.(map[string]any)
	if !ok {
		return false
	}
	return ruleMap["outboundTag"] == "api"
}

// GetXrayTraffic fetches the current traffic statistics from the running Xray process.
func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, []*xray.ClientTraffic, error) {
	if !s.IsXrayRunning() {
		err := errors.New("xray is not running")
		logger.Debug("Attempted to fetch Xray traffic, but Xray is not running:", err)
		return nil, nil, err
	}
	apiPort := p.GetAPIPort()
	if err := s.xrayAPI.Init(apiPort); err != nil {
		logger.Debug("Failed to initialize Xray API:", err)
		return nil, nil, err
	}
	defer s.xrayAPI.Close()

	traffic, clientTraffic, err := s.xrayAPI.GetTraffic(true)
	if err != nil {
		logger.Debug("Failed to fetch Xray traffic:", err)
		return nil, nil, err
	}
	return traffic, clientTraffic, nil
}

// RestartXray restarts the Xray process, optionally forcing a restart even if config unchanged.
func (s *XrayService) RestartXray(isForce bool) error {
	lock.Lock()
	defer lock.Unlock()
	logger.Debug("restart Xray, force:", isForce)
	isManuallyStopped.Store(false)

	xrayConfig, err := s.GetXrayConfig()
	if err != nil {
		return err
	}

	if s.IsXrayRunning() {
		if !isForce && p.GetConfig().Equals(xrayConfig) && !isNeedXrayRestart.Load() {
			logger.Debug("It does not need to restart Xray")
			return nil
		}
		p.Stop()
	}

	p = xray.NewProcess(xrayConfig)
	result = ""
	err = p.Start()
	if err != nil {
		return err
	}

	return nil
}

// StopXray stops the running Xray process.
func (s *XrayService) StopXray() error {
	lock.Lock()
	defer lock.Unlock()
	isManuallyStopped.Store(true)
	logger.Debug("Attempting to stop Xray...")
	if s.IsXrayRunning() {
		return p.Stop()
	}
	return errors.New("xray is not running")
}

// SetToNeedRestart marks that Xray needs to be restarted.
func (s *XrayService) SetToNeedRestart() {
	isNeedXrayRestart.Store(true)
}

// IsNeedRestartAndSetFalse checks if restart is needed and resets the flag to false.
func (s *XrayService) IsNeedRestartAndSetFalse() bool {
	return isNeedXrayRestart.CompareAndSwap(true, false)
}

// DidXrayCrash checks if Xray crashed by verifying it's not running and wasn't manually stopped.
func (s *XrayService) DidXrayCrash() bool {
	return !s.IsXrayRunning() && !isManuallyStopped.Load()
}
