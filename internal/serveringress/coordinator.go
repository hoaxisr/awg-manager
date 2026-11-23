package serveringress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type XrayComponent interface {
	GetConfig() vlessserver.Config
	GetStatus() vlessserver.Status
	PrepareCandidate(txID string, candidate vlessserver.Config) (string, error)
	CommitPrepared(txID string) error
	FinalizePrepared(txID string) error
	RollbackPrepared(txID string) error
	Restart() error
}

type DispatcherComponent interface {
	GetConfig() cdndispatcher.Config
	IsRunning() bool
	Start() error
	Stop() error
	ApplyConfig(cfg cdndispatcher.Config) error
	Reconfigure(cfg cdndispatcher.Config) error
	PrepareCandidate(txID string, candidate cdndispatcher.Candidate) (string, error)
	CommitPrepared(txID string) error
	RollbackPrepared(txID string) error
	FinalizePrepared(txID string) error
}

type TgWebProxyComponent interface {
	GetConfig() tgwebproxy.PublicConfig
	GetInternalConfig() tgwebproxy.Config
	GetStatus() tgwebproxy.Status
	ApplyManagedIngress(cfg tgwebproxy.ManagedIngressConfig) error
	UpdateConfig(cfg tgwebproxy.Config) error
	PrepareCandidate(txID string, candidate tgwebproxy.Config) (string, error)
	CommitPrepared(txID string) error
	RollbackPrepared(txID string) error
	FinalizePrepared(txID string) error
}

type FircRoutingComponent interface {
	IsServerRoutingEnabled(serverID string) bool
	PrepareServerRouting(ctx context.Context, txID string, serverID string, enabled bool) (string, error)
	CommitServerRouting(ctx context.Context, txID string) error
	RollbackServerRouting(ctx context.Context, txID string) error
	FinalizeServerRouting(txID string) error
}

type Coordinator struct {
	mu               sync.Mutex
	txMu             sync.Mutex
	dataDir          string
	xraySvc          XrayComponent
	dispatcher       DispatcherComponent
	tgSvc            TgWebProxyComponent
	fircRouting      FircRoutingComponent
	keendns          KeenDNSOperator
	lock             IngressLocker
	recoveryNeeded   bool
	recoveryReason   string
	procDir          string
	dialTimeout      func(network, address string, timeout time.Duration) (net.Conn, error)
	onPhaseChange    func(phase JournalPhase) error
	seamsMu          sync.RWMutex
	writeJournalFn   func(path string, j *TransactionJournal) error
	archiveJournalFn func(srcPath, reason, txID string) error
}

func New(dataDir string, xray XrayComponent, disp DispatcherComponent, tg TgWebProxyComponent) *Coordinator {
	lockPath := filepath.Join(dataDir, "xray", "server-ingress.lock")
	return &Coordinator{
		dataDir:     dataDir,
		xraySvc:     xray,
		dispatcher:  disp,
		tgSvc:       tg,
		lock:        NewFileLock(lockPath),
		procDir:     "/proc",
		dialTimeout: net.DialTimeout,
	}
}

// SetLock configures a custom ingress locker for testing.
func (c *Coordinator) SetLock(l IngressLocker) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lock = l
}

// SetFircRouting wires the FIRC server routing transactional participant.
func (c *Coordinator) SetFircRouting(f FircRoutingComponent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fircRouting = f
}

// SetKeenDNS wires the KeenDNS publication transactional participant.
func (c *Coordinator) SetKeenDNS(k KeenDNSOperator) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keendns = k
}

// GetKeenDNS returns the configured KeenDNS operator, if any.
func (c *Coordinator) GetKeenDNS() KeenDNSOperator {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keendns
}

// SetOnPhaseChange configures a hook callback before committing phase for testing.
func (c *Coordinator) SetOnPhaseChange(fn func(phase JournalPhase) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onPhaseChange = fn
}

func (c *Coordinator) writeJournal(path string, j *TransactionJournal) error {
	c.seamsMu.RLock()
	fn := c.writeJournalFn
	c.seamsMu.RUnlock()
	if fn != nil {
		return fn(path, j)
	}
	return writeJournal(path, j)
}

func (c *Coordinator) archiveJournal(srcPath, reason, txID string) error {
	c.seamsMu.RLock()
	fn := c.archiveJournalFn
	c.seamsMu.RUnlock()
	if fn != nil {
		return fn(srcPath, reason, txID)
	}
	return archiveJournal(srcPath, reason, txID)
}

// SetJournalSeamsForTest configures custom journal write and archive functions for fault injection in tests.
func (c *Coordinator) SetJournalSeamsForTest(writeFn func(path string, j *TransactionJournal) error, archiveFn func(srcPath, reason, txID string) error) {
	c.seamsMu.Lock()
	defer c.seamsMu.Unlock()
	c.writeJournalFn = writeFn
	c.archiveJournalFn = archiveFn
}

// SetProcDir configures a custom procfs root directory for testing.
func (c *Coordinator) SetProcDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.procDir = dir
}

// SetDialTimeout configures a custom dialer function for testing.
func (c *Coordinator) SetDialTimeout(d func(network, address string, timeout time.Duration) (net.Conn, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dialTimeout = d
}

func (c *Coordinator) getProcDir() string {
	if c.procDir != "" {
		return c.procDir
	}
	return "/proc"
}

func (c *Coordinator) getDialTimeout() func(network, address string, timeout time.Duration) (net.Conn, error) {
	if c.dialTimeout != nil {
		return c.dialTimeout
	}
	return net.DialTimeout
}

func (c *Coordinator) setRecovery(needed bool, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recoveryNeeded = needed
	c.recoveryReason = reason
}

// recoveryFlagged — тот же флаг одним значением, для условий.
func (c *Coordinator) recoveryFlagged() bool {
	needed, _ := c.isRecoveryNeeded()
	return needed
}

func (c *Coordinator) isRecoveryNeeded() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recoveryNeeded, c.recoveryReason
}

func (c *Coordinator) CanAutoStart() (bool, string) {
	needed, reason := c.isRecoveryNeeded()
	if needed {
		return false, reason
	}
	return true, ""
}

func (c *Coordinator) journalPath() string {
	return filepath.Join(c.dataDir, "xray", "server-ingress-transaction.json")
}

func (c *Coordinator) IsRecoveryRequired() (bool, string) {
	return c.isRecoveryNeeded()
}

func (c *Coordinator) computeFingerprints() map[string]string {
	fp := make(map[string]string)

	if c.xraySvc != nil {
		data, _ := json.Marshal(c.xraySvc.GetConfig())
		h := sha256.Sum256(data)
		fp["xray"] = hex.EncodeToString(h[:])
	}
	if c.dispatcher != nil {
		data, _ := json.Marshal(c.dispatcher.GetConfig())
		h := sha256.Sum256(data)
		fp["dispatcher"] = hex.EncodeToString(h[:])
	}
	if c.tgSvc != nil {
		data, _ := json.Marshal(c.tgSvc.GetConfig())
		h := sha256.Sum256(data)
		fp["tgwebproxy"] = hex.EncodeToString(h[:])
	}
	if c.fircRouting != nil {
		data := []byte(fmt.Sprintf("vless_xhttp_server:%t", c.fircRouting.IsServerRoutingEnabled("vless_xhttp_server")))
		h := sha256.Sum256(data)
		fp["firc"] = hex.EncodeToString(h[:])
	}
	return fp
}

func (c *Coordinator) getCurrentTopology() IngressTopology {
	topo := IngressTopology{}

	if c.xraySvc != nil {
		xc := c.xraySvc.GetConfig()
		topo.XrayEnabled = xc.Enabled
		topo.XrayOutboundMode = xc.OutboundMode
		topo.XrayAddress = xc.ListenAddress
		if topo.XrayAddress == "" {
			topo.XrayAddress = "127.0.0.1"
		}
		topo.XrayPort = xc.ListenPort
		if topo.XrayPort <= 0 {
			topo.XrayPort = 9008
		}
		topo.XrayPublicHostname = xc.PublicDomain
		topo.XrayPublicPort = xc.PublicPort
		if topo.XrayPublicPort <= 0 {
			topo.XrayPublicPort = 443
		}
		topo.XrayPathPrefix = cdndispatcher.NormalizePathPrefix(xc.Path)
		if topo.PublicHostname == "" {
			topo.PublicHostname = xc.PublicDomain
		}
	}

	if c.dispatcher != nil {
		dc := c.dispatcher.GetConfig()
		topo.DispatcherEnabled = c.dispatcher.IsRunning()
		topo.DispatcherAddress = dc.ListenAddr
		if dc.ListenAddr != "" {
			_, portStr, err := net.SplitHostPort(dc.ListenAddr)
			if err == nil {
				if p, err := strconv.Atoi(portStr); err == nil {
					topo.DispatcherPort = p
				}
			} else if strings.HasPrefix(dc.ListenAddr, ":") {
				if p, err := strconv.Atoi(strings.TrimPrefix(dc.ListenAddr, ":")); err == nil {
					topo.DispatcherPort = p
				}
			}
		}
		if topo.DispatcherPort <= 0 {
			topo.DispatcherPort = 9009
		}
		if dc.PublicHostname != "" && topo.PublicHostname == "" {
			topo.PublicHostname = dc.PublicHostname
		}
	}

	if c.tgSvc != nil {
		tc := c.tgSvc.GetInternalConfig()
		topo.TgEnabled = tc.Enabled
		topo.TgScenario = tc.Scenario
		topo.TgDirectAddress = tc.DirectHost
		topo.TgDirectPort = tc.DirectPort
		if tc.Backend != "" {
			host, portStr, err := net.SplitHostPort(tc.Backend)
			if err == nil {
				topo.TgRawAddress = host
				if p, err := strconv.Atoi(portStr); err == nil {
					topo.TgRawPort = p
				}
			}
		}
		if topo.TgRawAddress == "" {
			topo.TgRawAddress = "127.0.0.1"
		}
		if topo.TgRawPort <= 0 {
			topo.TgRawPort = 2398
		}
		topo.TgWebAddress = "127.0.0.1"
		topo.TgWebPort = tc.ListenPort
		if topo.TgWebPort <= 0 {
			topo.TgWebPort = 8085
		}
		topo.TgPublicHostname = tc.PublicHostname
		topo.TgPort = tc.ListenPort
		if topo.PublicHostname == "" {
			topo.PublicHostname = tc.PublicHostname
		}
	}

	if c.fircRouting != nil {
		topo.FircServerRouting = c.fircRouting.IsServerRoutingEnabled("vless_xhttp_server")
	}

	return topo
}

func (c *Coordinator) calculateAffected(prev, desired IngressTopology) []string {
	var affected []string

	xrayChanged := prev.XrayEnabled != desired.XrayEnabled ||
		prev.XrayAddress != desired.XrayAddress ||
		prev.XrayPort != desired.XrayPort ||
		prev.XrayPathPrefix != desired.XrayPathPrefix ||
		prev.XrayPublicHostname != desired.XrayPublicHostname ||
		prev.XrayPublicPort != desired.XrayPublicPort ||
		prev.XrayOutboundMode != desired.XrayOutboundMode ||
		((prev.XrayEnabled || desired.XrayEnabled) && prev.PublicHostname != desired.PublicHostname)

	if xrayChanged {
		affected = append(affected, "xray")
	}

	if c.fircRouting != nil && (prev.FircServerRouting != desired.FircServerRouting || prev.XrayOutboundMode != desired.XrayOutboundMode || desired.XrayOutboundMode == "router") {
		affected = append(affected, "firc")
	}

	tgChanged := prev.TgEnabled != desired.TgEnabled ||
		prev.TgScenario != desired.TgScenario ||
		prev.TgDirectAddress != desired.TgDirectAddress ||
		prev.TgDirectPort != desired.TgDirectPort ||
		prev.TgRawAddress != desired.TgRawAddress ||
		prev.TgRawPort != desired.TgRawPort ||
		prev.TgWebAddress != desired.TgWebAddress ||
		prev.TgWebPort != desired.TgWebPort ||
		prev.TgPublicHostname != desired.TgPublicHostname ||
		prev.TgPort != desired.TgPort ||
		((prev.TgEnabled || desired.TgEnabled) && prev.PublicHostname != desired.PublicHostname)

	if tgChanged || (xrayChanged && desired.TgEnabled && (desired.TgScenario == tgwebproxy.ScenarioCDNHTTP || desired.TgScenario == tgwebproxy.ScenarioDual)) {
		affected = append(affected, "tgwebproxy")
	}

	dispChanged := prev.DispatcherEnabled != desired.DispatcherEnabled ||
		prev.DispatcherPort != desired.DispatcherPort ||
		prev.DispatcherAddress != desired.DispatcherAddress ||
		prev.PublicHostname != desired.PublicHostname ||
		prev.XrayPublicHostname != desired.XrayPublicHostname ||
		prev.TgPublicHostname != desired.TgPublicHostname ||
		prev.XrayPathPrefix != desired.XrayPathPrefix ||
		prev.XrayAddress != desired.XrayAddress ||
		prev.XrayPort != desired.XrayPort ||
		prev.TgWebPort != desired.TgWebPort ||
		prev.TgPort != desired.TgPort

	if dispChanged {
		affected = append(affected, "dispatcher")
	}

	return affected
}

func (c *Coordinator) withIngressLock(txID string, fn func() error) (retErr error) {
	c.txMu.Lock()
	defer c.txMu.Unlock()

	if err := c.lock.Lock(txID); err != nil {
		return err
	}
	defer func() {
		if err := c.lock.Unlock(); err != nil {
			c.setRecovery(true, fmt.Sprintf("unlock failed for tx %s: %v", txID, err))
			unlockErr := fmt.Errorf("unlock ingress transaction %s: %w", txID, err)
			if retErr != nil {
				retErr = errors.Join(retErr, unlockErr)
			} else {
				retErr = unlockErr
			}
		}
	}()

	return fn()
}

