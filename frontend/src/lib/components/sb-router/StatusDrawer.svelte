<!--
  Единое меню движка sing-box. Открывается кликом по движку/статус-pill в hero (drawerStore).
  beginner: состояние + здоровье + управление. expert: + редактируемые настройки (auto-save).
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount } from 'svelte';
  import { SideDrawer, Toggle, Button, Badge, StatusDot } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
  import { singboxStatus } from '$lib/stores/singbox';
  import { singboxMemory } from '$lib/stores/singboxMemory';
  import { singboxTrafficLive } from '$lib/stores/singboxEngineStats';
  import { formatBytes, formatByteRate } from '$lib/utils/format';
  import { systemInfo } from '$lib/stores/system';
  import { opkgTunUnsupportedReason, opkgTunSupported } from '$lib/utils/opkgTunSupport';
  import { notifications } from '$lib/stores/notifications';
  import { drawerOpen, closeDrawer } from './drawerStore';
  import { openSourceDrawer } from './sourceDrawerStore';
  import { mode } from './modeStore';
  import DepRow from './DepRow.svelte';
  import IssueRow from './IssueRow.svelte';
  import PortChipsInput from './PortChipsInput.svelte';
  import SubnetChipsInput from './SubnetChipsInput.svelte';
  import TrafficSourceSettings from './TrafficSourceSettings.svelte';
  import QosSettingsCard from './QosSettingsCard.svelte';
  import PolicyTunCard from './PolicyTunCard.svelte';
  import BypassGeoIPTags from './BypassGeoIPTags.svelte';
  import OutboundOption from './OutboundOption.svelte';
  import { deriveDeps, deriveIssues } from './drawerData';
  import { formatSuppressedUntil } from './crashInfo';
  import { mergeAndSaveSettings, BYPASS_PRESETS } from './settingsActions';
  import { resolveWanAuto, planToggleAutoDetect, planSelectWanInterface, type WanAutoOverride } from './wanMode';
  import type { SingboxRouterSettings, SingboxRouterWANInterface } from '$lib/types';

  const status = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;
  const storeOptions = singboxRouterStore.options;

  let open = $derived($drawerOpen);
  let s = $derived($status);
  // Эффективный путь cache.db: при незаданной настройке это может быть
  // рукописный путь из 00-base.json, который селектор выразить не может.
  let cfg = $derived($storeSettings);
  let isExpert = $derived($mode === 'expert');

  let singboxInstallStatus = $derived($singboxStatus.data);
  let sysInfo = $derived($systemInfo.data);

  let deps = $derived(deriveDeps(s));
  let engineEnabled = $derived(s?.enabled ?? false);
  // Реальная работа перехвата (цепочки + PREROUTING-jump'ы), не просто
  // persisted-тумблер. Заголовок различает «включён, но не работает».
  let engineActive = $derived(engineEnabled && (s?.active ?? false));

  // Тумблер/кнопка управляют режимом через общий modeSwitch (детерминированно
  // <выбранный режим>↔off), а не enable/disable «текущего» режима. checked —
  // mode-aware: «вкл» только когда активен один из режимов этого дровера
  // (а не голый enabled) — FakeIP живёт на своей вкладке.
  const settings = singboxRouterStore.settings;
  const switchBusy = $derived(modeSwitchBusy($modeSwitch));

  // Режимы захвата, которыми управляет этот дровер.
  type CaptureMode = 'tproxy' | 'policy-tun';
  let activeMode = $derived.by<CaptureMode | null>(() => {
    if (!(s?.enabled ?? false)) return null;
    const rm = $settings?.routingMode;
    return rm === 'tproxy' || rm === 'policy-tun' ? rm : null;
  });
  let captureOn = $derived(activeMode !== null);
  // Выбор пользователя в этой сессии — что включит тумблер, пока движок
  // выключен. Пусто → persisted routingMode (легаси/пустой = tproxy).
  let pickedMode = $state<CaptureMode | null>(null);
  let targetMode = $derived<CaptureMode>(
    activeMode ?? pickedMode ?? ($settings?.routingMode === 'policy-tun' ? 'policy-tun' : 'tproxy'),
  );
  let policyTunMode = $derived(targetMode === 'policy-tun');
  // KeeneticOS 4.x не знает интерфейсов OpkgTun — policy-tun там не поднять.
  let tunSupported = $derived(opkgTunSupported($systemInfo.data));

  // policy-tun-unbound показывает карточка режима (там же ссылка на политики) —
  // в общем списке замечаний он был бы вторым экземпляром той же строки.
  let issues = $derived(
    deriveIssues(
      policyTunMode && s
        ? { ...s, issues: (s.issues ?? []).filter((i) => i.kind !== 'policy-tun-unbound') }
        : s,
    ),
  );
  let issueCount = $derived(issues.length);

  let wanInterfaces = $state<SingboxRouterWANInterface[]>([]);
  let saving = $state(false);
  let restarting = $state(false);
  let lastError = $state<string | null>(null);
  let wanAutoOverride = $state<WanAutoOverride>(null);
  let wanAuto = $derived(resolveWanAuto(wanAutoOverride, cfg?.wanAutoDetect));
  function versionLabel(value?: string | null): string {
    const v = (value ?? '').trim();
    return v ? `v${v}` : '—';
  }
  let sbVersionLabel = $derived(versionLabel(
    singboxInstallStatus?.version ?? singboxInstallStatus?.currentVersion ?? sysInfo?.singbox?.version,
  ));

  let bigTitle = $derived.by(() => {
    if (!engineEnabled) return m.sb_router_status_engine_off();
    return engineActive ? m.sb_router_status_engine_running() : m.sb_router_status_engine_not_running();
  });
  let bigSubtitle = $derived.by(() => {
    if (!engineEnabled) return m.sb_router_status_sub_inactive();
    if (!engineActive) return m.sb_router_status_sub_no_intercept();
    const n = s?.ruleCount ?? 0;
    return m.sb_router_status_sub_traffic_via({ count: n });
  });

  let engineState = $derived.by<'off' | 'warn' | 'on'>(() => {
    if (!engineEnabled) return 'off';
    if (!engineActive) return 'warn';
    return 'on';
  });

  let engineDotVariant = $derived(
    engineState === 'on' ? 'success' as const :
    engineState === 'warn' ? 'warning' as const :
    'muted' as const,
  );

  // ── Падения движка (#456): счётчик за окно backoff'а, причина последнего
  // падения и пауза авто-перезапуска. Блок виден, пока падения не выйдут из
  // 10-минутного окна; escape hatch — кнопка «Перезапустить» в футере.
  let crashCount = $derived(s?.crashCount ?? 0);
  let crashSuppressedLabel = $derived(formatSuppressedUntil(s?.restartSuppressedUntil));
  let showCrashInfo = $derived(crashCount > 0 || crashSuppressedLabel !== null);

  // ── Ресурсы: живая память (SSE singbox:memory, Go-рантайм по Clash API) и
  // агрегатный трафик (кумулятивные totals Clash, singbox:traffic-totals).
  // Секция видна только при работающем режиме этого дровера (tproxy/policy-tun):
  // в режиме FakeIP или после остановки движка SSE замолкает и сторы держат
  // протухшие числа (окно до ближайшего тика watchdog'а ~30 с — принятая задержка).
  let resourcesVisible = $derived(engineActive && activeMode !== null);
  let liveStats = $derived($singboxTrafficLive);
  let memoryLabel = $derived($singboxMemory > 0 ? formatBytes($singboxMemory) : '—');
  let rateLabel = $derived(
    liveStats.rate.hasRate
      ? `↓ ${formatByteRate(liveStats.rate.downloadRate)} · ↑ ${formatByteRate(liveStats.rate.uploadRate)}`
      : '—',
  );
  let sessionLabel = $derived(
    `↓ ${formatBytes(liveStats.totals.downloadBytes)} · ↑ ${formatBytes(liveStats.totals.uploadBytes)}`,
  );

  onMount(async () => {
    void singboxRouterStore.loadAll();
    try {
      wanInterfaces = await api.singboxRouterListWANInterfaces();
    } catch (_e) {
      // ignore
    }
  });

  // Новичку TPROXY-настройки живут в SourceDrawer (узел «Источник» во FlowGraph);
  // здесь — сводка и переход, чтобы под выбором режима не было пусто (#730).
  let sourceSummary = $derived.by(() => {
    if (cfg?.deviceMode === 'all') return m.sb_router_status_source_all();
    const name = (cfg?.policyName ?? '').trim();
    return name
      ? m.sb_router_status_source_policy({ name })
      : m.sb_router_status_source_none();
  });
  function goToSourceSettings() {
    closeDrawer();
    openSourceDrawer();
  }

  // ── Engine control ──
  function toggleEngine(turnOn: boolean) {
    // targetMode может быть policy-tun из сохранённых настроек: на прошивке без
    // OpkgTun тумблер иначе позвал бы режим, который бэкенд всё равно отвергнет.
    if (turnOn && targetMode === 'policy-tun' && !tunSupported) return;
    modeSwitch.request(turnOn ? targetMode : 'off');
  }
  function handleToggleClick(_e: MouseEvent) {
    toggleEngine(!captureOn);
  }
  // Выбор режима: при выключенном движке только запоминаем цель тумблера,
  // при включённом — сразу просим переключение (общий confirm + прогресс).
  function selectMode(next: CaptureMode) {
    if (switchBusy || next === targetMode) return;
    if (next === 'policy-tun' && !tunSupported) return;
    pickedMode = next;
    if (activeMode !== null) modeSwitch.request(next);
  }
  async function restartEngine(_e: MouseEvent) {
    if (restarting) return;
    restarting = true;
    try {
      await api.singboxControl('restart');
      await singboxRouterStore.reloadStatus();
      notifications.success(m.sb_router_status_restarted());
    } catch (e) {
      notifications.error(m.sb_router_status_restart_failed({ message: e instanceof Error ? e.message : String(e) }));
    } finally {
      restarting = false;
    }
  }

  // ── Settings (expert, auto-save) ──
  async function applyPatch(patch: Partial<SingboxRouterSettings>) {
    if (!cfg) return;
    saving = true;
    lastError = null;
    try {
      await mergeAndSaveSettings(patch);
    } catch (e) {
      lastError = e instanceof Error ? e.message : String(e);
      notifications.error(m.sb_router_common_save_failed({ message: lastError }));
    } finally {
      saving = false;
    }
  }
  function toggleAutoDetect(checked: boolean) {
    const { override, patch } = planToggleAutoDetect(checked);
    wanAutoOverride = override;
    if (patch) void applyPatch(patch);
  }
  function onCacheLocationChange(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    if (v === 'flash' || v === 'tmp') void applyPatch({ cacheFileLocation: v });
  }

  function onWanInterfaceChange(e: Event) {
    const action = planSelectWanInterface((e.currentTarget as HTMLSelectElement).value);
    if (!action) return;
    wanAutoOverride = action.override;
    if (action.patch) void applyPatch(action.patch);
  }
  function toggleSniffer(checked: boolean) { void applyPatch({ snifferEnabled: checked }); }
  function togglePreset(id: string) {
    const current = cfg?.bypassPresets ?? [];
    const next = current.includes(id) ? current.filter((x) => x !== id) : [...current, id];
    void applyPatch({ bypassPresets: next });
  }

  const UDP_TIMEOUT_OPTIONS = $derived([
    { value: '', label: m.sb_router_status_udp_timeout_default() },
    { value: '5m0s', label: m.sb_router_status_udp_timeout_5m() },
    { value: '10m0s', label: m.sb_router_status_udp_timeout_10m() },
    { value: '15m0s', label: m.sb_router_status_udp_timeout_15m() },
    { value: '30m0s', label: m.sb_router_status_udp_timeout_30m() },
    { value: '1h0m0s', label: m.sb_router_status_udp_timeout_1h() },
    { value: '3h0m0s', label: m.sb_router_status_udp_timeout_3h() },
  ]);

  const UDP_NAT_MAX_OPTIONS = $derived([
    { value: '', label: m.sb_router_status_udp_nat_auto() },
    { value: '2048', label: '2048' },
    { value: '4096', label: '4096' },
    { value: '8192', label: '8192' },
    { value: '16384', label: '16384' },
  ]);
