package warpscout

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// backoffSteps — растущие паузы после неудачной замены: скан не нашёл ни
// одного узла (глобальный шатдаун WARP у провайдера), подмена не удалась или
// контрольная проверка после неё не прошла. Роутер не должен сканировать и
// переписывать endpoint без остановки; счётчик сбрасывает успешная проверка.
var backoffSteps = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}

// Тайминги сторожа (переменные — для тестов).
var (
	watchStartDelay  = 15 * time.Second
	colorecheckDelay = 3 * time.Second
	verifyAfterSwap  = 6 * time.Second
	verifyRetryDelay = 1500 * time.Millisecond
	swapScanDeadline = 120 * time.Second
	swapScanMaxHits  = 3
	// scanDeadline — дедлайн ручного скана, scanKillGrace — запас сверх
	// дедлайна, после которого зависший процесс сканера снимается.
	scanDeadline  = 180 * time.Second
	scanKillGrace = 15 * time.Second
)

// watcher — запущенный сторож и профиль, с которым он запущен.
type watcher struct {
	cancel context.CancelFunc
	p      Profile
}

func (s *Service) reconcileWatchers(cfg Config) {
	now := s.d.Now()
	active := map[string]bool{}
	for _, p := range cfg.Profiles {
		if p.Enabled && (cfg.ProfileMode != ProfileModeSingle || p.ID == cfg.ActiveProfileID) {
			active[p.ID] = true
		}
	}
	s.mu.Lock()
	for _, p := range cfg.Profiles {
		w, running := s.watchers[p.ID]
		if running && active[p.ID] && reflect.DeepEqual(w.p, p) {
			// Профиль не менялся — сторож остаётся со своим cooldown и backoff.
			continue
		}
		if running {
			w.cancel()
			delete(s.watchers, p.ID)
		}
		if !active[p.ID] {
			continue
		}
		ctx, cancel := context.WithCancel(s.rootCtx)
		s.watchers[p.ID] = &watcher{cancel: cancel, p: p}
		if _, ok := s.watch[p.ID]; !ok {
			s.watch[p.ID] = &WatchStatus{ProfileID: p.ID, State: WatchIdle}
		}
		ps, ok := s.stats[p.ID]
		if !ok {
			ps = &ProfileStats{ProfileID: p.ID, StartedAt: now}
			s.stats[p.ID] = ps
		} else if ps.StartedAt.IsZero() {
			ps.StartedAt = now
		}
		s.wg.Add(1)
		go func(p Profile) {
			defer s.wg.Done()
			s.runWatcher(ctx, p)
		}(p)
	}
	for id, ws := range s.watch {
		if !active[id] {
			ws.State = WatchIdle
			ws.Message = ""
			ws.Fails = 0
		}
	}
	// Статусы и статистику удалённых профилей не держим.
	known := map[string]bool{}
	for _, p := range cfg.Profiles {
		known[p.ID] = true
	}
	removedStats := false
	for id, w := range s.watchers {
		if !known[id] {
			w.cancel()
			delete(s.watchers, id)
		}
	}
	for id := range s.watch {
		if !known[id] {
			delete(s.watch, id)
		}
	}
	for id := range s.stats {
		if !known[id] {
			delete(s.stats, id)
			removedStats = true
		}
	}
	s.mu.Unlock()
	if removedStats {
		s.flushStats()
	}
}

// watcherState — локальное состояние цикла одного профиля.
type watcherState struct {
	fails     int
	nextSwap  time.Time
	backoffIx int
}

func (s *Service) runWatcher(ctx context.Context, p Profile) {
	s.logInfo("autopilot", p.Name, fmt.Sprintf("Автопилот запущен: %s %s, проверка каждые %d с", p.TargetMode, p.TargetID, p.CheckIntervalSec))
	st := &watcherState{}
	timer := time.NewTimer(watchStartDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.checkOnce(ctx, p, st)
		timer.Reset(time.Duration(p.CheckIntervalSec) * time.Second)
	}
}

// setWatch и record* сверяют ctx под s.mu: reconcileWatchers отменяет сторожа
// под тем же замком, поэтому отменённый сторож уже не воскресит состояние
// удалённого профиля.
func (s *Service) setWatch(ctx context.Context, id string, mut func(ws *WatchStatus)) WatchStatus {
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return WatchStatus{}
	}
	ws, ok := s.watch[id]
	if !ok {
		ws = &WatchStatus{ProfileID: id}
		s.watch[id] = ws
	}
	mut(ws)
	out := *ws
	s.mu.Unlock()
	s.publish(TopicWatchdog, out)
	return out
}

