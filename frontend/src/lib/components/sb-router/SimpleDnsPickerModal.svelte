<!--
  Выбор выходного DNS для простого режима: список известных провайдеров
  вместо ручного ввода адреса. Экспертный DNSServerEditModal не заменяет —
  правит только один сервер и только поля транспорта.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { Modal, SegmentedControl, Input, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
  import type { SegmentedOption } from '$lib/components/ui/segmentedControl';
  import { api } from '$lib/api/client';
  import { singboxRouter } from '$lib/stores/singboxRouter';
  import { awgTags } from '$lib/stores/awgTags';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import type { SingboxRouterDNSServer, MihomoNativeGroup } from '$lib/types';
  import {
    DNS_PRESETS,
    buildDnsServer,
    certPinWillReset,
    findDnsPresetByIp,
    protoOfDnsServer,
    udpDropsTls,
    type DnsPresetProto,
  } from './dnsPresets';
  import { normalizeDnsServerDetour } from '$lib/utils/dnsServerDetour';

  interface Props {
    server: SingboxRouterDNSServer;
    /** Переключатель DoH/DoT/UDP. Для туннельного DNS выключен: он и так внутри туннеля. */
    allowProtocol: boolean;
    onclose: () => void;
    onsaved: () => void;
  }

  let { server, allowProtocol, onclose, onsaved }: Props = $props();

  let mihomoGroups = $state<MihomoNativeGroup[]>([]);
  let mihomoSubscriptions = $state<import('$lib/types').MihomoNativeSubscription[]>([]);

  onMount(async () => {
    try {
      const [grps, subs] = await Promise.all([
        api.mihomoNativeGroups().catch(() => []),
        api.mihomoNativeSubscriptions().catch(() => []),
      ]);
      mihomoGroups = grps;
      mihomoSubscriptions = subs;
    } catch {}
  });

  const optionsStore = singboxRouter.options;
  const awgStore = awgTags;
  const subsStore = subscriptionsStore;

  const detourOptions = $derived.by<DropdownOption[]>(() => {
    const opts: DropdownOption[] = [{ value: '', label: 'Напрямую (без VPN)' }];

    // 1. Groups from Mihomo native
    if (mihomoGroups.length > 0) {
      for (const g of mihomoGroups) {
        opts.push({ value: g.name, label: `${g.name} (${g.type})`, group: 'Группы прокси Mihomo' });
      }
    }

    // 2. AWG / Wireguard tunnels
    const tags = $awgStore?.data ?? [];
    for (const t of tags) {
      opts.push({ value: t.tag, label: `${t.label} (${t.iface})`, group: 'AWG / WireGuard туннели' });
    }

    // 3. Mihomo Subscriptions
    if (mihomoSubscriptions.length > 0) {
      for (const s of mihomoSubscriptions) {
        opts.push({ value: s.name, label: `Подписка: ${s.name}`, group: 'Подписки' });
      }
    }

    // 4. Sing-box options store fallback
    const fromOptions = ($optionsStore ?? []).flatMap((g) =>
      g.items
        .filter((i) => i.value !== 'direct' && !opts.some((o) => o.value === i.value))
        .map((i) => ({ value: i.value, label: i.label, group: g.group })),
    );
    opts.push(...fromOptions);

    return opts;
  });

  const CUSTOM = '__custom__';

  // svelte-ignore state_referenced_locally
  const initialPreset = findDnsPresetByIp(server.server);
  let choice = $state(initialPreset?.id ?? CUSTOM);
  // svelte-ignore state_referenced_locally
  let customAddr = $state(initialPreset ? '' : server.server);
  const PROTO_OPTIONS: SegmentedOption[] = [
    { value: 'udp', label: 'UDP' },
    { value: 'dot', label: 'DoT' },
    { value: 'doh', label: 'DoH' },
  ];

  // svelte-ignore state_referenced_locally
  let proto = $state<DnsPresetProto>(protoOfDnsServer(server));
  // svelte-ignore state_referenced_locally
  let selectedDetour = $state(normalizeDnsServerDetour(server.detour) ?? '');
  let busy = $state(false);
  let error = $state('');

  const preset = $derived(DNS_PRESETS.find((p) => p.id === choice));
  const addr = $derived(preset ? preset.ip : customAddr.trim());
  const isTunnelServer = $derived(server.tag !== 'dns-direct' && server.tag !== 'dns-local');
  const canSave = $derived(!busy && addr.length > 0);

  async function save() {
    if (!canSave) return;
    busy = true;
    error = '';
    try {
      const built = buildDnsServer(server, addr, preset?.sni ?? '', choice === CUSTOM ? 'udp' : proto);
      if (isTunnelServer) {
        built.detour = selectedDetour;
      }
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
        options={PROTO_OPTIONS}
        value={proto}
        onchange={(v) => (proto = v as DnsPresetProto)}
        disabled={choice === CUSTOM}
        ariaLabel="Протокол DNS"
      />
      {#if choice === CUSTOM}
        <span class="hint">Свой адрес доступен только по обычному DNS</span>
      {:else if proto === 'udp' && udpDropsTls(server)}
        <span class="warn">Настройки TLS будут удалены</span>
      {/if}
    </div>
  {/if}

  {#if isTunnelServer}
    <div class="detour-block">
      <span class="detour-label">Куда направить DNS (Туннель / Группа)</span>
      <Dropdown
        options={detourOptions}
        bind:value={selectedDetour}
        placeholder="Выберите туннель или группу"
      />
    </div>
  {/if}

  <div class="list">
    {#each DNS_PRESETS as p (p.id)}
      <label class="row">
        <input type="radio" name="dns-preset" value={p.id} checked={choice === p.id} onchange={() => (choice = p.id)} />
        <span class="label">{p.label}</span>
        <span class="ip">{p.ip}</span>
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
  .detour-block {
    margin-bottom: 14px;
    padding-bottom: 12px;
    border-bottom: 1px solid var(--border);
  }
  .detour-label {
    display: block;
    font-size: 12px;
    font-weight: 500;
    color: var(--text-secondary);
    margin-bottom: 6px;
  }
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
    grid-template-columns: auto minmax(6rem, max-content) minmax(0, 1fr);
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
  .custom {
    grid-column: 3 / -1;
    min-width: 0;
  }
</style>
