package warpscout

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	mrand "math/rand"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/signature"
)

// TopicFindJunk — топик шины событий с прогрессом автоподбора маскировки.
const TopicFindJunk = "warpscout:findjunk"

const (
	findJunkDefaultAttempts  = 10
	findJunkMaxAttempts      = 30
	findJunkDefaultThreshold = 95
	findJunkDefaultPerSubnet = 2
	findJunkMaxPerSubnet     = 5

	// Диапазоны случайной генерации Jc/Jmin/Jmax (совместимы с WARP и vernette/warpscout).
	junkCountMin = 4
	junkCountMax = 6
	junkSizeMin  = 10
	junkSizeMax  = 50
	junkSpanMin  = 20
	junkSpanMax  = 100
)

// FindJunkRequest — параметры автоматического подбора маскировки AWG (Jc/Jmin/Jmax + I1..I5).
type FindJunkRequest struct {
	I1Profile    string `json:"i1Profile"`              // "random", "quic_initial", "dns", "sip", "stun", "dtls", "none"
	MaxAttempts  int    `json:"maxAttempts,omitempty"`  // по умолчанию 10 (1..30)
	ThresholdPct int    `json:"thresholdPct,omitempty"` // целевой процент рабочих узлов, по умолчанию 95 (50..100)
	PerSubnet    int    `json:"perSubnet,omitempty"`    // эндпоинтов на префикс, по умолчанию 2 (1..5)
	Ifname       string `json:"ifname,omitempty"`       // явный интерфейс выхода
	AutoApply    bool   `json:"autoApply,omitempty"`    // сразу сохранить лучший набор в настройки сканера
}

// JunkAttempt — результат одной попытки перебора параметров маскировки.
type JunkAttempt struct {
	Attempt    int       `json:"attempt"`
	AWG        AWGParams `json:"awg"`
	Profile    string    `json:"profile"`
	Working    int       `json:"working"`
	Scanned    int       `json:"scanned"`
	Total      int       `json:"total"`
	Pct        int       `json:"pct"`
	AvgPingMs  int       `json:"avgPingMs,omitempty"`
	DurationMs int       `json:"durationMs"`
	Incomplete bool      `json:"incomplete,omitempty"`
	GoodEnough bool      `json:"goodEnough,omitempty"`
}

// FindJunkStatus — текущее состояние и история автоподбора маскировки.
type FindJunkStatus struct {
	RunID          uint64        `json:"runId,omitempty"`
	Running        bool          `json:"running"`
	StartedAt      time.Time     `json:"startedAt,omitempty"`
	FinishedAt     time.Time     `json:"finishedAt,omitempty"`
	I1Profile      string        `json:"i1Profile,omitempty"`
	Attempt        int           `json:"attempt"`
	MaxAttempts    int           `json:"maxAttempts"`
	ThresholdPct   int           `json:"thresholdPct"`
	Attempts       []JunkAttempt `json:"attempts"`
	Best           *JunkAttempt  `json:"best,omitempty"`
	Applied        bool          `json:"applied,omitempty"`
	AppliedAttempt int           `json:"appliedAttempt,omitempty"`
	Aborted        bool          `json:"aborted,omitempty"`
	Error          string        `json:"error,omitempty"`
}

func cloneFindJunkStatus(in FindJunkStatus) FindJunkStatus {
	out := in
	if len(in.Attempts) > 0 {
		out.Attempts = make([]JunkAttempt, len(in.Attempts))
		copy(out.Attempts, in.Attempts)
	} else {
		out.Attempts = []JunkAttempt{}
	}
	if in.Best != nil {
		b := *in.Best
		out.Best = &b
	}
	return out
}

func normalizeFindJunkRequest(req FindJunkRequest) (FindJunkRequest, error) {
	prof := strings.ToLower(strings.TrimSpace(req.I1Profile))
	switch prof {
	case "", "random":
		prof = "random"
	case "none":
		prof = "none"
	case "quic":
		prof = "quic_initial"
	default:
		canon := signature.CanonicalProtocol(prof)
		if canon == "" {
			return req, fmt.Errorf("%w: неизвестный профиль I1 %q", ErrInvalidInput, req.I1Profile)
		}
		prof = canon
	}
	req.I1Profile = prof
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = findJunkDefaultAttempts
	} else if req.MaxAttempts > findJunkMaxAttempts {
		req.MaxAttempts = findJunkMaxAttempts
	}
	if req.ThresholdPct <= 0 {
		req.ThresholdPct = findJunkDefaultThreshold
	} else if req.ThresholdPct < 50 {
		req.ThresholdPct = 50
	} else if req.ThresholdPct > 100 {
		req.ThresholdPct = 100
	}
	if req.PerSubnet <= 0 {
		req.PerSubnet = findJunkDefaultPerSubnet
	} else if req.PerSubnet > findJunkMaxPerSubnet {
		req.PerSubnet = findJunkMaxPerSubnet
	}
	return req, nil
}

