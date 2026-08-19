<!--
  Подсекция «Обход по geoip» внутри блока исключений движка (StatusDrawer).
  Выбранные теги geoip-.dat целиком обходят sing-box через набор AWGM-BYPASS —
  та же семантика, что у «Доп. подсетей», но набором из-за объёма.

  Сохранение — тем же auto-save пайплайном, что и остальные настройки дровера:
  onPatch → applyPatch → mergeAndSaveSettings → PUT /singbox/router/settings.
  Статус набора живёт в сторе bypassSetStatus: наполнение асинхронное, о его
  завершении бэкенд говорит resource:invalidated («bypass-set»).
-->
<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { api } from '$lib/api/client';
  import { Button } from '$lib/components/ui';
  import { bypassSetStatus } from '$lib/stores/bypassSet';
  import { notifications } from '$lib/stores/notifications';
  import { formatRelativeTime } from '$lib/utils/format';
  import {
    aggregateGeoIPTags,
    sumSelectedTags,
    BYPASS_SET_MAX_ELEM,
    type GeoIPTagOption,
  } from './bypassGeoTags';
  import type { SingboxRouterSettings } from '$lib/types';

  interface Props {
    cfg: SingboxRouterSettings;
    onPatch: (patch: Partial<SingboxRouterSettings>) => void | Promise<void>;
  }
  let { cfg, onPatch }: Props = $props();

  let options = $state<GeoIPTagOption[]>([]);
  let geoFileCount = $state(0);
  let catalogLoading = $state(true);
  let catalogError = $state<string | null>(null);
  let query = $state('');
  // Причина, по которой последний клик не сохранён (бюджет набора). Гаснет
  // на следующем действии пользователя.
  let budgetError = $state<string | null>(null);
  let installing = $state<'ipset' | 'conntrack' | null>(null);

  const status = $derived($bypassSetStatus.data);
  const selected = $derived(cfg.bypassGeoipTags ?? []);
  const total = $derived(sumSelectedTags(selected, options));
  const overBudget = $derived(total > BYPASS_SET_MAX_ELEM);
  const filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    return q ? options.filter((o) => o.name.includes(q)) : options;
  });
  // Пакет ставится синхронным запросом, но «ставится» может прийти и от
  // другой вкладки — обе причины блокируют обе кнопки.
  const installBusy = $derived(installing !== null || (status?.installing ?? false));
  const entryCountLabel = $derived(
    status?.entryCountOK ? status.entryCount.toLocaleString('ru-RU') : 'н/д',
  );

  onMount(async () => {
    try {
      const files = (await api.getGeoFiles()) ?? [];
      const geoip = files.filter((f) => f.type === 'geoip');
      geoFileCount = geoip.length;
      const perFile = await Promise.all(
        geoip.map(async (f) => {
          try {
            return (await api.getGeoTags(f.path)) ?? [];
          } catch {
            return []; // файл мог исчезнуть между списком и разбором
          }
        }),
      );
      options = aggregateGeoIPTags(perFile);
    } catch (e) {
      catalogError = e instanceof Error ? e.message : String(e);
    } finally {
      catalogLoading = false;
    }
  });

  // Галка — отражение сохранённых настроек, а не собственного состояния: и
  // отказ по бюджету, и неудавшийся PUT обязаны вернуть её к тому, что реально
  // сохранено (иначе браузерное состояние input'а расходится с настройками).
  async function toggleTag(name: string, e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    budgetError = null;
    const on = selected.includes(name);
    const next = on ? selected.filter((t) => t !== name) : [...selected, name];
    const nextTotal = sumSelectedTags(next, options);
    if (!on && nextTotal > BYPASS_SET_MAX_ELEM) {
      budgetError = `Не помещается в набор: ${nextTotal.toLocaleString('ru-RU')} записей при пределе ${BYPASS_SET_MAX_ELEM.toLocaleString('ru-RU')}. Уберите часть тегов.`;
      input.checked = on;
      return;
    }
    await onPatch({ bypassGeoipTags: next });
    // tick() — чтобы сверяться с уже долетевшими до пропса настройками:
    // mergeAndSaveSettings перечитывает стор, а до cfg это доезжает
    // следующим циклом обновления.
    await tick();
    input.checked = selected.includes(name);
  }

  async function install(what: 'ipset' | 'conntrack') {
    installing = what;
    try {
      const fresh =
        what === 'ipset'
          ? await api.singboxRouterBypassSetInstallDeps()
          : await api.singboxRouterBypassSetInstallConntrack();
      bypassSetStatus.applyMutationResponse(fresh);
    } catch (e) {
      const pkg = what === 'ipset' ? 'ipset' : 'conntrack-tools';
      notifications.error(`Не удалось установить ${pkg}: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      installing = null;
    }
  }
</script>

<div class="geo">
  <div class="geo-cap">Обход по geoip</div>

  {#if catalogLoading}
    <p class="hint">Читаем теги geoip-файлов…</p>
  {:else if catalogError}
    <p class="hint warn">Не удалось прочитать гео-данные: {catalogError}</p>
  {:else if geoFileCount === 0}
    <p class="hint">
      Нет подключённых файлов <code class="mono">geoip.dat</code> — добавьте их в
      <a class="geo-link" href="/routing?tab=geodata">Маршрутизация → Гео-данные</a>.
    </p>
  {:else}
    <input
      class="inp"
      type="search"
      placeholder="Поиск тега…"
      aria-label="Поиск geoip-тега"
      bind:value={query}
    />
    <div class="tag-list">
      {#each filtered as opt (opt.name)}
        <label class="tag-row">
          <input
            type="checkbox"
            checked={selected.includes(opt.name)}
            onchange={(e) => void toggleTag(opt.name, e)}
          />
          <span class="tag-name">{opt.name}</span>
          <span class="tag-count">{opt.count.toLocaleString('ru-RU')}</span>
        </label>
      {:else}
        <p class="hint">Ничего не найдено.</p>
      {/each}
    </div>

    <div class="budget" class:warn={overBudget}>
      выбрано ~{total.toLocaleString('ru-RU')} из {BYPASS_SET_MAX_ELEM.toLocaleString('ru-RU')}
    </div>
    {#if overBudget}
      <p class="hint warn">
        Выбор больше предела набора — сохранение заблокировано, снимите часть тегов.
      </p>
    {/if}
    {#if budgetError}
      <p class="hint warn">{budgetError}</p>
    {/if}

    <p class="hint">
      Обход полный, включая DNS: клиенты с DNS-серверами из выбранных диапазонов не будут
      обслуживаться DNS-перехватом. Изменения действуют на новые соединения.
    </p>

    {#if selected.length > 0}
      <div class="stat-line">
        <span class="stat-label">Записей в наборе обхода</span>
        <span class="stat-value">{entryCountLabel}</span>
      </div>
      <div class="stat-line">
        <span class="stat-label">Последнее наполнение</span>
        <span class="stat-value">
          {status?.lastPopulate ? formatRelativeTime(status.lastPopulate) : '—'}
        </span>
      </div>
      {#if status?.lastError}
        <p class="hint warn">Ошибка наполнения: {status.lastError}</p>
      {/if}
      {#if status?.missingTags?.length}
        <p class="hint warn">Теги не найдены в геоданных: {status.missingTags.join(', ')}</p>
      {/if}

      {#if status && !status.available}
        <p class="hint warn">
          Требуется пакет <code class="mono">ipset</code> — без него набор обхода не собрать.
        </p>
        <Button
          variant="ghost"
          size="sm"
          fullWidth
          disabled={installBusy}
          loading={installing === 'ipset'}
          onclick={() => void install('ipset')}
        >
          {installing === 'ipset' ? 'Установка…' : 'Установить ipset'}
        </Button>
      {/if}

      {#if status && !status.conntrackAvailable}
        <p class="hint">
          Без <code class="mono">conntrack-tools</code> уже установленные соединения доживут
          по старому пути до истечения таймаута.
        </p>
        <Button
          variant="ghost"
          size="sm"
          fullWidth
          disabled={installBusy}
          loading={installing === 'conntrack'}
          onclick={() => void install('conntrack')}
        >
          {installing === 'conntrack' ? 'Установка…' : 'Установить conntrack-tools'}
        </Button>
      {/if}
    {/if}
  {/if}
</div>

<style>
  /* Стили повторяют .hint/.inp/.stat-line дровера: scoped-CSS родителя не
     проникает в дочерний компонент (как в PolicyTunCard). */
  .geo { display: flex; flex-direction: column; gap: 8px; }
  .geo-cap {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .hint { margin: 0; font-size: 11.5px; color: var(--text-muted); line-height: 1.4; }
  .hint.warn { color: var(--color-warning, #dab856); }
  .geo-link { color: var(--accent); text-decoration: none; }
  .geo-link:hover { text-decoration: underline; }
  .inp {
    padding: 6px 10px;
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    border: 1px solid var(--border);
    color: var(--text-primary);
    font-size: 12.5px;
    font-family: inherit;
    min-width: 0;
  }
  .tag-list {
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 220px;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    padding: 4px;
  }
  .tag-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 6px;
    border-radius: var(--radius-sm);
    cursor: pointer;
    font-size: 12.5px;
    min-width: 0;
  }
  .tag-row:hover { background: var(--bg-secondary); }
  .tag-name {
    flex: 1;
    min-width: 0;
    color: var(--text-primary);
    font-family: var(--font-mono);
    font-size: 12px;
    word-break: break-all;
  }
  .tag-count {
    flex-shrink: 0;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .budget { font-size: 11.5px; color: var(--text-secondary); }
  .budget.warn { color: var(--color-error, #dc2626); font-weight: 600; }
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
  code.mono {
    font-family: var(--font-mono);
    font-size: 10.5px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 0 3px;
    color: var(--text-secondary);
  }
</style>