// WithIngressLock acquires the shared server ingress lock and executes fn.
func (c *Coordinator) WithIngressLock(txID string, fn func() error) error {
	return c.withIngressLock(txID, fn)
}

// StartupRecovery checks for and recovers pending transactions after an unexpected restart.
func (c *Coordinator) StartupRecovery(ctx context.Context) error {
	txID := fmt.Sprintf("ing-rec-%d-%d", time.Now().UnixNano(), os.Getpid())
	return c.withIngressLock(txID, func() error {
		return c.recoverIngressJournalLocked(ctx)
	})
}

func (c *Coordinator) recoverIngressJournalLocked(ctx context.Context) error {
	jPath := c.journalPath()
	j, err := readJournal(jPath)
	if err != nil {
		c.setRecovery(true, fmt.Sprintf("corrupt journal: %v", err))
		return ErrCorruptJournal
	}
	if j == nil {
		// No active journal, everything clean
		return nil
	}

	if j.Phase == PhaseCommitted {
		if len(j.Fingerprints) > 0 {
			currentFP := c.computeFingerprints()
			for k, expected := range j.Fingerprints {
				if actual, ok := currentFP[k]; ok && actual != expected {
					c.setRecovery(true, fmt.Sprintf("fingerprint mismatch for component %s: expected %s, got %s", k, expected, actual))
					return ErrRecoveryConflict
				}
			}
		}

		// At PhaseCommitted, verify idempotent finalization and archive journal. NEVER rolls back!
		var finErrs []error
		if c.xraySvc != nil {
			xrayTxID := j.ComponentTransactionIDs["xray"]
			if xrayTxID == "" {
				xrayTxID = j.TransactionID
			}
			if err := c.xraySvc.FinalizePrepared(xrayTxID); err != nil {
				finErrs = append(finErrs, err)
			}
		}
		if c.tgSvc != nil {
			tgTxID := j.ComponentTransactionIDs["tgwebproxy"]
			if tgTxID == "" {
				tgTxID = j.TransactionID
			}
			if err := c.tgSvc.FinalizePrepared(tgTxID); err != nil {
				finErrs = append(finErrs, err)
			}
		}
		if c.dispatcher != nil {
			dispTxID := j.ComponentTransactionIDs["dispatcher"]
			if dispTxID == "" {
				dispTxID = j.TransactionID
			}
			if err := c.dispatcher.FinalizePrepared(dispTxID); err != nil {
				finErrs = append(finErrs, err)
			}
		}
		if c.fircRouting != nil {
			if fircTxID := j.ComponentTransactionIDs["firc"]; fircTxID != "" {
				if err := c.fircRouting.FinalizeServerRouting(fircTxID); err != nil {
					finErrs = append(finErrs, err)
				}
			}
		}
		if len(finErrs) > 0 {
			c.setRecovery(true, fmt.Sprintf("recovery finalize in PhaseCommitted failed: %v", errors.Join(finErrs...)))
			return fmt.Errorf("%w: recovery finalize in PhaseCommitted failed: %v", ErrRecoveryRequired, errors.Join(finErrs...))
		}

		if err := c.archiveJournal(jPath, "committed", j.TransactionID); err != nil {
			c.setRecovery(true, fmt.Sprintf("archive committed journal failed: %v", err))
			return fmt.Errorf("%w: %v", ErrRecoveryRequired, err)
		}
		return nil
	}

	if j.Phase == PhaseCommitting {
		// Past point of no return: candidate is ALREADY active on disk. Recovery ONLY calls FinalizePrepared, NEVER CommitPrepared!
		var finErrs []error
		if c.xraySvc != nil {
			xrayTxID := j.ComponentTransactionIDs["xray"]
			if xrayTxID == "" {
				xrayTxID = j.TransactionID
			}
			if err := c.xraySvc.FinalizePrepared(xrayTxID); err != nil {
				finErrs = append(finErrs, fmt.Errorf("finalize xray (%s): %w", xrayTxID, err))
			}
		}
		if c.tgSvc != nil {
			tgTxID := j.ComponentTransactionIDs["tgwebproxy"]
			if tgTxID == "" {
				tgTxID = j.TransactionID
			}
			if err := c.tgSvc.FinalizePrepared(tgTxID); err != nil {
				finErrs = append(finErrs, fmt.Errorf("finalize tgwebproxy (%s): %w", tgTxID, err))
			}
		}
		if c.dispatcher != nil {
			dispTxID := j.ComponentTransactionIDs["dispatcher"]
			if dispTxID == "" {
				dispTxID = j.TransactionID
			}
			if err := c.dispatcher.FinalizePrepared(dispTxID); err != nil {
				finErrs = append(finErrs, fmt.Errorf("finalize dispatcher (%s): %w", dispTxID, err))
			}
		}
		if c.fircRouting != nil {
			if fircTxID := j.ComponentTransactionIDs["firc"]; fircTxID != "" {
				if err := c.fircRouting.FinalizeServerRouting(fircTxID); err != nil {
					finErrs = append(finErrs, fmt.Errorf("finalize firc (%s): %w", fircTxID, err))
				}
			}
		}

		if len(finErrs) > 0 {
			c.setRecovery(true, fmt.Sprintf("recovery finalize in PhaseCommitting failed: %v", errors.Join(finErrs...)))
			return fmt.Errorf("%w: %v", ErrRecoveryRequired, errors.Join(finErrs...))
		}

		j.Phase = PhaseCommitted
		j.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		j.Checksum = computeJournalChecksum(j)
		if err := c.writeJournal(jPath, j); err != nil {
			c.setRecovery(true, fmt.Sprintf("write committed journal in recovery failed: %v", err))
			return fmt.Errorf("%w: %v", ErrRecoveryRequired, err)
		}

		if err := c.archiveJournal(jPath, "committed", j.TransactionID); err != nil {
			c.setRecovery(true, fmt.Sprintf("archive roll-forward committed journal failed: %v", err))
			return fmt.Errorf("%w: %v", ErrRecoveryRequired, err)
		}
		return nil
	}

	// Prior to PhaseCommitting: reverse rollback Dispatcher -> Telegram -> Xray
	if err := c.failAndRollback(j, errors.New("startup recovery rolled back uncommitted transaction")); err != nil {
		if c.recoveryFlagged() {
			return err
		}
	}
	return nil
}

// Apply atomically coordinates topology changes across Xray, Dispatcher, and Telegram Web Proxy.
func (c *Coordinator) Apply(ctx context.Context, desired IngressTopology) error {
	txID := fmt.Sprintf("ing-%d-%d", time.Now().UnixNano(), os.Getpid())
	return c.withIngressLock(txID, func() error {
		if c.recoveryFlagged() {
			return ErrRecoveryRequired
		}
		return c.applyLocked(ctx, txID, desired, nil, nil)
	})
}