func (s *Service) recordCheckSample(ctx context.Context, id string, now time.Time, ep string, tr *TraceResult, ok bool, reason string) {
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	ps, exists := s.stats[id]
	if !exists {
		ps = &ProfileStats{ProfileID: id, StartedAt: now}
		s.stats[id] = ps
	}
	if ps.StartedAt.IsZero() {
		ps.StartedAt = now
	}
	if ep != "" && ps.CurrentEndpoint != ep {
		ps.CurrentEndpoint = ep
		ps.EndpointSince = now
	} else if ep != "" && ps.EndpointSince.IsZero() {
		ps.EndpointSince = now
	}
	ps.ChecksTotal++
	sample := CheckSample{At: now, OK: ok}
	if !ok && reason != "" {
		sample.Reason = reason
	}
	if tr != nil {
		sample.LatencyMs = tr.LatencyMs
		sample.Colo = tr.Colo
		sample.Loc = tr.Loc
	}
	if ok {
		ps.ChecksOK++
		if tr != nil && tr.LatencyMs > 0 {
			ps.LatencySumMs += int64(tr.LatencyMs)
			ps.LatencyCount++
			if ps.MinLatencyMs == 0 || tr.LatencyMs < ps.MinLatencyMs {
				ps.MinLatencyMs = tr.LatencyMs
			}
			if tr.LatencyMs > ps.MaxLatencyMs {
				ps.MaxLatencyMs = tr.LatencyMs
			}
			ps.AvgLatencyMs = int((ps.LatencySumMs + ps.LatencyCount/2) / ps.LatencyCount)
		}
		if tr != nil && tr.Colo != "" {
			if ps.Colos == nil {
				ps.Colos = map[string]int{}
			}
			ps.Colos[strings.ToUpper(tr.Colo)]++
		}
	} else {
		ps.ChecksFail++
	}
	ps.Samples = append(ps.Samples, sample)
	if len(ps.Samples) > maxCheckSamples {
		ps.Samples = ps.Samples[len(ps.Samples)-maxCheckSamples:]
	}
	// Счётчики проверок живут в памяти: на диск они уходят с подменой
	// endpoint и при остановке, а не по таймеру (износ флеша).
	s.statsDirty = true
	s.mu.Unlock()
}

func (s *Service) recordSwapResult(ctx context.Context, id string, now time.Time, ok bool, swapped bool, newEp string, tr *TraceResult) {
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	ps, exists := s.stats[id]
	if !exists {
		ps = &ProfileStats{ProfileID: id, StartedAt: now}
		s.stats[id] = ps
	}
	if ok {
		ps.SwapsOK++
	} else {
		ps.SwapsFail++
	}
	if swapped && newEp != "" {
		ps.CurrentEndpoint = newEp
		ps.EndpointSince = now
	}
	if n := len(ps.Samples); n > 0 {
		ps.Samples[n-1].Swapped = true
	} else {
		sm := CheckSample{At: now, OK: ok, Swapped: true}
		if tr != nil {
			sm.LatencyMs, sm.Colo, sm.Loc = tr.LatencyMs, tr.Colo, tr.Loc
		}
		ps.Samples = append(ps.Samples, sm)
	}
	s.statsDirty = true
	s.mu.Unlock()
	if swapped {
		s.flushStats()
	}
}

func evaluate(tr TraceResult, err error, f Filters) (reason string, coloBad bool) {
	if err != nil {
		return "нет связи через туннель: " + err.Error(), false
	}
	for _, c := range f.ExcludeColos {
		if strings.EqualFold(c, tr.Colo) {
			return "узел переехал в запрещённый колокейшн " + tr.Colo, true
		}
	}
	if len(f.AllowedCountries) > 0 {
		ok := false
		for _, c := range f.AllowedCountries {
			if strings.EqualFold(c, tr.Loc) {
				ok = true
				break
			}
		}
		if !ok {
			return "страна выхода " + tr.Loc + " не из разрешённых", true
		}
	}
	if tr.Warp != "" && tr.Warp != "on" && tr.Warp != "plus" {
		return "трафик идёт мимо WARP (warp=" + tr.Warp + ")", false
	}
	if f.MaxRTTMs > 0 && tr.LatencyMs > f.MaxRTTMs {
		return fmt.Sprintf("задержка %d мс выше лимита %d мс", tr.LatencyMs, f.MaxRTTMs), false
	}
	return "", false
}

