package tgwebproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tproxyconfig "github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	tproxyserver "github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/server"
	tproxysession "github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/session"
)

const defaultTproxyDir = "/opt/etc/awg-manager/tproxy"

type tproxySpec struct {
	key          string
	configPath   string
	profilesPath string
}

type tproxyInstance struct {
	key          string
	configPath   string
	profilesPath string
	listenAddr   string
	adminAddr    string
	cfgBytes     []byte
	profBytes    []byte
	tokenBytes   []byte
	app          *tproxyserver.Server
	srv          *http.Server
	lis          net.Listener
	adminSrv     *http.Server
	adminLis     net.Listener
}

// TproxyEngine runs the Telegram Web Proxy HTTP/WebSocket relay in-process,
// supervising primary and optional multi-endpoint instances configured via
// config*.json and profiles*.json.
type TproxyEngine struct {
	mu        sync.Mutex
	dir       string
	lastCfg   Config
	hasCfg    bool
	instances map[string]*tproxyInstance
}

func NewTproxyEngine(dir string) *TproxyEngine {
	if strings.TrimSpace(dir) == "" {
		dir = defaultTproxyDir
	}
	return &TproxyEngine{
		dir:       dir,
		instances: make(map[string]*tproxyInstance),
	}
}

func (e *TproxyEngine) SetDir(dir string) {
	if e == nil || strings.TrimSpace(dir) == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dir = dir
}

func (e *TproxyEngine) getDirLocked() string {
	if strings.TrimSpace(e.dir) != "" {
		return e.dir
	}
	return defaultTproxyDir
}

// validateTproxyFiles validates candidate or runtime config.json + profiles.json
// in-process using the exact parser and budget constraints of tproxy.
func validateTproxyFiles(cfgPath, profPath string) error {
	var parsed tproxyconfig.Config
	var err error
	if profPath != "" && fileExists(profPath) {
		parsed, err = tproxyconfig.Load(cfgPath, profPath)
	} else {
		parsed, err = tproxyconfig.Load(cfgPath)
	}
	if err != nil {
		return fmt.Errorf("tproxy config check failed: %w", err)
	}
	if err := tproxysession.ValidateBudget(parsed); err != nil {
		return fmt.Errorf("tproxy budget check failed: %w", err)
	}
	if parsed.TokenKeyFile != "" && fileExists(parsed.TokenKeyFile) {
		if _, err := tproxyconfig.ReadTokenKey(parsed.TokenKeyFile); err != nil {
			return fmt.Errorf("tproxy token key check failed: %w", err)
		}
	}
	return nil
}

func (e *TproxyEngine) desiredSpecsLocked(cfg Config) []tproxySpec {
	dir := e.getDirLocked()
	defaultProfiles := filepath.Join(dir, "profiles.json")
	specs := []tproxySpec{
		{
			key:          "primary",
			configPath:   filepath.Join(dir, "config.json"),
			profilesPath: defaultProfiles,
		},
	}
	for i, ep := range cfg.GetEndpoints() {
		if i == 0 || ep.ID == "" || !ep.Enabled || ep.ExternalBackend {
			continue
		}
		epCfg := filepath.Join(dir, fmt.Sprintf("config_%s.json", ep.ID))
		if !fileExists(epCfg) {
			continue
		}
		epProf := filepath.Join(dir, fmt.Sprintf("profiles_%s.json", ep.ID))
		if !fileExists(epProf) {
			epProf = defaultProfiles
		}
		specs = append(specs, tproxySpec{
			key:          ep.ID,
			configPath:   epCfg,
			profilesPath: epProf,
		})
	}
	return specs
}

func (e *TproxyEngine) Apply(cfg Config) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applyLocked(cfg, false)
}

func (e *TproxyEngine) Restart() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.hasCfg {
		return nil
	}
	return e.applyLocked(e.lastCfg, true)
}

func (e *TproxyEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopAllLocked()
}

func (e *TproxyEngine) IsPrimaryRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	inst := e.instances["primary"]
	return inst != nil && inst.app != nil && inst.lis != nil
}