func newFindJunkRand() *mrand.Rand {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return mrand.New(mrand.NewSource(int64(binary.LittleEndian.Uint64(b[:]))))
}

func randBetween(r *mrand.Rand, lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + r.Intn(hi-lo+1)
}

func generateJunkCandidate(r *mrand.Rand, i1Profile string) (AWGParams, string, error) {
	jc := randBetween(r, junkCountMin, junkCountMax)
	jmin := randBetween(r, junkSizeMin, junkSizeMax)
	jmax := jmin + randBetween(r, junkSpanMin, junkSpanMax)

	awg := AWGParams{
		Jc:   jc,
		Jmin: jmin,
		Jmax: jmax,
	}
	if i1Profile == "none" {
		awg.SignatureProfile = "none"
		return awg, "none", nil
	}
	prof := i1Profile
	if prof == "random" {
		prof = signature.Profiles[r.Intn(len(signature.Profiles))]
	}
	res, err := signature.Generate(prof)
	if err != nil {
		return awg, prof, err
	}
	awg.SignatureProfile = res.Profile
	awg.I1 = res.Packets.I1
	awg.I2 = res.Packets.I2
	awg.I3 = res.Packets.I3
	awg.I4 = res.Packets.I4
	awg.I5 = res.Packets.I5
	return awg, res.Profile, nil
}

// buildSampleEndpoints выбирает по perSubnet случайных адресов из каждого
// префикса DefaultPrefixes со случайным портом из DefaultPorts.
func buildSampleEndpoints(r *mrand.Rand, perSubnet int) []string {
	primaryPorts := []int{2408, 500, 1701, 4500}
	out := make([]string, 0, len(DefaultPrefixes)*perSubnet)
	seen := make(map[string]struct{}, len(DefaultPrefixes)*perSubnet)
	for _, cidr := range DefaultPrefixes {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		for i := 0; i < perSubnet; i++ {
			host := byte(randBetween(r, 1, 254))
			var port int
			if i == 0 || len(DefaultPorts) == 0 {
				port = primaryPorts[r.Intn(len(primaryPorts))]
			} else {
				port = DefaultPorts[r.Intn(len(DefaultPorts))]
			}
			ep := fmt.Sprintf("%d.%d.%d.%d:%d", ip4[0], ip4[1], ip4[2], host, port)
			if _, dup := seen[ep]; dup {
				continue
			}
			seen[ep] = struct{}{}
			out = append(out, ep)
		}
	}
	return out
}

func isUsableJunkAttempt(a *JunkAttempt) bool {
	return a != nil && !a.Incomplete && a.Total > 0 && a.Working > 0
}

func findJunkDeadlineMs(sampleCount int, outerWarp bool) int {
	if sampleCount <= 0 {
		return 20000
	}
	if outerWarp {
		// Concurrency=1 внутри внешнего WARP: последовательная проверка каждого узла.
		return sampleCount*4500 + 5000
	}
	waves := (sampleCount + 23) / 24
	ms := waves*6000 + 10000
	if ms < 25000 {
		ms = 25000
	}
	return ms
}

func betterJunkAttempt(cand, cur *JunkAttempt) bool {
	if cur == nil {
		return true
	}
	if cand.Working != cur.Working {
		return cand.Working > cur.Working
	}
	if cand.Pct != cur.Pct {
		return cand.Pct > cur.Pct
	}
	if cand.AvgPingMs > 0 && (cur.AvgPingMs == 0 || cand.AvgPingMs < cur.AvgPingMs) {
		return true
	}
	return false
}