</script>

<SideDrawer {open} onClose={closeDrawer} title={m.sb_router_status_title()} width={420}>
  <div class="sections">
    <!-- Состояние -->
    <section class="sec">
      <div class="sec-cap">{m.sb_router_status_sec_state()}</div>
      <div class="engine-status" class:state-off={engineState === 'off'} class:state-warn={engineState === 'warn'} class:state-on={engineState === 'on'}>
        <div class="engine-main">
          <Toggle checked={captureOn} controlled loading={switchBusy} onchange={toggleEngine} />
          <div class="engine-text">
            <div class="engine-head">
              <StatusDot variant={engineDotVariant} size="sm" />
              <div class="engine-title">{bigTitle}</div>
            </div>
            <div class="engine-sub">{bigSubtitle}</div>
          </div>
        </div>
        <div class="engine-meta">
          <span>{m.sb_router_status_version()}</span>
          <span class="engine-version">{sbVersionLabel}</span>
        </div>
      </div>

      <div class="sec-cap">{m.sb_router_status_sec_capture()}</div>
      <div class="card-grid">
        <OutboundOption
          label={m.sb_router_status_mode_tproxy()}
          sub={m.sb_router_status_mode_tproxy_sub()}
          tone="accent"
          selected={targetMode === 'tproxy'}
          onclick={() => selectMode('tproxy')}
        />
        <OutboundOption
          label={m.sb_router_status_mode_policy_tun()}
          sub={m.sb_router_status_mode_policy_tun_sub()}
          tone="accent"
          selected={targetMode === 'policy-tun'}
          disabled={!tunSupported}
          title={tunSupported ? undefined : opkgTunUnsupportedReason()}
          onclick={() => selectMode('policy-tun')}
        />
      </div>
      <p class="hint">{m.sb_router_status_fakeip_hint()}</p>
      {#if !tunSupported}
        <p class="hint">{opkgTunUnsupportedReason()}</p>
      {/if}

      {#if showCrashInfo}
        <div class="crash-info">
          <!-- FIX-D: при crashCount 0 (например, серия неудачных стартов до
               grace-периода без записанных падений) строка счётчика скрыта —
               «Падений: 0» рядом с активным подавлением только путает. -->
          {#if crashCount > 0}
            <div class="crash-line">
              <span class="crash-label">{m.sb_router_status_crash_label()}</span>
              <span class="crash-value">{crashCount}</span>
            </div>
          {/if}
          {#if s?.lastCrashReason}
            <p class="crash-reason">{m.sb_router_status_crash_reason({ reason: s.lastCrashReason })}</p>
          {/if}
          {#if crashSuppressedLabel}
            <p class="crash-suppressed">
              {#if crashCount > 0}
                {m.sb_router_status_crash_suppressed_count({ time: crashSuppressedLabel, count: crashCount })}
              {:else}
                {m.sb_router_status_crash_suppressed({ time: crashSuppressedLabel })}
              {/if}
            </p>
          {/if}
        </div>
      {/if}
    </section>

    <!-- Ресурсы: живая память и трафик движка -->
    {#if resourcesVisible}
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_resources()}</div>
        <div class="stat-line" title={m.sb_router_status_memory_title()}>
          <span class="stat-label">{m.sb_router_status_memory()}</span>
          <span class="stat-value">{memoryLabel}</span>
        </div>
        <div class="stat-line">
          <span class="stat-label">{m.sb_router_status_speed()}</span>
          <span class="stat-value">{rateLabel}</span>
        </div>
        <div class="stat-line">
          <span class="stat-label">{m.sb_router_status_session()}</span>
          <span class="stat-value">{sessionLabel}</span>
        </div>
      </section>
    {/if}

    <!-- Зависимости -->
    <section class="sec">
      <div class="sec-cap">{m.sb_router_status_sec_deps()}</div>
      {#each deps as dep}
        <DepRow tone={dep.tone} label={dep.label} hint={dep.hint} />
      {/each}
    </section>

    <!-- Замечания -->
    {#if issueCount > 0}
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_issues()} <Badge variant="warning" size="sm">{issueCount}</Badge></div>
        {#each issues as issue}
          <IssueRow tone={issue.tone} text={issue.text} ctaHint={issue.ctaHint} />
        {/each}
      </section>
    {/if}

    <!-- Карточка режима «Политики + tun»: статус интерфейса + source-preserve.
         Видна и новичку — это состояние режима, а не эксперт-настройка. -->
    {#if policyTunMode && cfg}
      <PolicyTunCard {cfg} status={s} onPatch={(patch) => applyPatch(patch)} />
    {/if}

    <!-- TPROXY у новичка: сводка источника + переход в SourceDrawer. Иначе под
         выбором режима пусто, тогда как policy-tun показывает свою карточку. -->
    {#if !policyTunMode && !isExpert && cfg}
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_source()}</div>
        <p class="hint">{sourceSummary}</p>
        <Button variant="ghost" size="sm" onclick={goToSourceSettings}>{m.sb_router_status_configure_source()}</Button>
      </section>
    {/if}

    <!-- Кэш sing-box (issue #842): единственное место настройки, вне expert-гейта —
         износ флеша касается любого режима с fakeip. -->
    {#if cfg}
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_cache()}</div>
        <div class="field">
          <label class="lbl" for="ed-cache-location">{m.sb_router_status_cache_location()}</label>
          <select
            id="ed-cache-location"
            class="inp"
            value={cfg.cacheFileLocation ?? ''}
            onchange={onCacheLocationChange}
          >
            <!-- Пустое значение — «настройка не задана, путь из 00-base.json»;
                 показываем, пока оно есть, иначе выбрать «Флеш» было бы нечем.
                 Повторный выбор пустого отсекает onCacheLocationChange. -->
            {#if !cfg.cacheFileLocation}
              <option value="">{m.sb_router_status_cache_unset()}</option>
            {/if}
            <option value="flash">{m.sb_router_status_cache_flash()}</option>
            <option value="tmp">{m.sb_router_status_cache_tmp()}</option>
          </select>
        </div>
        <p class="hint">{s?.cacheDbPath ? m.sb_router_status_cache_hint_now({ path: s.cacheDbPath }) : m.sb_router_status_cache_hint()}</p>
      </section>
    {/if}

    {#if isExpert && cfg}
      <!-- Источник трафика (deviceMode/policy) — только TPROXY: в policy-tun
           захват задаётся привязкой интерфейса к политике доступа NDMS. -->
      {#if !policyTunMode}
        <TrafficSourceSettings
          {cfg}
          deviceCount={s?.deviceCount ?? 0}
          policyExists={s?.policyExists !== false}
          variant="expert"
          onPatch={(patch) => void applyPatch(patch)}
        />
      {/if}

      <!-- WAN-интерфейс -->
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_wan()}</div>
        <div class="field-row">
          <span>{m.sb_router_status_wan_auto()}</span>
          <Toggle checked={wanAuto} onchange={(checked) => toggleAutoDetect(checked)} />
        </div>
        {#if !wanAuto}
          <div class="field">
            <label class="lbl" for="ed-wan">{m.sb_router_status_wan_interface()}</label>
            <select id="ed-wan" class="inp" value={cfg.wanInterface ?? ''} onchange={onWanInterfaceChange}>
              <option value="">{m.sb_router_status_wan_choose()}</option>
              {#each wanInterfaces as iface (iface.name)}
                <option value={iface.name}>{iface.name}{iface.label ? ` — ${iface.label}` : ''}</option>
              {/each}
            </select>
          </div>
        {/if}
        <p class="hint">{m.sb_router_status_wan_hint()}</p>
      </section>

      <!-- Анализ трафика -->
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_sniff()}</div>
        <div class="field-row">
          <span>{m.sb_router_status_sniff_enable()}</span>
          <Toggle checked={cfg.snifferEnabled} onchange={(checked) => toggleSniffer(checked)} />
        </div>
        <p class="hint">{m.sb_router_status_sniff_hint()}</p>
        <div class="field">
          <label class="lbl" for="ed-udp-timeout">{m.sb_router_status_udp_timeout()}</label>
          <div class="udp-timeout-row">
            <select
              id="ed-udp-timeout"
              class="inp"
              value={cfg.udpTimeout ?? ''}
              onchange={(e) => void applyPatch({ udpTimeout: (e.currentTarget as HTMLSelectElement).value || undefined })}
            >
              {#each UDP_TIMEOUT_OPTIONS as opt (opt.value)}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
        </div>
        <p class="hint">{m.sb_router_status_udp_timeout_hint()}</p>
        <div class="field">
          <label class="lbl" for="ed-udp-nat-max">{m.sb_router_status_udp_nat_max()}</label>
          <div class="udp-timeout-row">
            <select
              id="ed-udp-nat-max"
              class="inp"
              value={cfg.udpNatMax ? String(cfg.udpNatMax) : ''}
              onchange={(e) => {
                const v = (e.currentTarget as HTMLSelectElement).value;
                void applyPatch({ udpNatMax: v ? Number(v) : undefined });
              }}
            >
              {#each UDP_NAT_MAX_OPTIONS as opt (opt.value)}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
        </div>
        <p class="hint">{m.sb_router_status_udp_nat_max_hint()}</p>
      </section>

      <!-- QoS-маршрутизация (DSCP): onPatch возвращает Promise — карточка
           сериализует свои PUT-ы и ресинкается со стором после дренажа очереди. -->
      <QosSettingsCard
        {cfg}
        status={s}
        outboundOptions={$storeOptions}
        onPatch={(patch) => applyPatch(patch)}
      />

      <!-- Исключения: порт-пресеты + IP-пресеты (keendns) + ручные порты/подсети -->
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_bypass()}</div>
        <div class="bypass-presets">
          {#each BYPASS_PRESETS as p (p.id)}
            {@const active = (cfg.bypassPresets ?? []).includes(p.id)}
            <button type="button" class="bypass-preset" class:active onclick={() => togglePreset(p.id)}>
              <span class="preset-label">{p.label}</span>
              <span class="preset-desc">{p.desc}</span>
            </button>
          {/each}
        </div>
        <div class="field">
          <label class="lbl" for="ed-ports-input">{m.sb_router_status_extra_ports()}</label>
          <PortChipsInput inputId="ed-ports-input" value={cfg.bypassExtraPorts ?? ''} onChange={(v) => void applyPatch({ bypassExtraPorts: v })} />
        </div>
        <p class="hint">{m.sb_router_status_ports_hint_pre()}<code class="mono">443 TCP</code>{m.sb_router_status_ports_hint_mid()}<code class="mono">5000-5500 UDP</code>{m.sb_router_status_ports_hint_post()}</p>
        <!-- В «Политики + tun» перехвата netfilter нет вовсе, поэтому исключения
             работают иначе, чем в TPROXY: они влияют только на классы QoS и на
             перехват DNS. Про 53 сказано отдельно — там выключатель СОЗНАТЕЛЬНО
             грубее, чем в TPROXY (пер-протокольный там, общий здесь), потому что
             сам перехват 53-го неделим: на усечённый ответ клиент переспрашивает
             по TCP, и половинчатый перехват дал бы резолвинг, зависящий от
             размера ответа. -->
        {#if policyTunMode}
          <p class="hint">{m.sb_router_status_policy_tun_bypass_pre()} <code class="mono">53</code> {m.sb_router_status_policy_tun_bypass_post()}</p>
        {/if}
        <div class="field">
          <label class="lbl" for="ed-subnets-input">{m.sb_router_status_extra_subnets()}</label>
          <SubnetChipsInput inputId="ed-subnets-input" value={cfg.bypassExtraSubnets ?? ''} onChange={(v) => void applyPatch({ bypassExtraSubnets: v })} />
        </div>
        <p class="hint">{m.sb_router_status_subnets_hint()}</p>
        <!-- Набор AWGM-BYPASS живёт только в TPROXY-перехвате: в policy-tun
             (DSCPOnly) правило обхода не эмитится — обходить нечего. -->
        {#if !policyTunMode}
          <BypassGeoIPTags {cfg} onPatch={(patch) => applyPatch(patch)} />
        {/if}
      </section>
    {/if}
  </div>

  {#snippet footer()}
    <div class="footer-actions">
      <div class="footer-btns">
        <Button variant={captureOn ? 'danger' : 'primary'} size="sm" fullWidth disabled={switchBusy} onclick={handleToggleClick}>
          {captureOn ? m.sb_router_status_turn_off() : m.sb_router_status_turn_on()}
        </Button>
        <Button variant="ghost" size="sm" fullWidth loading={restarting} onclick={restartEngine}>{m.sb_router_status_restart()}</Button>
      </div>
      {#if isExpert}
        <span class="save-status" class:err={lastError}>
          {saving ? m.sb_router_status_saving() : lastError ? m.sb_router_status_save_error() : m.sb_router_status_saved()}
        </span>
      {/if}
    </div>
  {/snippet}
</SideDrawer>

<style>
  .sections { display: flex; flex-direction: column; }
  .sec {
    padding: 14px var(--sp-4);
    border-bottom: 1px solid var(--border);
    display: flex; flex-direction: column; gap: 10px;
  }
  .sec:last-of-type { border-bottom: 0; }
  .sec-cap {
    font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em;
    color: var(--text-muted); display: flex; align-items: center; gap: 8px;
  }

  .engine-status {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
  }
  .engine-status.state-on {
    border-left: 3px solid var(--color-success, #22c55e);
  }
  .engine-status.state-warn {
    border-left: 3px solid var(--color-warning, #dab856);
  }
  .engine-status.state-off {
    border-left: 3px solid color-mix(in srgb, var(--text-muted) 55%, var(--border));
  }
  .engine-main {
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }
  .engine-text {
    flex: 1;
    min-width: 0;
    padding-top: 2px;
  }
  .engine-head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .engine-title {
    font-weight: 600;
    font-size: 14px;
    color: var(--text-primary);
    line-height: 1.25;
  }
  .engine-sub {
    font-size: 11.5px;
    color: var(--text-muted);
    margin-top: 4px;
    line-height: 1.4;
  }
  .engine-meta {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding-top: 8px;
    border-top: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-muted);
  }
  .engine-version {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-secondary);
  }

  .card-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  @media (max-width: 480px) { .card-grid { grid-template-columns: 1fr; } }

  .field { display: flex; flex-direction: column; gap: 4px; }
  .field-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    font-size: 13px;
  }
  .field-row > span {
    flex: 1;
    min-width: 0;
  }
  .field-row > :global([role='switch']),
  .field-row > :global(.toggle-container) {
    flex-shrink: 0;
  }
  .lbl { font-size: 11px; color: var(--text-muted); font-weight: 500; }
  .inp {
    padding: 6px 10px; border-radius: var(--radius-sm); background: var(--bg-primary);
    border: 1px solid var(--border); color: var(--text-primary); font-size: 12.5px; font-family: inherit;
  }
  .udp-timeout-row { display: flex; gap: 6px; }
  .udp-timeout-row .inp { flex: 1; }
  .hint { margin: 0; font-size: 11.5px; color: var(--text-muted); line-height: 1.4; }
  .bypass-presets { display: flex; flex-direction: column; gap: 6px; }
  /* Не .chip: имя занято утилитой Skeleton и app.css (nowrap + центровка). */
  .bypass-preset {
    text-align: left; padding: 8px 10px; border-radius: var(--radius-sm); background: var(--bg-tertiary);
    border: 1px solid var(--border); cursor: pointer; font-family: inherit; color: inherit;
    display: flex; flex-direction: column; gap: 2px;
    transition: background var(--t-fast) ease, color var(--t-fast) ease, border-color var(--t-fast) ease;
  }
  .bypass-preset:hover { color: var(--color-text-primary); border-color: var(--color-border-hover); }
  .bypass-preset.active { background: var(--accent-soft); border-color: var(--accent); }
  .preset-label { font-size: 12.5px; font-weight: 600; }
  .preset-desc { font-size: 11px; color: var(--text-muted); font-family: var(--font-mono); }

  .footer-actions { display: flex; flex-direction: column; gap: 6px; width: 100%; }
  .footer-btns {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 6px;
    width: 100%;
  }
  .save-status { align-self: flex-end; font-size: 11px; color: var(--text-muted); }
  .save-status.err { color: var(--color-error, #dc2626); }
  code.mono {
    font-family: var(--font-mono);
    font-size: 10.5px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 0 3px;
    color: var(--text-secondary);
  }
  .crash-info {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-left: 3px solid var(--color-warning, #dab856);
  }
  .crash-line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    font-size: 12px;
  }
  .crash-label { color: var(--text-muted); }
  .crash-value {
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .crash-reason {
    margin: 0;
    font-size: 11.5px;
    color: var(--text-secondary);
    line-height: 1.4;
    word-break: break-word;
  }
  .crash-suppressed {
    margin: 0;
    font-size: 11.5px;
    color: var(--color-warning, #dab856);
    line-height: 1.4;
  }
  .stat-line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    font-size: 12px;
  }
  .stat-label { color: var(--text-muted); }
  .stat-value {
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: 11.5px;
    text-align: right;
  }
</style>