func validateTopologyAndChains(desired IngressTopology, candXray *vlessserver.Config, candTg *tgwebproxy.Config) error {
	used := make(map[int]string)
	reserve := func(port int, owner string) error {
		if port <= 0 {
			return nil
		}
		if prev, exists := used[port]; exists {
			return fmt.Errorf("topology port conflict on :%d between %s and %s", port, prev, owner)
		}
		used[port] = owner
		return nil
	}

	if desired.XrayEnabled {
		xp := desired.XrayPort
		if xp <= 0 {
			xp = 9008
		}
		if err := reserve(xp, "VLESS (XHTTP)"); err != nil {
			return err
		}
	}
	if desired.DispatcherEnabled {
		dp := desired.DispatcherPort
		if dp <= 0 {
			dp = 9009
		}
		if err := reserve(dp, "CDN Dispatcher"); err != nil {
			return err
		}
	}
	if desired.TgEnabled {
		scen := desired.TgScenario
		if scen == "" {
			scen = tgwebproxy.ScenarioDual
		}
		if scen == tgwebproxy.ScenarioDirectFakeTLS || scen == tgwebproxy.ScenarioDual {
			directPort := desired.TgDirectPort
			if directPort <= 0 {
				directPort = tgwebproxy.DefaultDirectPort
			}
			if err := reserve(directPort, "Telegram MTProto Direct"); err != nil {
				return err
			}
		}
		if scen == tgwebproxy.ScenarioCDNHTTP || scen == tgwebproxy.ScenarioDual {
			webPort := desired.TgWebPort
			if webPort <= 0 {
				webPort = desired.TgPort
			}
			if webPort <= 0 {
				webPort = tgwebproxy.DefaultListenPort
			}
			if err := reserve(webPort, "Telegram Web Proxy Mux"); err != nil {
				return err
			}
			if candTg != nil {
				if err := tgwebproxy.ValidatePortsAndUpstreams(*candTg); err != nil {
					return err
				}
				for i, ep := range candTg.GetEndpoints() {
					if !ep.Enabled || ep.ExternalBackend {
						continue
					}
					epListen := ep.ListenPort
					if epListen <= 0 {
						epListen = 8084
					}
					if prev, exists := used[epListen]; exists {
						return fmt.Errorf("topology port conflict on :%d between %s and tproxy endpoint %d", epListen, prev, i+1)
					}
					epAdmin := ep.AdminPort
					if epAdmin <= 0 {
						epAdmin = 8086
					}
					if prev, exists := used[epAdmin]; exists {
						return fmt.Errorf("topology port conflict on :%d between %s and tproxy endpoint %d admin", epAdmin, prev, i+1)
					}
				}
			} else {
				for port, label := range map[int]string{
					tgwebproxy.DefaultRawPort:   "Telegram MTProto raw",
					8084:                        "Telegram tproxy endpoint",
					tgwebproxy.DefaultAdminPort: "Telegram tproxy admin",
				} {
					if err := reserve(port, label); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (c *Coordinator) applyLocked(ctx context.Context, txID string, desired IngressTopology, candidateXray *vlessserver.Config, candidateTg *tgwebproxy.Config) error {
	prev := c.getCurrentTopology()
	if candidateXray != nil {
		desired.XrayOutboundMode = candidateXray.OutboundMode
		desired.FircServerRouting = candidateXray.Enabled && candidateXray.OutboundMode == "router"
	}
	if err := validateTopologyAndChains(desired, candidateXray, candidateTg); err != nil {
		return err
	}
	affected := c.calculateAffected(prev, desired)
	if candidateXray != nil && !contains(affected, "xray") {
		affected = append(affected, "xray")
	}
	if candidateXray != nil && len(candidateXray.Endpoints) > 0 && c.dispatcher != nil && !contains(affected, "dispatcher") {
		affected = append(affected, "dispatcher")
	}
	if candidateXray != nil && c.fircRouting != nil && !contains(affected, "firc") {
		if candidateXray.OutboundMode == "router" || prev.FircServerRouting != desired.FircServerRouting {
			affected = append(affected, "firc")
		}
	}
	if candidateTg != nil && !contains(affected, "tgwebproxy") {
		affected = append(affected, "tgwebproxy")
	}
	if len(affected) == 0 {
		return nil
	}

	jPath := c.journalPath()
	journal := &TransactionJournal{
		Version:                 1,
		TransactionID:           txID,
		Phase:                   PhaseStaged,
		CreatedAt:               time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:               time.Now().UTC().Format(time.RFC3339),
		Desired:                 desired,
		Previous:                prev,
		Affected:                affected,
		ComponentTransactionIDs: make(map[string]string),
	}
	journal.Checksum = computeJournalChecksum(journal)

	if err := c.writeJournal(jPath, journal); err != nil {
		return fmt.Errorf("write staged journal: %w", err)
	}

	// Step 1: Prepare Phase
	if contains(affected, "xray") && c.xraySvc != nil {
		var candidate vlessserver.Config
		if candidateXray != nil {
			candidate = *candidateXray
		} else {
			candidate = c.xraySvc.GetConfig()
			candidate.Enabled = desired.XrayEnabled
			if desired.XrayAddress != "" {
				candidate.ListenAddress = desired.XrayAddress
			}
			if desired.XrayPort > 0 {
				candidate.ListenPort = desired.XrayPort
			}
			if desired.XrayPathPrefix != "" {
				candidate.Path = desired.XrayPathPrefix
			}
			if desired.XrayPublicHostname != "" {
				candidate.PublicDomain = desired.XrayPublicHostname
			} else if desired.PublicHostname != "" {
				candidate.PublicDomain = desired.PublicHostname
			}
			if desired.XrayPublicPort > 0 {
				candidate.PublicPort = desired.XrayPublicPort
			}
		}

		xrayTxID, err := c.xraySvc.PrepareCandidate(txID, candidate)
		if err != nil {
			return c.failAndRollback(journal, fmt.Errorf("prepare xray candidate: %w", err))
		}
		journal.ComponentTransactionIDs["xray"] = xrayTxID
		journal.Checksum = computeJournalChecksum(journal)
		if err := c.writeJournal(jPath, journal); err != nil {
			return c.failAndRollback(journal, fmt.Errorf("write staged journal with xray tx id: %w", err))
		}
	}

	if contains(affected, "tgwebproxy") && c.tgSvc != nil {
		var candidate tgwebproxy.Config
		coverUpstream := ""
		if desired.XrayEnabled {
			xrayAddr := desired.XrayAddress
			if xrayAddr == "" {
				xrayAddr = "127.0.0.1"
			}
			xrayPort := desired.XrayPort
			if xrayPort <= 0 {
				xrayPort = 9008
			}
			coverUpstream = fmt.Sprintf("http://%s:%d", xrayAddr, xrayPort)
		}
		if candidateTg != nil {
			candidate = *candidateTg
			candidate.CoverUpstream = coverUpstream
		} else {
			candidate = c.tgSvc.GetInternalConfig()
			candidate.Enabled = desired.TgEnabled
			candidate.CoverUpstream = coverUpstream
			if desired.TgScenario != "" {
				candidate.Scenario = desired.TgScenario
			}
			if desired.TgDirectAddress != "" {
				candidate.DirectHost = desired.TgDirectAddress
			}
			if desired.TgDirectPort > 0 {
				candidate.DirectPort = desired.TgDirectPort
			}
			rawHost := desired.TgRawAddress
			if rawHost == "" {
				rawHost = "127.0.0.1"
			}
			rawPort := desired.TgRawPort
			if rawPort <= 0 {
				rawPort = 2398
			}
			candidate.Backend = fmt.Sprintf("%s:%d", rawHost, rawPort)

			if desired.TgWebPort > 0 {
				candidate.ListenPort = desired.TgWebPort
			} else if desired.TgPort > 0 {
				candidate.ListenPort = desired.TgPort
			}
			if desired.TgScenario == "direct_fake_tls" {
				candidate.PublicHostname = ""
			} else {
				candidate.PublicHostname = desired.TgPublicHostname
				if candidate.PublicHostname == "" && desired.PublicHostname != "" {
					candidate.PublicHostname = desired.PublicHostname
				}
			}
		}

		tgTxID, err := c.tgSvc.PrepareCandidate(txID, candidate)
		if err != nil {
			return c.failAndRollback(journal, fmt.Errorf("prepare tg candidate: %w", err))
		}
		journal.ComponentTransactionIDs["tgwebproxy"] = tgTxID
		journal.Checksum = computeJournalChecksum(journal)
		if err := c.writeJournal(jPath, journal); err != nil {
			return c.failAndRollback(journal, fmt.Errorf("write staged journal with tg tx id: %w", err))
		}
	}

	if contains(affected, "dispatcher") && c.dispatcher != nil {
		dispPort := desired.DispatcherPort
		if dispPort <= 0 {
			dispPort = 9009
		}
		xrayAddr := desired.XrayAddress
		if xrayAddr == "" {
			xrayAddr = "127.0.0.1"
		}
		xrayPort := desired.XrayPort
		if xrayPort <= 0 {
			xrayPort = 9008
		}
		tgAddr := desired.TgWebAddress
		if tgAddr == "" {
			tgAddr = "127.0.0.1"
		}
		tgPort := desired.TgWebPort
		if tgPort <= 0 {
			tgPort = desired.TgPort
		}
		if tgPort <= 0 {
			tgPort = 8085
		}

		existingDisp := c.dispatcher.GetConfig()
		dispDefaultAction := strings.TrimSpace(existingDisp.DefaultAction)
		if dispDefaultAction == "" {
			dispDefaultAction = "404"
		}
		var effXray vlessserver.Config
		if candidateXray != nil {
			effXray = *candidateXray
		} else if c.xraySvc != nil {
			effXray = c.xraySvc.GetConfig()
		}
		dispCandidate := cdndispatcher.Candidate{
			Enabled: desired.DispatcherEnabled,
			Config: cdndispatcher.Config{
				Enabled:                desired.DispatcherEnabled,
				ListenAddr:             fmt.Sprintf(":%d", dispPort),
				XrayTarget:             fmt.Sprintf("http://%s:%d", xrayAddr, xrayPort),
				TgTarget:               fmt.Sprintf("http://%s:%d", tgAddr, tgPort),
				Routes:                 mergeTopologyRoutesWithEndpoints(existingDisp.Routes, desired, effXray.Endpoints),
				DefaultAction:          dispDefaultAction,
				PublicHostname:         desired.PublicHostname,
				XrayPublicHost:         desired.XrayPublicHostname,
				TgPublicHost:           desired.TgPublicHostname,
				XrayPathPrefix:         desired.XrayPathPrefix,
				XrayAdditionalPrefixes: xrayAdditionalPrefixes(effXray, desired.XrayPathPrefix),
			},
		}

		dispTxID, err := c.dispatcher.PrepareCandidate(txID, dispCandidate)
		if err != nil {
			return c.failAndRollback(journal, fmt.Errorf("prepare dispatcher candidate: %w", err))
		}
		journal.ComponentTransactionIDs["dispatcher"] = dispTxID
		journal.Checksum = computeJournalChecksum(journal)
		if err := c.writeJournal(jPath, journal); err != nil {
			return c.failAndRollback(journal, fmt.Errorf("write staged journal with dispatcher tx id: %w", err))
		}
	}

	if contains(affected, "firc") && c.fircRouting != nil {
		fircTxID, err := c.fircRouting.PrepareServerRouting(ctx, txID, "vless_xhttp_server", desired.FircServerRouting)
		if err != nil {
			return c.failAndRollback(journal, fmt.Errorf("prepare firc server routing: %w", err))
		}
		journal.ComponentTransactionIDs["firc"] = fircTxID
		journal.Checksum = computeJournalChecksum(journal)
		if err := c.writeJournal(jPath, journal); err != nil {
			return c.failAndRollback(journal, fmt.Errorf("write staged journal with firc tx id: %w", err))
		}
	}

	// Step 2: Candidate Activation (CommitPrepared)
	if contains(affected, "xray") && c.xraySvc != nil {
		xrayTxID := journal.ComponentTransactionIDs["xray"]
		if xrayTxID != "" {
			if err := c.xraySvc.CommitPrepared(xrayTxID); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("activate xray candidate: %w", err))
			}
		}
	}

	if contains(affected, "tgwebproxy") && c.tgSvc != nil {
		tgTxID := journal.ComponentTransactionIDs["tgwebproxy"]
		if tgTxID != "" {
			if err := c.tgSvc.CommitPrepared(tgTxID); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("activate tg candidate: %w", err))
			}
		}
	}

	if contains(affected, "dispatcher") && c.dispatcher != nil {
		dispTxID := journal.ComponentTransactionIDs["dispatcher"]
		if dispTxID != "" {
			if err := c.dispatcher.CommitPrepared(dispTxID); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("activate dispatcher candidate: %w", err))
			}
		}
	}

	if contains(affected, "firc") && c.fircRouting != nil {
		fircTxID := journal.ComponentTransactionIDs["firc"]
		if fircTxID != "" {
			if err := c.fircRouting.CommitServerRouting(ctx, fircTxID); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("activate firc server routing: %w", err))
			}
		}
	}

	journal.Phase = PhaseCandidateActive
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	journal.Checksum = computeJournalChecksum(journal)
	if err := c.writeJournal(jPath, journal); err != nil {
		return c.failAndRollback(journal, fmt.Errorf("write candidate_active journal: %w", err))
	}

	// Step 3: Committing Phase (Point of No Return)
	if c.onPhaseChange != nil {
		if err := c.onPhaseChange(PhaseCommitting); err != nil {
			return c.failAndRollback(journal, fmt.Errorf("onPhaseChange hook failed before committing: %w", err))
		}
	}

	journal.Phase = PhaseCommitting
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	journal.Checksum = computeJournalChecksum(journal)
	if err := c.writeJournal(jPath, journal); err != nil {
		return c.failAndRollback(journal, fmt.Errorf("write committing journal: %w", err))
	}

	// Step 4: Finalize Components
	var finErrs []error
	if contains(affected, "xray") && c.xraySvc != nil {
		if id := journal.ComponentTransactionIDs["xray"]; id != "" {
			if err := c.xraySvc.FinalizePrepared(id); err != nil {
				finErrs = append(finErrs, fmt.Errorf("finalize xray: %w", err))
			}
		}
	}
	if contains(affected, "tgwebproxy") && c.tgSvc != nil {
		if id := journal.ComponentTransactionIDs["tgwebproxy"]; id != "" {
			if err := c.tgSvc.FinalizePrepared(id); err != nil {
				finErrs = append(finErrs, fmt.Errorf("finalize tgwebproxy: %w", err))
			}
		}
	}
	if contains(affected, "dispatcher") && c.dispatcher != nil {
		if id := journal.ComponentTransactionIDs["dispatcher"]; id != "" {
			if err := c.dispatcher.FinalizePrepared(id); err != nil {
				finErrs = append(finErrs, fmt.Errorf("finalize dispatcher: %w", err))
			} else {
				_ = c.saveDispatcherConfigLocked(c.dispatcher.GetConfig())
			}
		}
	}
	if contains(affected, "firc") && c.fircRouting != nil {
		if id := journal.ComponentTransactionIDs["firc"]; id != "" {
			if err := c.fircRouting.FinalizeServerRouting(id); err != nil {
				finErrs = append(finErrs, fmt.Errorf("finalize firc: %w", err))
			}
		}
	}
	if len(finErrs) > 0 {
		c.setRecovery(true, fmt.Sprintf("finalize components failed: %v", errors.Join(finErrs...)))
		return fmt.Errorf("%w: finalize components failed: %v", ErrRecoveryRequired, errors.Join(finErrs...))
	}

	// Step 5: Committed Phase (ONLY after all FinalizePrepared succeed!)
	journal.Phase = PhaseCommitted
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	journal.Fingerprints = c.computeFingerprints()
	journal.Checksum = computeJournalChecksum(journal)
	if err := c.writeJournal(jPath, journal); err != nil {
		c.setRecovery(true, fmt.Sprintf("write committed journal failed: %v", err))
		return fmt.Errorf("%w: write committed journal: %v", ErrRecoveryRequired, err)
	}

	// Step 6: Archive committed journal
	if err := c.archiveJournal(jPath, "committed", txID); err != nil {
		c.setRecovery(true, fmt.Sprintf("archive committed journal failed: %v", err))
		return fmt.Errorf("%w: archive journal: %v", ErrRecoveryRequired, err)
	}

	return nil
}

func (c *Coordinator) failAndRollback(j *TransactionJournal, origErr error) error {
	jPath := c.journalPath()
	j.Phase = PhaseRollingBack
	j.Error = origErr.Error()
	j.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	j.Checksum = computeJournalChecksum(j)

	var persistErrs []error
	if err := c.writeJournal(jPath, j); err != nil {
		persistErrs = append(persistErrs, fmt.Errorf("write rolling_back journal: %w", err))
	}

	var rollbackErrs []error

	// Reverse order: KeenDNS -> FIRC -> Dispatcher -> Telegram -> Xray
	if contains(j.Affected, "keendns") && j.Desired.KeenDNS != nil && c.keendns != nil {
		if err := c.keendns.RollbackPublication(context.Background(), *j.Desired.KeenDNS); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback keendns (%s): %w", j.Desired.KeenDNS.Label, err))
		}
	}

	if fircTxID := j.ComponentTransactionIDs["firc"]; fircTxID != "" && c.fircRouting != nil {
		if err := c.fircRouting.RollbackServerRouting(context.Background(), fircTxID); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback firc (%s): %w", fircTxID, err))
		}
	}

	if dispTxID := j.ComponentTransactionIDs["dispatcher"]; dispTxID != "" && c.dispatcher != nil {
		if err := c.dispatcher.RollbackPrepared(dispTxID); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback dispatcher (%s): %w", dispTxID, err))
		}
	} else if contains(j.Affected, "dispatcher") && c.dispatcher != nil {
		prevDisp := cdndispatcher.Config{
			ListenAddr:     fmt.Sprintf(":%d", j.Previous.DispatcherPort),
			XrayTarget:     fmt.Sprintf("http://%s:%d", j.Previous.XrayAddress, j.Previous.XrayPort),
			TgTarget:       fmt.Sprintf("http://127.0.0.1:%d", j.Previous.TgPort),
			PublicHostname: j.Previous.PublicHostname,
			XrayPathPrefix: j.Previous.XrayPathPrefix,
		}
		if err := c.dispatcher.ApplyConfig(prevDisp); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback dispatcher apply config: %w", err))
		}
		if j.Previous.DispatcherEnabled && !c.dispatcher.IsRunning() {
			if err := c.dispatcher.Start(); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback dispatcher start: %w", err))
			}
		} else if !j.Previous.DispatcherEnabled && c.dispatcher.IsRunning() {
			if err := c.dispatcher.Stop(); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback dispatcher stop: %w", err))
			}
		}
	}

	if tgTxID := j.ComponentTransactionIDs["tgwebproxy"]; tgTxID != "" && c.tgSvc != nil {
		if err := c.tgSvc.RollbackPrepared(tgTxID); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback tgwebproxy (%s): %w", tgTxID, err))
		}
	} else if contains(j.Affected, "tgwebproxy") && c.tgSvc != nil {
		prevTG := tgwebproxy.ManagedIngressConfig{
			Enabled:        j.Previous.TgEnabled,
			ListenPort:     j.Previous.TgPort,
			PublicHostname: j.Previous.PublicHostname,
		}
		if err := c.tgSvc.ApplyManagedIngress(prevTG); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback tgwebproxy: %w", err))
		}
	}

	if xrayTxID := j.ComponentTransactionIDs["xray"]; xrayTxID != "" && c.xraySvc != nil {
		if err := c.xraySvc.RollbackPrepared(xrayTxID); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback xray (%s): %w", xrayTxID, err))
		}
	}

	if len(rollbackErrs) > 0 {
		c.setRecovery(true, fmt.Sprintf("rollback failed for components: %v", errors.Join(rollbackErrs...)))
		j.Error = fmt.Sprintf("%v; rollback errors: %v", origErr, errors.Join(rollbackErrs...))
		j.Checksum = computeJournalChecksum(j)
		if err := c.writeJournal(jPath, j); err != nil {
			persistErrs = append(persistErrs, fmt.Errorf("write rolling_back error journal: %w", err))
		}
		allErrs := append([]error{ErrRecoveryRequired, origErr}, rollbackErrs...)
		allErrs = append(allErrs, persistErrs...)
		return errors.Join(allErrs...)
	}

	if err := c.archiveJournal(jPath, "failed", j.TransactionID); err != nil {
		persistErrs = append(persistErrs, fmt.Errorf("archive failed journal: %w", err))
	}

	if len(persistErrs) > 0 {
		c.setRecovery(true, fmt.Sprintf("rollback persistence failed: %v", errors.Join(persistErrs...)))
		return errors.Join(append([]error{ErrRecoveryRequired, origErr}, persistErrs...)...)
	}

	return origErr
}

