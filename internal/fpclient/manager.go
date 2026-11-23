package fpclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
)

const (
	DefaultBinaryPath  = "/opt/bin/awgm-fpc"
	ExpectedVersion    = "1.0.0-c"
	ExpectedBannerPref = "awgm-fpc "

	defaultStorePath  = "/opt/etc/awg-manager/fp-clients.json"
	fallbackStorePath = "/tmp/awgm-fp-clients.json"

	fpKeySize   = 32
	fpKeyHexLen = 64
)

var (
	// ErrLegacyFPMigrationRequired is returned when importing a legacy UUID-only FiniteProto
	// profile or a profile whose secret is derived from a UUID.
	ErrLegacyFPMigrationRequired = errors.New("fpclient: legacy UUID-derived FiniteProto profile requires migration to v2 random credentials (missing fp_id or 64-hex fp_secret)")
	// ErrInvalidFPCredentials is returned when fp_id or fp_secret is malformed.
	ErrInvalidFPCredentials = errors.New("fpclient: invalid FiniteProto v2 credentials (requires non-empty fp_id and 64-hex random fp_secret)")
	// ErrCDaemonDisabled is returned when AWGM_DISABLE_C_FPC explicitly disables awgm-fpc.
	ErrCDaemonDisabled = errors.New("fpclient: C client daemon (awgm-fpc) is explicitly disabled via AWGM_DISABLE_C_FPC; Go FiniteProto fallback has been removed")
	// ErrCDaemonUnavailable is returned when awgm-fpc is missing, non-executable, or fails version verification.
	ErrCDaemonUnavailable = errors.New("fpclient: mandatory C client daemon (awgm-fpc) is unavailable or invalid")
)

// EndpointSpec describes a FiniteProto v2 client endpoint derived from a vless:// or fp:// / finiteproto:// link.
type EndpointSpec struct {
	Key         string `json:"key"`
	RawURI      string `json:"raw_uri"`
	Server      string `json:"server"`
	ServerPort  int    `json:"server_port"`
	UUID        string `json:"uuid,omitempty"`
	FPClientID  string `json:"fp_client_id"`
	FPSecretHex string `json:"fp_secret_hex"`
	Method      string `json:"method,omitempty"`
	SNI         string `json:"sni"`
	Host        string `json:"host"`
	Path        string `json:"path"`
	TLS         bool   `json:"tls"`
	Insecure    bool   `json:"insecure"`
	SocksPort   int    `json:"socks_port"`
	BridgePort  int    `json:"bridge_port"`
}

// BinaryInfo describes the verified local awgm-fpc binary state.
type BinaryInfo struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ResolveBinaryPath resolves the awgm-fpc executable path from AWGM_C_FPC_BIN or DefaultBinaryPath.
func ResolveBinaryPath() string {
	if envBin := strings.TrimSpace(os.Getenv("AWGM_C_FPC_BIN")); envBin != "" {
		return envBin
	}
	return DefaultBinaryPath
}

