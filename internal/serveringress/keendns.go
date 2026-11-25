package serveringress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// KeenDNSStatusReader abstracts reading KeenDNS status from NDMS.
type KeenDNSStatusReader interface {
	GetStatus(ctx context.Context) (*ndmsquery.KeenDNSStatus, error)
}

// HTTPProxyStateReader abstracts uncached reads of `/show/rc/ip/http` and `/show/ip/http/proxy`.
type HTTPProxyStateReader interface {
	FetchConfigUncached(ctx context.Context) (*ndmsquery.HTTPProxyConfigSnapshot, error)
	FetchRuntimeUncached(ctx context.Context) (map[string]ndmsquery.HTTPProxyRuntimeEntry, error)
}

// HTTPProxyStateWriter abstracts validated writes and saves for `ip.http.proxy`.
type HTTPProxyStateWriter interface {
	UpsertPublication(ctx context.Context, spec ndmscommand.HTTPProxyPublicationSpec) error
	DeletePublication(ctx context.Context, label string) error
	RestorePublicationRaw(ctx context.Context, label string, rawJSON string) error
	SaveConfig(ctx context.Context) error
}

// NDMSKeenDNSOperator implements KeenDNSOperator backed by NDMS RCI queries and commands.
type NDMSKeenDNSOperator struct {
	mu           sync.Mutex
	keendns      KeenDNSStatusReader
	proxyReader  HTTPProxyStateReader
	proxyWriter  HTTPProxyStateWriter
	adminPortFn  func() int
	wanInfoFn    func(ctx context.Context) (string, bool)
	allowedPorts []int
}

func NewNDMSKeenDNSOperator(
	keendns KeenDNSStatusReader,
	proxyReader HTTPProxyStateReader,
	proxyWriter HTTPProxyStateWriter,
) *NDMSKeenDNSOperator {
	return &NDMSKeenDNSOperator{
		keendns:      keendns,
		proxyReader:  proxyReader,
		proxyWriter:  proxyWriter,
		allowedPorts: []int{9009},
	}
}

func (op *NDMSKeenDNSOperator) SetAdminPortProvider(fn func() int) {
	op.adminPortFn = fn
}

func (op *NDMSKeenDNSOperator) SetWANInfoProvider(fn func(ctx context.Context) (string, bool)) {
	op.wanInfoFn = fn
}

// IsPrivateOrCGNATIP reports whether ipStr is a non-publicly-routable address,
// including RFC 1918 private ranges, RFC 6598 CGNAT (100.64.0.0/10), loopback,
// link-local, unspecified, multicast, or RFC 2544 benchmarking ranges.
func IsPrivateOrCGNATIP(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// RFC 6598 Shared Address Space (CGNAT): 100.64.0.0/10 (100.64.0.0 – 100.127.255.255)
		if ip4[0] == 100 && (ip4[1]&0xc0) == 64 {
			return true
		}
		// RFC 2544 benchmarking: 198.18.0.0/15
		if ip4[0] == 198 && (ip4[1]&0xfe) == 18 {
			return true
		}
	}
	return false
}

func isValidISPWANInterface(iface *ndms.Interface) bool {
	if iface == nil {
		return false
	}
	if iface.SecurityLevel != "public" {
		return false
	}
	if ndmsquery.IsNonISPInterface(iface.ID) || ndmsquery.IsNonISPInterface(iface.SystemName) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(iface.Type)) {
	case "wireguard", "openvpn", "sstp", "ipsec", "proxy", "opkgtun", "bridge", "loopback", "port":
		return false
	}
	addr := strings.TrimSpace(iface.Address)
	if addr == "" {
		return false
	}
	ip := net.ParseIP(addr)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		return false
	}
	return true
}

// ResolveWANInfo determines the active ISP WAN IPv4 address and whether it is
// private/CGNAT from the existing NDMS query stores (Routes + Interfaces).
// It never selects VPN/tunnel interfaces or arbitrary non-WAN interfaces, and
// returns ("", false) when the WAN address is unknown or unavailable.
func ResolveWANInfo(ctx context.Context, q *ndmsquery.Queries) (string, bool) {
	iface := ResolveWANInterface(ctx, q)
	if iface == nil {
		return "", false
	}
	addr := strings.TrimSpace(iface.Address)
	return addr, IsPrivateOrCGNATIP(addr)
}