func (c *Coordinator) rollback(j *TransactionJournal, xrayTxID string, origErr error) error {
	return c.failAndRollback(j, origErr)
}

func (c *Coordinator) topologyForXrayConfig(cfg vlessserver.Config) IngressTopology {
	prev := c.getCurrentTopology()
	desired := prev

	desired.XrayEnabled = cfg.Enabled
	desired.XrayOutboundMode = cfg.OutboundMode
	desired.FircServerRouting = cfg.Enabled && cfg.OutboundMode == "router"
	if cfg.ListenAddress != "" {
		desired.XrayAddress = cfg.ListenAddress
	} else if desired.XrayAddress == "" {
		desired.XrayAddress = "127.0.0.1"
	}
	if cfg.ListenPort > 0 {
		desired.XrayPort = cfg.ListenPort
	} else if desired.XrayPort <= 0 {
		desired.XrayPort = 9008
	}
	desired.XrayPublicHostname = cfg.PublicDomain
	if cfg.PublicDomain != "" {
		desired.PublicHostname = cfg.PublicDomain
	} else if prev.TgPublicHostname == "" || prev.PublicHostname == prev.XrayPublicHostname {
		desired.PublicHostname = ""
		desired.TgPublicHostname = ""
	}
	if cfg.PublicPort > 0 {
		desired.XrayPublicPort = cfg.PublicPort
	} else if desired.XrayPublicPort <= 0 {
		desired.XrayPublicPort = 443
	}
	if cfg.Path != "" {
		desired.XrayPathPrefix = cdndispatcher.NormalizePathPrefix(cfg.Path)
	} else if desired.XrayPathPrefix == "" {
		desired.XrayPathPrefix = cdndispatcher.NormalizePathPrefix("")
	}
	if cfg.DispatcherPort > 0 {
		desired.DispatcherPort = cfg.DispatcherPort
	} else if desired.DispatcherPort <= 0 {
		desired.DispatcherPort = 9009
	}

	// Dispatcher needed check
	desired.DispatcherEnabled = (desired.XrayEnabled && desired.XrayPublicHostname != "") ||
		(desired.TgEnabled && (desired.TgScenario == "cdn_http" || desired.TgScenario == "dual"))
	return desired
}

func (c *Coordinator) topologyForTelegramConfig(cfg tgwebproxy.Config) IngressTopology {
	prev := c.getCurrentTopology()
	desired := prev

	desired.TgEnabled = cfg.Enabled
	if cfg.Scenario != "" {
		desired.TgScenario = cfg.Scenario
	}
	if cfg.DirectHost != "" {
		desired.TgDirectAddress = cfg.DirectHost
	}
	if cfg.DirectPort > 0 {
		desired.TgDirectPort = cfg.DirectPort
	}
	if cfg.Backend != "" {
		host, portStr, err := net.SplitHostPort(cfg.Backend)
		if err == nil {
			desired.TgRawAddress = host
			if p, err := strconv.Atoi(portStr); err == nil {
				desired.TgRawPort = p
			}
		}
	}
	if desired.TgRawAddress == "" {
		desired.TgRawAddress = "127.0.0.1"
	}
	if desired.TgRawPort <= 0 {
		desired.TgRawPort = 2398
	}
	desired.TgWebAddress = "127.0.0.1"
	if cfg.ListenPort > 0 {
		desired.TgWebPort = cfg.ListenPort
		desired.TgPort = cfg.ListenPort
	}
	if desired.TgWebPort <= 0 {
		desired.TgWebPort = 8085
		desired.TgPort = 8085
	}
	if cfg.PublicHostname != "" {
		desired.TgPublicHostname = cfg.PublicHostname
	}
	if desired.DispatcherPort <= 0 {
		desired.DispatcherPort = 9009
	}

	desired.DispatcherEnabled = (desired.XrayEnabled && desired.XrayPublicHostname != "") ||
		(desired.TgEnabled && (desired.TgScenario == "cdn_http" || desired.TgScenario == "dual"))
	return desired
}

// ApplyXrayConfig translates a VLESS server configuration into a desired topology and atomically applies it.

func (c *Coordinator) ApplyXrayConfig(ctx context.Context, cfg vlessserver.Config) error {
	if len(cfg.Endpoints) > 0 {
		vlessserver.NormalizeEndpoints(&cfg)
	}
	txID := fmt.Sprintf("ing-%d-%d", time.Now().UnixNano(), os.Getpid())
	return c.withIngressLock(txID, func() error {
		if c.recoveryFlagged() {
			return ErrRecoveryRequired
		}
		desired := c.topologyForXrayConfig(cfg)
		return c.applyLocked(ctx, txID, desired, &cfg, nil)
	})
}

// ApplyTelegramConfig updates Telegram Web Proxy configuration and coordinates Dispatcher state.

func (c *Coordinator) ApplyTelegramConfig(ctx context.Context, cfg tgwebproxy.Config) error {
	txID := fmt.Sprintf("ing-tg-%d-%d", time.Now().UnixNano(), os.Getpid())
	return c.withIngressLock(txID, func() error {
		if c.recoveryFlagged() {
			return ErrRecoveryRequired
		}
		if c.tgSvc == nil {
			return errors.New("tgwebproxy service unavailable")
		}
		desired := c.topologyForTelegramConfig(cfg)
		return c.applyLocked(ctx, txID, desired, nil, &cfg)
	})
}

// SetXrayEnabled toggles Xray enabled status and coordinates Dispatcher state.
func (c *Coordinator) SetXrayEnabled(ctx context.Context, enabled bool) error {
	txID := fmt.Sprintf("ing-xray-enable-%d-%d", time.Now().UnixNano(), os.Getpid())
	return c.withIngressLock(txID, func() error {
		if c.recoveryFlagged() {
			return ErrRecoveryRequired
		}
		if c.xraySvc == nil {
			return errors.New("xray service unavailable")
		}
		cfg := c.xraySvc.GetConfig()
		cfg.Enabled = enabled
		if len(cfg.Endpoints) > 0 {
			vlessserver.NormalizeEndpoints(&cfg)
		}
		desired := c.topologyForXrayConfig(cfg)
		return c.applyLocked(ctx, txID, desired, &cfg, nil)
	})
}

// SetTelegramEnabled toggles Telegram Web Proxy enabled status and coordinates Dispatcher state.
func (c *Coordinator) SetTelegramEnabled(ctx context.Context, enabled bool) error {
	txID := fmt.Sprintf("ing-tg-enable-%d-%d", time.Now().UnixNano(), os.Getpid())
	return c.withIngressLock(txID, func() error {
		if c.recoveryFlagged() {
			return ErrRecoveryRequired
		}
		if c.tgSvc == nil {
			return errors.New("tgwebproxy service unavailable")
		}
		cfg := c.tgSvc.GetInternalConfig()
		cfg.Enabled = enabled
		desired := c.topologyForTelegramConfig(cfg)
		return c.applyLocked(ctx, txID, desired, nil, &cfg)
	})
}

// RestartXray restarts the Xray process under coordinator lock.
func (c *Coordinator) RestartXray(ctx context.Context) error {
	txID := fmt.Sprintf("ing-restart-%d-%d", time.Now().UnixNano(), os.Getpid())
	return c.withIngressLock(txID, func() error {
		if c.recoveryFlagged() {
			return ErrRecoveryRequired
		}
		if c.xraySvc == nil {
			return errors.New("xray service unavailable")
		}
		return c.xraySvc.Restart()
	})
}

func (c *Coordinator) dispatcherConfigPath() string {
	return filepath.Join(c.dataDir, "dispatcher", "config.json")
}

func (c *Coordinator) saveDispatcherConfigLocked(cfg cdndispatcher.Config) error {
	path := c.dispatcherConfigPath()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dispatcher config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create dispatcher dir: %w", err)
	}
	return atomicWriteFile(path, data, 0600)
}

// GetDispatcherConfig returns the current dispatcher configuration.
func (c *Coordinator) GetDispatcherConfig() cdndispatcher.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dispatcher == nil {
		return cdndispatcher.Config{ListenAddr: ":9009", DefaultAction: "404"}
	}
	return c.dispatcher.GetConfig()
}

// IsDispatcherRunning reports whether the CDN ingress dispatcher listener is currently active.
func (c *Coordinator) IsDispatcherRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dispatcher != nil && c.dispatcher.IsRunning()
}

// HydrateDispatcherRoutes checks if dispatcher routes are configured; if empty,
// hydrates initial rules from Xray and Telegram domain settings (SSOT migration).
func (c *Coordinator) HydrateDispatcherRoutes(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dispatcher == nil {
		return nil
	}

	dispCfg := c.dispatcher.GetConfig()
	if len(dispCfg.Routes) > 0 {
		return nil
	}

	// 1. Try reading existing saved config from disk
	savedPath := c.dispatcherConfigPath()
	var persisted *cdndispatcher.Config
	if data, err := os.ReadFile(savedPath); err == nil {
		var parsed cdndispatcher.Config
		if json.Unmarshal(data, &parsed) == nil {
			persisted = &parsed
		}
	}

	var effXrayCfg vlessserver.Config
	hasXrayCfg := false
	if c.xraySvc != nil {
		effXrayCfg = c.xraySvc.GetConfig()
		hasXrayCfg = true
	}

	if persisted != nil {
		if persisted.ListenAddr != "" {
			dispCfg.ListenAddr = persisted.ListenAddr
		}
		if persisted.XrayTarget != "" {
			dispCfg.XrayTarget = persisted.XrayTarget
		}
		if persisted.TgTarget != "" {
			dispCfg.TgTarget = persisted.TgTarget
		}
		if persisted.DefaultAction != "" {
			dispCfg.DefaultAction = persisted.DefaultAction
		}
		if persisted.PublicHostname != "" {
			dispCfg.PublicHostname = persisted.PublicHostname
		}
		if persisted.XrayPublicHost != "" {
			dispCfg.XrayPublicHost = persisted.XrayPublicHost
		}
		if persisted.TgPublicHost != "" {
			dispCfg.TgPublicHost = persisted.TgPublicHost
		}
		if persisted.XrayPathPrefix != "" {
			dispCfg.XrayPathPrefix = persisted.XrayPathPrefix
		}
		if len(persisted.XrayAdditionalPrefixes) > 0 {
			dispCfg.XrayAdditionalPrefixes = persisted.XrayAdditionalPrefixes
		}
		if persisted.Enabled {
			dispCfg.Enabled = true
		}
		if len(persisted.Routes) > 0 {
			dispCfg.Routes = persisted.Routes
			changed := false
			if dispCfg.DefaultAction == "" {
				dispCfg.DefaultAction = "404"
				changed = true
			}
			if hasXrayCfg && effXrayCfg.Enabled && strings.TrimSpace(effXrayCfg.Path) != "" {
				normPath := cdndispatcher.NormalizePathPrefix(effXrayCfg.Path)
				if dispCfg.XrayPathPrefix != normPath {
					dispCfg.XrayPathPrefix = normPath
					dispCfg.XrayAdditionalPrefixes = xrayAdditionalPrefixes(effXrayCfg, normPath)
					changed = true
				}
			}
			_ = c.dispatcher.ApplyConfig(dispCfg)
			if changed {
				_ = c.saveDispatcherConfigLocked(dispCfg)
			}
			return nil
		}
	}

	// 2. Synthesize initial routes from existing Xray and Telegram domains
	var xrayDomains []string
	if c.xraySvc != nil {
		xCfg := c.xraySvc.GetConfig()
		if xCfg.PublicDomain != "" {
			for _, d := range strings.Split(xCfg.PublicDomain, ",") {
				d = strings.ToLower(strings.TrimSpace(d))
				if d != "" {
					xrayDomains = append(xrayDomains, d)
				}
			}
		}
	}

	var tgDomains []string
	if c.tgSvc != nil {
		tCfg := c.tgSvc.GetConfig()
		if tCfg.PublicHostname != "" {
			for _, d := range strings.Split(tCfg.PublicHostname, ",") {
				d = strings.ToLower(strings.TrimSpace(d))
				if d != "" {
					tgDomains = append(tgDomains, d)
				}
			}
		}
	}

	if len(xrayDomains) == 0 && len(tgDomains) == 0 && dispCfg.PublicHostname != "" {
		for _, d := range strings.Split(dispCfg.PublicHostname, ",") {
			d = strings.ToLower(strings.TrimSpace(d))
			if d != "" {
				xrayDomains = append(xrayDomains, d)
				tgDomains = append(tgDomains, d)
			}
		}
	}

	if len(xrayDomains) == 0 && len(tgDomains) == 0 {
		return nil
	}

	ruleMap := make(map[string]*cdndispatcher.CDNRouteRule)
	order := make([]string, 0)

	for _, d := range xrayDomains {
		if r, exists := ruleMap[d]; exists {
			r.RouteXray = true
		} else {
			rule := &cdndispatcher.CDNRouteRule{
				ID:            fmt.Sprintf("route-%s", d),
				Domain:        d,
				Description:   d,
				RouteXray:     true,
				RouteTelegram: false,
				Enabled:       true,
			}
			ruleMap[d] = rule
			order = append(order, d)
		}
	}

	for _, d := range tgDomains {
		if r, exists := ruleMap[d]; exists {
			r.RouteTelegram = true
		} else {
			rule := &cdndispatcher.CDNRouteRule{
				ID:            fmt.Sprintf("route-%s", d),
				Domain:        d,
				Description:   d,
				RouteXray:     false,
				RouteTelegram: true,
				Enabled:       true,
			}
			ruleMap[d] = rule
			order = append(order, d)
		}
	}

	routes := make([]cdndispatcher.CDNRouteRule, 0, len(order))
	for _, d := range order {
		routes = append(routes, *ruleMap[d])
	}

	dispCfg.Routes = routes
	dispCfg.Enabled = true
	if dispCfg.ListenAddr == "" {
		dispCfg.ListenAddr = ":9009"
	}
	if dispCfg.DefaultAction == "" {
		dispCfg.DefaultAction = "404"
	}
	if hasXrayCfg && effXrayCfg.Enabled && strings.TrimSpace(effXrayCfg.Path) != "" {
		normPath := cdndispatcher.NormalizePathPrefix(effXrayCfg.Path)
		dispCfg.XrayPathPrefix = normPath
		dispCfg.XrayAdditionalPrefixes = xrayAdditionalPrefixes(effXrayCfg, normPath)
	}

	_ = c.dispatcher.ApplyConfig(dispCfg)
	_ = c.saveDispatcherConfigLocked(dispCfg)
	return nil
}