func (s *Service) checkOnce(ctx context.Context, p Profile, st *watcherState) {
	now := s.d.Now()
	if s.d.Store.Account() == nil {
		s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
			ws.State, ws.Message, ws.LastCheck = WatchNoAccount, "зарегистрируйте учётную запись WARP", now
		})
		return
	}
	if _, up := s.d.WAN(ctx); !up {
		// Упал провайдер — менять узел бессмысленно.
		st.fails = 0
		s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
			ws.State, ws.Message, ws.LastCheck, ws.Fails = WatchWANDown, "нет подключения к провайдеру", now, 0
		})
		return
	}
	ref, err := s.d.Tunnels.Get(ctx, p.TargetMode, p.TargetID)
	if err != nil || ref == nil {
		s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
			ws.State, ws.LastCheck = WatchTargetMissing, now
			ws.Message = "туннель не найден"
			if err != nil {
				ws.Message += ": " + err.Error()
			}
		})
		return
	}
	if err := checkTarget(ref); err != nil {
		state := WatchNotWarp
		if errors.Is(err, ErrProtected) {
			state = WatchProtected
		}
		s.setWatch(ctx, p.ID, func(ws *WatchStatus) { ws.State, ws.Message, ws.LastCheck = state, err.Error(), now })
		return
	}
	if !ref.Running {
		st.fails = 0
		s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
			ws.State, ws.Message, ws.LastCheck, ws.Fails, ws.Endpoint = WatchTargetStopped, "туннель выключен — автопилот ждёт", now, 0, ref.Endpoint
		})
		return
	}

	tr, terr := s.d.Prober.Trace(ctx, ref.Iface)
	reason, coloBad := evaluate(tr, terr, p.Filters)
	if reason != "" && coloBad {
		// Колокейшн мог мигнуть на одном запросе — подтверждаем вторым.
		select {
		case <-ctx.Done():
			return
		case <-time.After(colorecheckDelay):
		}
		tr2, terr2 := s.d.Prober.Trace(ctx, ref.Iface)
		if r2, bad2 := evaluate(tr2, terr2, p.Filters); r2 == "" || !bad2 {
			reason, coloBad, tr, terr = r2, false, tr2, terr2
		}
	}
	var trp *TraceResult
	if terr == nil {
		t := tr
		trp = &t
	}
	if reason == "" {
		st.fails, st.backoffIx = 0, 0
		s.recordCheckSample(ctx, p.ID, now, ref.Endpoint, trp, true, "")
		s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
			ws.State, ws.Message, ws.LastCheck, ws.Trace, ws.Fails, ws.Endpoint = WatchOK, "", now, trp, 0, ref.Endpoint
			ws.NextSwapAfter = st.nextSwap
		})
		return
	}
	st.fails++
	s.recordCheckSample(ctx, p.ID, now, ref.Endpoint, trp, false, reason)
	trigger := coloBad || st.fails >= p.FailThreshold
	s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
		ws.State, ws.Message, ws.LastCheck, ws.Fails, ws.Endpoint = WatchDegraded, reason, now, st.fails, ref.Endpoint
		if trp != nil {
			ws.Trace = trp
		}
		ws.NextSwapAfter = st.nextSwap
	})
	if !trigger {
		return
	}
	if now.Before(st.nextSwap) {
		return
	}
	s.swap(ctx, p, ref, reason, st)
}

