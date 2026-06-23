<!--
  Вкладка «Inbounds» страницы FakeIP по мокапу `page-inbounds-v2.html`.

  Две группы карточек в адаптивной сетке:

  - tun-in (READ-ONLY) — единственный вход движка fakeip-tun. Управляется
    движком: интерфейс/адрес/стек/MTU/DNS — факты из бэкенда, без правки.
    Источники (ЧЕСТНО, без хардкода magic-IP):
      • interface  — status.fakeipIface (e.g. «opkgtun10»);
      • address    — status.fakeipTunAddr (адрес tun-шлюза, e.g. «172.18.0.1»);
      • стек · MTU — settings.fakeipStack · settings.fakeipMtu;
      • DNS клиентам — status.fakeipDns (адрес для ручной настройки клиентов).
    Существует только в fakeip-режиме (страница уже гейтит это целиком).

  - SOCKS/HTTP-входы (EDITABLE) = инстансы device-proxy. ПЕРЕИСПОЛЬЗУЕМ фичу
    device-proxy целиком, не пересобираем:
      • api.listDeviceProxyInstances / getDeviceProxyListenChoices /
        getDeviceProxyInstanceRuntime / saveDeviceProxyInstance;
      • InboundSettingsDrawer (sb-router) — модал правки (SettingsCard внутри);
      • newDeviceProxyInstance / mergeInstanceConfig (utils);
      • deleteDeviceProxyInstanceWithNotice (utils) — удаление с уведомлением.
    Конфиг-инстансы видны всегда; статус-точка деградирует по runtime.alive.

  ЧЕСТНОСТЬ по счётчику соединений: записи Clash /connections НЕ несут тег
  inbound (см. ClashConnectionsRaw — только metadata/chains/rule), поэтому
  per-card «N соединений» вывести из доступных данных НЕЛЬЗЯ — не выдумываем,
  футер показывает только статус-точку. Мокап-число «N соединения» опущено.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { Plus } from 'lucide-svelte';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import InboundSettingsDrawer from '$lib/components/sb-router/InboundSettingsDrawer.svelte';
	import { newDeviceProxyInstance } from '$lib/utils/deviceProxyInstance';
	import { deleteDeviceProxyInstanceWithNotice } from '$lib/utils/deviceProxyDeleteNotice';
	import type { DeviceProxyInstance, DeviceProxyRuntime } from '$lib/types';
	import type { FakeIPEngineState } from '../engineState';
	import TunInboundCard from './TunInboundCard.svelte';
	import DeviceProxyInboundCard from './DeviceProxyInboundCard.svelte';

	interface Props {
		/** Состояние движка — гейтит живые сигналы (status-точки). */
		engineState: FakeIPEngineState;
	}
	let { engineState }: Props = $props();

	const settings = singboxRouter.settings;
	const status = singboxRouter.status;

	// Live = движок запущен (status-точки достоверны). При stopped/clash-down
	// показываем конфиг, но точки гасим (muted).
	const live = $derived(engineState === 'live');

	interface ListenChoices {
		lanIP: string;
		bridges: { id: string; label: string; ip: string }[];
		singboxRunning: boolean;
	}

	// DNS клиентам — из status (адрес для ручной настройки в fakeip-режиме).
	const tunDns = $derived($status?.fakeipDns ?? '');

	let instances = $state<DeviceProxyInstance[]>([]);
	let runtimes = $state<Record<string, DeviceProxyRuntime>>({});
	let listenChoices = $state<ListenChoices | null>(null);
	let loadError = $state<string | null>(null);
	let loaded = $state(false);

	async function loadDeviceProxy(): Promise<void> {
		try {
			const [ins, choices] = await Promise.all([
				api.listDeviceProxyInstances(),
				api.getDeviceProxyListenChoices().catch(() => null),
			]);
			instances = ins;
			listenChoices = choices;
			const entries = await Promise.all(
				ins.map(async (in_) => {
					const rt = await api
						.getDeviceProxyInstanceRuntime(in_.id)
						.catch((): DeviceProxyRuntime => ({ alive: false, activeTag: '', defaultTag: '' }));
					return [in_.id, rt] as const;
				}),
			);
			runtimes = Object.fromEntries(entries);
			loadError = null;
		} catch (e) {
			loadError = e instanceof Error ? e.message : String(e);
		} finally {
			loaded = true;
		}
	}

	onMount(async () => {
		await loadDeviceProxy();
	});

	function runtimeFor(id: string): DeviceProxyRuntime {
		return runtimes[id] ?? { alive: false, activeTag: '', defaultTag: '' };
	}

	// Подпись listen-хоста: listenAll → lanIP/0.0.0.0, иначе IP моста.
	function listenLabel(in_: DeviceProxyInstance): string {
		let host: string;
		if (listenChoices) {
			if (in_.listenAll) host = listenChoices.lanIP || '0.0.0.0';
			else
				host =
					listenChoices.bridges.find((b) => b.id === in_.listenInterface)?.ip ||
					in_.listenInterface ||
					'auto';
		} else {
			host = in_.listenAll ? '0.0.0.0' : in_.listenInterface || 'auto';
		}
		return `${host}:${in_.port}`;
	}

	// ── Edit drawer ─────────────────────────────────────────────
	let drawerInstance = $state<DeviceProxyInstance | null>(null);
	let drawerOpen = $state(false);

	function openEdit(in_: DeviceProxyInstance): void {
		drawerInstance = in_;
		drawerOpen = true;
	}

	async function addInbound(): Promise<void> {
		let existing: DeviceProxyInstance[] = [];
		try {
			existing = await api.listDeviceProxyInstances();
		} catch {
			existing = [];
		}
		drawerInstance = newDeviceProxyInstance(existing);
		drawerOpen = true;
	}

	function onDrawerSaved(): void {
		drawerOpen = false;
		void loadDeviceProxy();
	}

	// ── Toggle enabled (persist via saveDeviceProxyInstance) ────
	let togglingId = $state<string | null>(null);

	async function toggleInstance(in_: DeviceProxyInstance, next: boolean): Promise<void> {
		if (togglingId) return;
		togglingId = in_.id;
		try {
			await api.saveDeviceProxyInstance({ ...in_, enabled: next });
			await loadDeviceProxy();
		} catch (e) {
			notifications.error(
				`Не удалось ${next ? 'включить' : 'выключить'} inbound: ${e instanceof Error ? e.message : String(e)}`,
			);
		} finally {
			togglingId = null;
		}
	}

	async function deleteInstance(in_: DeviceProxyInstance): Promise<void> {
		try {
			await deleteDeviceProxyInstanceWithNotice(in_.id, {
				successMessage: 'Inbound удалён',
				pendingApplyMessage:
					'Inbound удалён из конфига, но sing-box ещё не обновлён — изменение применится, когда сервис снова будет доступен.',
			});
			await loadDeviceProxy();
		} catch (e) {
			notifications.error(`Не удалось удалить: ${e instanceof Error ? e.message : String(e)}`);
		}
	}

	const inboundsTotal = $derived(1 + instances.length);
