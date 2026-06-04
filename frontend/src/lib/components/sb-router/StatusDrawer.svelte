<!--
  Единое меню движка sing-box. Открывается кликом по движку/статус-pill в hero (drawerStore).
  beginner: состояние + здоровье + управление. expert: + редактируемые настройки (auto-save).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { SideDrawer, Toggle, Button, Badge } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { singboxStatus } from '$lib/stores/singbox';
  import { systemInfo } from '$lib/stores/system';
  import { notifications } from '$lib/stores/notifications';
  import { drawerOpen, closeDrawer } from './drawerStore';
  import { mode } from './modeStore';
  import DepRow from './DepRow.svelte';
  import IssueRow from './IssueRow.svelte';
  import PortChipsInput from './PortChipsInput.svelte';
  import TrafficSourceSettings from './TrafficSourceSettings.svelte';
  import { deriveDeps, deriveIssues } from './drawerData';
  import { mergeAndSaveSettings, BYPASS_PRESETS } from './settingsActions';
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

  let wanInterfaces = $state<SingboxRouterWANInterface[]>([]);
  let saving = $state(false);
  let lastError = $state<string | null>(null);
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

  onMount(async () => {
    void singboxRouterStore.loadAll();
    try {
      wanInterfaces = await api.singboxRouterListWANInterfaces();
    } catch (_e) {
      // ignore
    }
  });

  // ── Engine control ──
  async function toggleEngine(_checked: boolean) {
    try {
      if (engineEnabled) await api.singboxRouterDisable();
      else await api.singboxRouterEnable();
      await singboxRouterStore.reloadStatus();
    } catch (e) {
      console.error('toggleEngine failed', e);
    }
  }
  async function handleToggleClick(_e: MouseEvent) {
    await toggleEngine(!engineEnabled);
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
    if (checked) void applyPatch({ wanAutoDetect: true, wanInterface: '' });
    else void applyPatch({ wanAutoDetect: false });
  }
  function onWanInterfaceChange(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    void applyPatch({ wanAutoDetect: false, wanInterface: v });
  }
  function toggleSniffer(checked: boolean) { void applyPatch({ snifferEnabled: checked }); }
  function togglePreset(id: string) {
    const current = cfg?.bypassPresets ?? [];
    const next = current.includes(id) ? current.filter((x) => x !== id) : [...current, id];
    void applyPatch({ bypassPresets: next });
  }
</script>

<SideDrawer {open} onClose={closeDrawer} title="Движок sing-box" width={420}>
  <div class="sections">
    <!-- Состояние -->
    <section class="sec">
      <div class="sec-cap">Состояние</div>
      <div class="big-toggle" class:is-on={engineEnabled}>
        <Toggle checked={engineEnabled} onchange={toggleEngine} />
        <div class="big-text">
          <div class="big-title">{bigTitle}</div>
          <div class="big-sub">{bigSubtitle}</div>
        </div>
        <span class="big-version">{sbVersionLabel}</span>
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
          <Toggle checked={cfg.wanAutoDetect} onchange={(checked) => toggleAutoDetect(checked)} />
          <span>Авто-определение</span>
        </div>
        {#if !cfg.wanAutoDetect}
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
          <Toggle checked={cfg.snifferEnabled} onchange={(checked) => toggleSniffer(checked)} />
          <span>Включить sniff (HTTP/TLS/QUIC по содержимому)</span>
        </div>
        <p class="hint">Улучшает срабатывание domain-based правил при IP-only matchers.</p>
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
      </section>
    {/if}
  </div>

  {#snippet footer()}
    <div class="footer-actions">
      <Button variant={engineEnabled ? 'danger' : 'primary'} size="sm" fullWidth onclick={handleToggleClick}>
        {engineEnabled ? 'Выключить' : 'Включить'}
      </Button>
      <Button variant="ghost" size="sm" onclick={restartEngine}>Перезапустить</Button>
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

  .big-toggle {
    display: flex; align-items: center; gap: 14px; padding: 14px; border-radius: var(--radius);
    background: color-mix(in srgb, var(--text-muted) 5%, var(--bg-tertiary)); border: 1px solid var(--border);
  }
  .big-toggle.is-on {
    background: color-mix(in srgb, var(--success) 8%, var(--bg-tertiary));
    border-color: color-mix(in srgb, var(--success) 25%, var(--border));
  }
  .big-text { flex: 1; min-width: 0; }
  .big-title { font-weight: 600; font-size: 14px; color: var(--text-primary); }
  .big-sub { font-size: 11.5px; color: var(--text-muted); margin-top: 2px; }
  .big-version { font-family: var(--font-mono); font-size: 11px; color: var(--text-muted); flex-shrink: 0; }

  .field { display: flex; flex-direction: column; gap: 4px; }
  .field-row { display: flex; align-items: center; gap: 10px; font-size: 13px; }
  .lbl { font-size: 11px; color: var(--text-muted); font-weight: 500; }
  .inp {
    padding: 6px 10px; border-radius: var(--radius-sm); background: var(--bg-primary);
    border: 1px solid var(--border); color: var(--text-primary); font-size: 12.5px; font-family: inherit;
  }
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

  .footer-actions { display: flex; gap: 6px; width: 100%; align-items: center; }
  .save-status { margin-left: auto; font-size: 11px; color: var(--text-muted); }
  .save-status.err { color: var(--color-error, #dc2626); }
</style>
