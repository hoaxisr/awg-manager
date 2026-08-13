package wdtt

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *Service) serverConfigDirForID(serverID string) (string, ServerConfig, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return "", ServerConfig{}, err
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return "", ServerConfig{}, err
	}
	return cfgDir, inst.Config, nil
}

// ListServerDevices returns devices from passwords.json for WG or Raw tab.
func (s *Service) ListServerDevices(serverID, mode string) (ServerDevicesStatus, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != ServerDeviceModeWG && mode != ServerDeviceModeRaw {
		return ServerDevicesStatus{}, fmt.Errorf("mode должен быть wg или raw")
	}
	cfgDir, cfg, err := s.serverConfigDirForID(serverID)
	if err != nil {
		return ServerDevicesStatus{}, err
	}
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		return ServerDevicesStatus{}, err
	}
	devices := listServerDevices(doc, mode)
	st := s.serverProcs.get(serverID).Status()
	activeIDs := map[string]bool{}
	known := st.Running
	statsActive := 0
	if known {
		rawLogActive := parseRawActiveDevicesFromServerLog(st.Log)
		wgLogActive := parseWGActiveDevicesFromServerLog(st.Log)
		switch mode {
		case ServerDeviceModeRaw:
			activeIDs = rawLogActive
			statsActive = len(activeIDs)
		case ServerDeviceModeWG:
			activeIDs = wgLogActive
			if snap, ok := readServerStatsSnapshot(serverID); ok {
				statsActive = int(snap.Active)
				mergeActiveMaps(activeIDs, filterActiveIDsForMode(activeIDsFromStatsSnap(snap), doc, ServerDeviceModeWG))
			}
			if cfg.usesNDMSOpkgTun() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				ndmsActive := activeDevicesFromNDMSPeers(ctx, s.ndmsPeers, cfg.NdmsIface, doc)
				cancel()
				for id := range ndmsActive {
					if rawLogActive[id] && !wgLogActive[id] && !activeIDs[id] {
						continue
					}
					activeIDs[id] = true
				}
			}
		}
	}
	if statsActive == 0 && len(activeIDs) > 0 {
		statsActive = len(activeIDs)
	}
	devices = mergeDeviceActivity(devices, activeIDs, known)
	return ServerDevicesStatus{
		PasswordsJSONPath: passwordsJSONPath(cfgDir),
		Mode:              mode,
		ServerRunning:     st.Running,
		ActiveDeviceCount: countActiveDevices(devices),
		StatsActive:       statsActive,
		Devices:           devices,
	}, nil
}

type AddServerDeviceInput struct {
	DeviceID string `json:"deviceId"`
	IP       string `json:"ip,omitempty"`
	RawIP    string `json:"rawIp,omitempty"`
	Comment  string `json:"comment,omitempty"`
	Mode     string `json:"mode"`
}

// AddServerDevice pre-provisions a device row in passwords.json.
func (s *Service) AddServerDevice(serverID string, in AddServerDeviceInput) (ServerDevicesStatus, error) {
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	deviceID := strings.TrimSpace(in.DeviceID)
	if deviceID == "" {
		return ServerDevicesStatus{}, fmt.Errorf("device id не задан")
	}
	if deviceID == gatewayReservedDeviceID {
		return ServerDevicesStatus{}, fmt.Errorf("зарезервированный id")
	}
	if mode != ServerDeviceModeWG && mode != ServerDeviceModeRaw {
		return ServerDevicesStatus{}, fmt.Errorf("mode должен быть wg или raw")
	}

	cfgDir, _, err := s.serverConfigDirForID(serverID)
	if err != nil {
		return ServerDevicesStatus{}, err
	}
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		return ServerDevicesStatus{}, err
	}
	if _, exists := doc.Devices[deviceID]; exists {
		return ServerDevicesStatus{}, fmt.Errorf("устройство %s уже существует", deviceID)
	}

	ip := strings.TrimSpace(in.IP)
	rawIP := strings.TrimSpace(in.RawIP)
	switch mode {
	case ServerDeviceModeWG:
		if err := validateWGClientIP(ip, deviceID, doc.Devices); err != nil {
			return ServerDevicesStatus{}, err
		}
	case ServerDeviceModeRaw:
		if err := validateRawClientIP(rawIP, deviceID, doc.Devices); err != nil {
			return ServerDevicesStatus{}, err
		}
	}

	upsertPasswordsDevice(doc.Devices, deviceID, func(m map[string]any) {
		if ip != "" {
			m["ip"] = ip
		}
		if rawIP != "" {
			m["raw_ip"] = rawIP
		}
		if c := strings.TrimSpace(in.Comment); c != "" {
			m["comment"] = c
		}
	})
	if err := savePasswordsJSONDoc(cfgDir, doc); err != nil {
		return ServerDevicesStatus{}, err
	}
	s.notifyServerDevicesChanged(serverID)
	return s.ListServerDevices(serverID, mode)
}