// ApplyDispatcherConfig atomically updates dispatcher routing rules and persists them without restarting Xray.

func (c *Coordinator) ApplyDispatcherConfig(ctx context.Context, cfg cdndispatcher.Config) error {
	txID := fmt.Sprintf("ing-disp-%d-%d", time.Now().UnixNano(), os.Getpid())
	return c.withIngressLock(txID, func() error {
		if c.recoveryFlagged() {
			return ErrRecoveryRequired
		}
		if c.dispatcher == nil {
			return errors.New("dispatcher service unavailable")
		}

		if cfg.ListenAddr == "" {
			cfg.ListenAddr = ":9009"
		}
		if cfg.DefaultAction == "" {
			cfg.DefaultAction = "404"
		}

		// 1. Apply to dispatcher in memory (lock-free snapshot swap)
		if err := c.dispatcher.ApplyConfig(cfg); err != nil {
			return fmt.Errorf("apply dispatcher config: %w", err)
		}

		// 2. Persist to disk
		if err := c.saveDispatcherConfigLocked(cfg); err != nil {
			return fmt.Errorf("save dispatcher config: %w", err)
		}

		// 3. One-way projection to Xray and Telegram configs
		var xrayDomains []string
		var tgDomains []string
		for _, r := range cfg.Routes {
			if r.Enabled {
				if r.RouteXray {
					xrayDomains = append(xrayDomains, r.Domain)
				}
				if r.RouteTelegram {
					tgDomains = append(tgDomains, r.Domain)
				}
			}
		}

		if c.xraySvc != nil {
			if updater, ok := c.xraySvc.(interface{ UpdatePublicDomain(string) error }); ok {
				_ = updater.UpdatePublicDomain(strings.Join(xrayDomains, ","))
			}
		}

		if c.tgSvc != nil {
			tgCfg := c.tgSvc.GetInternalConfig()
			if len(tgDomains) > 0 {
				tgCfg.PublicHostname = strings.Join(tgDomains, ",")
			} else {
				tgCfg.PublicHostname = ""
			}
			_ = c.tgSvc.UpdateConfig(tgCfg)
		}

		return nil
	})
}

