package orchestrator

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/heavyop"
)

// ReloadNow cancels any pending debounced reload and applies the merged
// config immediately. Enable/Disable call this after SetEnabled+Save so
// sing-box cold-starts without waiting reloadDebounce — otherwise
// waitForSingbox burns the whole boot window before the process even
// starts (stand-found on aarch64 off→tproxy switches).
func (o *Orchestrator) ReloadNow() error {
	o.mu.Lock()
	if o.reloadTimer != nil {
		o.reloadTimer.Stop()
		o.reloadTimer = nil
	}
	// Явное применение накрывает и то, что подавил hold, — иначе release
	// выстрелил бы вторым, уже ненужным reload'ом.
	o.pendingReload = false
	o.mu.Unlock()
	return o.Reload()
}

// scheduleReload arms (or re-arms) the debounce timer. Caller MUST
// hold o.mu. Calling repeatedly within the window coalesces into one
// reload.
func (o *Orchestrator) scheduleReload() {
	if o.holds > 0 {
		o.pendingReload = true
		return
	}
	if o.reloadTimer != nil {
		o.reloadTimer.Reset(reloadDebounce)
		return
	}
	o.reloadTimer = time.AfterFunc(reloadDebounce, func() {
		// Выстреливший таймер обязан забыть себя: иначе указатель остаётся
		// ненулевым и следующий scheduleReload уходит в ветку Reset, а
		// HoldReloads видит «таймер взведён» там, где взводить уже нечего.
		o.mu.Lock()
		o.reloadTimer = nil
		o.mu.Unlock()
		if err := o.Reload(); err != nil {
			o.log("error", fmt.Sprintf("orchestrator reload: %v", err))
		}
	})
}

