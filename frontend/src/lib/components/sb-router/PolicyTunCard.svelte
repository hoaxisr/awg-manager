<!--
  Карточка режима «Политики + tun» (policy-tun) — секция настроек движка
  sing-box (StatusDrawer). Показывает интерфейс режима, под каким именем он
  виден в политиках доступа NDMS, поле его названия в веб-интерфейсе роутера,
  замечание policy-tun-unbound и тумблер source-preserve с предпоказом
  сегментов (GET .../policy-tun/nat-preview).

  Сохранение идёт тем же auto-save пайплайном, что и остальные настройки
  дровера: onPatch → applyPatch → mergeAndSaveSettings → PUT /singbox/router/settings.
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { Toggle, Button, Badge, Modal, Dropdown, Input } from '$lib/components/ui';
  import { tunStackOptions } from './tunStack';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { untrack } from 'svelte';
  import IssueRow from './IssueRow.svelte';
  import PolicyCombobox from './PolicyCombobox.svelte';
  import type {
    TunStack,
    PolicyTunNATEgress,
    PolicyTunNATSegmentInfo,
    SingboxRouterSettings,
    SingboxRouterStatus,
  } from '$lib/types';

  interface Props {
    cfg: SingboxRouterSettings;
    status: SingboxRouterStatus | null;
    onPatch: (patch: Partial<SingboxRouterSettings>) => void | Promise<void>;
  }
  let { cfg, status, onPatch }: Props = $props();

  const iface = $derived(status?.policyTunIface ?? '');
  const ndmsName = $derived(status?.policyTunNdmsName ?? '');
  // Желаемое (настройки) против применённого (статус). Статус — указатель на
  // бэкенде: absent = «неприменимо», а не «выключено», поэтому строгое === false.
  const wanted = $derived(cfg.policyTunSourcePreserve === true);
  const appliedOff = $derived(status?.policyTunSourcePreserve === false);
  const segments = $derived(cfg.policyTunNatSegments ?? []);
  const unboundIssue = $derived(
    (status?.issues ?? []).find((i) => i.kind === 'policy-tun-unbound') ?? null,
  );
  // Политика — то же поле, что у режима tproxy (cfg.policyName): в policy-tun
  // она задаёт, чей трафик уходит в туннель, и продукт сам разрешает ею наш
  // интерфейс. Счётчик устройств тут не украшение: ноль привязанных при живом
  // разрешении — второе молчаливо мёртвое состояние режима.
  const deviceCount = $derived(status?.deviceCount ?? 0);
  const policyMissing = $derived(!!cfg.policyName && status?.policyExists === false);

  // ── Предпоказ сегментов (модалка включения) ──
  let pickerOpen = $state(false);
  let preview = $state<PolicyTunNATSegmentInfo[]>([]);
  let previewLoading = $state(false);
  let previewError = $state<string | null>(null);
  let selected = $state<string[]>([]);
  // Выходы, на которых подмена адреса СОХРАНИТСЯ: static-NAT ставится на пару
  // «сегмент → выход», а к туннелю записи нет — на этом опция и держится.
  // Выходов несколько: правило вешается на каждый интерфейс роутера, через
  // который сеть может уйти наружу, а не только на текущий выход в интернет.
  let egresses = $state<PolicyTunNATEgress[]>([]);

  const tunName = $derived(ndmsName || m.sb_router_policy_tun_default_tun_name());

  // ── Название интерфейса в NDMS (policyTunDescription) ──
  // Пусто — штатное имя. Сохраняем на change (blur/Enter), а не на каждый
  // символ: каждое сохранение переименовывает интерфейс на роутере. Проверки
  // зеркалят бэкенд (normalizePolicyTunDescription) — ради мгновенного ответа,
  // последнее слово за ним.
  const DEFAULT_TUN_DESCRIPTION = 'awgm policy-tun';
  const TUN_DESCRIPTION_MAX = 32;
  const descDraft = $state({ v: '' });
  // Черновик сбрасывается только когда СОХРАНЁННОЕ имя действительно
  // сменилось. cfg перечитывается после каждого автосохранения дровера, и
  // сброс на каждый новый объект cfg стирал бы набираемый текст.
  // $state, как всё изменяемое в runes-компоненте; untrack — эффект зависит
  // только от cfg, а не от собственной записи.
  let lastSavedDesc: string | undefined = $state();
  $effect(() => {
    const saved = cfg.policyTunDescription ?? '';
    if (saved !== untrack(() => lastSavedDesc)) {
      lastSavedDesc = saved;
      descDraft.v = saved;
    }
  });

  async function commitDescription(raw: string): Promise<void> {
    const v = raw.trim();
    const next = v === DEFAULT_TUN_DESCRIPTION ? '' : v;
    if (next === (cfg.policyTunDescription ?? '')) {
      descDraft.v = next;
      return;
    }
    if ([...next].length > TUN_DESCRIPTION_MAX) {
      notifications.error(m.sb_router_policy_tun_desc_too_long({ max: TUN_DESCRIPTION_MAX }));
      descDraft.v = cfg.policyTunDescription ?? '';
      return;
    }
    if (next.toLowerCase().startsWith('awgm ')) {
      notifications.error(m.sb_router_policy_tun_desc_reserved());
      descDraft.v = cfg.policyTunDescription ?? '';
      return;
    }
    await onPatch({ policyTunDescription: next });
    // Отказ бэкенда (например, управляющий символ, который здесь не
    // проверяется) applyPatch показывает уведомлением и не пробрасывает —
    // о нём говорит cfg: сохранилось — там next, нет — прежнее имя.
    descDraft.v = cfg.policyTunDescription ?? '';
  }

  async function openPicker() {
    pickerOpen = true;
    previewLoading = true;
    previewError = null;
    preview = [];
    try {
      const data = await api.getPolicyTunNATPreview();
      preview = data.segments ?? [];
      egresses = data.egresses ?? [];
      // Уже выбранное пользователем важнее умолчания; на первом включении
      // предвыбираем сегменты за динамическим NAT — именно их маскарад скрывает
      // адреса клиентов от sing-box.
      const known = new Set(preview.map((s) => s.name));
      const keep = segments.filter((n) => known.has(n));
      selected = keep.length > 0 ? keep : preview.filter((s) => s.mode === 'dynamic').map((s) => s.name);
    } catch (e) {
      previewError = e instanceof Error ? e.message : String(e);
    } finally {
      previewLoading = false;
    }
  }

  function toggleSegment(name: string) {
    selected = selected.includes(name)
      ? selected.filter((n) => n !== name)
      : [...selected, name];
  }

  // Стек — то же поле settings.fakeipStack, что правит панель FakeIP: у бэкенда
  // он один на оба tun-режима. Пустое значение = собственный стек sing-tun.
  const stackHint = $derived(
    cfg.fakeipStack
      ? m.sb_router_policy_tun_stack_hint_legacy()
      : m.sb_router_policy_tun_stack_hint_default(),
  );

  function handleStack(v: TunStack) {
    if (v === (cfg.fakeipStack ?? '')) return;
    void onPatch({ fakeipStack: v });
  }

  function handleToggle(checked: boolean) {
    if (checked) {
      void openPicker();
      return;
    }
    // Выключение — без диалога: бэкенд вернёт сегментам исходный NAT на тике.
    void onPatch({ policyTunSourcePreserve: false, policyTunNatSegments: [] });
  }

  function confirmPicker() {
    pickerOpen = false;
    void onPatch({ policyTunSourcePreserve: true, policyTunNatSegments: selected });
  }

  // Вторая строка сегмента: системное имя (по нему сеть ищут в веб-морде
  // роутера) и подсеть — по ней свою сеть узнают вернее, чем по названию.
  function segmentTech(seg: PolicyTunNATSegmentInfo): string {
    return [seg.name, seg.subnet].filter(Boolean).join(' · ');
  }

  // Сегмент, уже переведённый на static-NAT вручную, трогать незачем: адреса
  // его устройств и так доезжают до sing-box.
  function alreadyPreserved(seg: PolicyTunNATSegmentInfo): boolean {
    return seg.mode === 'static';
  }
