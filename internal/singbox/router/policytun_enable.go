package router

import (
	"context"
	"fmt"
	"slices"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ownsOpkgTun сообщает, несёт ли живой NDMS-интерфейс наше описание policy-tun.
// Скана нет или он упал — false: «не знаем» ≠ «наш», а Create по чужому живому
// интерфейсу переписал бы его настройки.
func (s *ServiceImpl) ownsOpkgTun(ctx context.Context, ndmsName string) bool {
	if s.deps.OpkgTunScan == nil {
		return false
	}
	ids, err := s.deps.OpkgTunScan(ctx, policyTunDescription)
	if err != nil {
		return false
	}
	return slices.Contains(ids, ndmsName)
}

// enablePolicyTun provisions the policy-tun path: persist index → create a
// PUBLIC + `ip global` OpkgTun (so NDMS lists it as a policy exit) → addr/mtu/up
// → write+start the router slot with a tun inbound → wait carrier → park the
// NDMS default route on the tun → optional DSCP-only QoS netfilter. Called with
// s.mu held by Enable.
//
// Structurally a sibling of enableFakeIPTun: same persist-before-create
// invariant (the reap only sees orphans by persisted index), same LIFO rollback
// of ALL partial work, same rbCtx that survives a cancelled ctx. What differs is
// the steering model — fakeip pushes specific pool/CIDR routes onto a PRIVATE
// tun, policy-tun hands the whole default to a PUBLIC tun and lets an NDMS
// access policy decide which clients take it.
func (s *ServiceImpl) enablePolicyTun(ctx context.Context, settings *storage.Settings, sr storage.SingboxRouterSettings) (err error) {
	// Fail-fast nil-guard: a degraded / mis-wired build would otherwise
	// nil-panic mid-provision. Refuse loudly before touching any state.
	if s.deps.OpkgTun == nil || s.deps.OpkgTunIndices == nil || s.deps.DefaultRoute == nil {
		return fmt.Errorf("policy-tun: provisioning deps not wired")
	}

	// Single source of truth for the tun addresses + MTU, shared with fakeip.
	// ГОЧА: resolveFakeIPParams обнуляет TunAddr6 при пустом sr.FakeIPPool6, то
	// есть v6 в policy-tun скрыто зависит от настройки fakeip-пула. Осознанно:
	// отдельную настройку адресов policy-tun не заводим.
	p := resolveFakeIPParams(s.deps.FakeIPTun, sr)

	// Validate the tun addresses BEFORE any state is touched — a malformed
	// TunAddr4 must fail the enable, not surface later as a half-provisioned
	// iface with no address.
	addr4, mask4, err := splitCIDRToAddrMask(p.TunAddr4)
	if err != nil {
		return fmt.Errorf("enable policy-tun: tun addr: %w", err)
	}
	addr6 := ""
	if p.TunAddr6 != "" {
		// SetIPv6Address wants a bare address (it appends /128 internally).
		if addr6, err = bareAddrFromCIDR(p.TunAddr6); err != nil {
			return fmt.Errorf("enable policy-tun: tun addr6: %w", err)
		}
	}

	live, err := s.deps.OpkgTunIndices.LiveOpkgTunIndices(ctx)
	if err != nil {
		return fmt.Errorf("enable policy-tun: list opkgtun indices: %w", err)
	}

	// Idempotency guard (CRITICAL, same reason as fakeip): policy-tun installs
	// no main iptables, so Reconcile's installed-check is always false and
	// routes every tick here. Re-provisioning would allocate a new index,
	// clobber persist and orphan the live iface. Sits BEFORE any mutation, so
	// the no-op return runs before a single rollback entry is pushed.
	prev := settings.PolicyTun
	if prev != nil && prev.Provisioned && live[prev.Index] {
		return nil
	}

	// Prefer the persisted index while it is free: the user pins permits in the
	// NDMS policy to a concrete OpkgTun name, and silently renaming the exit on
	// every enable would break them.
	//
	// Занятый персистом индекс тоже наш, если на нём висит НАШ интерфейс:
	// выключение его больше не удаляет, а удерживает (holdOpkgTun). Без этой
	// ветки удержание оборачивалось бы дрейфом хуже прежнего — номер занят,
	// аллокатор берёт следующий, permit в политике остаётся на прежнем имени.
	// Владение доказывается описанием; скан не подключён — «не знаем» ≠ «наш».
	idx := 0
	switch {
	case prev != nil && !live[prev.Index]:
		idx = prev.Index
	case prev != nil && s.ownsOpkgTun(ctx, fakeIPNDMSName(prev.Index)):
		idx = prev.Index
	default:
		if idx, err = allocateFakeIPIndex(live); err != nil {
			return fmt.Errorf("enable policy-tun: allocate index: %w", err)
		}
	}
	// Two names per index: NDMS RCI takes the CamelCase ndmsName, the kernel
	// (sing-box config, ip flush, /sys carrier) sees the lowercase iface.
	ndmsName := fakeIPNDMSName(idx)
	iface := fakeIPIfaceName(idx)
	if prev != nil && prev.Index != idx {
		s.appLog.Warn("policy-tun", iface, "индекс OpkgTun изменился — проверьте permit в политиках")
	}

	// rollback is a LIFO stack of inverse operations; each resource-creating
	// step pushes its undo AFTER it succeeds.
	var rollback []func()
	defer func() {
		if err == nil {
			return
		}
		for i := len(rollback) - 1; i >= 0; i-- {
			rollback[i]()
		}
	}()
	push := func(undo func()) { rollback = append(rollback, undo) }

	// INVARIANT: persist FIRST, before creating the iface, so a crash in between
	// leaves a persist the reap can find by index.
	//
	// ГОЧА: держим ОДИН объект состояния и подшиваем его в загруженные settings.
	// SetPolicyTunState пишет в кэш стора, а промежуточные Load'ы (конфиг слота,
	// статус) кэш подменяют — финальный Save(settings) отсюда откатил бы всё,
	// что легло в персист после них (например NATSegments source-preserve).
	ptState := &storage.PolicyTunState{Provisioned: true, Index: idx}
	if prev != nil {
		// ГОЧА (re-provision): записи NAT-сегментов ОБЯЗАНЫ пережить повторный
		// провижининг (heal «интерфейс пропал» → reconcile → enableLocked). Они
		// — единственный след того, каким сегмент был ДО нас; потеряв их,
		// applyPolicyTunSourcePreserve пересканирует сегмент, стоящий на НАШЕМ
		// static-NAT, запишет «исходное = static» и после выключения режима
		// сегмент навсегда остался бы на нём.
		ptState.NATSegments = prev.NATSegments
	}
	if err = s.deps.Settings.SetPolicyTunState(ptState); err != nil {
		return fmt.Errorf("enable policy-tun: persist policy-tun state: %w", err)
	}
	// Мутируем локальную копию: `settings` всё ещё алиас живого кэша стора,
	// который параллельно читают другие горутины без лока. Копия именно
	// здесь, а не сразу после Load: так в неё попадает всё, что записали в
	// кэш узкие мутаторы выше, — ровно то, что сохранил бы прежний Save.
	cp := *settings
	settings = &cp
	settings.PolicyTun = ptState
	push(func() {
		// Откат ВОЗВРАЩАЕТ ПРЕЖНИЙ персист, а не обнуляет его.
		//
		// Первый enable (prev == nil) — прежнее поведение: до Create откат
		// вернёт nil, после Create ифейс уже снесён teardown'ом ниже по стеку,
		// так что nil остаётся честным (инвариант «persist-before-create» для
		// реапа не страдает).
		//
		// Re-provision (prev != nil) — обнуление было утечкой: в prev лежат
		// записи NAT-сегментов, в том числе УЖЕ ОТОЗВАННЫХ из желаемого списка
		// (опцию выключили, restore ещё не отработал). Их не восстанавливает
		// push источника source-preserve (он знает только про desired), и стерев
		// персист, мы теряем единственный след — static-NAT такого сегмента
		// осиротел бы навсегда. Заодно уцелеет пин индекса: без него следующий
		// enable молча взял бы другой OpkgTun и порвал permit'ы в политиках.
		//
		// Этот push стоит ПЕРВЫМ, значит по LIFO выполняется ПОСЛЕДНИМ — prev
		// ложится поверх всех остальных откатов.
		//
		// NB: prev.Provisioned=true при уже снесённом откатом ифейсе допустимо —
		// в policy-tun следующий reconcile переподнимет его по ТОМУ ЖЕ индексу
		// (желаемое поведение), а вне режима реап найдёт запись по индексу и
		// приберёт её вместе с NAT-сегментами.
		if e := s.deps.Settings.SetPolicyTunState(prev); e != nil {
			s.appLog.Warn("policy-tun-rollback", iface, "restore policy-tun persist: "+e.Error())
		}
	})

	// PUBLIC security-level (unlike fakeip's private): NDMS only offers public
	// interfaces as access-policy exits, and the policy IS the steering here.
	if err = s.deps.OpkgTun.CreateOpkgTunWithSecurityLevel(ctx, ndmsName, policyTunDescription, "public"); err != nil {
		return fmt.Errorf("enable policy-tun: create opkgtun: %w", err)
	}
	// rbCtx: откат обязан доехать и когда Enable упал ИЗ-ЗА отмены ctx (клиент
	// отвалился во время waitForSingbox) — иначе NDMS-вызовы отката no-op'ятся
	// с context.Canceled и OpkgTun остаётся с настроенным адресом (nginx-loop,
	// см. teardownOpkgTun).
	//
	// ОСОЗНАННАЯ ПОТЕРЯ: откат УДАЛЯЕТ интерфейс, даже если включение его не
	// создавало, а переиспользовало удержанный. Номер не теряется (персист цел,
	// следующее включение возьмёт его же), теряется идентичность интерфейса —
	// переживает ли пересоздание permit в политике, не проверено. Откат также
	// не снимает уже поставленный нами permit: DenyInterface мог бы снять
	// разрешение, которое пользователь дал интерфейсу сознательно.
	rbCtx := context.WithoutCancel(ctx)
	push(func() {
		_ = s.teardownOpkgTun(rbCtx, ndmsName, "policy-tun-rollback")
	})

	// `ip global` — без него интерфейс не появляется в списке выходов политики
	// (обратный 76f38fd7: fakeip его снял, потому что там steering идёт
	// маршрутами, а не политикой).
	if err = s.deps.OpkgTun.SetIPGlobal(ctx, ndmsName); err != nil {
		return fmt.Errorf("enable policy-tun: ip global: %w", err)
	}

	// NDMS-native разрешение трафика в tun: permit-all access-list + binding.
	// Без него firewall NDMS (isolate-private и т.п.) режет форвард в tun.
	if err = s.deps.OpkgTun.SetPermitAllACL(ctx, ndmsName); err != nil {
		return fmt.Errorf("enable policy-tun: permit acl: %w", err)
	}

	if err = s.deps.OpkgTun.SetAddress(ctx, ndmsName, addr4, mask4); err != nil {
		return fmt.Errorf("enable policy-tun: set address: %w", err)
	}
	if addr6 != "" {
		if err = s.deps.OpkgTun.SetIPv6Address(ctx, ndmsName, addr6); err != nil {
			return fmt.Errorf("enable policy-tun: set ipv6 address: %w", err)
		}
		// v6-разрешение — ПОСЛЕ адреса: у NDMS под v6 отдельное пространство
		// списков, и v4-ACL выше его не покрывает. Без него дефолт клиентов
		// припаркован на tun, а v6 в него режет firewall — уйти в обход некуда.
		if err = s.deps.OpkgTun.SetPermitAllACLv6(ctx, ndmsName); err != nil {
			return fmt.Errorf("enable policy-tun: permit acl v6: %w", err)
		}
	}
	if err = s.deps.OpkgTun.SetMTU(ctx, ndmsName, p.MTU); err != nil {
		return fmt.Errorf("enable policy-tun: set mtu: %w", err)
	}
	if err = s.deps.OpkgTun.InterfaceUp(ctx, ndmsName); err != nil {
		return fmt.Errorf("enable policy-tun: iface up: %w", err)
	}

	// Flush stale kernel addresses PRE-start, while the tun is still bare, so
	// sing-box's attach re-adds its own configured address cleanly (the fakeip
	// 1F.1 ordering — a post-start flush kills the just-attached address).
	if err = fakeIPAddrFlush(ctx, iface); err != nil {
		return fmt.Errorf("enable policy-tun: addr flush: %w", err)
	}

	// Slot 20 keeps its user rules/outbounds; only the ingress changes — the
	// tproxy/redirect pair is replaced by a single tun inbound.
	cfg, err := s.loadAppliedRouterConfig()
	if err != nil {
		return fmt.Errorf("enable policy-tun: load router config: %w", err)
	}
	cfg.Inbounds = ensurePolicyTunInbound(cfg.Inbounds, PolicyTunInboundSpec{
		Iface:      iface,
		TunAddr4:   p.TunAddr4,
		TunAddr6:   p.TunAddr6,
		MTU:        p.MTU,
		Stack:      sr.FakeIPStack,
		UDPTimeout: sr.UDPTimeout,
	})
	cfg.Outbounds = stripAutoManagedDirect(cfg.Outbounds)
	cfg.EnsureSystemRules(sr.SnifferEnabled)
	cfg.EnsureUDPTimeoutRule(resolveUDPTimeout(sr.UDPTimeout))
	qosClasses := activeQoSClasses(sr.QoSClasses)
	cfg.Inbounds, _ = ensureQoSInbounds(cfg.Inbounds, qosClasses, sr.UDPTimeout)
	cfg.EnsureRouteWAN(sr.WANAutoDetect, sr.WANInterface)

	// Promote SlotRouter FIRST so persistConfigDirect targets the active path.
	// The prior enabled-state is captured for rollback (SlotFakeIP is not
	// touched: leaving fakeip is the transition's teardown job, not ours).
	prevRouterEnabled := false
	if s.deps.Orch != nil {
		for _, st := range s.deps.Orch.Snapshot() {
			if st.Slot == orchestrator.SlotRouter {
				prevRouterEnabled = st.Enabled
				break
			}
		}
		if err = s.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
			return fmt.Errorf("enable policy-tun: orchestrator enable router slot: %w", err)
		}
		push(func() {
			if e := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, prevRouterEnabled); e != nil {
				s.appLog.Warn("policy-tun-rollback", iface, "restore router slot: "+e.Error())
			}
			// Разметка слотов вернулась — device-proxy должен перегенерировать
			// слот 30 под неё до следующего reload.
			s.notifyRoutingSlotsChanged()
		})
	} else {
		if running, _ := s.deps.Singbox.IsRunning(); !running {
			if err = s.deps.Singbox.Start(); err != nil {
				return fmt.Errorf("enable policy-tun: sing-box start: %w", err)
			}
		}
	}

	if err = s.persistConfigDirect(ctx, cfg); err != nil {
		return fmt.Errorf("enable policy-tun: persist router config: %w", err)
	}
	if _, err = s.syncQoSRoutesSlot(ctx, qosClasses, sr); err != nil {
		return fmt.Errorf("enable policy-tun: sync qos routes slot: %w", err)
	}
	// Слот 20 снова активен — зависимые продюсеры (device-proxy) перегенерируют
	// свои слоты ДО reload.
	s.notifyRoutingSlotsChanged()
	if err = s.orchestratorApplyNow(); err != nil {
		return fmt.Errorf("enable policy-tun: orchestrator reload: %w", err)
	}

	// HARD gate: an unready sing-box means the tun never attaches, and parking
	// the NDMS default route on a dead tun blackholes every policy client.
	bootWait := bootWaitWithFloor()
	if err = s.waitForSingbox(ctx, bootWait); err != nil {
		return fmt.Errorf("enable policy-tun: %w: waited %s (%v)", ErrSingboxNotReady, bootWait, err)
	}

	// Source-preserve (перевод сегментов на static-NAT) — ДО парковки дефолта:
	// WAN-цель берётся из NDMS-дефолта, а после SetDefaultRoute им становится
	// наш же tun, и резолвер отдал бы OpkgTun (см. resolvePolicyTunWAN).
	// Записанные исходные состояния кладём в персист рядом с Provisioned/Index —
	// их восстанавливает teardown/реап.
	if sr.PolicyTunSourcePreserve && len(sr.PolicyTunNATSegments) > 0 {
		var recorded []storage.PolicyTunNATSegment
		recorded, err = s.applyPolicyTunSourcePreserve(ctx, sr.PolicyTunNATSegments, ptState.NATSegments)
		// Откат регистрируем ДО проверки ошибки: apply отдаёт записи и о
		// сегментах, до которых успел дойти, а полуприменённые обязаны вернуться.
		push(func() {
			if e := s.restorePolicyTunNAT(rbCtx, recorded); e != nil {
				s.appLog.Warn("policy-tun-rollback", iface, "restore segment NAT: "+e.Error())
			}
		})
		// Merge, а не присваивание: перенесённые из prev записи сегментов, уже
		// выбывших из желаемого списка, обязаны дожить до восстановления в
		// restoreRevokedPolicyTunNAT — иначе их static-NAT остался бы навсегда.
		// Мутируем копию: ptState уже уходил в кэш стора (SetPolicyTunState
		// выше кладёт туда сам указатель), и запись по месту гонялась бы с
		// маршалом кэша. Копию публикуют мутатор ниже и финальный Save —
		// поэтому её же подшиваем в settings.
		next := *ptState
		next.NATSegments = mergePolicyTunNATRecords(ptState.NATSegments, recorded)
		ptState = &next
		settings.PolicyTun = ptState
		// Персист ДО проверки ошибки, как в сверке: откат выше может и сам упасть
		// (RCI, уронивший apply, обычно роняет и его), и тогда запись — всё, что
		// помнит исходный режим сегмента. Provisioned/Index уже в персисте (см.
		// выше), так что записями мы не утверждаем ничего нового о провижининге.
		if perr := s.deps.Settings.SetPolicyTunState(ptState); perr != nil {
			return fmt.Errorf("enable policy-tun: persist nat segments: %w", perr)
		}
		if err != nil {
			return fmt.Errorf("enable policy-tun: %w", err)
		}
	}

	// The NDMS default route goes onto the tun only AFTER carrier — the same
	// lesson as fakeip's pool routes: an NDMS route-table rebuild racing the
	// gvisor attach kept the tun from settling.
	if err = s.deps.DefaultRoute.SetDefaultRoute(ctx, ndmsName); err != nil {
		return fmt.Errorf("enable policy-tun: set default route: %w", err)
	}
	push(func() {
		if e := s.deps.DefaultRoute.RemoveDefaultRoute(rbCtx, ndmsName); e != nil {
			s.appLog.Warn("policy-tun-rollback", iface, "remove default route: "+e.Error())
		}
	})
	if p.TunAddr6 != "" {
		if err = s.deps.DefaultRoute.SetIPv6DefaultRoute(ctx, ndmsName); err != nil {
			return fmt.Errorf("enable policy-tun: set ipv6 default route: %w", err)
		}
		push(func() {
			if e := s.deps.DefaultRoute.RemoveIPv6DefaultRoute(rbCtx, ndmsName); e != nil {
				s.appLog.Warn("policy-tun-rollback", iface, "remove ipv6 default route: "+e.Error())
			}
		})
	}

	// Разрешаем tun выходом целевой политики. ПОСЛЕ подъёма интерфейса и
	// парковки дефолта: permit имени, под которым интерфейса ещё нет, NDMS
	// может отвергнуть, а разрешение до готовности sing-box увело бы трафик
	// членов политики в туннель без читателя.
	//
	// Откат уже поставленный permit НЕ снимает (осознанно: DenyInterface мог бы
	// снять разрешение, поставленное пользователем).
	permitted := false
	if s.deps.RunningConfig != nil {
		if lines, e := s.deps.RunningConfig.Lines(ctx); e == nil {
			permitted = policyTunPermitted(lines, ndmsName, sr.PolicyName)
		}
	}
	s.ensurePolicyTunPermit(ctx, sr, iface, ndmsName, permitted)

	// Ingress-заворот интерфейсов с галкой «Маршрутизация через sing-box» плюс
	// перехват DNS у членов политики: тот же механизм, что у fakeip (issue
	// #678). Best-effort (см. ensureFakeIPIngress): без заворота режим
	// работает, просто трафик таких серверов идёт мимо политики. Неприменимый
	// спек (марки не прочитались) пропускаем — их починит drift-heal.
	if spec, ok := s.policyTunIngressSpec(ctx, iface, ndmsName, sr); ok {
		s.ensureFakeIPIngress(ctx, spec)
	}
	push(func() {
		// Откат обязан снять и заворот: иначе `ip rule iif` пережил бы удаление
		// tun и увёл трафик ingress-серверов в несуществующий интерфейс.
		s.ensureFakeIPIngress(rbCtx, FakeIPIngressSpec{})
	})

	// QoS-гибрид: netfilter в этом режиме нужен ТОЛЬКО под DSCP-классы (без
	// классов не ставим ничего). Деградация та же, что у tproxy: отсутствие
	// xt_dscp выключает фичу, но не роняет Enable.
	qosSpecs := qosIPTablesSpecs(qosClasses)
	if len(qosSpecs) > 0 {
		// Классы QoS рендерят `-j TPROXY` / `-j REDIRECT`, поэтому xt_TPROXY
		// нужен и здесь: на чистом буте без netfilter.d-хука iptables-restore
		// упал бы на COMMIT и утащил в откат весь enable. prepareNetfilter —
		// тот же preflight, что у tproxy (модуль + доступность таргета), но тут
		// он НЕ фатальный: политика как у xt_dscp — нет netfilter, значит нет
		// классов QoS, а сам режим policy-tun работает (трафик заворачивает
		// NDMS-политика, а не netfilter).
		if e := s.prepareNetfilter(ctx); e != nil {
			s.appLog.Warn("qos-dscp", "", "netfilter TPROXY недоступен — классы QoS пропущены: "+e.Error())
			qosSpecs = nil
		}
	}
	if len(qosSpecs) > 0 {
		if e := EnsureXtDscpModule(ctx); e != nil {
			s.appLog.Warn("ensure-xt-dscp", "", e.Error())
		}
		if !s.xtDscpUsable(ctx) {
			s.appLog.Warn("qos-dscp", "", "xt_dscp недоступен — классы QoS пропущены (см. статус xtDscpAvailable)")
			qosSpecs = nil
		}
	}
	if len(qosSpecs) > 0 {
		// WAN-IP исключения обязательны и здесь: без них DSCP-меченный трафик
		// на собственный WAN-адрес роутера ушёл бы в sing-box петлёй.
		wanIPs, cerr := s.deps.WANIPCollector.Collect(ctx)
		if cerr != nil {
			err = fmt.Errorf("enable policy-tun: collect WAN IPs: %w", cerr)
			return err
		}
		bypassUDP, bypassTCP, _ := resolveBypassPorts(sr.BypassPresets, sr.BypassExtraPorts)
		bypassSubnets, _ := resolveBypassCIDRs(sr.BypassPresets, sr.BypassExtraSubnets, s.keenDNSBypass())
		if err = s.deps.IPTables.Install(ctx, RestoreInputSpec{
			DSCPOnly:       true,
			MatchAll:       true,
			WANIPs:         wanIPs,
			BypassUDPPorts: bypassUDP,
			BypassTCPPorts: bypassTCP,
			BypassCIDRs:    bypassSubnets,
			QoSClasses:     qosSpecs,
		}); err != nil {
			return fmt.Errorf("enable policy-tun: iptables install: %w", err)
		}
		// Применённое состояние netfilter — база сравнения для reconcile: без
		// него первое же runtime-изменение классов выглядело бы «без изменений»
		// (или, наоборот, переустанавливалось бы каждый тик). Пишем ТОЛЬКО после
		// успешной установки; когда классов нет, netfilterStateKnown остаётся
		// false, и первый тик reconcile сделает одноразовый Uninstall-свип
		// (снимет чужие цепочки и blackhole прежнего режима). s.mu уже держит
		// enableLocked.
		s.currentQoSClasses = qosSpecs
		s.netfilterStateKnown = true
	}

	// Persist enabled LAST (success). From here we do NOT roll back.
	settings.SingboxRouter = sr
	if err = s.deps.Settings.Save(settings); err != nil {
		return fmt.Errorf("enable policy-tun: save settings: %w", err)
	}

	s.emitStatus(ctx)
	return nil
}