// ExecuteIngressTransaction executes a full coordinated transaction for Server Wizards with candidate isolation and readiness probes.
func (c *Coordinator) ExecuteIngressTransaction(ctx context.Context, params IngressTransactionParams) error {
	txID := params.TxID
	if txID == "" {
		txID = fmt.Sprintf("ing-tx-%d-%d", time.Now().UnixNano(), os.Getpid())
	}

	return c.withIngressLock(txID, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if needed, _ := c.isRecoveryNeeded(); needed {
			return ErrRecoveryRequired
		}

		// Fingerprint check under lock
		if params.FingerprintFunc != nil {
			currentFp, err := params.FingerprintFunc(ctx)
			if err != nil {
				return fmt.Errorf("state probe failed: %w", err)
			}
			if params.ExpectedFingerprint != "" && currentFp != params.ExpectedFingerprint {
				return fmt.Errorf("%w: expected %s, got %s", ErrPlanStale, params.ExpectedFingerprint, currentFp)
			}
		}

		prevTopo := c.getCurrentTopology()
		desiredTopo := prevTopo

		var candXray *vlessserver.Config
		if params.Xray != nil && c.xraySvc != nil {
			cfg := BuildEffectiveXrayCandidate(c.xraySvc.GetConfig(), *params.Xray)
			candXray = &cfg

			desiredTopo.XrayEnabled = candXray.Enabled
			desiredTopo.XrayOutboundMode = candXray.OutboundMode
			desiredTopo.FircServerRouting = candXray.Enabled && candXray.OutboundMode == "router"
			desiredTopo.XrayAddress = candXray.ListenAddress
			desiredTopo.XrayPort = candXray.ListenPort
			desiredTopo.XrayPathPrefix = cdndispatcher.NormalizePathPrefix(candXray.Path)
			desiredTopo.XrayPublicHostname = candXray.PublicDomain
			desiredTopo.XrayPublicPort = candXray.PublicPort
			if candXray.PublicDomain != "" && desiredTopo.PublicHostname == "" {
				desiredTopo.PublicHostname = candXray.PublicDomain
			}
		}

		var candTg *tgwebproxy.Config
		if params.Telegram != nil && c.tgSvc != nil {
			cfg := c.tgSvc.GetInternalConfig()
			cfg.Enabled = params.Telegram.Enabled
			if params.Telegram.Scenario != "" {
				cfg.Scenario = params.Telegram.Scenario
			}
			if params.Telegram.DirectHost != "" {
				cfg.DirectHost = params.Telegram.DirectHost
			}
			if params.Telegram.DirectPort > 0 {
				cfg.DirectPort = params.Telegram.DirectPort
			}
			if params.Telegram.TlsDomain != "" {
				cfg.TlsDomain = params.Telegram.TlsDomain
			}
			if params.Telegram.ListenPort > 0 {
				cfg.ListenPort = params.Telegram.ListenPort
			}
			if params.Telegram.BackendPort > 0 {
				cfg.Backend = fmt.Sprintf("127.0.0.1:%d", params.Telegram.BackendPort)
			}
			if params.Telegram.AdminPort > 0 {
				cfg.AdminPort = params.Telegram.AdminPort
			}
			if params.Telegram.Scenario == "direct_fake_tls" {
				cfg.PublicHostname = ""
			} else if params.Telegram.PublicHostname != "" {
				cfg.PublicHostname = params.Telegram.PublicHostname
			}
			if params.Telegram.CarrierMode != "" {
				cfg.CarrierMode = params.Telegram.CarrierMode
			}
			if params.Telegram.NetworkPreset != "" {
				cfg.NetworkPreset = params.Telegram.NetworkPreset
			} else if params.Telegram.IsCDN != nil && cfg.PublicHostname != "" {
				cfg.NetworkPreset = tgwebproxy.InferEndpointPreset(tgwebproxy.WebProxyEndpoint{
					Hostname: cfg.PublicHostname,
					IsCDN:    *params.Telegram.IsCDN,
				}, cfg)
			}
			if len(cfg.Endpoints) > 0 {
				if cfg.PublicHostname != "" {
					cfg.Endpoints[0].Hostname = cfg.PublicHostname
				}
				if params.Telegram.CarrierMode != "" {
					cfg.Endpoints[0].CarrierMode = params.Telegram.CarrierMode
				}
				if cfg.NetworkPreset != "" {
					cfg.Endpoints[0].Preset = cfg.NetworkPreset
				}
				if params.Telegram.IsCDN != nil {
					cfg.Endpoints[0].IsCDN = *params.Telegram.IsCDN
				}
			}
			if params.Telegram.UpstreamDevice != "" {
				cfg.UpstreamDevice = params.Telegram.UpstreamDevice
			}
			if params.Telegram.Secret != "" {
				cfg.Secret = params.Telegram.Secret
			}
			candTg = &cfg

			desiredTopo.TgEnabled = candTg.Enabled
			desiredTopo.TgScenario = candTg.Scenario
			desiredTopo.TgDirectAddress = candTg.DirectHost
			desiredTopo.TgDirectPort = candTg.DirectPort
			desiredTopo.TgWebAddress = "127.0.0.1"
			desiredTopo.TgWebPort = candTg.ListenPort
			desiredTopo.TgPort = candTg.ListenPort
			if candTg.Scenario == "direct_fake_tls" {
				desiredTopo.TgPublicHostname = ""
			} else {
				desiredTopo.TgPublicHostname = candTg.PublicHostname
				if candTg.PublicHostname != "" && desiredTopo.PublicHostname == "" {
					desiredTopo.PublicHostname = candTg.PublicHostname
				}
			}
		}

		// Dispatcher required if Xray or TG uses CDN ingress
		needsDispatcher := false
		if desiredTopo.XrayEnabled && (desiredTopo.XrayPublicHostname != "" || desiredTopo.PublicHostname != "" || desiredTopo.XrayPathPrefix != "") {
			needsDispatcher = true
		}
		if desiredTopo.TgEnabled {
			scenario := desiredTopo.TgScenario
			if scenario == "cdn_http" || scenario == "dual" || scenario == "cdn_domain" || scenario == "both" || scenario == "" {
				if desiredTopo.TgPublicHostname != "" || desiredTopo.PublicHostname != "" {
					needsDispatcher = true
				}
			}
		}
		desiredTopo.DispatcherEnabled = needsDispatcher
		if desiredTopo.DispatcherPort == 0 {
			if prevTopo.DispatcherPort > 0 {
				desiredTopo.DispatcherPort = prevTopo.DispatcherPort
			} else {
				desiredTopo.DispatcherPort = 9009
			}
		}

		if candTg != nil {
			if desiredTopo.XrayEnabled {
				xrayAddr := desiredTopo.XrayAddress
				if xrayAddr == "" {
					xrayAddr = "127.0.0.1"
				}
				xrayPort := desiredTopo.XrayPort
				if xrayPort <= 0 {
					xrayPort = 9008
				}
				candTg.CoverUpstream = fmt.Sprintf("http://%s:%d", xrayAddr, xrayPort)
			} else {
				candTg.CoverUpstream = ""
			}
		} else if c.tgSvc != nil && desiredTopo.TgEnabled && (desiredTopo.TgScenario == tgwebproxy.ScenarioCDNHTTP || desiredTopo.TgScenario == tgwebproxy.ScenarioDual || desiredTopo.TgScenario == "") {
			cfg := c.tgSvc.GetInternalConfig()
			if desiredTopo.XrayEnabled {
				xrayAddr := desiredTopo.XrayAddress
				if xrayAddr == "" {
					xrayAddr = "127.0.0.1"
				}
				xrayPort := desiredTopo.XrayPort
				if xrayPort <= 0 {
					xrayPort = 9008
				}
				cfg.CoverUpstream = fmt.Sprintf("http://%s:%d", xrayAddr, xrayPort)
			} else {
				cfg.CoverUpstream = ""
			}
			candTg = &cfg
		}

		if err := validateTopologyAndChains(desiredTopo, candXray, candTg); err != nil {
			return err
		}

		affected := c.calculateAffected(prevTopo, desiredTopo)
		if params.Xray != nil && !contains(affected, "xray") {
			affected = append(affected, "xray")
		}
		if params.Xray != nil && c.fircRouting != nil && !contains(affected, "firc") {
			if desiredTopo.XrayOutboundMode == "router" || prevTopo.FircServerRouting != desiredTopo.FircServerRouting {
				affected = append(affected, "firc")
			}
		}
		if params.Telegram != nil && !contains(affected, "tgwebproxy") {
			affected = append(affected, "tgwebproxy")
		}
		if params.KeenDNS != nil && strings.TrimSpace(params.KeenDNS.Label) != "" {
			if c.keendns == nil {
				return fmt.Errorf("%w: KeenDNS operator is not configured", ErrKeenDNSUnavailable)
			}
			candKD := *params.KeenDNS
			if candKD.UpstreamPort <= 0 {
				candKD.UpstreamPort = desiredTopo.DispatcherPort
			}
			kdRec, err := c.keendns.PreparePublication(ctx, candKD)
			if err != nil {
				return err
			}
			desiredTopo.KeenDNS = &kdRec
			if !contains(affected, "keendns") {
				affected = append(affected, "keendns")
			}
		}

		jPath := c.journalPath()
		journal := &TransactionJournal{
			Version:                 2,
			TransactionID:           txID,
			Phase:                   PhaseStaged,
			CreatedAt:               time.Now().UTC().Format(time.RFC3339),
			UpdatedAt:               time.Now().UTC().Format(time.RFC3339),
			Desired:                 desiredTopo,
			Previous:                prevTopo,
			Affected:                affected,
			ComponentTransactionIDs: make(map[string]string),
		}
		journal.Checksum = computeJournalChecksum(journal)
		if err := c.writeJournal(jPath, journal); err != nil {
			return fmt.Errorf("write staged journal: %w", err)
		}

		var xrayTxID string
		if contains(affected, "xray") && c.xraySvc != nil && candXray != nil {
			var err error
			xrayTxID, err = c.xraySvc.PrepareCandidate(txID, *candXray)
			if err != nil {
				return c.failAndRollback(journal, fmt.Errorf("prepare xray candidate: %w", err))
			}
			journal.ComponentTransactionIDs["xray"] = xrayTxID
			journal.Checksum = computeJournalChecksum(journal)
			if err := c.writeJournal(jPath, journal); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("write staged journal with xray tx id: %w", err))
			}
		}

		if contains(affected, "tgwebproxy") && c.tgSvc != nil && candTg != nil {
			tgTxID, err := c.tgSvc.PrepareCandidate(txID, *candTg)
			if err != nil {
				return c.failAndRollback(journal, fmt.Errorf("prepare tg candidate: %w", err))
			}
			journal.ComponentTransactionIDs["tgwebproxy"] = tgTxID
			journal.Checksum = computeJournalChecksum(journal)
			if err := c.writeJournal(jPath, journal); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("write staged journal with tg tx id: %w", err))
			}
		}

		if contains(affected, "dispatcher") && c.dispatcher != nil {
			dispPort := desiredTopo.DispatcherPort
			if dispPort <= 0 {
				dispPort = 9009
			}
			xrayAddr := desiredTopo.XrayAddress
			if xrayAddr == "" {
				xrayAddr = "127.0.0.1"
			}
			xrayPort := desiredTopo.XrayPort
			if xrayPort <= 0 {
				xrayPort = 9008
			}
			tgAddr := desiredTopo.TgWebAddress
			if tgAddr == "" {
				tgAddr = "127.0.0.1"
			}
			tgPort := desiredTopo.TgWebPort
			if tgPort <= 0 {
				tgPort = desiredTopo.TgPort
			}
			if tgPort <= 0 {
				tgPort = 8085
			}

			existingDisp := c.dispatcher.GetConfig()
			dispDefaultAction := strings.TrimSpace(existingDisp.DefaultAction)
			if dispDefaultAction == "" {
				dispDefaultAction = "404"
			}
			var effXray vlessserver.Config
			if candXray != nil {
				effXray = *candXray
			} else if c.xraySvc != nil {
				effXray = c.xraySvc.GetConfig()
			}
			dispCandidate := cdndispatcher.Candidate{
				Enabled: desiredTopo.DispatcherEnabled,
				Config: cdndispatcher.Config{
					Enabled:                desiredTopo.DispatcherEnabled,
					ListenAddr:             fmt.Sprintf(":%d", dispPort),
					XrayTarget:             fmt.Sprintf("http://%s:%d", xrayAddr, xrayPort),
					TgTarget:               fmt.Sprintf("http://%s:%d", tgAddr, tgPort),
					Routes:                 mergeTopologyRoutesWithEndpoints(existingDisp.Routes, desiredTopo, effXray.Endpoints),
					DefaultAction:          dispDefaultAction,
					PublicHostname:         desiredTopo.PublicHostname,
					XrayPublicHost:         desiredTopo.XrayPublicHostname,
					TgPublicHost:           desiredTopo.TgPublicHostname,
					XrayPathPrefix:         cdndispatcher.NormalizePathPrefix(desiredTopo.XrayPathPrefix),
					XrayAdditionalPrefixes: xrayAdditionalPrefixes(effXray, desiredTopo.XrayPathPrefix),
				},
			}
			dispTxID, err := c.dispatcher.PrepareCandidate(txID, dispCandidate)
			if err != nil {
				return c.failAndRollback(journal, fmt.Errorf("prepare dispatcher candidate: %w", err))
			}
			journal.ComponentTransactionIDs["dispatcher"] = dispTxID
			journal.Checksum = computeJournalChecksum(journal)
			if err := c.writeJournal(jPath, journal); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("write staged journal with dispatcher tx id: %w", err))
			}
		}

		if contains(affected, "firc") && c.fircRouting != nil {
			fircTxID, err := c.fircRouting.PrepareServerRouting(ctx, txID, "vless_xhttp_server", desiredTopo.FircServerRouting)
			if err != nil {
				return c.failAndRollback(journal, fmt.Errorf("prepare firc server routing: %w", err))
			}
			journal.ComponentTransactionIDs["firc"] = fircTxID
			journal.Checksum = computeJournalChecksum(journal)
			if err := c.writeJournal(jPath, journal); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("write staged journal with firc tx id: %w", err))
			}
		}

		// Activate candidates (CommitPrepared) BEFORE PhaseCandidateActive and BEFORE ReadinessProbe!
		if contains(affected, "xray") && c.xraySvc != nil {
			xrayTxID := journal.ComponentTransactionIDs["xray"]
			if xrayTxID != "" {
				if err := c.xraySvc.CommitPrepared(xrayTxID); err != nil {
					return c.failAndRollback(journal, fmt.Errorf("activate xray candidate: %w", err))
				}
			}
		}

		if contains(affected, "tgwebproxy") && c.tgSvc != nil {
			tgTxID := journal.ComponentTransactionIDs["tgwebproxy"]
			if tgTxID != "" {
				if err := c.tgSvc.CommitPrepared(tgTxID); err != nil {
					return c.failAndRollback(journal, fmt.Errorf("activate tg candidate: %w", err))
				}
			}
		}

		if contains(affected, "dispatcher") && c.dispatcher != nil {
			dispTxID := journal.ComponentTransactionIDs["dispatcher"]
			if dispTxID != "" {
				if err := c.dispatcher.CommitPrepared(dispTxID); err != nil {
					return c.failAndRollback(journal, fmt.Errorf("activate dispatcher candidate: %w", err))
				}
			}
		}

		if contains(affected, "firc") && c.fircRouting != nil {
			fircTxID := journal.ComponentTransactionIDs["firc"]
			if fircTxID != "" {
				if err := c.fircRouting.CommitServerRouting(ctx, fircTxID); err != nil {
					return c.failAndRollback(journal, fmt.Errorf("activate firc server routing: %w", err))
				}
			}
		}

		// Advance to PhaseCandidateActive
		journal.Phase = PhaseCandidateActive
		journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		journal.Checksum = computeJournalChecksum(journal)
		if err := c.writeJournal(jPath, journal); err != nil {
			return c.failAndRollback(journal, fmt.Errorf("write candidate_active journal: %w", err))
		}

		// Check context before probe
		if err := ctx.Err(); err != nil {
			return c.failAndRollback(journal, err)
		}

		// Run readiness probe with combined topology!
		if params.ReadinessProbe != nil {
			if err := params.ReadinessProbe(ctx, desiredTopo); err != nil {
				rbErr := c.failAndRollback(journal, err)
				if c.recoveryFlagged() || errors.Is(rbErr, ErrRecoveryRequired) {
					return rbErr
				}
				return fmt.Errorf("readiness probe failed: %w", err)
			}
		}

		// Apply KeenDNS publication ONLY after local services and dispatcher readiness probe have succeeded
		if contains(affected, "keendns") && journal.Desired.KeenDNS != nil && c.keendns != nil {
			if err := ctx.Err(); err != nil {
				return c.failAndRollback(journal, err)
			}
			if !journal.Desired.KeenDNS.ExistedBefore && journal.Desired.KeenDNS.ActionMode != KeenDNSActionReuse {
				if journal.Desired.KeenDNS.ExpectedAppliedRaw == "" || journal.Desired.KeenDNS.ExpectedPostFingerprint == "" {
					host := strings.TrimSpace(journal.Desired.KeenDNS.UpstreamHost)
					if host == "" {
						host = "127.0.0.1"
					}
					port := journal.Desired.KeenDNS.UpstreamPort
					if port <= 0 {
						port = 9009
					}
					expRaw, expFP := buildExpectedCreatedEntry(journal.Desired.KeenDNS.Label, host, port)
					journal.Desired.KeenDNS.ExpectedAppliedRaw = expRaw
					journal.Desired.KeenDNS.ExpectedPostFingerprint = expFP
				}
				journal.Desired.KeenDNS.ActionMode = KeenDNSActionCreate
				journal.Desired.KeenDNS.WriteIntent = true
				journal.Desired.KeenDNS.WriteAttempted = false
				journal.Desired.KeenDNS.SetBeforeWriteHook(func(rec *KeenDNSJournalRecord) error {
					desiredTopo.KeenDNS = rec
					journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
					journal.Checksum = computeJournalChecksum(journal)
					return c.writeJournal(jPath, journal)
				})
			}
			if err := c.keendns.ApplyAndVerifyPublication(ctx, journal.Desired.KeenDNS); err != nil {
				rbErr := c.failAndRollback(journal, err)
				if rbErr != nil && !errors.Is(rbErr, err) {
					return errors.Join(fmt.Errorf("keendns publication failed: %w", err), fmt.Errorf("rollback failed: %w", rbErr))
				}
				return fmt.Errorf("keendns publication failed: %w", err)
			}
			desiredTopo.KeenDNS = journal.Desired.KeenDNS
			journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			journal.Checksum = computeJournalChecksum(journal)
			if err := c.writeJournal(jPath, journal); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("write journal after keendns apply: %w", err))
			}
		}

		// Check context before point of no return
		if err := ctx.Err(); err != nil {
			rbErr := c.failAndRollback(journal, err)
			if rbErr != nil && !errors.Is(rbErr, err) {
				return errors.Join(err, fmt.Errorf("rollback failed: %w", rbErr))
			}
			return err
		}

		// Pre-committing hook (can still rollback if hook fails)
		if params.OnPhaseChange != nil {
			if err := params.OnPhaseChange(PhaseCommitting); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("OnPhaseChange hook failed before committing: %w", err))
			}
		} else if c.onPhaseChange != nil {
			if err := c.onPhaseChange(PhaseCommitting); err != nil {
				return c.failAndRollback(journal, fmt.Errorf("onPhaseChange hook failed before committing: %w", err))
			}
		}

		// Point of no return: PhaseCommitting
		journal.Phase = PhaseCommitting
		journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		journal.Checksum = computeJournalChecksum(journal)
		if err := c.writeJournal(jPath, journal); err != nil {
			// Journal write failed: point of no return was NOT reached! Rollback active candidates.
			return c.failAndRollback(journal, fmt.Errorf("write committing journal: %w", err))
		}

		// Post-boundary callback: durable point of no return reached!
		if params.OnPointOfNoReturn != nil {
			if err := params.OnPointOfNoReturn(txID); err != nil {
				c.setRecovery(true, fmt.Sprintf("OnPointOfNoReturn callback failed: %v", err))
				return errors.Join(ErrRecoveryRequired, fmt.Errorf("OnPointOfNoReturn callback failed: %w", err))
			}
		}

		// Finalize Components (FinalizePrepared) - candidates were already activated!
		var finErrs []error
		if contains(affected, "xray") && c.xraySvc != nil {
			if id := journal.ComponentTransactionIDs["xray"]; id != "" {
				if err := c.xraySvc.FinalizePrepared(id); err != nil {
					finErrs = append(finErrs, fmt.Errorf("finalize xray: %w", err))
				}
			}
		}
		if contains(affected, "tgwebproxy") && c.tgSvc != nil {
			if id := journal.ComponentTransactionIDs["tgwebproxy"]; id != "" {
				if err := c.tgSvc.FinalizePrepared(id); err != nil {
					finErrs = append(finErrs, fmt.Errorf("finalize tgwebproxy: %w", err))
				}
			}
		}
		if contains(affected, "dispatcher") && c.dispatcher != nil {
			if id := journal.ComponentTransactionIDs["dispatcher"]; id != "" {
				if err := c.dispatcher.FinalizePrepared(id); err != nil {
					finErrs = append(finErrs, fmt.Errorf("finalize dispatcher: %w", err))
				} else {
					_ = c.saveDispatcherConfigLocked(c.dispatcher.GetConfig())
				}
			}
		}
		if contains(affected, "firc") && c.fircRouting != nil {
			if id := journal.ComponentTransactionIDs["firc"]; id != "" {
				if err := c.fircRouting.FinalizeServerRouting(id); err != nil {
					finErrs = append(finErrs, fmt.Errorf("finalize firc: %w", err))
				}
			}
		}
		if len(finErrs) > 0 {
			c.setRecovery(true, fmt.Sprintf("finalize components failed: %v", errors.Join(finErrs...)))
			return fmt.Errorf("%w: finalize components failed: %v", ErrRecoveryRequired, errors.Join(finErrs...))
		}

		// PhaseCommitted (ONLY after all FinalizePrepared succeed!)
		journal.Phase = PhaseCommitted
		journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		journal.Fingerprints = c.computeFingerprints()
		journal.Checksum = computeJournalChecksum(journal)
		if err := c.writeJournal(jPath, journal); err != nil {
			c.setRecovery(true, fmt.Sprintf("write committed journal failed: %v", err))
			return fmt.Errorf("%w: write committed journal: %v", ErrRecoveryRequired, err)
		}

		// Archive journal as committed
		if err := c.archiveJournal(jPath, "committed", txID); err != nil {
			c.setRecovery(true, fmt.Sprintf("archive committed journal failed: %v", err))
			return fmt.Errorf("%w: archive journal: %v", ErrRecoveryRequired, err)
		}

		return nil
	})
}

func (c *Coordinator) rollbackIngressTx(j *TransactionJournal, xrayTxID string, prevTg tgwebproxy.Config, prevDisp cdndispatcher.Config, origErr error) error {
	return c.failAndRollback(j, origErr)
}