// ResolveWANInterface — интерфейс, чей адрес отдаёт ResolveWANInfo.
func ResolveWANInterface(ctx context.Context, q *ndmsquery.Queries) *ndms.Interface {
	if q == nil || q.Interfaces == nil {
		return nil
	}

	// 1. Prefer the default-gateway interface from Routes if it is an active ISP WAN
	// with a valid IPv4 address (ignoring VPN tunnels even if they carry default route).
	if q.Routes != nil {
		if defID, err := q.Routes.GetDefaultGatewayInterface(ctx); err == nil && strings.TrimSpace(defID) != "" {
			if iface, err := q.Interfaces.Get(ctx, strings.TrimSpace(defID)); err == nil && isValidISPWANInterface(iface) {
				if !strings.EqualFold(iface.State, "down") && !strings.EqualFold(iface.ConfLayer, "disabled") && !strings.EqualFold(iface.IPv4, "disabled") {
					return iface
				}
			}
		}
	}

	// 2. Fall back to public ISP WAN interfaces from Interfaces.ListWAN(ctx),
	// ordered deterministically: Up == true first, then highest NDMS Priority, then ID.
	wans, err := q.Interfaces.ListWAN(ctx)
	if err != nil || len(wans) == 0 {
		return nil
	}

	sorted := append([]wan.Interface(nil), wans...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Up != sorted[j].Up {
			return sorted[i].Up
		}
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		return sorted[i].ID < sorted[j].ID
	})

	// First pass: active (Up) ISP WAN interfaces.
	for _, w := range sorted {
		if !w.Up {
			continue
		}
		if ndmsquery.IsNonISPInterface(w.ID) || ndmsquery.IsNonISPInterface(w.Name) {
			continue
		}
		iface, err := q.Interfaces.Get(ctx, w.ID)
		if err != nil || !isValidISPWANInterface(iface) {
			continue
		}
		return iface
	}

	// Second pass: WAN interfaces with State == "up" and a valid IPv4 address.
	for _, w := range sorted {
		if ndmsquery.IsNonISPInterface(w.ID) || ndmsquery.IsNonISPInterface(w.Name) {
			continue
		}
		iface, err := q.Interfaces.Get(ctx, w.ID)
		if err != nil || !isValidISPWANInterface(iface) || !strings.EqualFold(iface.State, "up") {
			continue
		}
		return iface
	}

	return nil
}

func (op *NDMSKeenDNSOperator) SetAllowedPorts(ports []int) {
	if len(ports) > 0 {
		op.allowedPorts = append([]int(nil), ports...)
	}
}

func (op *NDMSKeenDNSOperator) forbiddenPorts() []int {
	if op.adminPortFn != nil {
		if p := op.adminPortFn(); p > 0 {
			return []int{p}
		}
	}
	return nil
}

func classifyNDMSError(err error) (string, string) {
	if err == nil {
		return "supported", ""
	}
	var httpErr *transport.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusNotFound:
			return "unsupported", "KeenDNS не поддерживается этой моделью или версией прошивки"
		case http.StatusForbidden, http.StatusUnauthorized:
			return "forbidden", "Недостаточно прав для чтения настроек KeenDNS на роутере"
		default:
			return "read_error", fmt.Sprintf("Ошибка чтения настроек KeenDNS (код %d)", httpErr.Status)
		}
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "404") || strings.Contains(lower, "not found") || strings.Contains(lower, "unsupported") {
		return "unsupported", "KeenDNS не поддерживается этой моделью или версией прошивки"
	}
	if strings.Contains(lower, "403") || strings.Contains(lower, "401") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "unauthorized") {
		return "forbidden", "Недостаточно прав для чтения настроек KeenDNS на роутере"
	}
	return "read_error", "Не удалось прочитать настройки KeenDNS с роутера"
}

func isEntryCompatibleWithIngress(entry ndmsquery.HTTPProxyEntry, targetPort int) (bool, string) {
	if !entry.DomainNDNS {
		return false, "Используется статический домен вместо KeenDNS"
	}
	if entry.Auth {
		return false, "Включена защита паролем роутера"
	}
	if !entry.Public {
		return false, "Доступ из интернета (public) выключен"
	}
	proto := entry.UpstreamProto
	if proto != "" && proto != "http" {
		return false, fmt.Sprintf("Протокол назначения %s не совпадает с http", proto)
	}
	if entry.UpstreamHost != "127.0.0.1" {
		return false, fmt.Sprintf("Занято другим правилом (%s:%s)", entry.UpstreamHost, entry.UpstreamPort)
	}
	if entry.UpstreamPort != strconv.Itoa(targetPort) {
		return false, fmt.Sprintf("Направлено на другой порт роутера (127.0.0.1:%s)", entry.UpstreamPort)
	}
	return true, ""
}

