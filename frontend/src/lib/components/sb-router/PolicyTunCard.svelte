<!--
  Карточка режима «Политики + tun» (policy-tun) — секция настроек движка
  sing-box (StatusDrawer). Показывает интерфейс режима, под каким именем он
  виден в политиках доступа NDMS, поле его названия в веб-интерфейсе роутера,
  замечание policy-tun-unbound и тумблер source-preserve (SourcePreserveSection).

  Сохранение идёт тем же auto-save пайплайном, что и остальные настройки
  дровера: onPatch → applyPatch → mergeAndSaveSettings → PUT /singbox/router/settings.
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { Button, Dropdown, Input } from '$lib/components/ui';
  import { tunStackOptions } from './tunStack';
  import { notifications } from '$lib/stores/notifications';
  import { untrack } from 'svelte';
  import IssueRow from './IssueRow.svelte';
  import PolicyCombobox from './PolicyCombobox.svelte';
  import SourcePreserveSection from './SourcePreserveSection.svelte';
  import type {
    TunStack,
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
  const unboundIssue = $derived(
    (status?.issues ?? []).find((i) => i.kind === 'policy-tun-unbound') ?? null,
  );
  // Политика — то же поле, что у режима tproxy (cfg.policyName): в policy-tun
  // она задаёт, чей трафик уходит в туннель, и продукт сам разрешает ею наш
  // интерфейс. Счётчик устройств тут не украшение: ноль привязанных при живом
  // разрешении — второе молчаливо мёртвое состояние режима.
  const deviceCount = $derived(status?.deviceCount ?? 0);
  const policyMissing = $derived(!!cfg.policyName && status?.policyExists === false);

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

  <SourcePreserveSection {cfg} {status} {tunName} {onPatch} />
</section>

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
</style>
