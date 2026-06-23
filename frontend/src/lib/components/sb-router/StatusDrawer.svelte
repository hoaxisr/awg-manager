<!--
  Единое меню движка sing-box. Открывается кликом по движку/статус-pill в hero (drawerStore).
  beginner: состояние + здоровье + управление. expert: + редактируемые настройки (auto-save).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { SideDrawer, Toggle, Button, Badge, StatusDot } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
  import { singboxStatus } from '$lib/stores/singbox';
  import { systemInfo } from '$lib/stores/system';
  import { notifications } from '$lib/stores/notifications';
  import { drawerOpen, closeDrawer } from './drawerStore';
  import { mode } from './modeStore';
  import DepRow from './DepRow.svelte';
  import IssueRow from './IssueRow.svelte';
  import PortChipsInput from './PortChipsInput.svelte';
  import SubnetChipsInput from './SubnetChipsInput.svelte';
  import TrafficSourceSettings from './TrafficSourceSettings.svelte';
  import { deriveDeps, deriveIssues } from './drawerData';
  import { mergeAndSaveSettings, BYPASS_PRESETS } from './settingsActions';
  import { resolveWanAuto, planToggleAutoDetect, planSelectWanInterface, type WanAutoOverride } from './wanMode';
  import { pluralize, RULE_WORDS } from '$lib/utils/pluralize';
  import type { SingboxRouterSettings, SingboxRouterWANInterface } from '$lib/types';

  const status = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;

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

  onMount(async () => {
    void singboxRouterStore.loadAll();
    try {
      wanInterfaces = await api.singboxRouterListWANInterfaces();
    } catch (_e) {
      // ignore
    }
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
    </section>

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

      <!-- Исключения портов -->
      <section class="sec">
        <div class="sec-cap">Исключения портов</div>
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
        <p class="hint">Эти порты пойдут мимо sing-box (прямо в WAN). Полезно для L2TP/NTP/SMB не ломая LAN-сервисы.</p>
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
</style>
