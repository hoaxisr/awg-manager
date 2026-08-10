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
  import PolicyCombobox from './PolicyCombobox.svelte';
  import { pluralize, DEVICE_WORDS } from '$lib/utils/pluralize';
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
  // Выход, на котором подмена адреса СОХРАНИТСЯ: static-NAT ставится на пару
  // «сегмент → этот выход», а к туннелю записи нет — на этом опция и держится.
  let wanName = $state('');
  let wanLabel = $state('');

  const wanTitle = $derived(wanLabel || wanName || 'Интернет');
  const tunName = $derived(ndmsName || 'туннель sing-box');

  async function openPicker() {
    pickerOpen = true;
    previewLoading = true;
    previewError = null;
    preview = [];
    try {
      const data = await api.getPolicyTunNATPreview();
      preview = data.segments ?? [];
      wanName = data.wanName ?? '';
      wanLabel = data.wanLabel ?? '';
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
  <div class="sec-cap">Режим «Политики + tun»</div>

  <div class="stat-line">
    <span class="stat-label">Интерфейс</span>
    <span class="stat-value">{iface || '—'}</span>
  </div>

  {#if ndmsName}
    <p class="hint">Виден в политиках доступа как <strong>{ndmsName}</strong>.</p>
  {:else}
    <p class="hint">Интерфейс ещё не создан — он появится после включения режима.</p>
  {/if}

  <div class="field">
    <span class="lbl">Политика доступа</span>
    <PolicyCombobox value={cfg.policyName} onChange={(name) => void onPatch({ policyName: name })} />
  </div>

  {#if cfg.policyName}
    <p class="hint">
      Интерфейс разрешается выходом этой политики автоматически. В ней
      <strong>{pluralize(deviceCount, DEVICE_WORDS)}</strong> — их трафик и уходит в туннель.
      При смене политики прежняя останется разрешённой: снимите разрешение вручную.
    </p>
    {#if policyMissing}
      <p class="hint hint-warning">
        Политика «{cfg.policyName}» не найдена в NDMS — создайте заново или выберите другую.
      </p>
    {/if}
    <Button
      variant="ghost"
      size="sm"
      fullWidth
      href="/routing?tab=policy&policy={encodeURIComponent(cfg.policyName)}"
    >
      Управление устройствами →
    </Button>
  {:else}
    <p class="hint">
      Политика не выбрана — трафик устройств в туннель не пойдёт: интерфейсу некуда встать выходом.
    </p>
    <Button variant="ghost" size="sm" fullWidth href="/routing?tab=policy">
      Политики доступа →
    </Button>
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
      Отметьте сети, устройства которых sing-box должен видеть по их настоящим адресам.
      В интернет они продолжат выходить через адрес роутера.
    </p>

    <div class="flow">
      <div class="flow-col">
        <div class="flow-cap">Сети</div>
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
                      <Badge variant="muted" size="xs">уже настроено вручную</Badge>
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
        <div class="flow-cap">Что увидит выход</div>
        <div class="dest dest-free">
          <span class="dest-name">Туннель sing-box</span>
          <span class="dest-tech">{tunName}</span>
          <span class="dest-note">адреса устройств — правила и статистика по клиентам работают</span>
        </div>
        <div class="dest">
          <span class="dest-name">{wanTitle}</span>
          {#if wanName && wanName !== wanTitle}
            <span class="dest-tech">{wanName}</span>
          {/if}
          <span class="dest-note">адрес роутера — как и было, интернет не меняется</span>
        </div>
      </div>
    </div>

    <p class="hint hint-warning">
      Меняется способ выхода отмеченных сетей в интернет: <b>проверьте проброс портов и UPnP</b>,
      они настраиваются отдельно.
      {#if wanName}
        Адрес подменяется только на выходе <b>{wanName}</b>; если у сети есть другие выходы в
        интернет, трафик к ним пойдёт с адресом устройства и может не вернуться.
      {/if}
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