// Reload validates the merged enabled config and applies it to sing-box:
//   - If validation fails — log + return validation error, NO process change.
//   - If validation passes:
//   - If at least one non-base slot is enabled → ensure running:
//     start if stopped, SIGHUP if running.
//   - If only base (or nothing) is enabled → stop if running.
//
// Reload may be called manually (e.g. by Apply UI button) or fired by
// the internal debounce timer. Safe for concurrent callers — internally
// serialized by mu and the reloading flag.
func (o *Orchestrator) Reload() error {
	heavyop.Default.Lock()
	defer heavyop.Default.Unlock()

	o.mu.Lock()
	if o.reloading {
		o.mu.Unlock()
		return nil // collapse re-entrancy
	}
	o.reloading = true
	// Defense-in-depth: strip dangling selector/urltest members and defaults
	// (a tag whose outbound was deleted from another slot) BEFORE validating.
	// sing-box check does not catch these — like composite cycles, a missing
	// selector dependency only surfaces at "start service" (FATAL), so a stale
	// reference would otherwise reach the daemon and take the whole config down.
	pruneLogs := o.pruneDanglingSelectorRefsLocked()
	res := o.validateLocked()
	if !res.Ok() {
		// Запоминаем провал: reload пропущен, движок работает на старом
		// конфиге, а UI (router status / эксперт-редактор) может показать
		// причину через LastReloadValidation. Особенно важно для 90-user.json:
		// его висячие ссылки мы намеренно не чиним автоматически.
		failed := res
		o.lastReloadValidation = &failed
		o.reloading = false
		o.mu.Unlock()
		for _, m := range pruneLogs {
			// Prune mutated a producer's slot to keep sing-box alive — that is
			// self-healing of an inconsistency, not routine housekeeping. Warn
			// so the trace survives default log levels (issue #465: silent
			// prune of device-proxy selector members hid the corruption).
			o.log("warn", m)
		}
		msg := fmt.Sprintf("orchestrator validation failed; reload skipped: %s", res.Error())
		o.log("error", msg)
		return res
	}
	o.lastReloadValidation = nil // валидация прошла — прошлый провал неактуален
	newHash := o.enabledConfigHashLocked()
	needRunning := o.hasActiveWorkLocked()
	proc := o.proc
	shouldRun := o.shouldRun
	prevHasTun := o.prevHasTun
	newHasTun := res.HasTun
	o.mu.Unlock()
	for _, m := range pruneLogs {
		o.log("warn", m) // см. комментарий на warn-логировании prune выше
	}

	// Skip gate: if the config we are about to apply hashes identical to
	// what we last successfully applied AND sing-box is already running
	// in the shape we need, there is nothing to do — a SIGHUP would be a
	// no-op and a tun-restart would be actively harmful. This is what
	// lets a daemon restart (in-memory prevHasTun resets to false) ADOPT
	// an already-running sing-box instead of needlessly tearing it down.
	if proc != nil {
		if st, ok := loadAppliedState(o.appliedPath); ok && st.Hash == newHash {
			if running, pid := proc.IsRunning(); running && needRunning {
				o.mu.Lock()
				o.prevHasTun = st.HasTun
				o.reloading = false
				o.mu.Unlock()
				o.log("info", fmt.Sprintf("orchestrator: config unchanged — reload skipped (sing-box pid %d adopted)", pid))
				return nil
			}
		}
	}

	var err error
	// newApplied/saveApplied are decided per-branch below so the
	// suppressed cold-start sub-case (process stays down) does not get
	// mistaken for a successful apply just because err stayed nil.
	var newApplied appliedState
	saveApplied := false
	if proc == nil {
		// Test mode or pre-wiring — nothing to do, no real process to
		// describe, applied state left untouched.
	} else {
		running, _ := proc.IsRunning()
		switch {
		case needRunning && !running:
			// Honour the sticky-stop intent: if the user pressed Stop,
			// shouldRun returns false and we must not resurrect the
			// daemon merely because a slot file changed. SIGHUP and
			// stop branches stay unaffected — they don't cold-start.
			if shouldRun != nil && !shouldRun() {
				o.log("info", "orchestrator: cold-start suppressed by manual-stop intent")
				saveApplied = true // process stays down — record "nothing applied"
				break
			}
			o.log("info", "orchestrator: starting sing-box (active slots present)")
			err = proc.Start()
			if err == nil {
				newApplied = appliedState{Hash: newHash, HasTun: newHasTun}
			}
			saveApplied = err == nil
		case needRunning && running:
			if newHasTun != prevHasTun {
				// sing-box cannot add/remove a tun inbound via SIGHUP — the
				// tun device never gets carrier and readiness times out. A
				// presence toggle therefore requires a full restart.
				o.log("info", "orchestrator: restarting sing-box (tun inbound toggled)")
				if e := proc.Stop(); e != nil {
					o.log("warn", "orchestrator: stop before tun-restart: "+e.Error())
				}
				err = proc.Start()
			} else {
				// При живом tun proc.Reload делает Stop+Start (SIGHUP пересоздал
				// бы tun под удерживаемым fd → FATAL, см. process.go), поэтому
				// строка обязана называть то, что произойдёт на самом деле:
				// «SIGHUP» здесь сбивал с толку при разборе простоя.
				if prevHasTun {
					o.log("info", "orchestrator: restarting sing-box (config changed, tun active)")
				} else {
					o.log("info", "orchestrator: SIGHUP sing-box (config changed)")
				}
				err = proc.Reload()
			}
			if err == nil {
				newApplied = appliedState{Hash: newHash, HasTun: newHasTun}
			}
			saveApplied = err == nil
		case !needRunning && running:
			o.log("info", "orchestrator: stopping sing-box (no active slots)")
			err = proc.Stop()
			saveApplied = true // process is dead either way — record it
		default:
			// !needRunning && !running — nothing to do, but record
			// "nothing applied" (idempotent) so a stale hash from a
			// since-disabled config can't later satisfy the skip gate.
			saveApplied = true
		}
	}
	if saveApplied {
		if e := saveAppliedState(o.appliedPath, newApplied); e != nil {
			o.log("warn", "orchestrator: persist applied state: "+e.Error())
		}
	}

	o.mu.Lock()
	o.reloading = false
	// Record the tun presence of the config we just applied so the next
	// Reload compares against reality. Updated for every apply branch
	// (start / SIGHUP / restart / stop): after a stop newHasTun is false
	// anyway, after a fresh start prevHasTun == newHasTun.
	o.prevHasTun = newHasTun
	o.mu.Unlock()
	return err
}