// IsDisabledByEnv reports whether AWGM_DISABLE_C_FPC explicitly disables awgm-fpc.
func IsDisabledByEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AWGM_DISABLE_C_FPC"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// InspectBinary verifies that awgm-fpc exists as a regular executable file and reports the expected version.
func InspectBinary(binPath string) (BinaryInfo, error) {
	if strings.TrimSpace(binPath) == "" {
		binPath = ResolveBinaryPath()
	}
	info := BinaryInfo{Path: binPath}
	if IsDisabledByEnv() {
		info.Error = ErrCDaemonDisabled.Error()
		return info, ErrCDaemonDisabled
	}

	st, err := os.Stat(binPath)
	if err != nil {
		werr := fmt.Errorf("%w: stat %s: %v", ErrCDaemonUnavailable, binPath, err)
		info.Error = werr.Error()
		return info, werr
	}
	if st.IsDir() || !st.Mode().IsRegular() {
		werr := fmt.Errorf("%w: %s is not a regular file", ErrCDaemonUnavailable, binPath)
		info.Error = werr.Error()
		return info, werr
	}
	if runtime.GOOS != "windows" && (st.Mode()&0o111) == 0 {
		werr := fmt.Errorf("%w: %s is not executable (mode %04o)", ErrCDaemonUnavailable, binPath, st.Mode().Perm())
		info.Error = werr.Error()
		return info, werr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binPath, "-version").CombinedOutput()
	if err != nil {
		werr := fmt.Errorf("%w: executing '%s -version' failed: %v (%s)",
			ErrCDaemonUnavailable, binPath, err, strings.TrimSpace(string(out)))
		info.Error = werr.Error()
		return info, werr
	}
	banner := strings.TrimSpace(string(out))
	if nl := strings.IndexByte(banner, '\n'); nl >= 0 {
		banner = strings.TrimSpace(banner[:nl])
	}
	if !strings.HasPrefix(banner, ExpectedBannerPref) {
		werr := fmt.Errorf("%w: unexpected version banner %q from %s", ErrCDaemonUnavailable, banner, binPath)
		info.Error = werr.Error()
		return info, werr
	}
	ver := strings.TrimSpace(strings.TrimPrefix(banner, ExpectedBannerPref))
	if ver != ExpectedVersion {
		werr := fmt.Errorf("%w: version mismatch for %s (got %q, want %q)", ErrCDaemonUnavailable, binPath, ver, ExpectedVersion)
		info.Version = ver
		info.Error = werr.Error()
		return info, werr
	}

	info.Available = true
	info.Version = ver
	return info, nil
}

func normalizeFPMethodMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "post":
		return "post"
	case "get":
		return "get"
	case "head_get", "head+get", "head-get":
		return "head_get"
	case "head":
		return "head"
	case "options":
		return "options"
	case "delete":
		return "delete"
	case "put":
		return "put"
	default:
		return "auto"
	}
}

type runningInstance struct {
	spec       EndpointSpec
	bridgeLn   net.Listener
	cmd        *exec.Cmd
	doneCh     chan struct{}
	exitMu     sync.Mutex
	exitErr    error
	cancelFunc context.CancelFunc
	ensuredAt  time.Time // последний EnsureEndpoint (или старт)
}

func (inst *runningInstance) recordExit(err error) {
	inst.exitMu.Lock()
	inst.exitErr = err
	inst.exitMu.Unlock()
	close(inst.doneCh)
}

func (inst *runningInstance) waitErr() error {
	inst.exitMu.Lock()
	defer inst.exitMu.Unlock()
	return inst.exitErr
}

func (inst *runningInstance) isAlive() bool {
	if inst == nil || inst.cmd == nil || inst.cmd.Process == nil || inst.doneCh == nil {
		return false
	}
	select {
	case <-inst.doneCh:
		return false
	default:
		return true
	}
}

func (inst *runningInstance) stop() {
	if inst == nil {
		return
	}
	if inst.cancelFunc != nil {
		inst.cancelFunc()
	}
	if inst.bridgeLn != nil {
		_ = inst.bridgeLn.Close()
	}
	if inst.cmd != nil && inst.cmd.Process != nil {
		_ = inst.cmd.Process.Kill()
	}
	if inst.doneCh != nil {
		select {
		case <-inst.doneCh:
		case <-time.After(1 * time.Second):
		}
	}
}

var (
	globalMu        sync.Mutex
	globalInstances = make(map[string]*runningInstance)
	globalDormant   = make(map[string]EndpointSpec) // остановлены сверкой, ждут ссылки
	customStorePath string
)

// SetStorePath overrides the persistence path for tests or custom data directories.
func SetStorePath(path string) {
	globalMu.Lock()
	defer globalMu.Unlock()
	customStorePath = strings.TrimSpace(path)
}