// StartFindJunk запускает фоновый автоподбор параметров маскировки AmneziaWG
// (Jc, Jmin, Jmax, I1..I5) по принципу find-junk из vernette/warpscout.
func (s *Service) StartFindJunk(req FindJunkRequest) error {
	req, err := normalizeFindJunkRequest(req)
	if err != nil {
		return err
	}
	acct := s.d.Store.Account()
	if acct == nil {
		return ErrNoAccount
	}
	if _, ok := s.d.Runner.Available(); !ok {
		return ErrEngineMissing
	}
	s.findJunkOpMu.Lock()
	defer s.findJunkOpMu.Unlock()

	s.mu.Lock()
	if s.scan.Running || s.findJunk.Running {
		s.mu.Unlock()
		return ErrScanRunning
	}
	s.mu.Unlock()
	if !s.engineMu.TryLock() {
		return ErrEngineBusy
	}

	ctx, cancel := context.WithCancel(s.rootCtx)
	s.mu.Lock()
	s.findJunkSeq++
	runID := s.findJunkSeq
	s.findJunk = FindJunkStatus{
		RunID:        runID,
		Running:      true,
		StartedAt:    s.d.Now(),
		I1Profile:    req.I1Profile,
		MaxAttempts:  req.MaxAttempts,
		ThresholdPct: req.ThresholdPct,
		Attempts:     []JunkAttempt{},
	}
	s.findJunkCancel = cancel
	snap := cloneFindJunkStatus(s.findJunk)
	s.mu.Unlock()
	s.publish(TopicFindJunk, snap)
	s.logInfo("findjunk", "manual", fmt.Sprintf("Запуск подбора маскировки (профиль %s, порог %d%%, попыток до %d)", req.I1Profile, req.ThresholdPct, req.MaxAttempts))

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()

		var runErr error
		var best *JunkAttempt
		func() {
			defer s.engineMu.Unlock()
			scanIface := s.resolveScanIface(ctx, req.Ifname)
			outer := s.resolveRouteTunnel(ctx, scanIface)
			scanAcct := acct
			if outer != nil && outer.IsWarp {
				if !outer.Running {
					runErr = errors.New("внешний туннель WARP выключен — сначала запустите его")
				} else {
					scanAcct, runErr = s.ensureDistinctAccount(ctx, outer.Iface, outer.LocalPublicKey)
				}
			}

			r := newFindJunkRand()
			var sample []string
			if runErr == nil {
				perSubnet := req.PerSubnet
				if outer != nil && outer.IsWarp && perSubnet > 1 {
					// Внутри внешнего WARP берём по 1 адресу из каждой из 15 подсетей
					// при Concurrency=1 для представительности и укладки в бюджет времени.
					perSubnet = 1
				}
				sample = buildSampleEndpoints(r, perSubnet)
			}

			for attemptNum := 1; runErr == nil && attemptNum <= req.MaxAttempts; attemptNum++ {
				if ctx.Err() != nil {
					break
				}
				awg, usedProf, genErr := generateJunkCandidate(r, req.I1Profile)
				if genErr != nil {
					runErr = genErr
					break
				}
				s.mu.Lock()
				s.findJunk.Attempt = attemptNum
				snap := cloneFindJunkStatus(s.findJunk)
				s.mu.Unlock()
				s.publish(TopicFindJunk, snap)

				params, pErr := s.buildParams(ctx, scanAcct, awg, Filters{Protocol: ProtocolAWG}, len(sample), len(sample), sample, nil, req.Ifname, outer)
				if pErr != nil {
					runErr = pErr
					break
				}
				outerWarp := outer != nil && outer.IsWarp
				params.TimeoutMs = 1500
				params.DeadlineMs = findJunkDeadlineMs(len(sample), outerWarp)
				if !outerWarp {
					params.Concurrency = 24
					params.PPS = 120
					params.DurabilityPings = 3
					params.DurabilityIntervalMs = 120
				}

				started := s.d.Now()
				expectedTotal := len(sample)
				total := expectedTotal
				working := 0
				pingSum := 0
				pingCount := 0
				sawDone := false
				doneScanned := 0
				doneAborted := false
				err := s.d.Runner.Run(ctx, params, func(ev EngineEvent) {
					switch ev.Type {
					case "start":
						if ev.Total > expectedTotal {
							total = ev.Total
						}
					case "hit":
						working++
						if ev.PingMs > 0 {
							pingSum += ev.PingMs
							pingCount++
						}
					case "done":
						sawDone = true
						doneScanned = ev.Scanned
						doneAborted = ev.Aborted
					}
				})
				if ctx.Err() != nil {
					// Прервано пользователем (Стоп) — сохраняем лучшие уже завершённые попытки.
					break
				}
				if err != nil {
					runErr = err
					break
				}
				if total <= 0 {
					total = expectedTotal
				}
				if working > total {
					working = total
				}
				complete := sawDone && !doneAborted && total > 0 && doneScanned >= total
				pct := 0
				if total > 0 {
					pct = (working * 100) / total
				}
				avgPing := 0
				if pingCount > 0 {
					avgPing = pingSum / pingCount
				}
				durMs := int(s.d.Now().Sub(started).Milliseconds())
				if durMs < 0 {
					durMs = 0
				}
				att := JunkAttempt{
					Attempt:    attemptNum,
					AWG:        awg,
					Profile:    usedProf,
					Working:    working,
					Scanned:    doneScanned,
					Total:      total,
					Pct:        pct,
					AvgPingMs:  avgPing,
					DurationMs: durMs,
					Incomplete: !complete,
					GoodEnough: complete && total > 0 && pct >= req.ThresholdPct,
				}
				if isUsableJunkAttempt(&att) && betterJunkAttempt(&att, best) {
					b := att
					best = &b
				}
				s.mu.Lock()
				if s.findJunk.RunID == runID {
					s.findJunk.Attempts = append(s.findJunk.Attempts, att)
					if best != nil {
						b := *best
						s.findJunk.Best = &b
					}
				}
				snap = cloneFindJunkStatus(s.findJunk)
				s.mu.Unlock()
				s.publish(TopicFindJunk, snap)

				if att.GoodEnough {
					break
				}
			}
		}()

		s.findJunkOpMu.Lock()
		applied := false
		appliedAttempt := 0
		if runErr == nil && req.AutoApply && isUsableJunkAttempt(best) {
			if err := s.d.Store.SetAWG(best.AWG); err != nil {
				runErr = fmt.Errorf("не удалось сохранить подобранную маскировку: %w", err)
			} else {
				applied = true
				appliedAttempt = best.Attempt
			}
		}

		s.mu.Lock()
		if s.findJunk.RunID == runID {
			s.findJunk.Running = false
			s.findJunk.FinishedAt = s.d.Now()
			s.findJunk.Applied = applied
			s.findJunk.AppliedAttempt = appliedAttempt
			if ctx.Err() != nil {
				s.findJunk.Aborted = true
			}
			if runErr != nil {
				s.findJunk.Error = runErr.Error()
			}
			s.findJunkCancel = nil
		}
		finalSnap := cloneFindJunkStatus(s.findJunk)
		s.mu.Unlock()
		s.findJunkOpMu.Unlock()
		s.publish(TopicFindJunk, finalSnap)

		if runErr != nil {
			s.logWarn("findjunk", "manual", "Подбор маскировки завершился ошибкой: "+runErr.Error())
		} else if best != nil {
			s.logInfo("findjunk", "manual", fmt.Sprintf("Подбор маскировки завершён: лучший набор Jc=%d Jmin=%d Jmax=%d (%s), %d/%d (%d%%)", best.AWG.Jc, best.AWG.Jmin, best.AWG.Jmax, best.Profile, best.Working, best.Total, best.Pct))
		} else {
			s.logInfo("findjunk", "manual", "Подбор маскировки остановлен без рабочих наборов")
		}
	}()
	return nil
}