</script>

<section class="inbounds-tab">
	<div class="sectlbl">
		<span class="sect-name">Входы · {inboundsTotal}</span>
		<button type="button" class="add" onclick={() => void addInbound()}>
			<Plus size={13} aria-hidden="true" /> SOCKS/HTTP inbound
		</button>
	</div>

	<div class="icards">
		<TunInboundCard
			iface={$status?.fakeipIface}
			address={$status?.fakeipTunAddr}
			{tunDns}
			fakeipStack={$settings?.fakeipStack ?? 'gvisor'}
			fakeipMtu={$settings?.fakeipMtu}
			{live}
		/>

		{#each instances as in_ (in_.id)}
			<DeviceProxyInboundCard
				name={in_.name || in_.id}
				listen={listenLabel(in_)}
				authEnabled={in_.auth?.enabled ?? false}
				enabled={in_.enabled}
				alive={runtimeFor(in_.id).alive}
				{live}
				toggling={togglingId === in_.id}
				onEdit={() => openEdit(in_)}
				onToggle={(next) => void toggleInstance(in_, next)}
				onDelete={() => void deleteInstance(in_)}
			/>
		{/each}
	</div>

	{#if loadError}
		<p class="load-error">Не удалось загрузить inbound'ы: {loadError}</p>
	{:else if loaded && instances.length === 0}
		<p class="empty-note">
			SOCKS/HTTP-входов нет. «+ SOCKS/HTTP inbound» — локальный прокси для устройств с
			ручной настройкой.
		</p>
	{/if}
</section>

{#if drawerInstance}
	<InboundSettingsDrawer
		instance={drawerInstance}
		open={drawerOpen}
		onClose={() => (drawerOpen = false)}
		onSaved={onDrawerSaved}
	/>
{/if}

<style>
	.inbounds-tab {
		display: flex;
		flex-direction: column;
		gap: var(--sp-3, 0.75rem);
	}

	.sectlbl {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		font-size: 0.6875rem;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--text-muted);
	}

	.sect-name {
		font-weight: 600;
	}

	.add {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.75rem;
		letter-spacing: 0;
		text-transform: none;
		color: var(--color-accent);
		background: none;
		border: 1px solid var(--color-accent-border, var(--color-accent));
		border-radius: var(--radius-sm, 6px);
		padding: 0.25rem 0.55rem;
		cursor: pointer;
	}

	.add:hover {
		background: color-mix(in srgb, var(--color-accent) 12%, transparent);
	}

	.icards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(20rem, 1fr));
		gap: 0.875rem;
	}

	.load-error {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-error);
	}

	.empty-note {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
		line-height: 1.5;
		text-wrap: pretty;
	}
</style>