func activeStorePath() string {
	if customStorePath != "" {
		return customStorePath
	}
	if runtime.GOOS == "linux" {
		if dir := filepath.Dir(defaultStorePath); dirExists(dir) {
			return defaultStorePath
		}
	}
	return fallbackStorePath
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// IsFiniteProtoQuery reports whether a VLESS URL query requests the FiniteProto transport.
func IsFiniteProtoQuery(q url.Values) bool {
	if strings.EqualFold(strings.TrimSpace(q.Get("type")), "finiteproto") ||
		strings.EqualFold(strings.TrimSpace(q.Get("mode")), "finiteproto") ||
		strings.EqualFold(strings.TrimSpace(q.Get("fp_proto")), "1") {
		return true
	}
	extraRaw := strings.TrimSpace(q.Get("extra"))
	if extraRaw != "" {
		var extra map[string]any
		if json.Unmarshal([]byte(extraRaw), &extra) == nil {
			if te, ok := extra["transportEngine"].(string); ok && strings.EqualFold(te, "finiteproto") {
				return true
			}
			if fp, ok := extra["finiteproto"].(bool); ok && fp {
				return true
			}
		}
	}
	return false
}

func isUUIDDerivedSecret(candidate []byte, uuidStrings ...string) bool {
	if len(candidate) == 0 {
		return false
	}
	for _, s := range uuidStrings {
		clean := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
		if len(clean) != 32 {
			continue
		}
		raw, err := hex.DecodeString(clean)
		if err != nil || len(raw) != 16 {
			continue
		}
		canonical := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
		v1Sum := sha256.Sum256([]byte("awgm-fp-v1:" + canonical))
		rawSum := sha256.Sum256(raw)
		if hmac.Equal(candidate, v1Sum[:]) ||
			hmac.Equal(candidate, rawSum[:]) ||
			hmac.Equal(candidate, []byte(clean)) ||
			hmac.Equal(candidate, []byte(strings.ToUpper(clean))) ||
			hmac.Equal(candidate, raw) {
			return true
		}
	}
	return false
}

func validateSpecCredentials(uuidStr, fpClientID, fpSecretHex string) ([]byte, error) {
	id := strings.TrimSpace(fpClientID)
	secHex := strings.ToLower(strings.TrimSpace(fpSecretHex))
	if id == "" || secHex == "" {
		return nil, ErrLegacyFPMigrationRequired
	}
	if strings.ContainsAny(id, ":\r\n") || len(secHex) != fpKeyHexLen {
		return nil, ErrInvalidFPCredentials
	}
	sec, err := hex.DecodeString(secHex)
	if err != nil || len(sec) != fpKeySize {
		return nil, ErrInvalidFPCredentials
	}
	var nz byte
	for _, b := range sec {
		nz |= b
	}
	if nz == 0 {
		return nil, ErrInvalidFPCredentials
	}
	if isUUIDDerivedSecret(sec, uuidStr, id) {
		return nil, ErrLegacyFPMigrationRequired
	}
	return sec, nil
}

func firstNonEmptyQuery(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

// ParseVLESSFiniteProto parses a vless:// or fp:// / finiteproto:// URI if it uses FiniteProto v2 and returns its EndpointSpec.
// Legacy v1 UUID-only FiniteProto links return (nil, true, ErrLegacyFPMigrationRequired).
func ParseVLESSFiniteProto(rawURI string) (*EndpointSpec, bool, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return nil, false, nil
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	isDirectFP := scheme == "fp" || scheme == "finiteproto"
	q := u.Query()
	fpVersion := firstNonEmptyQuery(q, "fp_v", "fp_version", "v")
	if fpVersion != "" && fpVersion != "2" {
		return nil, true, ErrLegacyFPMigrationRequired
	}
	if !isDirectFP {
		if scheme != "vless" || !IsFiniteProtoQuery(q) {
			return nil, false, nil
		}
	}

	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return nil, true, fmt.Errorf("fpclient: missing server host")
	}
	port := 443
	if pStr := strings.TrimSpace(u.Port()); pStr != "" {
		p, err := strconv.Atoi(pStr)
		if err != nil || p <= 0 || p > 65535 {
			return nil, true, fmt.Errorf("fpclient: invalid server port")
		}
		port = p
	} else if !isDirectFP {
		return nil, true, fmt.Errorf("fpclient: invalid server port")
	}

	var uuid, fpClientID, fpSecretHex string
	if isDirectFP {
		if u.User == nil {
			return nil, true, ErrLegacyFPMigrationRequired
		}
		fpClientID = strings.TrimSpace(u.User.Username())
		if pwd, hasPwd := u.User.Password(); hasPwd {
			fpSecretHex = strings.TrimSpace(pwd)
		}
		if fpSecretHex == "" {
			fpSecretHex = firstNonEmptyQuery(q, "secret", "fp_secret", "fp_secret_hex")
		}
		uuid = strings.TrimSpace(q.Get("uuid"))
	} else {
		if u.User != nil {
			uuid = strings.TrimSpace(u.User.Username())
		}
		if uuid == "" {
			uuid = strings.TrimSpace(q.Get("id"))
		}
		if uuid == "" {
			return nil, true, fmt.Errorf("fpclient: missing client UUID")
		}
		fpClientID = firstNonEmptyQuery(q, "fp_id", "fp_client_id")
		fpSecretHex = firstNonEmptyQuery(q, "fp_secret", "fp_secret_hex")
		if (fpClientID == "" || fpSecretHex == "") && strings.TrimSpace(q.Get("extra")) != "" {
			var extra map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(q.Get("extra"))), &extra) == nil {
				if fpClientID == "" {
					if v, ok := extra["fpClientId"].(string); ok {
						fpClientID = strings.TrimSpace(v)
					} else if v, ok := extra["fp_id"].(string); ok {
						fpClientID = strings.TrimSpace(v)
					}
				}
				if fpSecretHex == "" {
					if v, ok := extra["fpSecretHex"].(string); ok {
						fpSecretHex = strings.TrimSpace(v)
					} else if v, ok := extra["fp_secret"].(string); ok {
						fpSecretHex = strings.TrimSpace(v)
					}
				}
			}
		}
	}

	if _, err := validateSpecCredentials(uuid, fpClientID, fpSecretHex); err != nil {
		return nil, true, err
	}
	fpSecretHex = strings.ToLower(strings.TrimSpace(fpSecretHex))
	fpClientID = strings.TrimSpace(fpClientID)

	path := strings.TrimSpace(q.Get("path"))
	if path == "" && isDirectFP && strings.TrimSpace(u.Path) != "" && u.Path != "/" {
		path = strings.TrimSpace(u.Path)
	}
	if path == "" {
		path = "/cdn-bridge"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	httpHost := strings.TrimSpace(q.Get("host"))
	if httpHost == "" {
		httpHost = host
	}
	sni := strings.TrimSpace(q.Get("sni"))
	if sni == "" {
		sni = strings.TrimSpace(q.Get("servername"))
	}
	if sni == "" {
		sni = host
	}
	sec := strings.ToLower(strings.TrimSpace(q.Get("security")))
	useTLS := sec == "" || sec == "tls" || sec == "reality"
	if sec == "none" {
		useTLS = false
	}
	insecure := q.Get("insecure") == "1" || strings.EqualFold(q.Get("insecure"), "true")

	fpMethod := firstNonEmptyQuery(q, "fp_method", "http_method", "method")
	if fpMethod == "" && strings.TrimSpace(q.Get("extra")) != "" {
		var extra map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(q.Get("extra"))), &extra) == nil {
			if v, ok := extra["fpMethod"].(string); ok && strings.TrimSpace(v) != "" {
				fpMethod = v
			} else if v, ok := extra["httpMethod"].(string); ok && strings.TrimSpace(v) != "" {
				fpMethod = v
			} else if v, ok := extra["method"].(string); ok && strings.TrimSpace(v) != "" {
				fpMethod = v
			}
		}
	}
	fpMethod = normalizeFPMethodMode(fpMethod)

	keySum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%s|%v",
		strings.ToLower(host), port, strings.ToLower(uuid), fpClientID, httpHost, path, useTLS)))
	key := hex.EncodeToString(keySum[:8])
	slot := int(binary.BigEndian.Uint16(keySum[:2])) % 350
	socksPort := 19100 + slot
	bridgePort := 19500 + slot

	return &EndpointSpec{
		Key:         key,
		RawURI:      rawURI,
		Server:      host,
		ServerPort:  port,
		UUID:        uuid,
		FPClientID:  fpClientID,
		FPSecretHex: fpSecretHex,
		Method:      fpMethod,
		SNI:         sni,
		Host:        httpHost,
		Path:        path,
		TLS:         useTLS,
		Insecure:    insecure,
		SocksPort:   socksPort,
		BridgePort:  bridgePort,
	}, true, nil
}

