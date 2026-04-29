package service

import (
	"encoding/json"
	"errors"
	"runtime"
	"sync"

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

// expandClientDevices takes a single parsed client object from settings.clients[]
// and returns the list of flat client entries to emit to xray.
//
// If the client carries a non-empty "devices" array, every device produces one
// emitted entry whose email is "<client.email>-<device.name>" and whose id is the
// device's own UUID; per-device flow / limitIp override the parent if non-zero.
// A device with enable==false is skipped.
//
// If devices is missing or empty, the original client object is returned as a
// single-entry slice unchanged (legacy single-device mode).
//
// Each returned map is a fresh copy and safe to mutate.
func expandClientDevices(c map[string]any) []map[string]any {
	parentEmail, _ := c["email"].(string)
	devicesRaw, _ := c["devices"].([]any)
	if len(devicesRaw) == 0 {
		// Legacy single-device mode: pass through (caller still strips extras).
		out := make(map[string]any, len(c))
		for k, v := range c {
			if k == "devices" {
				continue
			}
			out[k] = v
		}
		return []map[string]any{out}
	}

	parentFlow, _ := c["flow"].(string)
	parentLimitIP, _ := c["limitIp"].(float64)

	flat := make([]map[string]any, 0, len(devicesRaw))
	for _, dRaw := range devicesRaw {
		d, ok := dRaw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := d["name"].(string)
		id, _ := d["id"].(string)
		if name == "" || id == "" {
			continue
		}
		if enable, ok := d["enable"].(bool); ok && !enable {
			continue
		}
		entry := map[string]any{
			"email": parentEmail + "-" + name,
			"id":    id,
		}
		if flow, ok := d["flow"].(string); ok && flow != "" {
			entry["flow"] = flow
		} else if parentFlow != "" {
			entry["flow"] = parentFlow
		}
		if limitIP, ok := d["limitIp"].(float64); ok && limitIP > 0 {
			entry["limitIp"] = limitIP
		} else if parentLimitIP > 0 {
			entry["limitIp"] = parentLimitIP
		}
		// Carry over auth/password/security if the parent set them (Trojan/SS/etc.).
		for _, k := range []string{"password", "method", "auth", "security"} {
			if v, ok := c[k]; ok {
				if _, exists := entry[k]; !exists {
					entry[k] = v
				}
			}
		}
		flat = append(flat, entry)
	}
	return flat
}

// expandRoutingRules walks the parsed routing config and rewrites every
// rules[].user entry that references a parent client name (i.e. an entry that
// matches an Email in clientDevices) into the full list of derived device emails.
//
// Entries that don't match any parent name are left untouched, so device-level
// rules ("test-phone") and unrelated user entries continue to work.
//
// clientDevices maps parent client email -> ordered list of derived device emails
// ("test" -> ["test-pc","test-phone"]).
//
// The function is conservative: a missing/non-object routing config is returned
// as-is, and parsing failures fall through to leave the input unchanged.
func expandRoutingRules(routerCfg []byte, clientDevices map[string][]string) []byte {
	if len(routerCfg) == 0 || len(clientDevices) == 0 {
		return routerCfg
	}
	var router map[string]any
	if err := json.Unmarshal(routerCfg, &router); err != nil {
		return routerCfg
	}
	rulesRaw, ok := router["rules"].([]any)
	if !ok {
		return routerCfg
	}
	changed := false
	for i, ruleRaw := range rulesRaw {
		rule, ok := ruleRaw.(map[string]any)
		if !ok {
			continue
		}
		usersRaw, ok := rule["user"].([]any)
		if !ok {
			continue
		}
		expanded := make([]any, 0, len(usersRaw))
		ruleChanged := false
		for _, u := range usersRaw {
			s, ok := u.(string)
			if !ok {
				expanded = append(expanded, u)
				continue
			}
			if devices, exists := clientDevices[s]; exists {
				for _, d := range devices {
					expanded = append(expanded, d)
				}
				ruleChanged = true
				continue
			}
			expanded = append(expanded, s)
		}
		if ruleChanged {
			rule["user"] = expanded
			rulesRaw[i] = rule
			changed = true
		}
	}
	if !changed {
		return routerCfg
	}
	router["rules"] = rulesRaw
	out, err := json.Marshal(router)
	if err != nil {
		return routerCfg
	}
	return out
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

	s.inboundService.AddTraffic(nil, nil)

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	// clientDevices accumulates parent-email -> [device-email,...] for the
	// routing-rule expansion pass after all inbounds are processed.
	clientDevices := map[string][]string{}

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

				parentEmail, _ := c["email"].(string)

				// Master enable on the parent client gates every device under it.
				if manualEnable, ok := c["enable"].(bool); ok && !manualEnable {
					continue
				}

				// Expand into one entry per device (or pass-through for legacy
				// single-device clients).
				flatEntries := expandClientDevices(c)
				if len(flatEntries) > 1 || (len(flatEntries) == 1 && flatEntries[0]["email"] != parentEmail) {
					emails := make([]string, 0, len(flatEntries))
					for _, e := range flatEntries {
						if em, _ := e["email"].(string); em != "" {
							emails = append(emails, em)
						}
					}
					if len(emails) > 0 {
						clientDevices[parentEmail] = emails
					}
				}

				for _, entry := range flatEntries {
					email, _ := entry["email"].(string)

					// check users active or not via stats (per-device email)
					if enable, exists := enableMap[email]; exists && !enable {
						logger.Infof("Remove Inbound User %s due to expiration or traffic limit", email)
						continue
					}

					// clear client config for additional parameters
					for key := range entry {
						if key != "email" && key != "id" && key != "password" && key != "flow" && key != "method" && key != "auth" {
							delete(entry, key)
						}
						if flow, ok := entry["flow"].(string); ok && flow == "xtls-rprx-vision-udp443" {
							entry["flow"] = "xtls-rprx-vision"
						}
					}
					final_clients = append(final_clients, any(entry))
				}
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
	}

	// Expand routing rules: any rules[].user that names a parent client
	// (e.g. "test") is rewritten to the full list of derived device emails
	// ("test-pc","test-phone",...). Device-level entries pass through unchanged.
	if expanded := expandRoutingRules(xrayConfig.RouterConfig, clientDevices); len(expanded) > 0 {
		xrayConfig.RouterConfig = expanded
	}

	return xrayConfig, nil
}

// GetXrayTraffic fetches the current traffic statistics from the running Xray process.
func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, []*xray.ClientTraffic, error) {
	if !s.IsXrayRunning() {
		err := errors.New("xray is not running")
		logger.Debug("Attempted to fetch Xray traffic, but Xray is not running:", err)
		return nil, nil, err
	}
	apiPort := p.GetAPIPort()
	s.xrayAPI.Init(apiPort)
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