</script>

<section class="sec">
  <div class="sec-cap">{m.sb_router_policy_tun_title()}</div>

  <div class="stat-line">
    <span class="stat-label">{m.sb_router_policy_tun_interface()}</span>
    <span class="stat-value">{iface || '—'}</span>
  </div>

  {#if ndmsName}
    <p class="hint">{m.sb_router_policy_tun_visible_as_pre()} <strong>{ndmsName}</strong>.</p>
  {:else}
    <p class="hint">{m.sb_router_policy_tun_not_created()}</p>
  {/if}

  <div class="field">
    <label class="lbl" for="policy-tun-description">{m.sb_router_policy_tun_desc_label()}</label>
    <Input
      id="policy-tun-description"
      value={descDraft.v}
      placeholder={DEFAULT_TUN_DESCRIPTION}
      fullWidth
      oninput={(v) => (descDraft.v = v)}
      onchange={commitDescription}
    />
  </div>
  <p class="hint">
    {m.sb_router_policy_tun_desc_hint({ name: DEFAULT_TUN_DESCRIPTION })}
  </p>

  <div class="field">
    <span class="lbl">{m.sb_router_policy_tun_stack_label()}</span>
    <Dropdown value={cfg.fakeipStack ?? ''} options={tunStackOptions()} fullWidth onchange={handleStack} />
  </div>
  <p class="hint">{stackHint}</p>

  <div class="field">
    <span class="lbl">{m.sb_router_policy_tun_access_policy()}</span>
    <PolicyCombobox value={cfg.policyName} onChange={(name) => void onPatch({ policyName: name })} />
  </div>

  {#if cfg.policyName}
    <p class="hint">
      {m.sb_router_policy_tun_policy_in_pre()}
      <strong>{m.sb_router_devices_count({ count: deviceCount })}</strong> {m.sb_router_policy_tun_policy_in_post()}
    </p>
    {#if policyMissing}
      <p class="hint hint-warning">
        {m.sb_router_policy_tun_policy_missing({ name: cfg.policyName })}
      </p>
    {/if}
    <Button
      variant="ghost"
      size="sm"
      fullWidth
      href="/routing?tab=policy&policy={encodeURIComponent(cfg.policyName)}"
    >
      {m.sb_router_policy_tun_manage_devices()}
    </Button>
  {:else}
    <p class="hint">
      {m.sb_router_policy_tun_no_policy()}
    </p>
    <Button variant="ghost" size="sm" fullWidth href="/routing?tab=policy">
      {m.sb_router_policy_tun_access_policies()}
    </Button>
  {/if}

  {#if unboundIssue}
    <IssueRow tone="warning" text={unboundIssue.message} />
  {/if}

  <div class="field-row">
    <span>{m.sb_router_policy_tun_preserve_clients()}</span>
    <Toggle checked={wanted} controlled onchange={handleToggle} ariaLabel={m.sb_router_policy_tun_preserve_clients()} />
  </div>

  {#if !wanted}
    <p class="hint">
      {m.sb_router_policy_tun_preserve_off_hint()}
    </p>
  {:else}
    <p class="hint">
      {m.sb_router_policy_tun_segments_nat_pre()} <strong>{segments.length > 0 ? segments.join(', ') : '—'}</strong>.
    </p>
    {#if appliedOff}
      <p class="hint hint-warning">
        {m.sb_router_policy_tun_applied_partial()}
      </p>
    {/if}
  {/if}
</section>

<Modal
  open={pickerOpen}
  title={m.sb_router_policy_tun_preserve_clients()}
  size="md"
  onclose={() => (pickerOpen = false)}
>
  {#if previewLoading}
    <p class="hint">{m.sb_router_policy_tun_loading_segments()}</p>
  {:else if previewError}
    <p class="hint hint-warning">{m.sb_router_policy_tun_segments_failed({ message: previewError })}</p>
  {:else if preview.length === 0}
    <p class="hint">{m.sb_router_policy_tun_segments_none()}</p>
  {:else}
    <p class="hint">
      {m.sb_router_policy_tun_picker_hint()}
    </p>

    <div class="flow">
      <div class="flow-col">
        <div class="flow-cap">{m.sb_router_policy_tun_networks()}</div>
        <ul class="seg-list">
          {#each preview as seg (seg.name)}
            <li class="seg-row" class:seg-on={selected.includes(seg.name)}>
              <label class="seg-label">
                <input
                  type="checkbox"
                  checked={selected.includes(seg.name)}
                  onchange={() => toggleSegment(seg.name)}
                />
                <span class="seg-main">
                  <span class="seg-name">{seg.label || seg.name}</span>
                  <span class="seg-tech">{segmentTech(seg)}</span>
                  {#if alreadyPreserved(seg)}
                    <span class="seg-side">
                      <Badge variant="muted" size="xs">{m.sb_router_policy_tun_already_manual()}</Badge>
                    </span>
                  {/if}
                </span>
              </label>
            </li>
          {/each}
        </ul>
      </div>

      <div class="flow-arrow" aria-hidden="true">→</div>

      <div class="flow-col">
        <div class="flow-cap">{m.sb_router_policy_tun_exit_sees()}</div>
        <div class="dest dest-free">
          <span class="dest-name">{m.sb_router_policy_tun_tunnel_singbox()}</span>
          <span class="dest-tech">{tunName}</span>
          <span class="dest-note">{m.sb_router_policy_tun_note_client_addrs()}</span>
        </div>
        {#each egresses as eg (eg.name)}
          <div class="dest">
            <span class="dest-name">{eg.label || eg.name}</span>
            {#if eg.label}
              <span class="dest-tech">{eg.name}</span>
            {/if}
            <span class="dest-note">{m.sb_router_policy_tun_note_router_addr()}</span>
          </div>
        {:else}
          <div class="dest">
            <span class="dest-name">{m.sb_router_policy_tun_internet_exits()}</span>
            <span class="dest-note">{m.sb_router_policy_tun_note_router_addr()}</span>
          </div>
        {/each}
      </div>
    </div>

    <p class="hint hint-warning">
      {m.sb_router_policy_tun_warn_pre()} <b>{m.sb_router_policy_tun_warn_bold()}</b>{m.sb_router_policy_tun_warn_post()}
      {#if egresses.length > 0}
        {m.sb_router_policy_tun_warn_egresses({ count: egresses.length })}
      {/if}
    </p>
  {/if}

  {#snippet actions()}
    <Button variant="ghost" size="md" onclick={() => (pickerOpen = false)}>{m.common_cancel()}</Button>
    <Button variant="primary" size="md" disabled={selected.length === 0} onclick={confirmPicker}>
      {m.sb_router_policy_tun_enable()}
    </Button>
  {/snippet}
</Modal>

<style>
  /* Стили секции повторяют .sec/.sec-cap/.hint дровера (scoped-стили родителя
     не проникают в дочерний компонент — как в QosSettingsCard). */
  .sec {
    padding: 14px var(--sp-4);
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .sec:last-of-type { border-bottom: 0; }
  .sec-cap {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .hint { margin: 0; font-size: 11.5px; color: var(--text-muted); line-height: 1.4; }
  .hint strong { color: var(--text-primary); font-weight: 600; }
  .hint-warning { color: var(--color-warning, #dab856); }
  .field { display: flex; flex-direction: column; gap: 4px; }
  .lbl { font-size: 11px; color: var(--text-muted); font-weight: 500; }
  .field-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    font-size: 13px;
  }
  .field-row > span { flex: 1; min-width: 0; }
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
    word-break: break-all;
  }
  /* Схема «слева сети → справа выходы»: экран объясняет форму́й, а не текстом,
     что подмена адреса снимается для ПАРЫ «сеть → выход», а не для сети. */
  .flow {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 18px minmax(0, 1fr);
    gap: 10px;
    align-items: start;
    margin: 0.75rem 0;
  }
  @media (max-width: 560px) {
    .flow { grid-template-columns: minmax(0, 1fr); }
    .flow-arrow { display: none; }
  }
  .flow-col { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
  .flow-cap {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .flow-arrow {
    align-self: center;
    color: var(--text-muted);
    font-size: 14px;
    text-align: center;
  }
  .dest {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 9px 11px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
  }
  .dest-free { border-color: var(--color-success, #9ece6a); }
  .dest-name { font-size: 13px; font-weight: 600; color: var(--text-primary); }
  .dest-tech { font-family: var(--font-mono); font-size: 11px; color: var(--text-muted); }
  .dest-note { font-size: 11.5px; color: var(--text-secondary); line-height: 1.4; }

  .seg-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .seg-row {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
  }
  .seg-on { border-color: var(--color-accent); }
  .seg-label {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    /* flex:1 + min-width:0 обязательны: без них бейдж справа выдавливает текст
       сети в нулевую ширину и тот ломается по одному символу в столбик. */
    flex: 1 1 auto;
    min-width: 0;
    cursor: pointer;
  }
  .seg-label input { flex: none; margin-top: 2px; }
  .seg-side { margin-top: 3px; }
  .seg-main { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .seg-name {
    font-size: 13px;
    color: var(--text-primary);
    word-break: break-word;
  }
  .seg-tech {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-muted);
    word-break: break-all;
  }
</style>
