package warpscout

import (
	"context"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/hoaxisr/awg-manager/internal/signature"
)

// runCandidates keeps each hit paired with the exact signature used on the wire.
func (s *Service) runCandidates(ctx context.Context, params ScanParams, base AWGParams, f Filters, nested bool, profile string, emit func(EngineEvent, *AWGParams)) error {
	if !f.Adaptive || f.Protocol == ProtocolWG {
		return s.d.Runner.Run(ctx, params, func(e EngineEvent) { emit(e, nil) })
	}
	seconds := f.SearchSeconds
	if seconds == 0 {
		seconds = 30
	}
	budget := time.Duration(max(6, min(seconds, 120))) * time.Second
	deadline := time.Now().Add(budget)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	r := newFindJunkRand()
	type trial struct {
		awg      AWGParams
		endpoint string
	}
	trials := []trial{}
	seen := map[AWGParams]bool{}
	for _, ev := range s.d.Store.History() {
		if profile == "" || ev.ProfileID != profile || ev.ScanInterface != params.Ifname || slices.Contains(params.ExcludeEndpoints, ev.NewEndpoint) || slices.Contains(params.ExcludeEndpoints, endpointIPv4(ev.NewEndpoint)) || !ev.OK || ev.AWG == nil || seen[*ev.AWG] || validEndpoint(ev.NewEndpoint) != nil {
			continue
		}
		if len(params.Ports) > 0 {
			allowed := false
			for _, port := range params.Ports {
				if ev.NewEndpoint == net.JoinHostPort(endpointIPv4(ev.NewEndpoint), strconv.Itoa(port)) {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		seen[*ev.AWG] = true
		trials = append(trials, trial{*ev.AWG, ev.NewEndpoint})
		if len(trials) == 2 {
			break
		}
	}
	trials = append(trials, trial{awg: base})
	for _, preset := range []string{signature.HostPresetRU, signature.HostPresetCDN, "default"} {
		opts := signature.GenerateOptions{}
		if pool, ok := signature.PresetDomains(preset); ok && len(pool) > 0 {
			opts.Domain = pool[r.Intn(len(pool))]
		}
		a, _, _, err := generateJunkCandidate(r, "quic_initial", opts, preset, "")
		if err != nil {
			return err
		}
		trials = append(trials, trial{awg: mergePreservedAWG3(a, base)})
	}
	// A recently failing configuration goes last only when another configuration succeeded on this interface.
	if s.lastPoorIface == params.Ifname && s.lastPoorAWG != nil && s.d.Now().Before(s.lastPoorUntil) {
		good := make([]trial, 0, len(trials))
		var deferred []trial
		for _, t := range trials {
			if t.awg == *s.lastPoorAWG {
				deferred = append(deferred, t)
			} else {
				good = append(good, t)
			}
		}
		trials = append(good, deferred...)
	}
	params.Seed = int64(r.Int31()) + 1
	best := map[string]Hit{}
	order := []string{}
	var lastErr error
	baseMiss := false
	attempts := 0
	start := time.Now()
	for i, t := range trials {
		if ctx.Err() != nil {
			break
		}
		p := params
		options, err := buildSwapOptions(ProtocolAWG, t.awg, nested)
		if err != nil {
			lastErr = err
			continue
		}
		a := options.AWG
		sig, err := signatureHex(a)
		if err != nil {
			lastErr = err
			continue
		}
		p.AWG = EngineAWG{Jc: a.Jc, Jmin: a.Jmin, Jmax: a.Jmax, I: sig}
		if t.endpoint != "" {
			p.Endpoints = []string{t.endpoint}
			p.Prefixes = []string{}
		}
		p.DeadlineMs = int(time.Until(deadline).Milliseconds()) / (len(trials) - i)
		if p.DeadlineMs < 100 {
			break
		}
		p.MaxHits = 1
		attempts++
		var found []Hit
		err = s.d.Runner.Run(ctx, p, func(e EngineEvent) {
			if e.Type == "hit" {
				h := e.Hit()
				if h.PingMs <= 0 || (f.MaxRTTMs > 0 && h.PingMs > f.MaxRTTMs) {
					return
				}
				h.AWG = &a
				found = append(found, h)
			}
		})
		if err != nil {
			lastErr = err
			continue
		}
		if t.awg == base && t.endpoint == "" && len(found) == 0 {
			baseMiss = true
		}
		if baseMiss && len(found) > 0 && t.awg != base {
			s.lastPoorIface = params.Ifname
			s.lastPoorAWG = &base
			s.lastPoorUntil = s.d.Now().Add(10 * time.Minute)
		}
		if t.awg == base && len(found) > 0 && s.lastPoorIface == params.Ifname {
			s.lastPoorAWG = nil
		}
		for _, h := range found {
			prev, ok := best[h.Endpoint]
			if !ok {
				order = append(order, h.Endpoint)
			}
			if !ok || h.PingMs < prev.PingMs {
				best[h.Endpoint] = h
			}
		}
		emit(EngineEvent{Type: "progress", Done: i + 1, Total: len(trials)}, nil)
		if len(found) > 0 && f.MaxRTTMs > 0 {
			break
		}
	}
	for _, ep := range order {
		h := best[ep]
		emit(EngineEvent{Type: "hit", Endpoint: ep, PingMs: h.PingMs, HandshakeMs: h.HandshakeMs, Colo: h.Colo, Loc: h.Loc, Warp: h.Warp, TgOK: h.TgOK}, h.AWG)
	}
	emit(EngineEvent{Type: "done", Scanned: attempts, ElapsedMs: int(time.Since(start).Milliseconds())}, nil)
	if len(order) == 0 {
		return lastErr
	}
	return nil
}