// Discover reads KeenDNS state and existing 4th-level HTTP proxy publications without mutating anything.
func (op *NDMSKeenDNSOperator) Discover(ctx context.Context, dispatcherPort int) (KeenDNSDiscovery, error) {
	if dispatcherPort <= 0 {
		dispatcherPort = 9009
	}
	var out KeenDNSDiscovery
	if op.wanInfoFn != nil {
		out.WANAddress, out.WANPrivate = op.wanInfoFn(ctx)
		if out.WANAddress != "" && !out.WANPrivate {
			out.WANPrivate = IsPrivateOrCGNATIP(out.WANAddress)
		}
	}
	if op == nil || op.keendns == nil {
		out.State = "unsupported"
		out.StatusMessage = "Подсистема KeenDNS недоступна"
		return out, nil
	}

	st, err := op.keendns.GetStatus(ctx)
	if err != nil {
		state, msg := classifyNDMSError(err)
		out.State = state
		out.StatusMessage = msg
		return out, nil
	}
	if st == nil || !st.Enabled || strings.TrimSpace(st.Name) == "" || strings.TrimSpace(st.Domain) == "" {
		out.State = "unconfigured"
		out.StatusMessage = "KeenDNS ещё не настроен на роутере (не выбрано доменное имя в настройках Keenetic)"
		return out, nil
	}

	baseFQDN := strings.TrimSpace(st.FQDN)
	if baseFQDN == "" {
		baseFQDN = strings.TrimSpace(st.Name) + "." + strings.TrimSpace(st.Domain)
	}

	out.State = "supported"
	out.BaseFQDN = baseFQDN
	out.Name = strings.TrimSpace(st.Name)
	out.Zone = strings.TrimSpace(st.Domain)
	out.AccessMode = st.Mode
	out.DirectAccess = st.DirectAccessible
	out.AcmeV2 = st.AcmeV2

	if op.proxyReader != nil {
		snap, err := op.proxyReader.FetchConfigUncached(ctx)
		if err != nil {
			state, msg := classifyNDMSError(err)
			if state == "forbidden" || state == "read_error" {
				out.State = state
				out.StatusMessage = msg
				return out, nil
			}
		}
		rtMap, _ := op.proxyReader.FetchRuntimeUncached(ctx)
		if snap != nil {
			out.SSLEnabled = snap.SSLEnabled
			for _, e := range snap.Entries {
				portNum, _ := strconv.Atoi(e.UpstreamPort)
				compat, conflictReason := isEntryCompatibleWithIngress(e, dispatcherPort)
				fqdn := e.Name + "." + baseFQDN
				if e.StaticDomain != "" && !e.DomainNDNS {
					fqdn = e.StaticDomain
				}
				rtActive := false
				if rtMap != nil {
					if rt, ok := rtMap[e.Name]; ok {
						if rt.FQDN != "" {
							fqdn = rt.FQDN
						}
						rtActive = rt.NDNS && strings.EqualFold(rt.Allow, "public")
					}
				}
				out.Publications = append(out.Publications, KeenDNSPublicationView{
					Label:                 e.Name,
					FQDN:                  fqdn,
					UpstreamProto:         e.UpstreamProto,
					UpstreamHost:          e.UpstreamHost,
					UpstreamPort:          portNum,
					DomainNDNS:            e.DomainNDNS,
					Public:                e.Public,
					Auth:                  e.Auth,
					CompatibleWithIngress: compat,
					RuntimeActive:         rtActive,
					ConflictReason:        conflictReason,
				})
			}
		}
	}

	return out, nil
}

type canonicalHTTPProxyState struct {
	Name          string         `json:"name"`
	UpstreamProto string         `json:"upstream_proto"`
	UpstreamHost  string         `json:"upstream_host"`
	UpstreamPort  string         `json:"upstream_port"`
	DomainNDNS    bool           `json:"domain_ndns"`
	StaticDomain  string         `json:"static_domain,omitempty"`
	SSLRedirect   bool           `json:"ssl_redirect"`
	Public        bool           `json:"public"`
	Private       bool           `json:"private"`
	Auth          bool           `json:"auth"`
	PreserveHost  bool           `json:"preserve_host"`
	Raw           map[string]any `json:"raw,omitempty"`
}

func normalizeProxyRawMap(rawJSON string) map[string]any {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil || m == nil {
		return map[string]any{"_unparsed": trimmed}
	}
	if authVal, ok := m["auth"]; ok {
		switch av := authVal.(type) {
		case bool:
			if !av {
				delete(m, "auth")
			}
		case map[string]any:
			if noVal, isNo := av["no"].(bool); isNo && noVal && len(av) == 1 {
				delete(m, "auth")
			}
		}
	}
	if upVal, ok := m["upstream"].(map[string]any); ok {
		if pFloat, isNum := upVal["port"].(float64); isNum {
			upVal["port"] = strconv.Itoa(int(pFloat))
		}
	}
	return m
}

