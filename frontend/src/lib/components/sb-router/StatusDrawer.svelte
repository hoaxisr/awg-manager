<!--
  Единое меню движка sing-box. Открывается кликом по движку/статус-pill в hero (drawerStore).
  beginner: состояние + здоровье + управление. expert: + редактируемые настройки (auto-save).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { SideDrawer, Toggle, Button, Badge, StatusDot, Modal } from '$lib/components/ui';
  import { api, ApiGatewayError } from '$lib/api/client';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
  import { singboxStatus } from '$lib/stores/singbox';
  import { singboxMemory } from '$lib/stores/singboxMemory';
  import { singboxTrafficLive } from '$lib/stores/singboxEngineStats';
  import { formatBytes, formatByteRate } from '$lib/utils/format';
  import { systemInfo } from '$lib/stores/system';
  import { notifications } from '$lib/stores/notifications';
  import { drawerOpen, closeDrawer } from './drawerStore';
  import { mode } from './modeStore';
  import DepRow from './DepRow.svelte';
  import IssueRow from './IssueRow.svelte';
  import PortChipsInput from './PortChipsInput.svelte';
  import SubnetChipsInput from './SubnetChipsInput.svelte';
  import TrafficSourceSettings from './TrafficSourceSettings.svelte';
  import SelectiveIpsetSnapshot from './SelectiveIpsetSnapshot.svelte';
  import QosSettingsCard from './QosSettingsCard.svelte';
  import { deriveDeps, deriveIssues } from './drawerData';
  import { formatSuppressedUntil, CRASH_WORDS } from './crashInfo';
  import { mergeAndSaveSettings, BYPASS_PRESETS } from './settingsActions';
  import { resolveWanAuto, planToggleAutoDetect, planSelectWanInterface, type WanAutoOverride } from './wanMode';
  import { pluralize, pluralForm, RULE_WORDS } from '$lib/utils/pluralize';
  import { selectiveBypass } from '$lib/stores/selectiveBypass';
  import type { SingboxRouterSettings, SingboxRouterWANInterface } from '$lib/types';

  const { status: selectiveBypassStatus } = selectiveBypass;

  const status = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;
  const storeOptions = singboxRouterStore.options;

  let open = $derived($drawerOpen);
  let s = $derived($status);
  let cfg = $derived($storeSettings);
  let isExpert = $derived($mode === 'expert');

  let singboxInstallStatus = $derived($singboxStatus.data);
  let sysInfo = $derived($systemInfo.data);

  let deps = $derived(deriveDeps(s));
  let issues = $derived(deriveIssues(s));
  let issueCount = $derived(issues.length);
  let engineEnabled = $derived(s?.enabled ?? false);
  // Реальная работа перехвата (цепочки + PREROUTING-jump'ы), не просто
  // persisted-тумблер. Заголовок различает «включён, но не работает».
  let engineActive = $derived(engineEnabled && (s?.active ?? false));

  // Тумблер/кнопка управляют режимом через общий modeSwitch (детерминированно
  // tproxy↔off), а не enable/disable «текущего» режима. checked — mode-aware:
  // «вкл» только когда routingMode==='tproxy' (а не голый enabled).
  const settings = singboxRouterStore.settings;
  let tproxyOn = $derived((s?.enabled ?? false) && ($settings?.routingMode === 'tproxy'));
  const switchBusy = $derived(modeSwitchBusy($modeSwitch));

  let wanInterfaces = $state<SingboxRouterWANInterface[]>([]);
  let saving = $state(false);
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
    if (!engineEnabled) return 'Движок выключен';
    return engineActive ? 'Движок работает' : 'Движок не работает';
  });
  let bigSubtitle = $derived.by(() => {
    if (!engineEnabled) return 'Не активен';
    if (!engineActive) return 'Перехват не активен — правила не применены';
    const n = s?.ruleCount ?? 0;
    return `Трафик идёт через ${pluralize(n, RULE_WORDS)}`;
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
  // Секция видна только при работающем TProxy-перехвате: в режиме FakeIP или
  // после остановки движка SSE замолкает и сторы держат протухшие числа
  // (окно до ближайшего тика watchdog'а ~30 с — принятая задержка статуса).
  let resourcesVisible = $derived(engineActive && $settings?.routingMode === 'tproxy');
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
    void loadSelectiveStatus();
  });

  // ── Engine control ──
  function toggleEngine(turnOn: boolean) {
    modeSwitch.request(turnOn ? 'tproxy' : 'off');
  }
  function handleToggleClick(_e: MouseEvent) {
    toggleEngine(!tproxyOn);
  }
  async function restartEngine(_e: MouseEvent) {
    try {
      await api.singboxControl('restart');
      await singboxRouterStore.reloadStatus();
    } catch (e) {
      console.error('restart failed', e);
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
      notifications.error(`Не удалось сохранить: ${lastError}`);
    } finally {
      saving = false;
    }
  }
  function toggleAutoDetect(checked: boolean) {
    const { override, patch } = planToggleAutoDetect(checked);
    wanAutoOverride = override;
    if (patch) void applyPatch(patch);
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

  const UDP_TIMEOUT_OPTIONS = [
    { value: '', label: 'По умолчанию (3 мин)' },
    { value: '5m0s', label: '5 минут' },
    { value: '10m0s', label: '10 минут' },
    { value: '15m0s', label: '15 минут' },
    { value: '30m0s', label: '30 минут' },
    { value: '1h0m0s', label: '1 час' },
    { value: '3h0m0s', label: '3 часа' },
  ];

  // ── Selective bypass ──
  let selectiveInstalling = $state(false);
  let rebuilding = $state(false);
  let snapshotOpen = $state(false);

  let routeFinal = $derived(s?.final || 'direct');
  let selectiveFinalOk = $derived(routeFinal === 'direct');

  let selectiveStatus = $derived($selectiveBypassStatus);
  let selectiveStatusLoaded = $derived(selectiveStatus !== null);
  // Пересборка в процессе: локальный флаг покрывает HTTP round-trip (202),
  // status.rebuilding — фон после ответа и «страница открыта во время пересборки».
  // Сбрасывается по SSE selective-status с rebuilding: false.
  let rebuildInFlight = $derived(rebuilding || (selectiveStatus?.rebuilding ?? false));
  let selectiveIpsetOk = $derived(selectiveStatus?.available ?? false);
  let selectiveSnapshot = $derived(selectiveStatus?.snapshot ?? null);
  let hasSnapshot = $derived(
    !!selectiveSnapshot
      && ((selectiveSnapshot.entryCount ?? 0) > 0
        || (selectiveSnapshot.domainMatcherCount ?? selectiveSnapshot.domainResults?.length ?? 0) > 0
        || (selectiveSnapshot.staticCidrCount ?? selectiveSnapshot.staticCidrs?.length ?? 0) > 0),
  );

  async function loadSelectiveStatus() {
    try {
      const status = await api.singboxRouterSelectiveStatus();
      selectiveBypass.applyStatus(status);
    } catch (_e) { /* ignore */ }
  }

  async function installSelectiveDeps() {
    selectiveInstalling = true;
    try {
      const status = await api.singboxRouterSelectiveInstallDeps();
      selectiveBypass.applyStatus(status);
    } catch (e) {
      notifications.error('Не удалось установить ipset: ' + (e instanceof Error ? e.message : String(e)));
    } finally {
      selectiveInstalling = false;
    }
  }

  async function triggerRebuild() {
    rebuilding = true;
    selectiveBypass.resetProgress();
    selectiveBypass.requestModal();
    const epochBefore = selectiveBypass.statusEpoch();
    try {
      // 202 Accepted = «запущено», не «завершено»: статус приходит с
      // rebuilding: true, а завершение доскажут SSE-события
      // selective-progress / selective-status (модалка закрывается по ним).
      const status = await api.singboxRouterSelectiveRebuild();
      // Мгновенно упавшая пересборка публикует терминальный SSE
      // selective-status РАНЬШЕ, чем разрешится 202 — не затираем более
      // свежее состояние устаревшим телом ответа (иначе кнопка залипает
      // в «Пересборка…»).
      if (selectiveBypass.statusEpoch() === epochBefore) {
        selectiveBypass.applyStatus(status);
      }
    } catch (e) {
      if (e instanceof ApiGatewayError) {
        // Шлюз (nginx) не дождался ответа, но пересборка продолжается на
        // сервере — без тоста об ошибке, прогресс доедет по SSE.
        console.warn('selective rebuild: gateway error, rebuild continues in background', e);
      } else if ((e as { body?: { code?: string } })?.body?.code === 'OPERATION_IN_PROGRESS') {
        // 409: сейчас применяется конфигурация sing-box — честный тост
        // вместо сырого «занято: …».
        notifications.error('Пересборка недоступна: применяется конфигурация sing-box. Повторите позже.');
      } else {
        notifications.error('Не удалось пересобрать ipset: ' + (e instanceof Error ? e.message : String(e)));
      }
    } finally {
      rebuilding = false;
    }
  }

  function toggleSelectiveBypass(checked: boolean) {
    if (checked && !selectiveFinalOk) {
      notifications.error('Селективный перехват требует route.final = direct');
      return;
    }
    void applyPatch({ selectiveBypass: checked });
  }
</script>

<SideDrawer {open} onClose={closeDrawer} title="Движок sing-box" width={420}>
  <div class="sections">
    <!-- Состояние -->
    <section class="sec">
      <div class="sec-cap">Состояние</div>
      <div class="engine-status" class:state-off={engineState === 'off'} class:state-warn={engineState === 'warn'} class:state-on={engineState === 'on'}>
        <div class="engine-main">
          <Toggle checked={tproxyOn} controlled loading={switchBusy} onchange={toggleEngine} />
          <div class="engine-text">
            <div class="engine-head">
              <StatusDot variant={engineDotVariant} size="sm" />
              <div class="engine-title">{bigTitle}</div>
            </div>
            <div class="engine-sub">{bigSubtitle}</div>
          </div>
        </div>
        <div class="engine-meta">
          <span>Версия sing-box</span>
          <span class="engine-version">{sbVersionLabel}</span>
        </div>
      </div>

      {#if showCrashInfo}
        <div class="crash-info">
          <!-- FIX-D: при crashCount 0 (например, серия неудачных стартов до
               grace-периода без записанных падений) строка счётчика скрыта —
               «Падений: 0» рядом с активным подавлением только путает. -->
          {#if crashCount > 0}
            <div class="crash-line">
              <span class="crash-label">Падений за 10 мин</span>
              <span class="crash-value">{crashCount}</span>
            </div>
          {/if}
          {#if s?.lastCrashReason}
            <p class="crash-reason">Причина: {s.lastCrashReason}</p>
          {/if}
          {#if crashSuppressedLabel}
            <p class="crash-suppressed">
              Автоперезапуск приостановлен до {crashSuppressedLabel}{#if crashCount > 0}&nbsp;({crashCount}
              {pluralForm(crashCount, CRASH_WORDS)} за 10 мин){/if}.
              Кнопка «Перезапустить» ниже запускает движок немедленно.
            </p>
          {/if}
        </div>
      {/if}
    </section>

    <!-- Ресурсы: живая память и трафик движка -->
    {#if resourcesVisible}
      <section class="sec">
        <div class="sec-cap">Ресурсы</div>
        <div class="stat-line" title="Память Go-рантайма sing-box по данным Clash API; фактический RSS процесса выше">
          <span class="stat-label">Память sing-box</span>
          <span class="stat-value">{memoryLabel}</span>
        </div>
        <div class="stat-line">
          <span class="stat-label">Скорость</span>
          <span class="stat-value">{rateLabel}</span>
        </div>
        <div class="stat-line">
          <span class="stat-label">За сессию</span>
          <span class="stat-value">{sessionLabel}</span>
        </div>
      </section>
    {/if}

    <!-- Зависимости -->
    <section class="sec">
      <div class="sec-cap">Зависимости</div>
      {#each deps as dep}
        <DepRow tone={dep.tone} label={dep.label} hint={dep.hint} />
      {/each}
    </section>

    <!-- Замечания -->
    {#if issueCount > 0}
      <section class="sec">
        <div class="sec-cap">Замечания <Badge variant="warning" size="sm">{issueCount}</Badge></div>
        {#each issues as issue}
          <IssueRow tone={issue.tone} text={issue.text} ctaHint={issue.ctaHint} />
        {/each}
      </section>
    {/if}

    {#if isExpert && cfg}
      <TrafficSourceSettings
        {cfg}
        deviceCount={s?.deviceCount ?? 0}
        policyExists={s?.policyExists !== false}
        variant="expert"
        onPatch={(patch) => void applyPatch(patch)}
      />

      <!-- WAN-интерфейс -->
      <section class="sec">
        <div class="sec-cap">WAN-интерфейс</div>
        <div class="field-row">
          <span>Авто-определение</span>
          <Toggle checked={wanAuto} onchange={(checked) => toggleAutoDetect(checked)} />
        </div>
        {#if !wanAuto}
          <div class="field">
            <label class="lbl" for="ed-wan">Интерфейс</label>
            <select id="ed-wan" class="inp" value={cfg.wanInterface ?? ''} onchange={onWanInterfaceChange}>
              <option value="">— выберите —</option>
              {#each wanInterfaces as iface (iface.name)}
                <option value={iface.name}>{iface.name}{iface.label ? ` — ${iface.label}` : ''}</option>
              {/each}
            </select>
          </div>
        {/if}
        <p class="hint">Через какой внешний интерфейс sing-box отправляет прямой трафик.</p>
      </section>

      <!-- Анализ трафика -->
      <section class="sec">
        <div class="sec-cap">Анализ трафика</div>
        <div class="field-row">
          <span>Включить sniff</span>
          <Toggle checked={cfg.snifferEnabled} onchange={(checked) => toggleSniffer(checked)} />
        </div>
        <p class="hint">Анализ HTTP/TLS/QUIC по содержимому. Улучшает срабатывание domain-based правил при IP-only matchers.</p>
        <div class="field">
          <label class="lbl" for="ed-udp-timeout">UDP таймаут сессии</label>
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
        <p class="hint">Как долго sing-box держит UDP-сессии активными. Увеличьте если игры или другие UDP-приложения обрываются каждые несколько минут.</p>
      </section>

      <!-- Селективный перехват -->
      <section class="sec">
        <div class="sec-cap">Селективный перехват</div>

        <div class="field-row">
          <span>Только трафик из правил</span>
          <Toggle
            checked={cfg.selectiveBypass ?? false}
            disabled={!selectiveFinalOk || (selectiveStatusLoaded && !selectiveIpsetOk)}
            onchange={toggleSelectiveBypass}
          />
        </div>

        {#if !selectiveFinalOk}
          <p class="hint selective-warn">
            Несовместимо с route.final = «{routeFinal}»: при catch-all проксировании весь трафик идёт через sing-box,
            селективный ipset не имеет смысла. Установите final в «direct» в разделе маршрутизации.
          </p>
        {:else if !selectiveStatusLoaded}
          <!-- Статус ещё грузится — показываем описание, toggle активен -->
          <p class="hint">
            При включении в sing-box попадает только трафик к целевым IP из правил маршрутизации (proxy).
            Весь остальной трафик полностью обходит sing-box — не только VPN, а сам движок — и идёт напрямую в WAN.
            Так соединения стабильнее и предсказуемее: игры, стриминг и локальный трафик не проходят через прокси-цепочку.
          </p>
        {:else if !selectiveIpsetOk}
          <!-- ipset не установлен -->
          <p class="hint selective-warn">
            Требуется пакет <code class="mono">ipset</code> — он не установлен на роутере.
          </p>
          <Button
            variant="ghost"
            size="sm"
            fullWidth
            loading={selectiveInstalling}
            onclick={installSelectiveDeps}
          >
            {selectiveInstalling ? 'Установка…' : 'Установить ipset'}
          </Button>
        {:else if cfg.selectiveBypass}
          <!-- Включено и доступно — показываем статистику всегда -->
          <p class="hint">
            В sing-box попадает только трафик к IP из правил proxy; остальное полностью обходит движок
            и уходит в WAN напрямую — стабильнее для игр, стриминга и локального трафика.
          </p>
          <div class="selective-stats">
            <div class="stat-line">
              <span class="stat-label">Записей в ipset</span>
              <span class="stat-value">{selectiveStatus?.entryCount ?? 0}</span>
            </div>
            <div class="stat-line">
              <span class="stat-label">Последняя пересборка</span>
              <span class="stat-value">
                {selectiveStatus?.lastRebuild
                  ? new Date(selectiveStatus.lastRebuild).toLocaleString()
                  : '—'}
              </span>
            </div>
          </div>

          {#if selectiveStatus?.lastError}
            <p class="hint selective-warn">Ошибка пересборки: {selectiveStatus.lastError}</p>
          {/if}

          <p class="hint">
            После смены правил нажмите «Применить» — ipset пересоберётся автоматически.
            Уже открытые соединения (вкладки браузера) могут идти по старому пути, пока не закроются —
            откройте сайт в новой вкладке для проверки.
          </p>
          <p class="hint">
            Подробный лог резолва: <a class="logs-link" href="/tools?tab=logs">Диагностика → Журнал</a>,
            группа «Маршрутизация», подгруппа «Селективный ipset».
          </p>

          {#if hasSnapshot}
            <Button variant="ghost" size="sm" fullWidth onclick={() => (snapshotOpen = true)}>
              Содержимое ipset (домены → IP)
            </Button>
          {/if}

          <Button variant="ghost" size="sm" fullWidth loading={rebuildInFlight} onclick={triggerRebuild}>
            {rebuildInFlight ? 'Пересборка…' : 'Пересобрать ipset'}
          </Button>
        {:else}
          <!-- Доступно, но выключено -->
          <p class="hint">
            При включении в sing-box попадает только трафик к целевым IP из правил маршрутизации (proxy).
            Весь остальной трафик полностью обходит sing-box — не только VPN, а сам движок — и идёт напрямую в WAN.
            Так соединения стабильнее и предсказуемее: игры, стриминг и локальный трафик не проходят через прокси-цепочку.
          </p>
        {/if}
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
        <div class="sec-cap">Исключения</div>
        <div class="chips">
          {#each BYPASS_PRESETS as p (p.id)}
            {@const active = (cfg.bypassPresets ?? []).includes(p.id)}
            <button type="button" class="chip" class:active onclick={() => togglePreset(p.id)}>
              <span class="chip-label">{p.label}</span>
              <span class="chip-desc">{p.desc}</span>
            </button>
          {/each}
        </div>
        <div class="field">
          <label class="lbl" for="ed-ports-input">Доп. порты</label>
          <PortChipsInput inputId="ed-ports-input" value={cfg.bypassExtraPorts ?? ''} onChange={(v) => void applyPatch({ bypassExtraPorts: v })} />
        </div>
        <p class="hint">Эти порты пойдут мимо sing-box (прямо в WAN). Полезно для L2TP/NTP/SMB не ломая LAN-сервисы. Поддерживаются одиночные порты (<code class="mono">443 TCP</code>) и диапазоны (<code class="mono">5000-5500 UDP</code>).</p>
        <div class="field">
          <label class="lbl" for="ed-subnets-input">Доп. подсети</label>
          <SubnetChipsInput inputId="ed-subnets-input" value={cfg.bypassExtraSubnets ?? ''} onChange={(v) => void applyPatch({ bypassExtraSubnets: v })} />
        </div>
        <p class="hint">IP или подсети, чей трафик целиком пойдёт мимо sing-box (прямо в WAN). Нужно для корпоративных VPN (Cisco AnyConnect и т.п.), чтобы их трафик не перехватывался.</p>
      </section>
    {/if}
  </div>

  {#snippet footer()}
    <div class="footer-actions">
      <div class="footer-btns">
        <Button variant={tproxyOn ? 'danger' : 'primary'} size="sm" fullWidth disabled={switchBusy} onclick={handleToggleClick}>
          {tproxyOn ? 'Выключить' : 'Включить'}
        </Button>
        <Button variant="ghost" size="sm" fullWidth onclick={restartEngine}>Перезапустить</Button>
      </div>
      {#if isExpert}
        <span class="save-status" class:err={lastError}>
          {saving ? 'Сохраняем…' : lastError ? `Ошибка` : '✓ Сохранено'}
        </span>
      {/if}
    </div>
  {/snippet}
</SideDrawer>

<Modal
  open={snapshotOpen}
  title="Содержимое ipset"
  size="lg"
  onclose={() => (snapshotOpen = false)}
>
  {#if selectiveSnapshot}
    <SelectiveIpsetSnapshot snapshot={selectiveSnapshot} />
  {/if}
</Modal>

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
  .logs-link { color: var(--accent); text-decoration: none; }
  .logs-link:hover { text-decoration: underline; }
  .chips { display: flex; flex-direction: column; gap: 6px; }
  .chip {
    text-align: left; padding: 8px 10px; border-radius: var(--radius-sm); background: var(--bg-tertiary);
    border: 1px solid var(--border); cursor: pointer; font-family: inherit; color: inherit;
    display: flex; flex-direction: column; gap: 2px;
  }
  .chip.active { background: var(--accent-soft); border-color: var(--accent); }
  .chip-label { font-size: 12.5px; font-weight: 600; }
  .chip-desc { font-size: 11px; color: var(--text-muted); font-family: var(--font-mono); }

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
  .selective-warn {
    color: var(--color-warning, #dab856);
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
  .selective-stats {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
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