func (s *Service) swap(ctx context.Context, p Profile, ref *TunnelRef, reason string, st *watcherState) {
	acct := s.d.Store.Account()
	if acct == nil {
		return
	}
	if _, ok := s.d.Runner.Available(); !ok {
		s.setWatch(ctx, p.ID, func(ws *WatchStatus) { ws.State, ws.Message = WatchEngineMissing, ErrEngineMissing.Error() })
		return
	}
	if !s.engineMu.TryLock() {
		s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
			ws.State, ws.Message = WatchEngineBusy, "сканер занят — повтор на следующей проверке"
		})
		return
	}
	defer s.engineMu.Unlock()
	s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
		ws.State, ws.Message = WatchSwapping, "поиск нового узла: "+reason
	})
	s.logWarn("autopilot", p.Name, "Замена узла WARP: "+reason)

	ev := SwapEvent{At: s.d.Now(), ProfileID: p.ID, ProfileName: p.Name, TargetMode: p.TargetMode,
		TargetID: p.TargetID, Reason: reason, OldEndpoint: ref.Endpoint}

	exclude := []string{}
	if ref.Endpoint != "" {
		exclude = append(exclude, ref.Endpoint)
	}
	// Нельзя сканировать изнутри самого заменяемого туннеля — упадём на WAN по умолчанию.
	scanOverride := s.d.Store.Config().ScanInterface
	if scanOverride != "" && ref.Iface != "" && scanOverride == ref.Iface {
		wan, _ := s.d.WAN(ctx)
		scanOverride = wan
	}
	params, err := s.buildParams(ctx, acct, s.awgForUse(), p.Filters, swapScanMaxHits, 0, nil, exclude, scanOverride)
	if err != nil {
		s.finishSwap(ctx, p, ev, st, err, nil, false, false)
		return
	}
	params.DeadlineMs = int(swapScanDeadline / time.Millisecond)
	var hits []Hit
	scanCtx, cancel := context.WithTimeout(ctx, swapScanDeadline+scanKillGrace)
	runErr := s.d.Runner.Run(scanCtx, params, func(e EngineEvent) {
		if e.Type == "hit" {
			hits = append(hits, e.Hit())
		}
	})
	cancel()
	if ctx.Err() != nil {
		return
	}
	if len(hits) == 0 {
		if runErr == nil {
			runErr = errors.New("ни одного подходящего узла не найдено")
		}
		s.finishSwap(ctx, p, ev, st, runErr, nil, true, false)
		return
	}
	sortHits(hits)
	best := hits[0]
	ev.NewEndpoint, ev.Colo, ev.Loc, ev.PingMs = best.Endpoint, best.Colo, best.Loc, best.PingMs
	if err := validEndpoint(best.Endpoint); err != nil {
		s.finishSwap(ctx, p, ev, st, fmt.Errorf("сканер вернул узел %q: %w", best.Endpoint, err), nil, false, false)
		return
	}
	if err := s.d.Tunnels.SwapEndpoint(ctx, p.TargetMode, p.TargetID, best.Endpoint); err != nil {
		s.finishSwap(ctx, p, ev, st, fmt.Errorf("подмена endpoint: %w", err), nil, false, false)
		return
	}
	// Контрольный trace после подмены.
	select {
	case <-ctx.Done():
		return
	case <-time.After(verifyAfterSwap):
	}
	var (
		verifyErr error
		verified  *TraceResult
	)
	cur, err := s.d.Tunnels.Get(ctx, p.TargetMode, p.TargetID)
	switch {
	case err != nil:
		verifyErr = fmt.Errorf("после подмены не удалось проверить туннель: %w", err)
	case cur == nil:
		verifyErr = errors.New("после подмены туннель не найден")
	case !cur.Running:
		verifyErr = errors.New("после подмены туннель выключен")
	default:
		tr, terr := s.d.Prober.Trace(ctx, cur.Iface)
		if terr != nil && verifyRetryDelay > 0 {
			// Первый SYN через свежепереключённый туннель запускает рукопожатие WG —
			// если стартовый пакет потерялся на WAN, даём вторую попытку.
			select {
			case <-ctx.Done():
				return
			case <-time.After(verifyRetryDelay):
			}
			tr, terr = s.d.Prober.Trace(ctx, cur.Iface)
		}
		if terr == nil {
			t := tr
			verified = &t
		}
		if r, _ := evaluate(tr, terr, p.Filters); r != "" {
			verifyErr = errors.New("после подмены: " + r)
		} else {
			ev.Colo, ev.Loc = tr.Colo, tr.Loc
		}
	}
	s.finishSwap(ctx, p, ev, st, verifyErr, verified, false, true)
}

func (s *Service) finishSwap(ctx context.Context, p Profile, ev SwapEvent, st *watcherState, err error, tr *TraceResult, noHits, swapped bool) {
	if ctx.Err() != nil {
		return
	}
	now := s.d.Now()
	ev.OK = err == nil
	if err != nil {
		ev.Error = err.Error()
	}
	var state, msg string
	if err != nil {
		d := backoffSteps[min(st.backoffIx, len(backoffSteps)-1)]
		st.backoffIx++
		st.nextSwap = now.Add(d)
		msg = fmt.Sprintf("%s — повтор через %s", ev.Error, d)
	}
	switch {
	case noHits:
		state = WatchBackoff
		s.logWarn("autopilot", p.Name, "Скан не нашёл узлов: "+ev.Error)
	case !swapped:
		state = WatchDegraded
		s.logWarn("autopilot", p.Name, "Замена не удалась: "+ev.Error)
	case err != nil:
		st.fails = 0
		state = WatchDegraded
		s.logWarn("autopilot", p.Name, fmt.Sprintf("Узел заменён %s → %s, но проверка не прошла: %s", ev.OldEndpoint, ev.NewEndpoint, ev.Error))
	default:
		st.fails, st.backoffIx = 0, 0
		st.nextSwap = now.Add(time.Duration(p.CooldownSec) * time.Second)
		state = WatchOK
		s.logInfo("autopilot", p.Name, fmt.Sprintf("Узел заменён: %s → %s (%s %s, %d мс)", ev.OldEndpoint, ev.NewEndpoint, ev.Colo, ev.Loc, ev.PingMs))
	}
	_ = s.d.Store.AppendHistory(ev)
	s.recordSwapResult(ctx, p.ID, now, ev.OK, swapped, ev.NewEndpoint, tr)
	evCopy := ev
	s.setWatch(ctx, p.ID, func(ws *WatchStatus) {
		ws.State, ws.Message, ws.NextSwapAfter, ws.LastSwap = state, msg, st.nextSwap, &evCopy
		if swapped {
			ws.Endpoint, ws.Fails, ws.Trace = ev.NewEndpoint, st.fails, tr
		}
	})
}