func computeEntryCanonicalFingerprint(entry ndmsquery.HTTPProxyEntry) string {
	proto := strings.ToLower(strings.TrimSpace(entry.UpstreamProto))
	if proto == "" {
		proto = "http"
	}
	rawMap := normalizeProxyRawMap(entry.RawJSON)
	if rawMap == nil {
		rawMap = map[string]any{
			"upstream": map[string]any{
				"proto":    proto,
				"upstream": strings.TrimSpace(entry.UpstreamHost),
				"port":     strings.TrimSpace(entry.UpstreamPort),
			},
			"domain":         map[string]any{"ndns": entry.DomainNDNS},
			"ssl":            map[string]any{"redirect": entry.SSLRedirect},
			"security-level": map[string]any{"public": entry.Public},
			"preserve-host":  entry.PreserveHost,
		}
	}
	canon := canonicalHTTPProxyState{
		Name:          strings.ToLower(strings.TrimSpace(entry.Name)),
		UpstreamProto: proto,
		UpstreamHost:  strings.TrimSpace(entry.UpstreamHost),
		UpstreamPort:  strings.TrimSpace(entry.UpstreamPort),
		DomainNDNS:    entry.DomainNDNS,
		StaticDomain:  strings.TrimSpace(entry.StaticDomain),
		SSLRedirect:   entry.SSLRedirect,
		Public:        entry.Public,
		Private:       entry.Private,
		Auth:          entry.Auth,
		PreserveHost:  entry.PreserveHost,
		Raw:           rawMap,
	}
	data, _ := json.Marshal(canon)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func buildExpectedCreatedEntry(label, host string, port int) (string, string) {
	rawMap := map[string]any{
		"upstream": map[string]any{
			"proto":    "http",
			"upstream": host,
			"port":     strconv.Itoa(port),
		},
		"domain":         map[string]any{"ndns": true},
		"ssl":            map[string]any{"redirect": true},
		"security-level": map[string]any{"public": true},
		"preserve-host":  true,
	}
	rawBytes, _ := json.Marshal(rawMap)
	expectedEntry := ndmsquery.HTTPProxyEntry{
		Name:          label,
		UpstreamProto: "http",
		UpstreamHost:  host,
		UpstreamPort:  strconv.Itoa(port),
		DomainNDNS:    true,
		SSLRedirect:   true,
		Public:        true,
		Private:       false,
		Auth:          false,
		PreserveHost:  true,
		RawJSON:       string(rawBytes),
	}
	return string(rawBytes), computeEntryCanonicalFingerprint(expectedEntry)
}

func findEntryByLabel(snap *ndmsquery.HTTPProxyConfigSnapshot, label string) *ndmsquery.HTTPProxyEntry {
	if snap == nil {
		return nil
	}
	for i := range snap.Entries {
		if strings.EqualFold(snap.Entries[i].Name, label) {
			return &snap.Entries[i]
		}
	}
	return nil
}

// PreparePublication validates the candidate, checks KeenDNS readiness and conflicts, and builds the journal snapshot record.
func (op *NDMSKeenDNSOperator) PreparePublication(ctx context.Context, req KeenDNSPublicationCandidate) (KeenDNSJournalRecord, error) {
	if op == nil || op.keendns == nil || op.proxyReader == nil || op.proxyWriter == nil {
		return KeenDNSJournalRecord{}, fmt.Errorf("%w: KeenDNS operator is not configured", ErrKeenDNSUnavailable)
	}
	op.mu.Lock()
	defer op.mu.Unlock()

	label := strings.ToLower(strings.TrimSpace(req.Label))
	host := strings.TrimSpace(req.UpstreamHost)
	if host == "" {
		host = "127.0.0.1"
	}
	port := req.UpstreamPort
	if port <= 0 {
		port = 9009
	}

	allowed := op.allowedPorts
	if len(allowed) == 0 {
		allowed = []int{9009}
	}

	spec := ndmscommand.HTTPProxyPublicationSpec{
		Label:          label,
		UpstreamProto:  "http",
		UpstreamHost:   host,
		UpstreamPort:   port,
		AllowedPorts:   allowed,
		ForbiddenPorts: op.forbiddenPorts(),
	}
	if err := ndmscommand.ValidatePublicationSpec(spec); err != nil {
		return KeenDNSJournalRecord{}, err
	}

	st, err := op.keendns.GetStatus(ctx)
	if err != nil {
		_, msg := classifyNDMSError(err)
		return KeenDNSJournalRecord{}, fmt.Errorf("%w: %s (%v)", ErrKeenDNSUnavailable, msg, err)
	}
	if st == nil || !st.Enabled || strings.TrimSpace(st.Name) == "" || strings.TrimSpace(st.Domain) == "" {
		return KeenDNSJournalRecord{}, fmt.Errorf("%w: KeenDNS не настроен на роутере", ErrKeenDNSUnavailable)
	}

	baseFQDN := strings.TrimSpace(st.FQDN)
	if baseFQDN == "" {
		baseFQDN = strings.TrimSpace(st.Name) + "." + strings.TrimSpace(st.Domain)
	}
	expectedFQDN := label + "." + baseFQDN
	if reqFQDN := strings.ToLower(strings.TrimSpace(req.FQDN)); reqFQDN != "" && !strings.EqualFold(reqFQDN, expectedFQDN) {
		return KeenDNSJournalRecord{}, fmt.Errorf("%w: requested FQDN %q does not match router KeenDNS domain %q", ndmscommand.ErrInvalidKeenDNSLabel, req.FQDN, expectedFQDN)
	}

	snap, err := op.proxyReader.FetchConfigUncached(ctx)
	if err != nil {
		return KeenDNSJournalRecord{}, fmt.Errorf("%w: failed to read current HTTP proxy config: %v", ErrKeenDNSUnavailable, err)
	}

	rec := KeenDNSJournalRecord{
		Label:        label,
		FQDN:         expectedFQDN,
		UpstreamHost: host,
		UpstreamPort: port,
		State:        KeenDNSStatePrepared,
		Services:     append([]string(nil), req.Services...),
	}

	if existing := findEntryByLabel(snap, label); existing != nil {
		compat, reason := isEntryCompatibleWithIngress(*existing, port)
		if !compat {
			return KeenDNSJournalRecord{}, fmt.Errorf("%w: имя %s (%s) уже занято другим правилом на роутере (%s) — выберите другое имя (например, connect, vless или tg)", ErrKeenDNSConflict, label, expectedFQDN, reason)
		}
		preFP := computeEntryCanonicalFingerprint(*existing)
		rec.ActionMode = KeenDNSActionReuse
		rec.ExistedBefore = true
		rec.CreatedByTx = false
		rec.PreConfigFingerprint = preFP
		rec.PreviousRaw = existing.RawJSON
		rec.ExpectedAppliedRaw = existing.RawJSON
		rec.ExpectedPostFingerprint = preFP
		return rec, nil
	}

	expectedRaw, expectedFP := buildExpectedCreatedEntry(label, host, port)
	rec.ActionMode = KeenDNSActionCreate
	rec.ExistedBefore = false
	rec.CreatedByTx = true
	rec.PreConfigFingerprint = KeenDNSPreStateAbsent
	rec.ExpectedAppliedRaw = expectedRaw
	rec.ExpectedPostFingerprint = expectedFP
	return rec, nil
}

// ApplyAndVerifyPublication verifies pre-state under lock, reuses a compatible existing publication read-only
// (without overwriting NDMS or calling SaveConfig), or creates a new publication, verifies readback from
// `/show/rc/ip/http` (and `/show/ip/http/proxy` when available), and flushes startup configuration (`system.configuration.save`).
func (op *NDMSKeenDNSOperator) ApplyAndVerifyPublication(ctx context.Context, rec *KeenDNSJournalRecord) error {
	if op == nil || op.proxyReader == nil || op.proxyWriter == nil || rec == nil {
		return fmt.Errorf("%w: KeenDNS operator or record is nil", ErrKeenDNSUnavailable)
	}
	op.mu.Lock()
	defer op.mu.Unlock()

	allowed := op.allowedPorts
	if len(allowed) == 0 {
		allowed = []int{9009}
	}

	host := strings.TrimSpace(rec.UpstreamHost)
	if host == "" {
		host = "127.0.0.1"
	}
	port := rec.UpstreamPort
	if port <= 0 {
		port = 9009
	}

	spec := ndmscommand.HTTPProxyPublicationSpec{
		Label:          rec.Label,
		UpstreamProto:  "http",
		UpstreamHost:   host,
		UpstreamPort:   port,
		AllowedPorts:   allowed,
		ForbiddenPorts: op.forbiddenPorts(),
	}
	if err := ndmscommand.ValidatePublicationSpec(spec); err != nil {
		return err
	}

	// Step 1: Fresh uncached pre-check of `/show/rc/ip/http` immediately before reuse or write
	preSnap, err := op.proxyReader.FetchConfigUncached(ctx)
	if err != nil {
		if !rec.ExistedBefore && rec.ActionMode != KeenDNSActionReuse {
			rec.WriteAttempted = false
			rec.State = KeenDNSStatePrecheckAborted
		}
		return fmt.Errorf("%w: pre-apply read /show/rc/ip/http failed: %v", ErrKeenDNSUnavailable, err)
	}
	preEntry := findEntryByLabel(preSnap, rec.Label)

	if rec.ExistedBefore || rec.ActionMode == KeenDNSActionReuse {
		// Read-only reuse path: verify the existing publication was not deleted or modified since PreparePublication
		if preEntry == nil {
			return fmt.Errorf("%w: существующая публикация KeenDNS %s была удалена внешне после проверки", ErrKeenDNSConflict, rec.Label)
		}
		compat, reason := isEntryCompatibleWithIngress(*preEntry, port)
		if !compat {
			return fmt.Errorf("%w: существующая публикация KeenDNS %s больше не совместима (%s)", ErrKeenDNSConflict, rec.Label, reason)
		}
		actualPreFP := computeEntryCanonicalFingerprint(*preEntry)
		if rec.PreConfigFingerprint != "" && actualPreFP != rec.PreConfigFingerprint {
			return fmt.Errorf("%w: публикация KeenDNS %s была изменена внешне перед применением", ErrKeenDNSConflict, rec.Label)
		}
		if rec.PreviousRaw != "" && preEntry.RawJSON != rec.PreviousRaw {
			return fmt.Errorf("%w: конфигурация публикации KeenDNS %s была изменена внешне перед применением", ErrKeenDNSConflict, rec.Label)
		}
		if err := op.verifyRuntimeEntry(ctx, rec.Label, host, port, rec.FQDN); err != nil {
			return err
		}
		rec.ActionMode = KeenDNSActionReuse
		rec.State = KeenDNSStateReused
		rec.WriteAttempted = false
		rec.AppliedRaw = preEntry.RawJSON
		rec.AppliedFingerprint = actualPreFP
		return nil
	}

	// Create path: verify the label did NOT appear externally between PreparePublication and ApplyAndVerifyPublication
	if preEntry != nil {
		rec.WriteAttempted = false
		rec.State = KeenDNSStatePrecheckAborted
		compat, reason := isEntryCompatibleWithIngress(*preEntry, port)
		if !compat {
			return fmt.Errorf("%w: имя %s (%s) было занято внешним правилом после подготовки (%s)", ErrKeenDNSConflict, rec.Label, rec.FQDN, reason)
		}
		return fmt.Errorf("%w: имя %s (%s) было создано внешне после подготовки плана — обновите проверку перед применением", ErrKeenDNSConflict, rec.Label, rec.FQDN)
	}

	if rec.ExpectedAppliedRaw == "" || rec.ExpectedPostFingerprint == "" {
		expRaw, expFP := buildExpectedCreatedEntry(rec.Label, host, port)
		rec.ExpectedAppliedRaw = expRaw
		rec.ExpectedPostFingerprint = expFP
	}
	rec.ActionMode = KeenDNSActionCreate
	rec.WriteIntent = true
	rec.State = KeenDNSStateWritePending
	rec.WriteAttempted = true
	if rec.onBeforeWrite != nil {
		if err := rec.onBeforeWrite(rec); err != nil {
			rec.WriteAttempted = false
			rec.State = KeenDNSStatePrecheckAborted
			return fmt.Errorf("persist journal before KeenDNS write: %w", err)
		}
	}

	if err := op.proxyWriter.UpsertPublication(ctx, spec); err != nil {
		rec.State = KeenDNSStateAmbiguous
		return fmt.Errorf("upsert KeenDNS publication %s: %w", rec.Label, err)
	}

	// Readback verification from /show/rc/ip/http
	snap, err := op.proxyReader.FetchConfigUncached(ctx)
	if err != nil {
		rec.State = KeenDNSStateAmbiguous
		return fmt.Errorf("%w: readback /show/rc/ip/http failed: %v", ErrKeenDNSReadbackMismatch, err)
	}
	if snap == nil {
		rec.State = KeenDNSStateAmbiguous
		return fmt.Errorf("%w: /show/rc/ip/http returned empty snapshot after upsert of %s", ErrKeenDNSReadbackMismatch, rec.Label)
	}

	matched := findEntryByLabel(snap, rec.Label)
	if matched == nil {
		rec.State = KeenDNSStateAmbiguous
		return fmt.Errorf("%w: publication %s missing from /show/rc/ip/http after write", ErrKeenDNSReadbackMismatch, rec.Label)
	}
	compat, reason := isEntryCompatibleWithIngress(*matched, port)
	if !compat {
		rec.State = KeenDNSStateAmbiguous
		return fmt.Errorf("%w: publication %s readback state mismatch (%s)", ErrKeenDNSReadbackMismatch, rec.Label, reason)
	}
	actualPostFP := computeEntryCanonicalFingerprint(*matched)
	if rec.ExpectedPostFingerprint != "" && actualPostFP != rec.ExpectedPostFingerprint {
		rec.State = KeenDNSStateAmbiguous
		return fmt.Errorf("%w: publication %s readback fingerprint mismatch (concurrent modification detected)", ErrKeenDNSReadbackMismatch, rec.Label)
	}
	rec.AppliedRaw = matched.RawJSON
	rec.AppliedFingerprint = actualPostFP

	// Optional runtime readback verification from /show/ip/http/proxy
	if err := op.verifyRuntimeEntry(ctx, rec.Label, host, port, rec.FQDN); err != nil {
		rec.State = KeenDNSStateAmbiguous
		return err
	}

	// Persist NDMS startup configuration
	if err := op.proxyWriter.SaveConfig(ctx); err != nil {
		rec.State = KeenDNSStateAmbiguous
		return fmt.Errorf("save NDMS configuration for KeenDNS publication %s: %w", rec.Label, err)
	}

	rec.State = KeenDNSStateApplied
	return nil
}

func (op *NDMSKeenDNSOperator) verifyRuntimeEntry(ctx context.Context, label, host string, port int, fqdn string) error {
	if rtMap, rtErr := op.proxyReader.FetchRuntimeUncached(ctx); rtErr == nil && len(rtMap) > 0 {
		if rt, ok := rtMap[label]; ok {
			expectedUpstream := fmt.Sprintf("%s:%d", host, port)
			if rt.Upstream != "" && rt.Upstream != expectedUpstream {
				return fmt.Errorf("%w: runtime upstream for %s is %s, expected %s", ErrKeenDNSReadbackMismatch, label, rt.Upstream, expectedUpstream)
			}
			if rt.FQDN != "" && fqdn != "" && !strings.EqualFold(rt.FQDN, fqdn) {
				return fmt.Errorf("%w: runtime FQDN for %s is %s, expected %s", ErrKeenDNSReadbackMismatch, label, rt.FQDN, fqdn)
			}
		}
	}
	return nil
}

// RollbackPublication safely reverts or verifies a KeenDNS publication using the journaled snapshot:
//   - If ExistedBefore is true ("reuse"), NEVER deletes or overwrites the entry; verifies it was not externally deleted or edited.
//   - If CreatedByTx is true ("create"), deletes the publication ONLY when exact post-state ownership fingerprint matches,
//     verifies removal via uncached `/show/rc/ip/http` readback, and requires `SaveConfig` to succeed.
func (op *NDMSKeenDNSOperator) RollbackPublication(ctx context.Context, rec KeenDNSJournalRecord) error {
	if op == nil || op.proxyReader == nil || op.proxyWriter == nil || rec.Label == "" {
		return nil
	}
	op.mu.Lock()
	defer op.mu.Unlock()

	if rec.CreatedByTx && !rec.ExistedBefore {
		if !rec.WriteAttempted {
			// No write was ever attempted by this transaction (e.g. aborted before ApplyAndVerifyPublication
			// or rejected by the uncached precheck before UpsertPublication). Leave router state untouched.
			return nil
		}

		snap, err := op.proxyReader.FetchConfigUncached(ctx)
		if err != nil {
			return fmt.Errorf("read /show/rc/ip/http during KeenDNS rollback for %s: %w", rec.Label, err)
		}
		current := findEntryByLabel(snap, rec.Label)

		if current == nil {
			// Absent from running-config, but WriteAttempted == true: must persist startup-config and NOT ignore SaveConfig errors!
			if err := op.proxyWriter.SaveConfig(ctx); err != nil {
				return fmt.Errorf("%w: save NDMS configuration during rollback of absent %s: %v", ErrRecoveryRequired, rec.Label, err)
			}
			verifySnap, err := op.proxyReader.FetchConfigUncached(ctx)
			if err != nil {
				return fmt.Errorf("%w: verify absence of %s after save: %v", ErrRecoveryRequired, rec.Label, err)
			}
			if findEntryByLabel(verifySnap, rec.Label) != nil {
				return fmt.Errorf("%w: KeenDNS publication %s reappeared after save", ErrRecoveryConflict, rec.Label)
			}
			return nil
		}

		// Entry is present on router: in the write_pending / crash / timeout window before post-write readback
		// confirmation (AppliedFingerprint == "" or AppliedRaw == ""), content equality (ExpectedPostFingerprint)
		// does NOT prove origin because NDMS RCI has no CAS or ownership tag. Never auto-delete without confirmed
		// post-write readback; enter safe recovery_required / ErrRecoveryConflict instead.
		if rec.AppliedFingerprint == "" || rec.AppliedRaw == "" {
			return fmt.Errorf("%w: ambiguous KeenDNS write_pending/crash window for %s (entry present on router without confirmed post-write readback)", ErrRecoveryConflict, rec.Label)
		}

		// Confirmed post-write readback exists: verify current state still matches our applied fingerprint and raw JSON before deleting
		compat, reason := isEntryCompatibleWithIngress(*current, rec.UpstreamPort)
		if !compat {
			return fmt.Errorf("%w: concurrent external edit detected on KeenDNS publication %s during rollback (%s)", ErrRecoveryConflict, rec.Label, reason)
		}
		actualFP := computeEntryCanonicalFingerprint(*current)
		if rec.ExpectedPostFingerprint != "" && actualFP != rec.ExpectedPostFingerprint {
			return fmt.Errorf("%w: KeenDNS publication %s fingerprint mismatch during rollback (foreign or modified entry on same port)", ErrRecoveryConflict, rec.Label)
		}
		if actualFP != rec.AppliedFingerprint {
			return fmt.Errorf("%w: KeenDNS publication %s diverges from applied fingerprint during rollback", ErrRecoveryConflict, rec.Label)
		}
		if current.RawJSON != rec.AppliedRaw {
			return fmt.Errorf("%w: KeenDNS publication %s raw config was modified externally after apply", ErrRecoveryConflict, rec.Label)
		}

		if err := op.proxyWriter.DeletePublication(ctx, rec.Label); err != nil {
			return fmt.Errorf("delete KeenDNS publication %s during rollback: %w", rec.Label, err)
		}

		// Readback verification that the entry is actually gone from `/show/rc/ip/http` before saving
		afterSnap, err := op.proxyReader.FetchConfigUncached(ctx)
		if err != nil {
			return fmt.Errorf("%w: readback /show/rc/ip/http after deleting %s failed: %v", ErrRecoveryRequired, rec.Label, err)
		}
		if findEntryByLabel(afterSnap, rec.Label) != nil {
			return fmt.Errorf("%w: KeenDNS publication %s still present in /show/rc/ip/http after delete", ErrRecoveryRequired, rec.Label)
		}

		if err := op.proxyWriter.SaveConfig(ctx); err != nil {
			return fmt.Errorf("%w: save NDMS configuration after deleting %s during rollback: %v", ErrRecoveryRequired, rec.Label, err)
		}
		return nil
	}

	snap, err := op.proxyReader.FetchConfigUncached(ctx)
	if err != nil {
		return fmt.Errorf("read /show/rc/ip/http during KeenDNS rollback for %s: %w", rec.Label, err)
	}
	current := findEntryByLabel(snap, rec.Label)

	// ExistedBefore == true (reused existing publication): NEVER delete and NEVER overwrite with RestorePublicationRaw!
	if current == nil {
		return fmt.Errorf("%w: existing KeenDNS publication %s was deleted externally", ErrRecoveryConflict, rec.Label)
	}
	if rec.WriteAttempted && rec.AppliedRaw == "" {
		return fmt.Errorf("%w: ambiguous crash window for existing KeenDNS publication %s", ErrRecoveryConflict, rec.Label)
	}
	actualFP := computeEntryCanonicalFingerprint(*current)
	if rec.PreConfigFingerprint != "" && actualFP != rec.PreConfigFingerprint {
		return fmt.Errorf("%w: concurrent external edit detected on existing KeenDNS publication %s", ErrRecoveryConflict, rec.Label)
	}
	if current.RawJSON != rec.PreviousRaw && rec.PreviousRaw != "" {
		return fmt.Errorf("%w: concurrent external edit detected on existing KeenDNS publication %s", ErrRecoveryConflict, rec.Label)
	}

	return nil
}

// DeleteIngressPublications safely deletes ONLY the listed KeenDNS 4th-level publications
// that belong to AWGM ingress (matching DomainNDNS, Public, !Auth, http://127.0.0.1:<allowedPort>),
// leaving foreign/router-protected rules untouched, and persists NDMS startup-config if any entry was deleted.
func (op *NDMSKeenDNSOperator) DeleteIngressPublications(ctx context.Context, labels []string, allowedPorts []int) (deleted []string, skipped []string, warnings []string) {
	if op == nil || op.proxyReader == nil || op.proxyWriter == nil {
		return nil, nil, []string{"KeenDNS operator is not configured"}
	}
	if len(labels) == 0 {
		return nil, nil, nil
	}

	op.mu.Lock()
	defer op.mu.Unlock()

	if len(allowedPorts) == 0 {
		allowedPorts = []int{9009}
	}
	forbidden := make(map[int]bool)
	for _, fp := range op.forbiddenPorts() {
		forbidden[fp] = true
	}

	snap, err := op.proxyReader.FetchConfigUncached(ctx)
	if err != nil {
		return nil, nil, []string{fmt.Sprintf("не удалось прочитать публикации KeenDNS перед удалением: %v", err)}
	}
	if snap == nil {
		return nil, nil, nil
	}

	seen := make(map[string]bool, len(labels))
	for _, raw := range labels {
		label := strings.ToLower(strings.TrimSpace(raw))
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true

		entry := findEntryByLabel(snap, label)
		if entry == nil {
			continue
		}

		compatible := false
		lastReason := ""
		for _, port := range allowedPorts {
			if port <= 0 || forbidden[port] {
				continue
			}
			ok, reason := isEntryCompatibleWithIngress(*entry, port)
			if ok {
				compatible = true
				break
			}
			lastReason = reason
		}
		if !compatible {
			if lastReason == "" {
				lastReason = "не принадлежит входящим маршрутам AWGM"
			}
			skipped = append(skipped, fmt.Sprintf("%s (%s)", entry.Name, lastReason))
			continue
		}

		if err := op.proxyWriter.DeletePublication(ctx, entry.Name); err != nil {
			warnings = append(warnings, fmt.Sprintf("не удалось удалить публикацию KeenDNS %s: %v", entry.Name, err))
			continue
		}
		deleted = append(deleted, entry.Name)
	}

	if len(deleted) > 0 {
		if err := op.proxyWriter.SaveConfig(ctx); err != nil {
			warnings = append(warnings, fmt.Sprintf("публикации KeenDNS удалены, но не удалось сохранить конфигурацию роутера: %v", err))
		}
	}

	return deleted, skipped, warnings
}
