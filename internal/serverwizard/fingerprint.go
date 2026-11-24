package serverwizard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

var DefaultTargetPorts = []int{
	9008, // xray inbound
	9009, // cdn dispatcher inbound
	8085, // tproxy-server web proxy
	8086, // tproxy-server admin
	// 8443 and 2398 (telemt) intentionally excluded: they change during normal startup (STUN probe),
	// which would cause false PLAN_STALE errors in the wizard apply flow.
}

// XrayStateReader exposes state needed from Xray.
type XrayStateReader interface {
	GetConfig() vlessserver.Config
	GetStatus() vlessserver.Status
}

// DispatcherStateReader exposes state needed from Dispatcher.
type DispatcherStateReader interface {
	GetConfig() cdndispatcher.Config
	IsRunning() bool
}

// TgWebProxyStateReader exposes state needed from Telegram Web Proxy.
type TgWebProxyStateReader interface {
	GetConfig() tgwebproxy.PublicConfig
	GetStatus() tgwebproxy.Status
}

// RecoveryStateReader checks if ingress coordinator or services require recovery.
type RecoveryStateReader interface {
	IsRecoveryRequired() (bool, string)
}

// EgressSummaryReader provides a representation of egress availability.
type EgressSummaryReader interface {
	EgressSummary(ctx context.Context) string
}

// FingerprintEngine calculates cryptographic state fingerprints.
type FingerprintEngine struct {
	xray        XrayStateReader
	disp        DispatcherStateReader
	tg          TgWebProxyStateReader
	rec         RecoveryStateReader
	egress      EgressSummaryReader
	procDir     string
	targetPorts []int
}

// NewFingerprintEngine constructs an evaluator with production defaults.
func NewFingerprintEngine(
	xray XrayStateReader,
	disp DispatcherStateReader,
	tg TgWebProxyStateReader,
	rec RecoveryStateReader,
	egress EgressSummaryReader,
) *FingerprintEngine {
	return &FingerprintEngine{
		xray:        xray,
		disp:        disp,
		tg:          tg,
		rec:         rec,
		egress:      egress,
		procDir:     "/proc",
		targetPorts: DefaultTargetPorts,
	}
}

// SetProcDir overrides procfs path (useful for unit testing).
func (e *FingerprintEngine) SetProcDir(dir string) {
	e.procDir = dir
}

// SetTargetPorts overrides target ports queried via procnet.
func (e *FingerprintEngine) SetTargetPorts(ports []int) {
	e.targetPorts = ports
}