func xrayAdditionalPrefixes(cfg vlessserver.Config, primaryPrefix string) []string {
	if len(cfg.Endpoints) == 0 {
		return nil
	}
	normPrimary := cdndispatcher.NormalizePathPrefix(primaryPrefix)
	seen := map[string]bool{normPrimary: true}
	var out []string
	for _, ep := range cfg.Endpoints {
		if !ep.Enabled {
			continue
		}
		p := strings.TrimSpace(ep.Path)
		if p == "" {
			p = cfg.Path
		}
		if ep.XHTTPTuning != nil && ep.XHTTPTuning.StreamPathSuffix != "" && !strings.HasSuffix(strings.TrimRight(p, "/"), ep.XHTTPTuning.StreamPathSuffix) {
			p = strings.TrimRight(p, "/") + "/" + ep.XHTTPTuning.StreamPathSuffix
		}
		norm := cdndispatcher.NormalizePathPrefix(p)
		if norm != "" && !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out
}

func mergeTopologyRoutesWithEndpoints(existing []cdndispatcher.CDNRouteRule, topo IngressTopology, endpoints []vlessserver.EndpointConfig) []cdndispatcher.CDNRouteRule {
	if len(endpoints) == 0 {
		return mergeTopologyRoutes(existing, topo)
	}
	routes := make([]cdndispatcher.CDNRouteRule, 0, len(existing))
	enabledXray := make(map[string]string)
	disabledXray := make(map[string]bool)

	for _, ep := range endpoints {
		for _, d := range strings.Split(ep.PublicDomain, ",") {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "" {
				continue
			}
			if ep.Enabled {
				desc := strings.TrimSpace(ep.Name)
				if desc == "" {
					desc = "VLESS (XHTTP)"
				}
				enabledXray[d] = desc
			} else if _, ok := enabledXray[d]; !ok {
				disabledXray[d] = true
			}
		}
		orig := strings.ToLower(strings.TrimSpace(ep.OriginHost))
		if orig != "" {
			if ep.Enabled {
				if _, ok := enabledXray[orig]; !ok {
					enabledXray[orig] = "KeenDNS Origin (VLESS)"
				}
			} else if _, ok := enabledXray[orig]; !ok {
				disabledXray[orig] = true
			}
		}
	}

	seenInRoutes := make(map[string]bool)
	for _, r := range existing {
		dom := strings.ToLower(strings.TrimSpace(r.Domain))
		if desc, isEnabled := enabledXray[dom]; isEnabled {
			r.RouteXray = true
			if strings.HasPrefix(dom, "tg.") && topo.TgEnabled {
				r.RouteTelegram = true
			}
			r.Enabled = true
			if r.Description == "" || r.Description == r.Domain {
				r.Description = desc
			}
			routes = append(routes, r)
			seenInRoutes[dom] = true
		} else if disabledXray[dom] {
			if r.RouteTelegram {
				r.RouteXray = false
			} else {
				r.RouteXray = true
				r.Enabled = false
			}
			routes = append(routes, r)
			seenInRoutes[dom] = true
		} else if r.RouteXray {
			// Domain was removed from endpoints
			if r.RouteTelegram {
				r.RouteXray = false
				routes = append(routes, r)
				seenInRoutes[dom] = true
			}
		} else {
			routes = append(routes, r)
			seenInRoutes[dom] = true
		}
	}

	for _, ep := range endpoints {
		if !ep.Enabled {
			continue
		}
		for _, d := range strings.Split(ep.PublicDomain, ",") {
			d = strings.ToLower(strings.TrimSpace(d))
			if d != "" && !seenInRoutes[d] {
				seenInRoutes[d] = true
				desc := strings.TrimSpace(ep.Name)
				if desc == "" {
					desc = "VLESS (XHTTP)"
				}
				routes = append(routes, cdndispatcher.CDNRouteRule{
					ID:          "ep-route-" + d,
					Domain:      d,
					Description: desc,
					RouteXray:   true,
					Enabled:     true,
				})
			}
		}
		orig := strings.ToLower(strings.TrimSpace(ep.OriginHost))
		if orig != "" && !seenInRoutes[orig] {
			seenInRoutes[orig] = true
			routes = append(routes, cdndispatcher.CDNRouteRule{
				ID:            "ep-origin-" + orig,
				Domain:        orig,
				Description:   "KeenDNS Origin (VLESS)",
				RouteXray:     true,
				RouteTelegram: strings.HasPrefix(orig, "tg.") && topo.TgEnabled,
				Enabled:       true,
			})
		}
	}

	return mergeTopologyRoutes(routes, topo)
}

func mergeTopologyRoutes(existing []cdndispatcher.CDNRouteRule, topo IngressTopology) []cdndispatcher.CDNRouteRule {
	routes := make([]cdndispatcher.CDNRouteRule, 0, len(existing)+2)
	for _, r := range existing {
		if !topo.XrayEnabled {
			r.RouteXray = false
		}
		if !topo.TgEnabled || topo.TgScenario == "direct_fake_tls" {
			r.RouteTelegram = false
		}
		if !r.RouteXray && !r.RouteTelegram {
			if strings.HasPrefix(r.ID, "wizard-") {
				continue
			}
			r.Enabled = false
		}
		routes = append(routes, r)
	}

	var xrayHost string
	if topo.XrayEnabled {
		xrayHost = strings.ToLower(strings.TrimSpace(topo.XrayPublicHostname))
		if xrayHost == "" {
			xrayHost = strings.ToLower(strings.TrimSpace(topo.PublicHostname))
		}
	}
	var tgHost string
	if topo.TgEnabled && topo.TgScenario != "direct_fake_tls" {
		tgHost = strings.ToLower(strings.TrimSpace(topo.TgPublicHostname))
		if tgHost == "" {
			tgHost = strings.ToLower(strings.TrimSpace(topo.PublicHostname))
		}
	}

	upsertSingleHost := func(domain string, wantXray, wantTg bool, defaultDesc string) {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			return
		}
		for i := range routes {
			if strings.EqualFold(strings.TrimSpace(routes[i].Domain), domain) {
				routes[i].Enabled = true
				if wantXray {
					routes[i].RouteXray = true
				}
				if wantTg {
					routes[i].RouteTelegram = true
				}
				return
			}
		}
		routes = append(routes, cdndispatcher.CDNRouteRule{
			ID:            "wizard-" + domain,
			Domain:        domain,
			Description:   defaultDesc,
			RouteXray:     wantXray,
			RouteTelegram: wantTg,
			Enabled:       true,
		})
	}

	upsertHost := func(domainsCSV string, wantXray, wantTg bool, defaultDesc string) {
		for _, single := range strings.Split(domainsCSV, ",") {
			upsertSingleHost(single, wantXray, wantTg, defaultDesc)
		}
	}

	if xrayHost != "" && xrayHost == tgHost {
		upsertHost(xrayHost, true, true, "Внешний доступ")
	} else {
		if xrayHost != "" {
			upsertHost(xrayHost, true, false, "VLESS (XHTTP)")
		}
		if tgHost != "" {
			upsertHost(tgHost, false, true, "Telegram")
		}
	}
	return routes
}

// BuildEffectiveXrayCandidate constructs the exact effective vlessserver.Config from a base
// persisted config and an IngressXrayCandidate. Both the transactional coordinator and the
// wizard share-link generator call this function so that revealed credentials always match
// the committed server configuration (including XHTTPTuning and durable origin metadata).
func BuildEffectiveXrayCandidate(base vlessserver.Config, cand IngressXrayCandidate) vlessserver.Config {
	cfg := base
	if base.Clients != nil {
		cfg.Clients = append([]vlessserver.Client(nil), base.Clients...)
	}
	if base.XHTTPTuning != nil {
		t := *base.XHTTPTuning
		cfg.XHTTPTuning = &t
	}
	if base.Endpoints != nil {
		cfg.Endpoints = make([]vlessserver.EndpointConfig, len(base.Endpoints))
		for i, ep := range base.Endpoints {
			if ep.XHTTPTuning != nil {
				t := *ep.XHTTPTuning
				ep.XHTTPTuning = &t
			}
			cfg.Endpoints[i] = ep
		}
	}

	cfg.Enabled = cand.Enabled
	if cand.ListenAddress != "" {
		cfg.ListenAddress = cand.ListenAddress
	} else if cfg.ListenAddress == "" {
		cfg.ListenAddress = "127.0.0.1"
	}
	if cand.ListenPort > 0 {
		cfg.ListenPort = cand.ListenPort
	} else if cfg.ListenPort == 0 {
		cfg.ListenPort = 9008
	}
	if cand.PublicDomain != "" {
		cfg.PublicDomain = cand.PublicDomain
	}
	if cand.PublicPort > 0 {
		cfg.PublicPort = cand.PublicPort
	} else if cfg.PublicPort == 0 {
		cfg.PublicPort = 443
	}
	if cand.ConnectionWay != "" {
		cfg.ConnectionWay = cand.ConnectionWay
	}
	if cand.OriginType != "" {
		cfg.OriginType = cand.OriginType
	}
	if cand.OriginHost != "" {
		cfg.OriginHost = cand.OriginHost
	}
	if cand.OriginScheme != "" {
		cfg.OriginScheme = cand.OriginScheme
	}
	if cand.OriginPort > 0 {
		cfg.OriginPort = cand.OriginPort
	}
	if cand.Path != "" {
		cfg.Path = cand.Path
	}
	if cand.Transport != "" {
		cfg.Transport = cand.Transport
	}
	if cand.Mode != "" {
		cfg.Mode = cand.Mode
	}
	if cand.UplinkMethod != "" {
		cfg.UplinkMethod = cand.UplinkMethod
	}
	if cand.OutboundMode != "" {
		cfg.OutboundMode = cand.OutboundMode
	}
	if cand.OutboundInterface != "" {
		cfg.OutboundInterface = cand.OutboundInterface
	}
	if cand.OutboundSocksPort > 0 {
		cfg.OutboundSocksPort = cand.OutboundSocksPort
	}

	if cand.PreserveTransport {
		if cfg.XHTTPTuning != nil && cfg.XHTTPTuning.UplinkMethod != "" {
			cfg.UplinkMethod = cfg.XHTTPTuning.UplinkMethod
		}
	} else if cfg.UplinkMethod != "" {
		isCustom := cfg.XHTTPTuning != nil && cfg.XHTTPTuning.Preset == "custom"
		if strings.EqualFold(cfg.UplinkMethod, "GET") {
			if isCustom {
				cfg.XHTTPTuning.UplinkMethod = "GET"
			} else {
				t := vlessserver.DefaultCDNGetTuning()
				cfg.XHTTPTuning = &t
			}
		} else if strings.EqualFold(cfg.UplinkMethod, "POST") {
			if isCustom {
				cfg.XHTTPTuning.UplinkMethod = "POST"
			} else if cfg.ConnectionWay == "direct" || strings.EqualFold(cfg.Mode, "stream-up") || strings.EqualFold(cfg.Mode, "stream-one") {
				t := vlessserver.DefaultBalancedTuning()
				cfg.XHTTPTuning = &t
			} else {
				t := vlessserver.DefaultLiveLowLatencyTuning()
				cfg.XHTTPTuning = &t
			}
		}
	}

	if cand.ClientUUID != "" {
		remark := cand.ClientRemark
		if remark == "" {
			remark = "Default Client"
		}
		found := false
		for i, cl := range cfg.Clients {
			if cl.ID == cand.ClientUUID || cl.Remark == remark {
				cfg.Clients[i].ID = cand.ClientUUID
				cfg.Clients[i].Remark = remark
				cfg.Clients[i].Enabled = true
				found = true
				break
			}
		}
		if !found {
			cfg.Clients = append(cfg.Clients, vlessserver.Client{
				ID:        cand.ClientUUID,
				Remark:    remark,
				Enabled:   true,
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			})
		}
	}

	if len(cfg.Endpoints) > 0 && cand.PublicDomain != "" {
		candDom := strings.ToLower(strings.TrimSpace(cand.PublicDomain))
		var epTuning *vlessserver.XHTTPTuning
		if cfg.XHTTPTuning != nil {
			t := *cfg.XHTTPTuning
			epTuning = &t
		}
		matchedIdx := -1
		for i := range cfg.Endpoints {
			if strings.EqualFold(strings.TrimSpace(cfg.Endpoints[i].PublicDomain), candDom) {
				matchedIdx = i
				break
			}
		}
		upserted := vlessserver.EndpointConfig{
			ID:            "ep-" + candDom,
			Name:          vlessserver.DefaultEndpointName(candDom, cfg.ConnectionWay, cfg.OriginType),
			Enabled:       true,
			ConnectionWay: cfg.ConnectionWay,
			OriginType:    cfg.OriginType,
			PublicDomain:  candDom,
			PublicPort:    cfg.PublicPort,
			OriginHost:    cfg.OriginHost,
			OriginScheme:  cfg.OriginScheme,
			OriginPort:    cfg.OriginPort,
			Path:          cfg.Path,
			Transport:     cfg.Transport,
			Mode:          cfg.Mode,
			UplinkMethod:  cfg.UplinkMethod,
			XHTTPTuning:   epTuning,
		}
		if matchedIdx >= 0 {
			if cfg.Endpoints[matchedIdx].ID != "" {
				upserted.ID = cfg.Endpoints[matchedIdx].ID
			}
			if cfg.Endpoints[matchedIdx].Name != "" {
				upserted.Name = cfg.Endpoints[matchedIdx].Name
			}
			rest := append([]vlessserver.EndpointConfig{}, cfg.Endpoints[:matchedIdx]...)
			rest = append(rest, cfg.Endpoints[matchedIdx+1:]...)
			cfg.Endpoints = append([]vlessserver.EndpointConfig{upserted}, rest...)
		} else {
			cfg.Endpoints = append([]vlessserver.EndpointConfig{upserted}, cfg.Endpoints...)
		}
		vlessserver.NormalizeEndpoints(&cfg)
	}

	return cfg
}

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