// ExportVLESSFiniteProtoURI formats an EndpointSpec into a canonical v2 vless:// FiniteProto share link.
func ExportVLESSFiniteProtoURI(spec EndpointSpec, remark string) (string, error) {
	uuid := strings.TrimSpace(spec.UUID)
	if uuid == "" {
		uuid = "00000000-0000-0000-0000-000000000000"
	}
	if _, err := validateSpecCredentials(uuid, spec.FPClientID, spec.FPSecretHex); err != nil {
		return "", err
	}
	secMode := "tls"
	if !spec.TLS {
		secMode = "none"
	}
	path := strings.TrimSpace(spec.Path)
	if path == "" {
		path = "/cdn-bridge"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := strings.TrimSpace(spec.Host)
	if host == "" {
		host = spec.Server
	}
	sni := strings.TrimSpace(spec.SNI)
	if sni == "" {
		sni = spec.Server
	}
	u := fmt.Sprintf(
		"vless://%s@%s:%d?encryption=none&security=%s&sni=%s&alpn=http/1.1&type=xhttp&host=%s&path=%s&mode=finiteproto&fp_v=2&fp_id=%s&fp_secret=%s",
		url.PathEscape(uuid),
		spec.Server,
		spec.ServerPort,
		secMode,
		url.QueryEscape(sni),
		url.QueryEscape(host),
		url.QueryEscape(path),
		url.QueryEscape(strings.TrimSpace(spec.FPClientID)),
		url.QueryEscape(strings.ToLower(strings.TrimSpace(spec.FPSecretHex))),
	)
	if spec.Insecure {
		u += "&insecure=1"
	}
	if m := normalizeFPMethodMode(spec.Method); m != "auto" {
		u += "&fp_method=" + url.QueryEscape(m)
	}
	if strings.TrimSpace(remark) != "" {
		u += "#" + url.QueryEscape(strings.TrimSpace(remark))
	}
	return u, nil
}

// ExportDirectFPURI formats an EndpointSpec into a canonical v2 fp:// share link.
func ExportDirectFPURI(spec EndpointSpec, remark string) (string, error) {
	if _, err := validateSpecCredentials(spec.UUID, spec.FPClientID, spec.FPSecretHex); err != nil {
		return "", err
	}
	path := strings.TrimSpace(spec.Path)
	if path == "" {
		path = "/cdn-bridge"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := strings.TrimSpace(spec.Host)
	if host == "" {
		host = spec.Server
	}
	sni := strings.TrimSpace(spec.SNI)
	if sni == "" {
		sni = spec.Server
	}
	q := url.Values{}
	q.Set("v", "2")
	q.Set("path", path)
	q.Set("host", host)
	q.Set("sni", sni)
	if !spec.TLS {
		q.Set("security", "none")
	}
	if spec.Insecure {
		q.Set("insecure", "1")
	}
	if strings.TrimSpace(spec.UUID) != "" {
		q.Set("uuid", strings.TrimSpace(spec.UUID))
	}
	if m := normalizeFPMethodMode(spec.Method); m != "auto" {
		q.Set("fp_method", m)
	}
	raw := fmt.Sprintf("fp://%s:%s@%s:%d?%s",
		url.PathEscape(strings.TrimSpace(spec.FPClientID)),
		strings.ToLower(strings.TrimSpace(spec.FPSecretHex)),
		spec.Server,
		spec.ServerPort,
		q.Encode(),
	)
	if strings.TrimSpace(remark) != "" {
		raw += "#" + url.QueryEscape(strings.TrimSpace(remark))
	}
	return raw, nil
}

// EnsureEndpoint starts (or reuses) the mandatory C FiniteProto client daemon (awgm-fpc)
// plus local TCP->TLS forwarder for spec and persists it.
func EnsureEndpoint(spec EndpointSpec) (int, error) {
	if _, err := validateSpecCredentials(spec.UUID, spec.FPClientID, spec.FPSecretHex); err != nil {
		return 0, err
	}
	if strings.TrimSpace(spec.Key) == "" {
		spec.Key = fmt.Sprintf("%s:%d%s:%s", spec.Server, spec.ServerPort, spec.Path, spec.FPClientID)
	}
	globalMu.Lock()
	defer globalMu.Unlock()

	if inst, ok := globalInstances[spec.Key]; ok {
		if inst.isAlive() {
			// Ссылку разобрали заново: слот с этим портом вот-вот сохранят,
			// сверка не должна снять endpoint до этого.
			inst.ensuredAt = time.Now()
			return inst.spec.SocksPort, nil
		}
		delete(globalInstances, spec.Key)
		inst.stop()
	}
	if d, ok := globalDormant[spec.Key]; ok {
		spec.SocksPort = d.SocksPort // прежний порт уже записан в слоты
		delete(globalDormant, spec.Key)
	}

	inst, err := startInstanceLocked(spec)
	if err != nil {
		return 0, err
	}
	globalInstances[spec.Key] = inst
	_ = savePersistedLocked()
	return inst.spec.SocksPort, nil
}

// ReconcileReferenced сверяет endpoint'ы со ссылками на их socks-порт в
// файлах под roots (слоты sing-box, конфиг FIRC). Неиспользуемый endpoint
// старше minAge останавливается, но его описание остаётся «спящим»: если
// ссылка вернётся (включили выключенный слот, восстановили снимок), следующая
// сверка поднимет его на прежнем порту. minAge закрывает окно между
// EnsureEndpoint и сохранением слота. Непарсящийся JSON отменяет сверку:
// лишний процесс лучше оборванного туннеля.
func ReconcileReferenced(roots []string, minAge time.Duration) error {
	used, err := referencedSocksPorts(roots)
	if err != nil {
		return err
	}
	globalMu.Lock()
	defer globalMu.Unlock()
	changed := false
	for k, inst := range globalInstances {
		if used[inst.spec.SocksPort] || time.Since(inst.ensuredAt) < minAge {
			continue
		}
		delete(globalInstances, k)
		inst.stop()
		globalDormant[k] = inst.spec
		changed = true
	}
	var errs []error
	for k, sp := range globalDormant {
		if !used[sp.SocksPort] {
			continue
		}
		inst, err := startInstanceLocked(sp)
		if err != nil {
			errs = append(errs, fmt.Errorf("revive %s: %w", k, err))
			continue
		}
		delete(globalDormant, k)
		globalInstances[k] = inst
		changed = true
	}
	if changed {
		errs = append(errs, savePersistedLocked())
	}
	return errors.Join(errs...)
}

var loopbackPortRe = regexp.MustCompile(`127\.0\.0\.1:(\d+)`)

// referencedSocksPorts: в *.json — socks-outbound на 127.0.0.1 на любой
// глубине (слоты sing-box), в прочих файлах — «127.0.0.1:<порт>» (FIRC пишет
// socks5://127.0.0.1:P). Каталог snapshots пропускается: там base64.
func referencedSocksPorts(roots []string) (map[int]bool, error) {
	used := map[int]bool{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				if path == root && os.IsNotExist(err) {
					return filepath.SkipDir
				}
				return err
			}
			if d.IsDir() {
				if d.Name() == "snapshots" {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil // сокет/FIFO: чтение повесило бы сверку
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.HasSuffix(path, ".json") {
				for _, m := range loopbackPortRe.FindAllSubmatch(data, -1) {
					if p, err := strconv.Atoi(string(m[1])); err == nil {
						used[p] = true
					}
				}
				return nil
			}
			var doc any
			if err := json.Unmarshal(data, &doc); err != nil {
				return fmt.Errorf("fpclient: parse %s: %w", path, err)
			}
			collectSocksPorts(doc, used)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return used, nil
}

func collectSocksPorts(v any, used map[int]bool) {
	switch t := v.(type) {
	case map[string]any:
		if t["type"] == "socks" && t["server"] == "127.0.0.1" {
			if p, ok := t["server_port"].(float64); ok {
				used[int(p)] = true
			}
		}
		for _, c := range t {
			collectSocksPorts(c, used)
		}
	case []any:
		for _, c := range t {
			collectSocksPorts(c, used)
		}
	}
}

// persistedEndpoint — запись fp-clients.json; старый формат (голый
// EndpointSpec) читается как неспящий.
type persistedEndpoint struct {
	EndpointSpec
	Dormant bool `json:"dormant,omitempty"`
}

// RestorePersistedEndpoints starts all persisted FiniteProto client endpoints on daemon startup.
func RestorePersistedEndpoints() {
	globalMu.Lock()
	defer globalMu.Unlock()

	data, err := os.ReadFile(activeStorePath())
	if err != nil || len(data) == 0 {
		return
	}
	var specs []persistedEndpoint
	if json.Unmarshal(data, &specs) != nil {
		return
	}
	for _, pe := range specs {
		sp := pe.EndpointSpec
		if sp.Key == "" || sp.Server == "" {
			continue
		}
		if _, err := validateSpecCredentials(sp.UUID, sp.FPClientID, sp.FPSecretHex); err != nil {
			continue
		}
		if pe.Dormant {
			globalDormant[sp.Key] = sp
			continue
		}
		if inst, exists := globalInstances[sp.Key]; exists && inst.isAlive() {
			continue
		}
		if inst, err := startInstanceLocked(sp); err == nil {
			// Восстановленный на старте не «свежий»: слоты уже на диске, и
			// первая сверка вправе его усыпить.
			inst.ensuredAt = time.Time{}
			globalInstances[sp.Key] = inst
		}
	}
}

func savePersistedLocked() error {
	specs := make([]persistedEndpoint, 0, len(globalInstances)+len(globalDormant))
	for _, inst := range globalInstances {
		specs = append(specs, persistedEndpoint{EndpointSpec: inst.spec})
	}
	for _, sp := range globalDormant {
		specs = append(specs, persistedEndpoint{EndpointSpec: sp, Dormant: true})
	}
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return err
	}
	p := activeStorePath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return storage.AtomicWritePerm(p, data, 0o600)
}

type boundedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *boundedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 {
		b.max = 4096
	}
	rem := b.max - b.buf.Len()
	if rem > 0 {
		if len(p) > rem {
			_, _ = b.buf.Write(p[:rem])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buf.String())
}

// selectAvailableLoopbackPort chooses candidate if free (without accepting/closing any foreign socket)
// or allocates a fresh ephemeral loopback port when candidate is occupied by another service.
func selectAvailableLoopbackPort(candidate int) (int, error) {
	if candidate > 0 && candidate <= 65535 {
		busyOnProc := false
		if runtime.GOOS == "linux" {
			if lookup, err := procnet.FindListeningProcess("/proc", "127.0.0.1", candidate); lookup.SocketFound || err != nil {
				if lookup.SocketFound {
					busyOnProc = true
				}
			}
		}
		if !busyOnProc {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", candidate))
			if err == nil {
				port := ln.Addr().(*net.TCPAddr).Port
				_ = ln.Close()
				return port, nil
			}
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("fpclient allocate local socks port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

func startInstanceLocked(spec EndpointSpec) (*runningInstance, error) {
	if _, err := validateSpecCredentials(spec.UUID, spec.FPClientID, spec.FPSecretHex); err != nil {
		return nil, err
	}
	binPath := ResolveBinaryPath()
	if _, err := InspectBinary(binPath); err != nil {
		return nil, err
	}

	socksPort, err := selectAvailableLoopbackPort(spec.SocksPort)
	if err != nil {
		return nil, err
	}
	spec.SocksPort = socksPort

	ctx, cancel := context.WithCancel(context.Background())
	inst := &runningInstance{
		spec:       spec,
		cancelFunc: cancel,
	}

	// 1. Start local TCP -> TLS socket forwarder on 127.0.0.1:BridgePort (zero FiniteProto wire code).
	var bln net.Listener
	if spec.BridgePort > 0 && spec.BridgePort <= 65535 && spec.BridgePort != spec.SocksPort {
		bln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", spec.BridgePort))
	}
	if bln == nil {
		bln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			cancel()
			return nil, fmt.Errorf("fpclient bridge listen: %w", err)
		}
	}
	spec.BridgePort = bln.Addr().(*net.TCPAddr).Port
	inst.spec = spec
	inst.bridgeLn = bln
	go serveTLSBridge(ctx, bln, spec)

	// 2. Spawn mandatory C client daemon awgm-fpc on 127.0.0.1:SocksPort with v2 credentials.
	httpHost := strings.TrimSpace(spec.Host)
	if httpHost == "" {
		httpHost = spec.Server
	}
	httpPath := strings.TrimSpace(spec.Path)
	if httpPath == "" {
		httpPath = "/cdn-bridge"
	}
	methodMode := normalizeFPMethodMode(spec.Method)
	stderrBuf := &boundedLogBuffer{max: 4096}
	cmd := exec.CommandContext(ctx, binPath,
		"-listen", fmt.Sprintf("127.0.0.1:%d", spec.SocksPort),
		"-connect", fmt.Sprintf("127.0.0.1:%d", spec.BridgePort),
		"-host", httpHost,
		"-path", httpPath,
		"-client-id", strings.TrimSpace(spec.FPClientID),
		"-method", methodMode,
	)
	cmd.Stderr = stderrBuf
	setParentDeath(cmd)
	cmd.Env = append(os.Environ(),
		"FP_CLIENT_SECRET_HEX="+strings.ToLower(strings.TrimSpace(spec.FPSecretHex)),
		"FP_CLIENT_METHOD="+methodMode,
	)
	if err := cmd.Start(); err != nil {
		_ = bln.Close()
		cancel()
		return nil, fmt.Errorf("%w: start %s: %v", ErrCDaemonUnavailable, binPath, err)
	}
	inst.cmd = cmd
	inst.ensuredAt = time.Now()
	inst.doneCh = make(chan struct{})
	go func() {
		inst.recordExit(cmd.Wait())
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-inst.doneCh:
			waitErr := inst.waitErr()
			_ = bln.Close()
			cancel()
			errMsg := stderrBuf.String()
			if errMsg != "" {
				return nil, fmt.Errorf("fpclient: awgm-fpc exited during startup (%v): %s", waitErr, errMsg)
			}
			return nil, fmt.Errorf("fpclient: awgm-fpc exited during startup: %v", waitErr)
		default:
		}

		if runtime.GOOS == "linux" {
			lookup, pErr := procnet.FindListeningProcess("/proc", "127.0.0.1", spec.SocksPort)
			if pErr == nil && lookup.SocketFound && lookup.PID > 0 {
				if lookup.PID != cmd.Process.Pid {
					inst.stop()
					return nil, fmt.Errorf("fpclient: listener ownership mismatch on 127.0.0.1:%d (owned by PID %d, expected awgm-fpc PID %d)",
						spec.SocksPort, lookup.PID, cmd.Process.Pid)
				}
				return inst, nil
			}
		} else {
			c, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", spec.SocksPort), 100*time.Millisecond)
			if dialErr == nil {
				_ = c.Close()
				return inst, nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	inst.stop()
	if errMsg := stderrBuf.String(); errMsg != "" {
		return nil, fmt.Errorf("fpclient: awgm-fpc failed readiness check on 127.0.0.1:%d: %s", spec.SocksPort, errMsg)
	}
	return nil, fmt.Errorf("fpclient: awgm-fpc timed out waiting for listener on 127.0.0.1:%d", spec.SocksPort)
}

func serveTLSBridge(ctx context.Context, ln net.Listener, spec EndpointSpec) {
	targetAddr := net.JoinHostPort(spec.Server, strconv.Itoa(spec.ServerPort))
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(50 * time.Millisecond)
				continue
			}
		}
		go func(localConn net.Conn) {
			defer localConn.Close()
			dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
			var remoteConn net.Conn
			var err error
			if spec.TLS {
				tlsDialer := &tls.Dialer{
					NetDialer: dialer,
					Config: &tls.Config{
						ServerName:         spec.SNI,
						NextProtos:         []string{"http/1.1"},
						InsecureSkipVerify: spec.Insecure,
					},
				}
				remoteConn, err = tlsDialer.DialContext(ctx, "tcp", targetAddr)
			} else {
				remoteConn, err = dialer.DialContext(ctx, "tcp", targetAddr)
			}
			if err != nil {
				return
			}
			defer remoteConn.Close()

			done := make(chan struct{}, 2)
			go func() {
				_, _ = io.Copy(remoteConn, localConn)
				done <- struct{}{}
			}()
			go func() {
				_, _ = io.Copy(localConn, remoteConn)
				done <- struct{}{}
			}()
			<-done
			_ = localConn.Close()
			_ = remoteConn.Close()
			<-done
		}(c)
	}
}
