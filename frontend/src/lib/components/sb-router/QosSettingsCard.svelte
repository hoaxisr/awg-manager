<!--
  QoS-маршрутизация (DSCP) — issue #371. Секция настроек движка sing-box
  (StatusDrawer, expert): таблица классов DSCP → outbound. Сохранение идёт
  через тот же auto-save пайплайн, что и остальные настройки дровера
  (onPatch → applyPatch → mergeAndSaveSettings → PUT /singbox/router/settings).

  Семантика: работает в режимах TProxy и «Политики + tun» (DSCP-перехват —
  единственный netfilter policy-tun), но не в fakeip-tun; трафик с меткой
  DSCP N уходит в outbound класса, минуя остальные правила маршрутизации.
-->
<script lang="ts">
  import { Plus, Trash2, TriangleAlert } from 'lucide-svelte';
  import { Toggle, Button, Dropdown, IconButton, type DropdownOption } from '$lib/components/ui';
  import { notifications } from '$lib/stores/notifications';
  import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
  import type { SingboxQosClass, SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';
  import IssueRow from './IssueRow.svelte';
  import QosHelpModal from './QosHelpModal.svelte';
  import {
    QOS_MAX_CLASSES,
    QOS_DSCP_MIN,
    QOS_DSCP_MAX,
    QOS_NAME_MAX,
    clampDscp,
    normalizeQosClasses,
    addQosClass,
    isDscpTaken,
    updateQosClass,
    removeQosClass,
    resolveOutboundOptions,
    createSaveQueue,
    isQosLockedByMode,
  } from './qosClasses';

  interface Props {
    cfg: SingboxRouterSettings;
    status: SingboxRouterStatus | null;
    outboundOptions: OutboundGroup[];
    /** Может вернуть Promise — карточка ждёт его для сериализации PUT-ов. */
    onPatch: (patch: Partial<SingboxRouterSettings>) => void | Promise<void>;
  }
  let { cfg, status, outboundOptions, onPatch }: Props = $props();

  // Оптимистичный черновик поверх cfg: коммиты применяются к нему сразу
  // (иначе коммит, сделанный пока предыдущий PUT в полёте, считался бы от
  // устаревшего cfg и потерял бы прошлое изменение). Сбрасывается, когда
  // очередь сохранений опустела: после успеха cfg уже перезагружен и равен
  // черновику; после ошибки cfg не изменился — строки откатываются к
  // сохранённой правде (значения инпутов перерисуются из cfg).
  let draft = $state<SingboxQosClass[] | null>(null);

  // mock-api / легаси-payload без qosClasses → пустой список (undefined-safe).
  const classes = $derived(draft ?? normalizeQosClasses(cfg.qosClasses));
  // Классы применимы в TProxy и «Политики + tun»; в fakeip-tun netfilter-перехвата
  // нет. Предикат общий с EngineQosCard (он гейтит им прайминг каталога
  // outbound'ов): разъехавшись, они дали бы пустой дропдаун класса.
  const locked = $derived(isQosLockedByMode(cfg.routingMode));
  // Строго false: undefined (мок/старый бэкенд) — неизвестно, баннер не показываем.
  const xtDscpMissing = $derived(status?.xtDscpAvailable === false);
  const atCap = $derived(classes.length >= QOS_MAX_CLASSES);

  // Тот же формат опций, что у RuleEditModal: группы из
  // singboxRouter.options (buildOutboundOptions) → плоский DropdownOption[].
  const outboundDropdownOptions = $derived<DropdownOption[]>(
    outboundOptions.flatMap((g) =>
      g.items.map((i) => ({ value: i.value, label: i.label, group: g.group })),
    ),
  );
  // Новый класс по умолчанию направляем в первый туннель (не direct):
  // класс с outbound=direct не имеет смысла для «направить в туннель».
  const defaultOutbound = $derived(
    outboundDropdownOptions.find((o) => o.value !== 'direct')?.value
      ?? outboundDropdownOptions[0]?.value
      ?? 'direct',
  );

  let helpOpen = $state(false);

  // Один PUT в полёте, следующие коммиты коалесцируются (последний снапшот
  // побеждает — он полный список, собранный поверх предыдущих). Когда
  // очередь опустела — сбрасываем черновик: UI ресинкается со стором.
  const enqueueSave = createSaveQueue<SingboxQosClass[]>(
    (next) => onPatch({ qosClasses: next }),
    () => {
      draft = null;
    },
  );

  function commit(next: SingboxQosClass[]) {
    draft = next;
    enqueueSave(next);
  }

  function handleAdd() {
    const next = addQosClass(classes, defaultOutbound);
    if (!next) return;
    commit(next);
  }

  // DSCP/название коммитятся только на blur (Enter → blur → коммит):
  // событие change у number-инпута стреляет на каждый клик по стрелкам —
  // это устраивало PUT-шторм с промежуточными значениями.
  function handleDscpCommit(e: Event, idx: number) {
    const input = e.currentTarget as HTMLInputElement;
    const parsed = clampDscp(Number(input.value));
    if (isDscpTaken(classes, idx, parsed)) {
      notifications.error(`DSCP ${parsed} уже используется другим классом`);
      input.value = String(classes[idx].dscp);
      return;
    }
    input.value = String(parsed);
    if (parsed === classes[idx].dscp) return;
    commit(updateQosClass(classes, idx, { dscp: parsed }));
  }

  function handleNameCommit(e: Event, idx: number) {
    const input = e.currentTarget as HTMLInputElement;
    const name = input.value.slice(0, QOS_NAME_MAX);
    input.value = name;
    if (name === classes[idx].name) return;
    commit(updateQosClass(classes, idx, { name }));
  }

  function blurOnEnter(e: KeyboardEvent) {
    if (e.key === 'Enter') (e.currentTarget as HTMLInputElement).blur();
  }

  function handleOutboundChange(idx: number, outbound: string) {
    if (outbound === classes[idx].outbound) return;
    commit(updateQosClass(classes, idx, { outbound }));
  }

  function handleToggle(idx: number, enabled: boolean) {
    commit(updateQosClass(classes, idx, { enabled }));
  }

  function handleRemove(idx: number) {
    commit(removeQosClass(classes, idx));
  }
</script>

<section class="sec">
  <!-- Подпись секции, а не карточки: на странице «Движок» название «QoS ·
       приоритеты» несёт шапка карточки (EngineQosCard), и «QoS» дважды подряд
       читалось бы как два разных блока. -->
  <div class="sec-cap">Классы DSCP</div>

  <p class="hint">
    Трафик с меткой DSCP направляется в выбранный туннель. Метку на трафик программ
    ставит клиентское устройство (например, политика QoS в Windows).
  </p>

  {#if locked}
    <!-- Строка вместо таблицы: почему именно недоступно, а не голое
         «недоступно». Классы ставит netfilter-цепочка (DSCP-перехват), а в
         fakeip-tun netfilter не участвует вовсе — трафик заходит в tun. -->
    <p class="hint locked-hint">
      Недоступно в режиме FakeIP: классы DSCP ставит netfilter-цепочка, а перехват
      здесь идёт мимо netfilter — через tun-интерфейс. Настроенные классы сохранятся и
      заработают после возврата на TPROXY или «Политики + tun».
    </p>
  {:else}
    {#if xtDscpMissing}
      <IssueRow tone="warning" text="Модуль ядра xt_dscp недоступен — правила DSCP не будут применены" />
    {/if}

    {#if classes.length > 0}
      <div class="qos-list">
        <div class="qos-grid qos-head">
          <span>DSCP</span>
          <span>Название</span>
          <span class="col-center">Вкл</span>
          <span></span>
        </div>
        {#each classes as cls, idx (cls.dscp)}
          {@const outboundView = resolveOutboundOptions(outboundDropdownOptions, cls.outbound)}
          <div class="qos-row" class:row-off={!cls.enabled}>
            <div class="qos-grid">
              <input
                class="inp dscp-inp"
                type="number"
                min={QOS_DSCP_MIN}
                max={QOS_DSCP_MAX}
                step="1"
                value={cls.dscp}
                aria-label="DSCP класса {idx + 1}"
                onblur={(e) => handleDscpCommit(e, idx)}
                onkeydown={blurOnEnter}
              />
              <input
                class="inp"
                type="text"
                maxlength={QOS_NAME_MAX}
                placeholder="Название"
                value={cls.name}
                aria-label="Название класса {idx + 1}"
                onblur={(e) => handleNameCommit(e, idx)}
                onkeydown={blurOnEnter}
              />
              <span class="col-center">
                <Toggle
                  size="sm"
                  checked={cls.enabled}
                  ariaLabel="Включить класс DSCP {cls.dscp}"
                  onchange={(v) => handleToggle(idx, v)}
                />
              </span>
              <IconButton
                variant="danger"
                size="sm"
                ariaLabel="Удалить класс {cls.name || cls.dscp}"
                title="Удалить класс"
                onclick={() => handleRemove(idx)}
              >
                <Trash2 size={14} />
              </IconButton>
            </div>
            <Dropdown
              value={cls.outbound}
              options={outboundView.options}
              placeholder="— outbound —"
              fullWidth
              onchange={(v) => handleOutboundChange(idx, String(v))}
            />
            {#if outboundView.missing}
              <p class="qos-warn">
                <TriangleAlert size={12} aria-hidden="true" />
                outbound не найден — выберите другой
              </p>
            {/if}
          </div>
        {/each}
      </div>
    {:else}
      <p class="hint">
        Классы не настроены. Добавьте класс, чтобы направлять помеченный трафик
        в отдельный туннель.
      </p>
    {/if}

    <Button variant="ghost" size="sm" fullWidth disabled={atCap} onclick={handleAdd}>
      {#snippet iconBefore()}
        <Plus size={14} aria-hidden="true" />
      {/snippet}
      Добавить класс
    </Button>
    {#if atCap}
      <p class="hint">Достигнут максимум — {QOS_MAX_CLASSES} классов.</p>
    {/if}

    <p class="hint">
      DSCP: 0–63. Типичные метки: 46 (EF — голос/игры), 32 (CS4 — видео),
      8 (CS1 — фоновая закачка).
    </p>

    <button type="button" class="link-btn" onclick={() => (helpOpen = true)}>
      Как пометить трафик на ПК →
    </button>
  {/if}
</section>

<QosHelpModal open={helpOpen} {classes} onclose={() => (helpOpen = false)} />

<style>
  /* Стили секции повторяют .sec/.sec-cap/.hint дровера (scoped-стили
     родителя не проникают в дочерний компонент — как в TrafficSourceSettings). */
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
  .locked-hint { color: var(--text-secondary); }

  .qos-list { display: flex; flex-direction: column; gap: 8px; }
  .qos-grid {
    display: grid;
    grid-template-columns: 56px minmax(0, 1fr) auto auto;
    gap: 6px;
    align-items: center;
  }
  .qos-head {
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    padding: 0 2px;
  }
  .col-center { display: inline-flex; justify-content: center; }
  .qos-row {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
  }
  .qos-row.row-off { opacity: 0.65; }
  .inp {
    min-width: 0;
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    border: 1px solid var(--border);
    color: var(--text-primary);
    font-size: 12.5px;
    font-family: inherit;
  }
  .dscp-inp { font-family: var(--font-mono); font-size: 12px; }

  .qos-warn {
    margin: 0;
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 11px;
    color: var(--warning);
    line-height: 1.4;
  }

  .link-btn {
    align-self: flex-start;
    padding: 0;
    border: 0;
    background: none;
    cursor: pointer;
    font-family: inherit;
    font-size: 11.5px;
    font-weight: 600;
    color: var(--accent);
  }
  .link-btn:hover { text-decoration: underline; }
</style>
