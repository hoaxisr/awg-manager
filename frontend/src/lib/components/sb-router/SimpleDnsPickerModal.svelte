<!--
  Выбор выходного DNS для простого режима: список известных провайдеров
  вместо ручного ввода адреса. Экспертный DNSServerEditModal не заменяет —
  правит только один сервер и только поля транспорта.
-->
<script lang="ts">
  import { Modal, SegmentedControl, Input, Button } from '$lib/components/ui';
  import type { SegmentedOption } from '$lib/components/ui/segmentedControl';
  import { api } from '$lib/api/client';
  import { singboxRouter } from '$lib/stores/singboxRouter';
  import type { SingboxRouterDNSServer } from '$lib/types';
  import {
    DNS_PRESETS,
    buildDnsServer,
    findDnsPresetByIp,
    protoOfDnsServer,
    udpDropsTls,
    type DnsPresetProto,
  } from './dnsPresets';

  interface Props {
    server: SingboxRouterDNSServer;
    /** Переключатель DoH/DoT/UDP. Для туннельного DNS выключен: он и так внутри туннеля. */
    allowProtocol: boolean;
    onclose: () => void;
    onsaved: () => void;
  }

  let { server, allowProtocol, onclose, onsaved }: Props = $props();

  const PROTO_OPTIONS: SegmentedOption<DnsPresetProto>[] = [
    { value: 'doh', label: 'DoH' },
    { value: 'dot', label: 'DoT' },
    { value: 'udp', label: 'UDP' },
  ];

  const CUSTOM = '__custom__';

  // svelte-ignore state_referenced_locally
  const initialPreset = findDnsPresetByIp(server.server);
  // svelte-ignore state_referenced_locally
  let proto = $state<DnsPresetProto>(protoOfDnsServer(server));
  let choice = $state(initialPreset?.id ?? CUSTOM);
  // svelte-ignore state_referenced_locally
  let customAddr = $state(initialPreset ? '' : server.server);
  let busy = $state(false);
  let error = $state('');

  const preset = $derived(DNS_PRESETS.find((p) => p.id === choice));
  const addr = $derived(preset ? preset.ip : customAddr.trim());
  // Без имени в сертификате шифрованный вариант не проверить — свой адрес только по UDP.
  const effectiveProto = $derived<DnsPresetProto>(preset && allowProtocol ? proto : 'udp');
  const tlsLoss = $derived(effectiveProto === 'udp' && udpDropsTls(server));
  const canSave = $derived(!busy && addr.length > 0);

  async function save() {
    if (!canSave) return;
    busy = true;
    error = '';
    try {
      const built = buildDnsServer(server, addr, preset?.sni ?? '', effectiveProto);
      await api.singboxRouterUpdateDNSServer(server.tag, built);
      await singboxRouter.loadAll();
      onsaved();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

<Modal open title="Выходной DNS" size="sm" {onclose} closeOnBackdrop={false}>
  {#if allowProtocol}
    <div class="proto">
      <SegmentedControl
        value={effectiveProto}
        options={PROTO_OPTIONS}
        ariaLabel="Протокол DNS"
        disabled={!preset}
        fullWidth
        onchange={(v) => (proto = v)}
      />
      {#if !preset}
        <p class="hint">Свой адрес доступен только по обычному DNS</p>
      {/if}
      {#if tlsLoss}
        <p class="warn">Настройки TLS будут удалены: обычный DNS их не поддерживает</p>
      {/if}
    </div>
  {/if}

  <div class="list">
    {#each DNS_PRESETS as p (p.id)}
      <label class="row">
        <input type="radio" name="dns-preset" value={p.id} checked={choice === p.id} onchange={() => (choice = p.id)} />
        <span class="label">{p.label}</span>
        <span class="ip">{p.ip}</span>
        <span class="note">{p.note ?? ''}</span>
      </label>
    {/each}
    <!-- Не оборачиваем в <label>: поле ввода внутри метки радиокнопки
         перехватывало бы на неё клики. -->
    <div class="row">
      <input
        id="dns-custom"
        type="radio"
        name="dns-preset"
        value={CUSTOM}
        checked={choice === CUSTOM}
        onchange={() => (choice = CUSTOM)}
      />
      <label class="label" for="dns-custom">Свой адрес</label>
      <span class="custom">
        <Input
          bind:value={customAddr}
          placeholder="192.168.1.1"
          disabled={choice !== CUSTOM}
          fullWidth
        />
      </span>
    </div>
  </div>

  {#if error}
    <p class="err">{error}</p>
  {/if}

  {#snippet actions()}
    <Button variant="ghost" onclick={onclose}>Отмена</Button>
    <Button variant="primary" disabled={!canSave} onclick={save}>Сохранить</Button>
  {/snippet}
</Modal>

<style>
  .proto {
    margin-bottom: 12px;
  }
  .hint,
  .warn,
  .err {
    margin: 6px 0 0;
    font-size: 11px;
    line-height: 1.35;
  }
  .hint {
    color: var(--text-muted);
  }
  .warn {
    color: var(--color-warning, #d97706);
  }
  .err {
    margin-top: 10px;
    color: var(--color-error, #dc2626);
  }
  .list {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .row {
    display: grid;
    grid-template-columns: auto minmax(6rem, max-content) minmax(0, 1fr) auto;
    align-items: center;
    gap: 10px;
    padding: 8px 6px;
    border-radius: var(--radius-sm, 6px);
    cursor: pointer;
  }
  @media (hover: hover) and (pointer: fine) {
    .row:hover {
      background: color-mix(in srgb, var(--bg-hover) 70%, transparent);
    }
  }
  .label {
    font-size: 13px;
  }
  .ip {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--text-secondary);
  }
  .note {
    font-size: 11px;
    color: var(--text-muted);
    text-align: right;
    white-space: nowrap;
  }
  .custom {
    grid-column: 3 / -1;
    min-width: 0;
  }
</style>