// StopFindJunk прерывает текущий подбор маскировки, сохраняя лучший найденный набор.
func (s *Service) StopFindJunk() {
	s.mu.Lock()
	cancel := s.findJunkCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ApplyFindJunk сохраняет лучший найденный набор маскировки в Config.AWG.
func (s *Service) ApplyFindJunk() (AWGParams, error) {
	s.findJunkOpMu.Lock()
	defer s.findJunkOpMu.Unlock()

	s.mu.Lock()
	if s.findJunk.Running {
		s.mu.Unlock()
		return AWGParams{}, ErrScanRunning
	}
	runID := s.findJunk.RunID
	var best *JunkAttempt
	if s.findJunk.Best != nil {
		cp := *s.findJunk.Best
		best = &cp
	}
	hook := s.testBeforeApplySave
	s.mu.Unlock()

	if !isUsableJunkAttempt(best) {
		return AWGParams{}, fmt.Errorf("%w: рабочие параметры маскировки не найдены", ErrInvalidInput)
	}
	if hook != nil {
		hook()
	}
	if err := s.d.Store.SetAWG(best.AWG); err != nil {
		return AWGParams{}, err
	}

	var snap FindJunkStatus
	published := false
	s.mu.Lock()
	if s.findJunk.RunID == runID && !s.findJunk.Running && s.findJunk.Best != nil && s.findJunk.Best.Attempt == best.Attempt {
		s.findJunk.Applied = true
		s.findJunk.AppliedAttempt = best.Attempt
		snap = cloneFindJunkStatus(s.findJunk)
		published = true
	}
	s.mu.Unlock()
	if published {
		s.publish(TopicFindJunk, snap)
	}
	return best.AWG, nil
}