func defaultDispatcherConfig() cdndispatcher.Config {
	return cdndispatcher.Config{
		Enabled:        false,
		ListenAddr:     ":9009",
		XrayTarget:     "http://127.0.0.1:9008",
		TgTarget:       "http://127.0.0.1:8085",
		XrayPathPrefix: "/cdn-bridge",
		DefaultAction:  "404",
	}
}

func extractDispatcherPort(cfg cdndispatcher.Config) int {
	addr := strings.TrimSpace(cfg.ListenAddr)
	if addr == "" {
		return 9009
	}
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			return p
		}
	} else if strings.HasPrefix(addr, ":") {
		if p, err := strconv.Atoi(strings.TrimPrefix(addr, ":")); err == nil && p > 0 {
			return p
		}
	}
	return 9009
}

func normalizeResetHost(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			s = u.Host
		}
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	s = strings.TrimSuffix(s, ".")
	return strings.TrimSpace(s)
}

func extractKeenDNSLabel(rawHost, baseFQDN string) string {
	host := normalizeResetHost(rawHost)
	base := normalizeResetHost(baseFQDN)
	if host == "" || base == "" {
		return ""
	}
	suffix := "." + base
	if !strings.HasSuffix(host, suffix) {
		return ""
	}
	label := strings.TrimSuffix(host, suffix)
	if label == "" || strings.Contains(label, ".") {
		return ""
	}
	return label
}

func appendSplitHosts(out []string, raw string) []string {
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func collectVlessHosts(cfg vlessserver.Config, routes []cdndispatcher.CDNRouteRule) []string {
	var hosts []string
	hosts = appendSplitHosts(hosts, cfg.PublicDomain)
	hosts = appendSplitHosts(hosts, cfg.OriginHost)
	for _, ep := range cfg.Endpoints {
		hosts = appendSplitHosts(hosts, ep.PublicDomain)
		hosts = appendSplitHosts(hosts, ep.OriginHost)
	}
	for _, r := range routes {
		if r.RouteXray {
			hosts = appendSplitHosts(hosts, r.Domain)
		}
	}
	return hosts
}

func collectTelegramHosts(cfg tgwebproxy.Config, routes []cdndispatcher.CDNRouteRule) []string {
	var hosts []string
	hosts = appendSplitHosts(hosts, cfg.PublicHostname)
	for _, ep := range cfg.Endpoints {
		hosts = appendSplitHosts(hosts, ep.Hostname)
	}
	for _, r := range routes {
		if r.RouteTelegram {
			hosts = appendSplitHosts(hosts, r.Domain)
		}
	}
	return hosts
}

func hasVlessCDNDomains(cfg vlessserver.Config) bool {
	if strings.TrimSpace(cfg.PublicDomain) != "" {
		return true
	}
	for _, ep := range cfg.Endpoints {
		if !ep.Enabled {
			continue
		}
		if strings.TrimSpace(ep.PublicDomain) != "" || strings.TrimSpace(ep.OriginHost) != "" {
			return true
		}
	}
	return false
}

func (c *Coordinator) resetDispatcherLocked() {
	if c.dispatcher == nil {
		return
	}
	_ = c.dispatcher.Stop()
	_ = c.dispatcher.ApplyConfig(defaultDispatcherConfig())
	if c.dataDir != "" {
		_ = os.Remove(c.dispatcherConfigPath())
		_ = os.RemoveAll(filepath.Join(c.dataDir, "dispatcher", "tx"))
	}
	if cleaner, ok := c.dispatcher.(DispatcherSystemHookCleaner); ok {
		cleaner.CleanupSystemHooks()
	}
}

func (c *Coordinator) cleanupKeenDNSForResetLocked(
	ctx context.Context,
	targetHosts, protectedHosts []string,
	dispPort int,
	allowedPorts []int,
	otherName string,
) (deleted, skipped, warnings []string) {
	if c.keendns == nil {
		return nil, nil, []string{"подсистема KeenDNS недоступна для удаления доменов"}
	}
	disc, err := c.keendns.Discover(ctx, dispPort)
	if err != nil {
		return nil, nil, []string{fmt.Sprintf("не удалось прочитать настройки KeenDNS: %v", err)}
	}
	if disc.BaseFQDN == "" {
		if disc.StatusMessage != "" {
			return nil, nil, []string{disc.StatusMessage}
		}
		return nil, nil, nil
	}

	protectedSet := make(map[string]bool)
	for _, h := range protectedHosts {
		if label := extractKeenDNSLabel(h, disc.BaseFQDN); label != "" {
			protectedSet[label] = true
		}
	}

	seen := make(map[string]bool)
	var toDelete []string
	for _, h := range targetHosts {
		label := extractKeenDNSLabel(h, disc.BaseFQDN)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		if protectedSet[label] {
			skipped = append(skipped, fmt.Sprintf("%s (используется %s)", label, otherName))
			continue
		}
		toDelete = append(toDelete, label)
	}

	if len(toDelete) == 0 {
		return deleted, skipped, warnings
	}
	deleter, ok := c.keendns.(KeenDNSResetOperator)
	if !ok {
		return deleted, skipped, append(warnings, "оператор KeenDNS не поддерживает удаление публикаций")
	}
	del, sk, warn := deleter.DeleteIngressPublications(ctx, toDelete, allowedPorts)
	deleted = append(deleted, del...)
	skipped = append(skipped, sk...)
	warnings = append(warnings, warn...)
	return deleted, skipped, warnings
}

// ResetVless resets only the VLESS server (and REALITY + VLESS routes in Dispatcher)
// back to factory defaults while keeping any active Telegram server running.
// If Telegram is inactive, CDN Dispatcher and its Keenetic hooks are fully reset as well.
func (c *Coordinator) ResetVless(ctx context.Context, opts ResetOptions) (ResetResult, error) {
	res := ResetResult{Server: "vless"}
	txID := fmt.Sprintf("ing-reset-vless-%d-%d", time.Now().UnixNano(), os.Getpid())
	err := c.withIngressLock(txID, func() error {
		if c.xraySvc == nil {
			return errors.New("vless service unavailable")
		}
		resetter, ok := c.xraySvc.(FactoryResetter)
		if !ok {
			return errors.New("vless service does not support factory reset")
		}

		prevXray := c.xraySvc.GetConfig()
		var prevDisp cdndispatcher.Config
		if c.dispatcher != nil {
			prevDisp = c.dispatcher.GetConfig()
		}
		dispPort := extractDispatcherPort(prevDisp)

		var tgCfg tgwebproxy.Config
		if c.tgSvc != nil {
			tgCfg = c.tgSvc.GetInternalConfig()
		}

		if err := resetter.FactoryReset(ctx); err != nil {
			return err
		}

		c.mu.Lock()
		firc := c.fircRouting
		c.mu.Unlock()

		if firc != nil && firc.IsServerRoutingEnabled("vless_xhttp_server") {
			fircTxID := txID + "-firc"
			if _, prepErr := firc.PrepareServerRouting(ctx, fircTxID, "vless_xhttp_server", false); prepErr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("не удалось отключить маршрутизацию VLESS в FIRC: %v", prepErr))
			} else if commitErr := firc.CommitServerRouting(ctx, fircTxID); commitErr != nil {
				_ = firc.RollbackServerRouting(ctx, fircTxID)
				res.Warnings = append(res.Warnings, fmt.Sprintf("не удалось применить сброс маршрутизации VLESS в FIRC: %v", commitErr))
			} else if finErr := firc.FinalizeServerRouting(fircTxID); finErr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("ошибка финализации сброса FIRC: %v", finErr))
			}
		}

		tgNeedsDisp := c.tgSvc != nil && tgCfg.Enabled && tgCfg.Scenario != tgwebproxy.ScenarioDirectFakeTLS
		if c.dispatcher != nil {
			if tgNeedsDisp {
				nextDisp := prevDisp
				nextDisp.XrayTarget = "http://127.0.0.1:9008"
				nextDisp.XrayPublicHost = ""
				nextDisp.XrayPathPrefix = "/cdn-bridge"
				nextDisp.XrayAdditionalPrefixes = nil
				nextDisp.PublicHostname = tgCfg.PublicHostname

				filtered := make([]cdndispatcher.CDNRouteRule, 0, len(prevDisp.Routes))
				for _, r := range prevDisp.Routes {
					r.RouteXray = false
					if r.RouteTelegram {
						filtered = append(filtered, r)
					}
				}
				nextDisp.Routes = filtered
				if nextDisp.ListenAddr == "" {
					nextDisp.ListenAddr = ":9009"
				}
				if nextDisp.TgTarget == "" {
					nextDisp.TgTarget = "http://127.0.0.1:8085"
				}
				if nextDisp.DefaultAction == "" {
					nextDisp.DefaultAction = "404"
				}

				if err := c.dispatcher.ApplyConfig(nextDisp); err != nil {
					return fmt.Errorf("reconcile dispatcher after vless reset: %w", err)
				}
				if err := c.saveDispatcherConfigLocked(nextDisp); err != nil {
					return fmt.Errorf("save dispatcher config after vless reset: %w", err)
				}
				res.DispatcherReset = false
			} else {
				c.resetDispatcherLocked()
				res.DispatcherReset = true
			}
		} else {
			res.DispatcherReset = true
		}

		if opts.DeleteKeenDNS {
			targetHosts := collectVlessHosts(prevXray, prevDisp.Routes)
			protectedHosts := collectTelegramHosts(tgCfg, prevDisp.Routes)
			del, sk, warn := c.cleanupKeenDNSForResetLocked(ctx, targetHosts, protectedHosts, dispPort, []int{dispPort}, "Telegram")
			res.KeenDNSDeleted = del
			res.KeenDNSSkipped = sk
			res.Warnings = append(res.Warnings, warn...)
		}

		if c.dataDir != "" {
			_ = os.Remove(c.journalPath())
		}
		c.setRecovery(false, "")
		res.Reset = true
		return nil
	})
	return res, err
}

// ResetTelegram resets only the Telegram proxy + Telegram routes in Dispatcher back to factory defaults
// while keeping any active VLESS server running. If VLESS is inactive/unconfigured for CDN,
// CDN Dispatcher and its Keenetic hooks are fully reset as well.
func (c *Coordinator) ResetTelegram(ctx context.Context, opts ResetOptions) (ResetResult, error) {
	res := ResetResult{Server: "tgwebproxy"}
	txID := fmt.Sprintf("ing-reset-tg-%d-%d", time.Now().UnixNano(), os.Getpid())
	err := c.withIngressLock(txID, func() error {
		if c.tgSvc == nil {
			return errors.New("tgwebproxy service unavailable")
		}
		resetter, ok := c.tgSvc.(FactoryResetter)
		if !ok {
			return errors.New("tgwebproxy service does not support factory reset")
		}

		prevTg := c.tgSvc.GetInternalConfig()
		var prevDisp cdndispatcher.Config
		if c.dispatcher != nil {
			prevDisp = c.dispatcher.GetConfig()
		}
		dispPort := extractDispatcherPort(prevDisp)

		var xrayCfg vlessserver.Config
		if c.xraySvc != nil {
			xrayCfg = c.xraySvc.GetConfig()
		}

		if err := resetter.FactoryReset(ctx); err != nil {
			return err
		}

		xrayNeedsDisp := c.xraySvc != nil && xrayCfg.Enabled && hasVlessCDNDomains(xrayCfg)
		if c.dispatcher != nil {
			if xrayNeedsDisp {
				nextDisp := prevDisp
				nextDisp.TgTarget = "http://127.0.0.1:8085"
				nextDisp.TgPublicHost = ""
				nextDisp.PublicHostname = xrayCfg.PublicDomain

				filtered := make([]cdndispatcher.CDNRouteRule, 0, len(prevDisp.Routes))
				for _, r := range prevDisp.Routes {
					r.RouteTelegram = false
					r.TelegramTarget = ""
					if r.RouteXray {
						filtered = append(filtered, r)
					}
				}
				nextDisp.Routes = filtered
				if nextDisp.ListenAddr == "" {
					nextDisp.ListenAddr = ":9009"
				}
				if nextDisp.XrayTarget == "" {
					nextDisp.XrayTarget = "http://127.0.0.1:9008"
				}
				if nextDisp.DefaultAction == "" {
					nextDisp.DefaultAction = "404"
				}

				if err := c.dispatcher.ApplyConfig(nextDisp); err != nil {
					return fmt.Errorf("reconcile dispatcher after telegram reset: %w", err)
				}
				if err := c.saveDispatcherConfigLocked(nextDisp); err != nil {
					return fmt.Errorf("save dispatcher config after telegram reset: %w", err)
				}
				res.DispatcherReset = false
			} else {
				c.resetDispatcherLocked()
				res.DispatcherReset = true
			}
		} else {
			res.DispatcherReset = true
		}

		if opts.DeleteKeenDNS {
			targetHosts := collectTelegramHosts(prevTg, prevDisp.Routes)
			protectedHosts := collectVlessHosts(xrayCfg, prevDisp.Routes)
			allowedPorts := []int{dispPort, tgwebproxy.DefaultListenPort}
			if prevTg.ListenPort > 0 && prevTg.ListenPort != dispPort && prevTg.ListenPort != tgwebproxy.DefaultListenPort && prevTg.ListenPort != 2222 {
				allowedPorts = append(allowedPorts, prevTg.ListenPort)
			}
			del, sk, warn := c.cleanupKeenDNSForResetLocked(ctx, targetHosts, protectedHosts, dispPort, allowedPorts, "VLESS")
			res.KeenDNSDeleted = del
			res.KeenDNSSkipped = sk
			res.Warnings = append(res.Warnings, warn...)
		}

		if c.dataDir != "" {
			_ = os.Remove(c.journalPath())
		}
		c.setRecovery(false, "")
		res.Reset = true
		return nil
	})
	return res, err
}
