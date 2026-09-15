<!--
  Вкладка «Inbounds» страницы FakeIP — зеркало ВСЕХ inbound'ов merged-конфига
  (GET /api/singbox/inbounds), сгруппированных по источнику, а не только
  «tun-in + device-proxy» как раньше.

  Группы:

  - «Движок» — tun-in (READ-ONLY, карточка TunInboundCard): единственный вход
    движка fakeip-tun. Интерфейс/адрес/стек/MTU/DNS — факты из бэкенда:
      • interface  — status.fakeipIface (e.g. «opkgtun10»);
      • address    — status.fakeipTunAddr (адрес tun-шлюза, e.g. «172.18.0.1»);
      • стек · MTU — settings.fakeipStack · settings.fakeipMtu;
      • DNS клиентам — status.fakeipDns (адрес для ручной настройки клиентов).
    Прочие engine-inbound'ы (tproxy/redirect чужого режима, если вдруг активны)
    показываются read-only строками.

  - «Прокси устройств» (EDITABLE) = инстансы device-proxy. ПЕРЕИСПОЛЬЗУЕМ фичу
    device-proxy целиком (карточки с редактированием/удалением/toggle):
      • api.listDeviceProxyInstances / getDeviceProxyListenChoices /
        getDeviceProxyInstanceRuntime / saveDeviceProxyInstance;
      • InboundSettingsDrawer (sb-router) — модал правки;
      • newDeviceProxyInstance / deleteDeviceProxyInstanceWithNotice (utils).
    Конфиг-инстансы видны всегда; статус-точка деградирует по runtime.alive.

  - «Подписки» / «Сводные группы» / «Туннели» / «QoS» — read-only строки
    (общий InboundsMirror с sb-router): tag, тип, 127.0.0.1:port, владелец;
    idle-записи («резерв порта» — нет route-правила / NDMS-прокси выключен /
    ProxyN не создан) помечаются muted-бейджем с title-пояснением (порт
    зарезервирован, чтобы номера портов не менялись).

  Счётчик «Входы · N» = все inbound'ы merged-конфига + выключенные инстансы
  device-proxy (карточки видны, но в слоте 30 их нет); при недоступном
  endpoint'е деградация к прежнему счёту 1 + device-proxy.

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
	import type { DeviceProxyInstance, DeviceProxyRuntime, SingboxInboundEntry } from '$lib/types';
	import type { FakeIPEngineState } from '../engineState';
	import TunInboundCard from './TunInboundCard.svelte';
	import DeviceProxyInboundCard from './DeviceProxyInboundCard.svelte';
	import InboundsMirror from '$lib/components/sb-router/InboundsMirror.svelte';
	import { inboundsPanelTotal } from '$lib/utils/singboxInbounds';

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

	// ── Зеркало всех inbound'ов merged-конфига ──────────────────
	// null = endpoint недоступен → деградация к прежнему виду
	// (tun-in + device-proxy) и прежнему счётчику.
	let allInbounds = $state<SingboxInboundEntry[] | null>(null);
	let inboundWarnings = $state<string[]>([]);

	async function loadAllInbounds(): Promise<void> {
		try {
			const res = await api.listSingboxInbounds();
			allInbounds = res.inbounds;
			inboundWarnings = res.warnings ?? [];
		} catch {
			allInbounds = null;
			inboundWarnings = [];
		}
	}

	// Прочие engine-inbound'ы, кроме fakeip-tun (он показан карточкой TunInboundCard).
	const engineExtras = $derived(
		(allInbounds ?? []).filter(
			(e) => e.source === 'engine' && !(e.slot === 'fakeip' && e.type === 'tun'),
		),
	);
	// Read-only источники: всё, кроме движка и device-proxy (у них свои карточки).
	const mirrorEntries = $derived(
		(allInbounds ?? []).filter((e) => e.source !== 'engine' && e.source !== 'deviceproxy'),
	);

	onMount(async () => {
		await Promise.all([loadDeviceProxy(), loadAllInbounds()]);
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
		void loadAllInbounds();
	}

	// ── Toggle enabled (persist via saveDeviceProxyInstance) ────
	let togglingId = $state<string | null>(null);

	async function toggleInstance(in_: DeviceProxyInstance, next: boolean): Promise<void> {
		if (togglingId) return;
		togglingId = in_.id;
		try {
			await api.saveDeviceProxyInstance({ ...in_, enabled: next });
			await loadDeviceProxy();
			await loadAllInbounds();
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
			await loadAllInbounds();
		} catch (e) {
			notifications.error(`Не удалось удалить: ${e instanceof Error ? e.message : String(e)}`);
		}
	}

	// Полный счёт видимых элементов: inbound'ы merged-конфига плюс
	// выключенные инстансы device-proxy (их нет в слоте 30, но карточки
	// рендерятся). Фолбэк — прежний счёт «tun-in + device-proxy», когда
	// endpoint недоступен.
	const inboundsTotal = $derived(
		allInbounds === null
			? 1 + instances.length
			: inboundsPanelTotal(
					allInbounds,
					instances.map((in_) => in_.id),
				),
	);
</script>

<section class="inbounds-tab">
	<div class="sectlbl">
		<span class="sect-name">Входы · {inboundsTotal}</span>
		<button type="button" class="add" onclick={() => void addInbound()}>
			<Plus size={13} aria-hidden="true" /> SOCKS/HTTP inbound
		</button>
	</div>

	{#if inboundWarnings.length > 0}
		<p class="warn-note">Не удалось прочитать: {inboundWarnings.join('; ')}</p>
	{/if}

	<div class="grp-h">Движок</div>
	<div class="icards">
		<TunInboundCard
			iface={$status?.fakeipIface}
			address={$status?.fakeipTunAddr}
			{tunDns}
			fakeipStack={$settings?.fakeipStack ?? ''}
			fakeipMtu={$settings?.fakeipMtu}
			{live}
		/>
	</div>
	{#if engineExtras.length > 0}
		<InboundsMirror entries={engineExtras} showGroupHeaders={false} />
	{/if}

	<div class="grp-h">Прокси устройств</div>
	{#if instances.length > 0}
		<div class="icards">
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
	{/if}

	{#if loadError}
		<p class="load-error">Не удалось загрузить inbound'ы: {loadError}</p>
	{:else if loaded && instances.length === 0}
		<p class="empty-note">
			SOCKS/HTTP-входов нет. «+ SOCKS/HTTP inbound» — локальный прокси для устройств с
			ручной настройкой.
		</p>
	{/if}

	{#if mirrorEntries.length > 0}
		<InboundsMirror entries={mirrorEntries} />
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

	.warn-note {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-warning, #d97706);
	}

	/* Заголовок группы источника — в стиле sectlbl, но строкой над блоком */
	.grp-h {
		font-size: 0.6875rem;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--text-muted);
		font-weight: 600;
	}

	.empty-note {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
		line-height: 1.5;
		text-wrap: pretty;
	}
</style>