func (e *TproxyEngine) CheckPrimaryReady(ctx context.Context) error {
	e.mu.Lock()
	inst := e.instances["primary"]
	if inst == nil || inst.app == nil || inst.lis == nil {
		e.mu.Unlock()
		return fmt.Errorf("embedded tproxy primary listener is not running")
	}
	adminAddr := inst.adminAddr
	e.mu.Unlock()

	if adminAddr == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+adminAddr+"/metrics", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("tproxy admin metrics request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "tproxy_") {
		return fmt.Errorf("tproxy admin metrics returned unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (e *TproxyEngine) applyLocked(cfg Config, forceRestart bool) error {
	e.lastCfg = cfg
	e.hasCfg = true

	if !cfg.Enabled || !cfg.IsWebEnabled() {
		e.stopAllLocked()
		return nil
	}

	specs := e.desiredSpecsLocked(cfg)
	desiredKeys := make(map[string]bool, len(specs))
	for _, spec := range specs {
		desiredKeys[spec.key] = true
	}

	for key, inst := range e.instances {
		if !desiredKeys[key] || forceRestart {
			stopTproxyInstance(inst)
			delete(e.instances, key)
		}
	}

	for _, spec := range specs {
		if err := e.reconcileInstanceLocked(spec); err != nil {
			if spec.key == "primary" {
				return err
			}
		}
	}
	return nil
}

func (e *TproxyEngine) reconcileInstanceLocked(spec tproxySpec) error {
	cfgBytes, err := os.ReadFile(spec.configPath)
	if err != nil {
		return fmt.Errorf("read tproxy config %s: %w", spec.configPath, err)
	}
	profBytes, err := os.ReadFile(spec.profilesPath)
	if err != nil {
		return fmt.Errorf("read tproxy profiles %s: %w", spec.profilesPath, err)
	}

	parsed, err := tproxyconfig.Load(spec.configPath, spec.profilesPath)
	if err != nil {
		return fmt.Errorf("load tproxy %s: %w", spec.key, err)
	}
	if err := tproxysession.ValidateBudget(parsed); err != nil {
		return fmt.Errorf("validate tproxy budget %s: %w", spec.key, err)
	}
	tokenBytes, err := os.ReadFile(parsed.TokenKeyFile)
	if err != nil {
		return fmt.Errorf("read tproxy token key %s: %w", parsed.TokenKeyFile, err)
	}

	if existing := e.instances[spec.key]; existing != nil &&
		existing.configPath == spec.configPath &&
		existing.profilesPath == spec.profilesPath &&
		bytes.Equal(existing.cfgBytes, cfgBytes) &&
		bytes.Equal(existing.tokenBytes, tokenBytes) {
		if bytes.Equal(existing.profBytes, profBytes) {
			return nil
		}
		if err := existing.app.UpdateProfiles(parsed.Profiles); err == nil {
			existing.profBytes = profBytes
			return nil
		}
	}

	if _, err := tproxyconfig.ReadTokenKey(parsed.TokenKeyFile); err != nil {
		return fmt.Errorf("validate tproxy token key %s: %w", spec.key, err)
	}

	if old := e.instances[spec.key]; old != nil {
		stopTproxyInstance(old)
		delete(e.instances, spec.key)
	}

	app, err := tproxyserver.New(parsed)
	if err != nil {
		return fmt.Errorf("init tproxy %s: %w", spec.key, err)
	}

	lis, err := net.Listen("tcp", parsed.Listen)
	if err != nil {
		app.Shutdown()
		return fmt.Errorf("listen tproxy %s on %s: %w", spec.key, parsed.Listen, err)
	}

	var adminLis net.Listener
	var adminSrv *http.Server
	var adminAddr string
	if parsed.AdminListen != "" {
		adminLis, err = net.Listen("tcp", parsed.AdminListen)
		if err != nil {
			_ = lis.Close()
			app.Shutdown()
			return fmt.Errorf("listen tproxy admin %s on %s: %w", spec.key, parsed.AdminListen, err)
		}
		adminAddr = adminLis.Addr().String()
		adminSrv = &http.Server{
			Handler:           app.AdminHandler(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       30 * time.Second,
		}
		go func(s *http.Server, l net.Listener) {
			_ = s.Serve(l)
		}(adminSrv, adminLis)
	}

	srv := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: parsed.Timeouts.ReadHeader.Value(),
		IdleTimeout:       parsed.Timeouts.Idle.Value(),
	}
	go func(s *http.Server, l net.Listener) {
		_ = s.Serve(l)
	}(srv, lis)

	e.instances[spec.key] = &tproxyInstance{
		key:          spec.key,
		configPath:   spec.configPath,
		profilesPath: spec.profilesPath,
		listenAddr:   lis.Addr().String(),
		adminAddr:    adminAddr,
		cfgBytes:     cfgBytes,
		profBytes:    profBytes,
		tokenBytes:   tokenBytes,
		app:          app,
		srv:          srv,
		lis:          lis,
		adminSrv:     adminSrv,
		adminLis:     adminLis,
	}
	return nil
}

func (e *TproxyEngine) stopAllLocked() {
	for key, inst := range e.instances {
		stopTproxyInstance(inst)
		delete(e.instances, key)
	}
}

func stopTproxyInstance(inst *tproxyInstance) {
	if inst == nil {
		return
	}
	if inst.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = inst.srv.Shutdown(ctx)
		cancel()
		_ = inst.srv.Close()
	}
	if inst.lis != nil {
		_ = inst.lis.Close()
	}
	if inst.adminSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		_ = inst.adminSrv.Shutdown(ctx)
		cancel()
		_ = inst.adminSrv.Close()
	}
	if inst.adminLis != nil {
		_ = inst.adminLis.Close()
	}
	if inst.app != nil {
		inst.app.Shutdown()
	}
}
