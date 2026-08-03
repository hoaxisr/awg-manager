<!--
  Карточка режима «Политики + tun» (policy-tun) — секция настроек движка
  sing-box (StatusDrawer). Показывает интерфейс режима, под каким именем он
  виден в политиках доступа NDMS, замечание policy-tun-unbound и тумблер
  source-preserve с предпоказом сегментов (GET .../policy-tun/nat-preview).

  Сохранение идёт тем же auto-save пайплайном, что и остальные настройки
  дровера: onPatch → applyPatch → mergeAndSaveSettings → PUT /singbox/router/settings.
-->
<script lang="ts">
  import { Toggle, Button, Badge, Modal } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import IssueRow from './IssueRow.svelte';
  import type {
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

  // ── Предпоказ сегментов (модалка включения) ──
  let pickerOpen = $state(false);
  let preview = $state<PolicyTunNATSegmentInfo[]>([]);
  let previewLoading = $state(false);
  let previewError = $state<string | null>(null);
  let selected = $state<string[]>([]);

  async function openPicker() {
    pickerOpen = true;
    previewLoading = true;
    previewError = null;
    preview = [];
    try {
      const data = await api.getPolicyTunNATPreview();
      preview = data.segments ?? [];
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

  function segmentNote(seg: PolicyTunNATSegmentInfo): string {
    if (seg.mode === 'dynamic') return 'динамический NAT';
    if (seg.mode === 'static') return `static-NAT → ${seg.staticWan || '—'}`;
    return 'без NAT';
  }
</script>

<section class="sec">
  <div class="sec-cap">Режим «Политики + tun»</div>

  <div class="stat-line">
    <span class="stat-label">Интерфейс</span>
    <span class="stat-value">{iface || '—'}</span>
  </div>

  {#if ndmsName}
    <p class="hint">Виден в политиках доступа как <strong>{ndmsName}</strong>.</p>
    <Button variant="ghost" size="sm" fullWidth href="/routing?tab=policy">
      Политики доступа →
    </Button>
  {:else}
    <p class="hint">Интерфейс ещё не создан — он появится после включения режима.</p>
  {/if}

  {#if unboundIssue}
    <IssueRow tone="warning" text={unboundIssue.message} />
  {/if}

  <div class="field-row">
    <span>Сохранять адреса клиентов</span>
    <Toggle checked={wanted} controlled onchange={handleToggle} ariaLabel="Сохранять адреса клиентов" />
  </div>

  {#if !wanted}
    <p class="hint">
      Сейчас sing-box видит все устройства под одним адресом tun-шлюза (172.18.0.1):
      правила по адресу устройства и разбивка соединений по клиентам не работают.
    </p>
  {:else}
    <p class="hint">
      Сегменты на static-NAT: <strong>{segments.length > 0 ? segments.join(', ') : '—'}</strong>.
    </p>
    {#if appliedOff}
      <p class="hint hint-warning">
        Изменение применится после перезапуска режима: пока дефолт-маршрут стоит на
        tun-интерфейсе, включить static-NAT вживую нельзя.
      </p>
    {/if}
  {/if}
</section>

<Modal
  open={pickerOpen}
  title="Сохранять адреса клиентов"
  size="md"
  onclose={() => (pickerOpen = false)}
>
  {#if previewLoading}
    <p class="hint">Загружаем сегменты роутера…</p>
  {:else if previewError}
    <p class="hint hint-warning">Не удалось получить сегменты: {previewError}</p>
  {:else if preview.length === 0}
    <p class="hint">Сегментов, которым можно сменить режим NAT, не найдено.</p>
  {:else}
    <p class="hint">
      Выбранные сегменты переводятся на static-NAT в WAN — sing-box увидит реальные
      адреса клиентов вместо адреса tun-шлюза.
    </p>
    <ul class="seg-list">
      {#each preview as seg (seg.name)}
        <li class="seg-row">
          <label class="seg-label">
            <input
              type="checkbox"
              checked={selected.includes(seg.name)}
              onchange={() => toggleSegment(seg.name)}
            />
            <span class="seg-name">{seg.name}</span>
          </label>
          <Badge variant={seg.mode === 'dynamic' ? 'default' : 'muted'} size="xs">
            {segmentNote(seg)}
          </Badge>
        </li>
      {/each}
    </ul>
    <p class="hint hint-warning">
      Меняет NAT-режим выбранных сегментов (static-NAT). Проверьте проброс портов и UPnP —
      они настраиваются отдельно от режима NAT.
    </p>
  {/if}

  {#snippet actions()}
    <Button variant="ghost" size="md" onclick={() => (pickerOpen = false)}>Отмена</Button>
    <Button variant="primary" size="md" disabled={selected.length === 0} onclick={confirmPicker}>
      Включить
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
  .seg-list {
    list-style: none;
    margin: 0.75rem 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .seg-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
  }
  .seg-label {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    cursor: pointer;
  }
  .seg-name {
    font-size: 13px;
    color: var(--text-primary);
    word-break: break-word;
  }
</style>