// pruneDanglingSelectorRefsLocked rewrites enabled slot files in place,
// dropping any selector/urltest member or `default` that points at an
// outbound tag no slot declares. Caller MUST hold o.mu. Returns log lines
// describing what was pruned — the caller logs them AFTER releasing o.mu
// (o.log takes o.mu, so logging here would self-deadlock).
//
// A selector member is the ONLY ref sing-box check lets through (the error
// is deferred to "start service"), so this is the one place the orchestrator
// must self-heal rather than merely reject. The surviving-member guard keeps
// a selector from being emptied (sing-box rejects a memberless selector); an
// all-dangling selector is left untouched so validateLocked still reports it.
func (o *Orchestrator) pruneDanglingSelectorRefsLocked() []string {
	var logs []string
	known := o.enabledOutboundTagsLocked(nil)

	for _, m := range KnownSlots() {
		meta := m
		if _, ok := o.slots[meta.Slot]; !ok || !o.enabled[meta.Slot] {
			continue
		}
		if meta.Slot == SlotUser {
			// 90-user.json пишет только сам пользователь через эксперт-
			// редактор — правки пользователя не мутируем молча. Висячая
			// ссылка в нём остаётся, validateLocked её отловит, reload
			// пропустится (движок продолжит работать на старом конфиге),
			// а причина всплывёт через LastReloadValidation в статусе.
			continue
		}
		data, err := o.readActiveBytes(meta.Slot)
		if err != nil || len(data) == 0 {
			continue
		}
		var root map[string]any
		if json.Unmarshal(data, &root) != nil {
			continue
		}
		changed, slotLogs := pruneDanglingSelectorRefsInDoc(root, known, meta.Slot)
		logs = append(logs, slotLogs...)
		if !changed {
			continue
		}
		out, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			continue
		}
		if err := writeAtomic(o.activePath(meta), out); err != nil {
			logs = append(logs, fmt.Sprintf("orchestrator: rewrite pruned slot [%s]: %v", meta.Slot, err))
		}
	}
	return logs
}

// pruneDanglingSelectorRefsInDoc applies the selector-member/default prune to
// ONE parsed slot document, in place: members/defaults whose tag is not in
// known are dropped. Never empties a selector (sing-box rejects a memberless
// selector) — an all-dangling selector is left untouched for validation to
// flag. Shared by the on-disk prune above and the virtual prune in
// validateDraftLocked (issue #633), so CheckMerged predicts exactly what
// Reload will apply.
func pruneDanglingSelectorRefsInDoc(root map[string]any, known map[string]bool, slot Slot) (changed bool, logs []string) {
	obs, ok := root["outbounds"].([]any)
	if !ok {
		return false, nil
	}
	for _, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		members, ok := ob["outbounds"].([]any)
		if !ok || len(members) == 0 {
			continue // not a selector/urltest
		}
		kept := make([]any, 0, len(members))
		var dropped []string
		for _, mv := range members {
			tag, _ := mv.(string)
			if tag == "" || known[tag] {
				kept = append(kept, mv)
				continue
			}
			dropped = append(dropped, tag)
		}
		// Never empty a selector — leave it for validateLocked to flag.
		if len(dropped) > 0 && len(kept) > 0 {
			ob["outbounds"] = kept
			changed = true
			tag, _ := ob["tag"].(string)
			logs = append(logs, fmt.Sprintf("orchestrator: pruned dangling selector members %v from %q in [%s]", dropped, tag, slot))
		}
		if def, _ := ob["default"].(string); def != "" && !known[def] {
			delete(ob, "default")
			changed = true
			tag, _ := ob["tag"].(string)
			logs = append(logs, fmt.Sprintf("orchestrator: cleared dangling selector default %q from %q in [%s]", def, tag, slot))
		}
	}
	return changed, logs
}

// enabledOutboundTagsLocked builds the set of outbound tags the merged
// enabled config declares, plus the builtins sing-box defines implicitly
// (mirrors validateWith). Slots listed in exclude are skipped. Caller
// MUST hold o.mu. This is the availability oracle both prune (above)
// and EnabledOutboundTags share — a selector member/default is valid
// exactly when its tag is in this set.
func (o *Orchestrator) enabledOutboundTagsLocked(exclude map[Slot]bool) map[string]bool {
	known := map[string]bool{"direct": true, "block": true, "dns": true}
	for _, m := range KnownSlots() {
		if exclude[m.Slot] {
			continue
		}
		if _, ok := o.slots[m.Slot]; !ok || !o.enabled[m.Slot] {
			continue
		}
		data, err := o.readActiveBytes(m.Slot)
		if err != nil || len(data) == 0 {
			continue
		}
		var c slotConfig
		if json.Unmarshal(data, &c) != nil {
			continue
		}
		for _, ob := range c.Outbounds {
			if ob.Tag != "" {
				known[ob.Tag] = true
			}
		}
		// Endpoints share the outbound tag namespace (validate.go collects them
		// the same way); include them so selector/device-proxy refs to an awg3
		// endpoint tag aren't pruned as dangling.
		for _, ep := range c.Endpoints {
			if ep.Tag != "" {
				known[ep.Tag] = true
			}
		}
	}
	return known
}