// Compute returns a deterministic SHA-256 state fingerprint across all system dimensions.
func (e *FingerprintEngine) Compute(ctx context.Context) string {
	h := sha256.New()

	// 1. Xray Config & Status
	if e.xray != nil {
		xc := e.xray.GetConfig()
		xs := e.xray.GetStatus()
		data, _ := json.Marshal(struct {
			Cfg vlessserver.Config `json:"cfg"`
			St  vlessserver.Status `json:"st"`
		}{Cfg: xc, St: xs})
		h.Write([]byte("xray:"))
		h.Write(data)
		h.Write([]byte("\n"))
	}

	// 2. Dispatcher Config & State
	if e.disp != nil {
		dc := e.disp.GetConfig()
		running := e.disp.IsRunning()
		data, _ := json.Marshal(struct {
			Cfg     cdndispatcher.Config `json:"cfg"`
			Running bool                 `json:"running"`
		}{Cfg: dc, Running: running})
		h.Write([]byte("disp:"))
		h.Write(data)
		h.Write([]byte("\n"))
	}

	// 3. Telegram Web Proxy Config only (Status excluded: PID/online flags are volatile during startup)
	if e.tg != nil {
		tc := e.tg.GetConfig()
		data, _ := json.Marshal(tc)
		h.Write([]byte("tg:"))
		h.Write(data)
		h.Write([]byte("\n"))
	}

	// 4. Recovery Required state
	if e.rec != nil {
		recReq, recReason := e.rec.IsRecoveryRequired()
		h.Write([]byte(fmt.Sprintf("rec:%t:%s\n", recReq, recReason)))
	}

	// 6. Procfs Port Occupancy via procnet
	sortedPorts := append([]int(nil), e.targetPorts...)
	sort.Ints(sortedPorts)
	procDir := e.procDir
	if procDir == "" {
		procDir = "/proc"
	}
	for _, port := range sortedPorts {
		lookup, err := procnet.FindListeningProcess(procDir, "0.0.0.0", port)
		if err != nil {
			h.Write([]byte(fmt.Sprintf("port:%d:err:%s\n", port, err.Error())))
		} else if !lookup.SocketFound {
			h.Write([]byte(fmt.Sprintf("port:%d:free\n", port)))
		} else {
			h.Write([]byte(fmt.Sprintf("port:%d:inode:%s:pid:%d\n", port, lookup.SocketInode, lookup.PID)))
		}
	}

	// 7. Egress Summary
	if e.egress != nil {
		summary := e.egress.EgressSummary(ctx)
		h.Write([]byte("egress:" + summary + "\n"))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// ComputeStrict computes the SHA-256 state fingerprint, but fails closed with an error if context is canceled
// or if any probe encounters an IO/system error rather than an expected absent/free state.
func (e *FingerprintEngine) ComputeStrict(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("context canceled: %w", err)
	}

	h := sha256.New()

	// 1. Xray Config & Status
	if e.xray != nil {
		xc := e.xray.GetConfig()
		xs := e.xray.GetStatus()
		data, err := json.Marshal(struct {
			Cfg vlessserver.Config `json:"cfg"`
			St  vlessserver.Status `json:"st"`
		}{Cfg: xc, St: xs})
		if err != nil {
			return "", fmt.Errorf("marshal xray state: %w", err)
		}
		h.Write([]byte("xray:"))
		h.Write(data)
		h.Write([]byte("\n"))
	}

	// 2. Dispatcher Config & State
	if e.disp != nil {
		dc := e.disp.GetConfig()
		running := e.disp.IsRunning()
		data, err := json.Marshal(struct {
			Cfg     cdndispatcher.Config `json:"cfg"`
			Running bool                 `json:"running"`
		}{Cfg: dc, Running: running})
		if err != nil {
			return "", fmt.Errorf("marshal disp state: %w", err)
		}
		h.Write([]byte("disp:"))
		h.Write(data)
		h.Write([]byte("\n"))
	}

	// 3. Telegram Web Proxy Config only (Status excluded: PID/online flags are volatile during startup)
	if e.tg != nil {
		tc := e.tg.GetConfig()
		data, err := json.Marshal(tc)
		if err != nil {
			return "", fmt.Errorf("marshal tg config: %w", err)
		}
		h.Write([]byte("tg:"))
		h.Write(data)
		h.Write([]byte("\n"))
	}

	// 4. Recovery Required state
	if e.rec != nil {
		recReq, recReason := e.rec.IsRecoveryRequired()
		h.Write([]byte(fmt.Sprintf("rec:%t:%s\n", recReq, recReason)))
	}

	// 6. Procfs Port Occupancy via procnet
	sortedPorts := append([]int(nil), e.targetPorts...)
	sort.Ints(sortedPorts)
	procDir := e.procDir
	if procDir == "" {
		procDir = "/proc"
	}
	for _, port := range sortedPorts {
		lookup, err := procnet.FindListeningProcess(procDir, "0.0.0.0", port)
		if err != nil {
			return "", fmt.Errorf("procnet lookup port %d: %w", port, err)
		}
		if !lookup.SocketFound {
			h.Write([]byte(fmt.Sprintf("port:%d:free\n", port)))
		} else {
			h.Write([]byte(fmt.Sprintf("port:%d:inode:%s:pid:%d\n", port, lookup.SocketInode, lookup.PID)))
		}
	}

	// 7. Egress Summary
	if e.egress != nil {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("context canceled before egress: %w", err)
		}
		summary := e.egress.EgressSummary(ctx)
		h.Write([]byte("egress:" + summary + "\n"))
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