type UpdateServerDeviceInput struct {
	IP      string `json:"ip,omitempty"`
	RawIP   string `json:"rawIp,omitempty"`
	Comment string `json:"comment,omitempty"`
	Mode    string `json:"mode"`
	Unbind  bool   `json:"unbind,omitempty"`
}

// UpdateServerDevice edits IP/comment or unbinds device from password.
func (s *Service) UpdateServerDevice(serverID, deviceID string, in UpdateServerDeviceInput) (ServerDevicesStatus, error) {
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return ServerDevicesStatus{}, fmt.Errorf("device id не задан")
	}
	if mode != ServerDeviceModeWG && mode != ServerDeviceModeRaw {
		return ServerDevicesStatus{}, fmt.Errorf("mode должен быть wg или raw")
	}

	cfgDir, _, err := s.serverConfigDirForID(serverID)
	if err != nil {
		return ServerDevicesStatus{}, err
	}
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		return ServerDevicesStatus{}, err
	}
	if _, exists := doc.Devices[deviceID]; !exists && !in.Unbind {
		return ServerDevicesStatus{}, fmt.Errorf("устройство %s не найдено", deviceID)
	}

	if in.Unbind {
		unbindPasswordsDevice(&doc, deviceID)
	} else {
		ip := strings.TrimSpace(in.IP)
		rawIP := strings.TrimSpace(in.RawIP)
		if mode == ServerDeviceModeWG && ip != "" {
			if err := validateWGClientIP(ip, deviceID, doc.Devices); err != nil {
				return ServerDevicesStatus{}, err
			}
		}
		if mode == ServerDeviceModeRaw && rawIP != "" {
			if err := validateRawClientIP(rawIP, deviceID, doc.Devices); err != nil {
				return ServerDevicesStatus{}, err
			}
		}
		upsertPasswordsDevice(doc.Devices, deviceID, func(m map[string]any) {
			if mode == ServerDeviceModeWG && ip != "" {
				m["ip"] = ip
			}
			if mode == ServerDeviceModeRaw && rawIP != "" {
				m["raw_ip"] = rawIP
			}
			if c := strings.TrimSpace(in.Comment); c != "" {
				m["comment"] = c
			}
		})
	}

	if err := savePasswordsJSONDoc(cfgDir, doc); err != nil {
		return ServerDevicesStatus{}, err
	}
	s.notifyServerDevicesChanged(serverID)
	return s.ListServerDevices(serverID, mode)
}

// RemoveServerDevice deletes device row and password bindings.
func (s *Service) RemoveServerDevice(serverID, deviceID, mode string) (ServerDevicesStatus, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return ServerDevicesStatus{}, fmt.Errorf("device id не задан")
	}
	if deviceID == gatewayReservedDeviceID {
		return ServerDevicesStatus{}, fmt.Errorf("нельзя удалить служебную запись")
	}

	cfgDir, _, err := s.serverConfigDirForID(serverID)
	if err != nil {
		return ServerDevicesStatus{}, err
	}
	doc, err := loadPasswordsJSON(cfgDir)
	if err != nil {
		return ServerDevicesStatus{}, err
	}
	removePasswordsDevice(&doc, deviceID)
	if err := savePasswordsJSONDoc(cfgDir, doc); err != nil {
		return ServerDevicesStatus{}, err
	}
	s.notifyServerDevicesChanged(serverID)
	return s.ListServerDevices(serverID, mode)
}

func (s *Service) notifyServerDevicesChanged(serverID string) {
	if !s.serverProcs.get(serverID).Status().Running {
		return
	}
	if err := s.reloadServerRuntimeConfig(serverID); err != nil && s.appLog != nil {
		s.appLog.Warn("panel", serverID, "перечитывание passwords.json: "+err.Error())
	}
}

func (s *Service) reloadServerRuntimeConfig(serverID string) error {
	st := s.serverProcs.get(serverID).Status()
	if !st.Running || st.PID <= 0 {
		return nil
	}
	if err := signalProcessHUP(st.PID); err == nil {
		return nil
	}
	return reloadServerPanelDB()
}