// EnabledOutboundTags returns the outbound tags declared by ENABLED
// slots (plus sing-box builtins direct/block/dns) — the same visibility
// rule pruneDanglingSelectorRefsLocked applies before every reload.
// Producers that reference tags across slots (device-proxy selectors →
// router composites, issue #465) use it to detect that a referenced tag
// would be dangling in the merged config and degrade gracefully at
// generation time instead of letting prune strip the reference. Slots
// passed in exclude are ignored (a producer excludes its own slot: its
// selectors are consumers, not member candidates).
func (o *Orchestrator) EnabledOutboundTags(exclude ...Slot) map[string]bool {
	ex := make(map[Slot]bool, len(exclude))
	for _, s := range exclude {
		ex[s] = true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.enabledOutboundTagsLocked(ex)
}

// HasActiveWork reports (under the lock) whether sing-box currently has
// anything to do beyond hosting base/catalog slots — the same predicate
// Reload uses to decide "ensure running". Exposed for the Operator's
// watchdog Reconcile (issue #456): a crashed process must be restarted
// whenever any active slot (router / deviceproxy / subscriptions / user
// tunnels) needs the daemon, not only when legacy tunnels exist.
func (o *Orchestrator) HasActiveWork() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hasActiveWorkLocked()
}

// hasActiveWorkLocked reports whether sing-box has anything to do
// beyond hosting base + catalog slots. Two activation paths:
//
//   - any non-AlwaysOn slot is enabled (router / deviceproxy /
//     subscriptions) — its mere presence is the signal;
//   - an AlwaysOn slot whose meta.HasContent returns true (currently
//     SlotTunnels, when the user has defined at least one sing-box
//     tunnel).
//
// AlwaysOn catalog slots without HasContent (SlotBase, SlotAwg) never
// activate the daemon on their own — they are infrastructure for
// other slots, not a reason to keep sing-box running.
//
// Исключение — SlotUser: включённый, но содержательно пустой
// пользовательский слот (90-user.json с «{}») работой не считается,
// см. userSlotHasMeaningfulContentLocked.
//
// Caller MUST hold o.mu.
func (o *Orchestrator) hasActiveWorkLocked() bool {
	for slot, enabled := range o.enabled {
		if !enabled {
			continue
		}
		meta, ok := o.slots[slot]
		if !ok {
			continue
		}
		if meta.AlwaysOn {
			if meta.HasContent != nil && meta.HasContent() {
				return true
			}
			continue
		}
		if slot == SlotUser {
			// Пустой пользовательский слот не должен держать/запускать
			// процесс: включённый 90-user.json с «{}» (или без содержательных
			// массивов) — не работа для демона, в отличие от системных слотов,
			// у которых сам факт включения — сигнал продюсера.
			if o.userSlotHasMeaningfulContentLocked() {
				return true
			}
			continue
		}
		return true
	}
	return false
}

// userSlotHasMeaningfulContentLocked reports whether the user slot's
// ACTIVE file carries anything sing-box would actually do work for: at
// least one non-empty array among inbounds / outbounds / dns.servers /
// dns.rules / route.rules / route.rule_set. Missing, empty or unparseable
// file → false (битый файл всё равно заблокирует reload на parse-error в
// validateLocked). Читаем и парсим файл на каждый вызов — reload не
// горячий путь, кэш не нужен. Caller MUST hold o.mu.
func (o *Orchestrator) userSlotHasMeaningfulContentLocked() bool {
	data, err := o.readActiveBytes(SlotUser)
	if err != nil || len(data) == 0 {
		return false
	}
	var c struct {
		Inbounds  []json.RawMessage `json:"inbounds"`
		Outbounds []json.RawMessage `json:"outbounds"`
		DNS       struct {
			Servers []json.RawMessage `json:"servers"`
			Rules   []json.RawMessage `json:"rules"`
		} `json:"dns"`
		Route struct {
			Rules   []json.RawMessage `json:"rules"`
			RuleSet []json.RawMessage `json:"rule_set"`
		} `json:"route"`
	}
	if json.Unmarshal(data, &c) != nil {
		return false
	}
	return len(c.Inbounds) > 0 || len(c.Outbounds) > 0 ||
		len(c.DNS.Servers) > 0 || len(c.DNS.Rules) > 0 ||
		len(c.Route.Rules) > 0 || len(c.Route.RuleSet) > 0
}
