<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import InstanceBar from '../freeturn/InstanceBar.svelte';
	import ProcessAlerts from './ProcessAlerts.svelte';
	import WdttClientSimple from './WdttClientSimple.svelte';
	import { parseLocalListenPort, patchWgConfEndpoint } from '$lib/utils/serverPeerOptions';
	import type {
		WdttClientConfig,
		WdttClientInstance,
		WdttConfig,
		WdttImportPayload,
		WdttStatus
	} from '$lib/types';

	let loading = $state(true);
	let saving = $state(false);
	let importing = $state(false);
	let installing = $state(false);

	let config = $state<WdttConfig | null>(null);
	let status = $state<WdttStatus | null>(null);
	let savedConfig = $state<WdttConfig | null>(null);
	let selectedClientId = $state('default');

	let statusPoll: ReturnType<typeof setInterval> | undefined;
	let wgEnsureSettled = $state(new Set<string>());
	let ensuringWg = $state(false);
	let refreshingSub = $state(false);
	let subscriptionTick = $state(0);

	const selectedClient = $derived(
		config
			? (config.clients.find((c: WdttClientInstance) => c.id === selectedClientId) ??
					config.clients[0] ??
					null)
			: null
	);
	const savedClient = $derived(
		savedConfig
			? (savedConfig.clients.find((c: WdttClientInstance) => c.id === selectedClientId) ??
					savedConfig.clients[0] ??
					null)
			: null
	);
	const clientStatus = $derived(
		status
			? (status.clients.find((c) => c.id === selectedClientId)?.status ?? status.client)
			: undefined
	);

	const clientBarItems = $derived(
		(config ? config.clients : []).map((c: WdttClientInstance) => {
			const st = status?.clients.find((s) => s.id === c.id)?.status ?? status?.client;
			return {
				id: c.id,
				name: c.name,
				running: st?.running,
				startedAt: st?.startedAt,
				pid: st?.pid,
				dtlsConnections: st?.dtlsConnections,
				binaryPresent: st?.binaryPresent
			};
		})
	);

	function errText(e: unknown): string {
		return e instanceof Error ? e.message : String(e ?? '');
	}

	function wdttTunnelName(profileName?: string): string {
		const base = profileName?.trim() || 'WDTT';
		if (base.toLowerCase().endsWith(' wdtt')) return base.slice(0, 60);
		return `${base} wdtt`.slice(0, 60);
	}

	function normalizeClient(c: WdttClientConfig): WdttClientConfig {
		return {
			...c,
			peer: c.peer ?? '',
			password: c.password ?? '',
			vkHashes: c.vkHashes ?? '',
			listen: c.listen || '127.0.0.1:9000',
			workers: c.workers > 0 ? c.workers : 24,
			obfs: c.obfs || 'audio',
			fingerprint: c.fingerprint || 'chrome',
			captchaMode: c.captchaMode || 'rjs',
			deviceId: c.deviceId ?? '',
			sub: c.sub ?? '',
			debug: !!c.debug
		};
	}

	function normalizeConfig(cfg: WdttConfig): WdttConfig {
		return {
			...cfg,
			clients: (cfg.clients ?? []).map((c) => ({
				...c,
				config: normalizeClient(c.config)
			}))
		};
	}

	async function loadConfig() {
		const norm = normalizeConfig(await api.getWdttConfig());
		savedConfig = structuredClone(norm);
		config = norm;
		if (!norm.clients.some((c) => c.id === selectedClientId)) {
			selectedClientId = norm.clients[0]?.id ?? 'default';
		}
	}

	async function loadStatus() {
		try {
			status = await api.getWdttStatus();
			await maybeEnsureWgFromLog();
		} catch {
			// polling — молча
		}
	}

	async function maybeEnsureWgFromLog() {
		if (!status || ensuringWg) return;
		const id = selectedClientId;
		if (wgEnsureSettled.has(id)) return;
		const st = status.clients.find((c) => c.id === id)?.status ?? status.client;
		if (!st?.running) return;
		const wg = st.wgConfig?.trim();
		if (!wg) return;
		ensuringWg = true;
		try {
			const result = await api.ensureWdttWgTunnel(id);
			if (result.created) {
				wgEnsureSettled.add(id);
				notifications.success(result.message ?? `Создан туннель «${result.tunnelName}»`);
			} else if (result.tunnelId) {
				wgEnsureSettled.add(id);
			}
		} catch (e) {
			notifications.error('AWG из лога: ' + errText(e));
		} finally {
			ensuringWg = false;
		}
	}

	async function ensureWgManual() {
		if (!selectedClient) return;
		ensuringWg = true;
		try {
			const result = await api.ensureWdttWgTunnel(selectedClient.id);
			if (result.created) {
				wgEnsureSettled.add(selectedClient.id);
				notifications.success(result.message ?? `Создан туннель «${result.tunnelName}»`);
			} else if (result.tunnelId) {
				wgEnsureSettled.add(selectedClient.id);
				notifications.info(result.message ?? 'Туннель уже привязан');
			} else {
				notifications.info(result.message ?? 'Конфиг WireGuard ещё не получен');
			}
		} catch (e) {
			notifications.error('AWG из лога: ' + errText(e));
		} finally {
			ensuringWg = false;
		}
	}

	onMount(async () => {
		try {
			await Promise.all([loadConfig(), loadStatus()]);
		} catch (e) {
			notifications.error('WDTT: ' + errText(e));
		} finally {
			loading = false;
		}
		statusPoll = setInterval(loadStatus, 1000);
	});

	onDestroy(() => {
		if (statusPoll) clearInterval(statusPoll);
	});

	function patchClientInConfig(id: string, cfg: WdttClientConfig) {
		if (!config || !savedConfig) return;
		const idx = config.clients.findIndex((c) => c.id === id);
		const sidx = savedConfig.clients.findIndex((c) => c.id === id);
		if (idx >= 0) config.clients[idx].config = structuredClone(cfg);
		if (sidx >= 0) savedConfig.clients[sidx].config = structuredClone(cfg);
	}

	function peersEqual(a: string, b: string): boolean {
		const norm = (p: string) => {
			const t = p.trim();
			return t.includes(':') ? t : `${t}:56000`;
		};
		return norm(a) === norm(b);
	}

	async function saveClientConfig(cfg: WdttClientConfig, opts?: { silent?: boolean }) {
		if (!selectedClient) return;
		saving = true;
		try {
			const id = selectedClient.id;
			const oldPeer = savedClient?.config.peer ?? '';
			const result =
				id === 'default'
					? await api.updateWdttClientConfig(cfg)
					: await api.updateWdttClientInstance(id, cfg);
			const norm = normalizeClient(result.config);
			if (!peersEqual(oldPeer, norm.peer)) {
				wgEnsureSettled.delete(id);
			}
			if (config && savedConfig) {
				const sidx = savedConfig.clients.findIndex((c) => c.id === id);
				if (sidx >= 0) savedConfig.clients[sidx].config = structuredClone(norm);
				const idx = config.clients.findIndex((c) => c.id === id);
				if (idx >= 0) {
					config.clients[idx].config = structuredClone(norm);
				}
			}
			if (!opts?.silent) {
				let msg = 'Настройки WDTT-клиента сохранены';
				const n = result.deletedTunnels?.length ?? 0;
				if (n > 0) {
					msg += ` · AWG-туннелей удалено: ${n}`;
					if (clientStatus?.running) msg += ' — перезапустите WDTT-клиент';
				}
				if (result.tunnelErrors?.length) {
					notifications.error('Сохранено, но часть туннелей не удалось убрать: ' + result.tunnelErrors.join('; '));
				} else {
					notifications.success(msg);
				}
			}
		} catch (e) {
			notifications.error('Не удалось сохранить: ' + errText(e));
			throw e;
		} finally {
			saving = false;
		}
	}

	function revertClient() {
		if (!config || !savedClient) return;
		patchClientInConfig(selectedClientId, $state.snapshot(savedClient.config));
	}

	async function toggleClientInstance(id: string, on: boolean) {
		try {
			if (on) {
				wgEnsureSettled.delete(id);
				await api.startWdttClientInstance(id);
				notifications.success('WDTT клиент запущен');
			} else {
				await api.stopWdttClientInstance(id);
				notifications.success('WDTT клиент остановлен');
			}
		} catch (e) {
			notifications.error(errText(e) || 'Не удалось переключить клиент');
		} finally {
			await loadStatus();
		}
	}

	async function addClient() {
		try {
			const inst = await api.createWdttClient();
			await loadConfig();
			selectedClientId = inst.id;
			notifications.success(`Клиент «${inst.name}» создан`);
		} catch (e) {
			notifications.error('Не удалось создать клиент: ' + errText(e));
		}
	}

	async function deleteClient(id: string) {
		const inst = config?.clients.find((c) => c.id === id);
		const name = inst?.name ?? id;
		const listen = inst?.config.listen?.trim();
		const confirmMsg = listen
			? `Удалить клиент «${name}»?\n\nБудут удалены настройки и AWG-туннели, созданные при импорте wdtt/qwdtt для этого клиента.`
			: `Удалить клиент «${name}»?\n\nБудут удалены настройки и связанные AWG-туннели.`;
		if (!confirm(confirmMsg)) return;
		try {
			const result = await api.deleteWdttClient(id);
			await loadConfig();
			await loadStatus();
			let msg = 'Клиент удалён';
			const n = result.deletedTunnels?.length ?? 0;
			if (n > 0) msg += `, AWG-туннелей удалено: ${n}`;
			if (result.tunnelErrors?.length) {
				notifications.error('Клиент удалён, но часть туннелей не удалось убрать: ' + result.tunnelErrors.join('; '));
			} else {
				notifications.success(msg);
			}
		} catch (e) {
			notifications.error('Не удалось удалить: ' + errText(e));
		}
	}

	async function renameClient(id: string, name: string) {
		try {
			await api.renameWdttClient(id, name);
			await loadConfig();
		} catch (e) {
			notifications.error('Не удалось переименовать: ' + errText(e));
		}
	}

	async function install() {
		installing = true;
		try {
			await api.installWdttClient();
			notifications.success('wdtt-client установлен');
		} catch (e) {
			notifications.error('Не удалось установить wdtt-client: ' + errText(e));
		} finally {
			installing = false;
			await loadStatus();
		}
	}

	async function applyImportPayload(
		payload: WdttImportPayload,
		meta?: { subUrl?: string; clientName?: string }
	) {
		if (!selectedClient || !config) return;
		importing = true;
		try {
			const c = selectedClient.config;
			const oldPeer = savedClient?.config.peer ?? '';
			if (payload.peer) c.peer = payload.peer;
			if (payload.password) c.password = payload.password;
			if (payload.vkHashes?.length) c.vkHashes = payload.vkHashes.join(',');
			if (payload.workers && payload.workers > 0) c.workers = payload.workers;
			const subUrl = payload.subUrl || meta?.subUrl;
			// Подписка задаёт listenPort=9000 для всех стран — порт у каждого клиента свой.
			if (payload.listen && !subUrl) c.listen = payload.listen;
			if (subUrl) c.sub = subUrl;
			if (payload.deviceId) c.deviceId = payload.deviceId;

			const clientName = meta?.clientName?.trim();
			if (clientName && clientName !== selectedClient.name) {
				await api.renameWdttClient(selectedClientId, clientName);
			}

			if (payload.peer && !peersEqual(oldPeer, payload.peer) && clientStatus?.running) {
				await api.stopWdttClientInstance(selectedClientId);
				await loadStatus();
			}

			let msg = 'Профиль импортирован';
			if (subUrl) msg += ' (URL подписки сохранён)';

			const wg = payload.wg?.trim();
			if (wg) {
				try {
					const listenPort = parseLocalListenPort(c.listen) ?? parseLocalListenPort(payload.listen) ?? 9000;
					const wgForImport = patchWgConfEndpoint(wg, listenPort);
					const tunnel = await api.importConfig(
						wgForImport,
						wdttTunnelName(meta?.clientName || payload.name),
						undefined,
						undefined,
						selectedClientId
					);
					msg += `. Создан туннель «${tunnel.name}» (Endpoint 127.0.0.1:${listenPort})`;
				} catch (e) {
					notifications.error('Поля заполнены, но не удалось создать туннель: ' + errText(e));
				}
			}
			if (payload.peer && !peersEqual(oldPeer, payload.peer)) {
				wgEnsureSettled.delete(selectedClientId);
			}
			await saveClientConfig(c, { silent: true });
			await loadConfig();
			notifications.success(msg);
		} catch (e) {
			notifications.error('Не удалось импортировать: ' + errText(e));
			throw e;
		} finally {
			importing = false;
		}
	}

	async function refreshFromSubscription() {
		if (!selectedClient) return;
		refreshingSub = true;
		try {
			const result = await api.refreshWdttSubscription(selectedClient.id);
			await loadConfig();
			wgEnsureSettled.delete(selectedClient.id);
			subscriptionTick++;
			notifications.success(result.message ?? 'Подписка обновлена');
			if (clientStatus?.running) {
				notifications.info('Перезапустите WDTT-клиент, если изменились пароль или VK-хеши');
			}
		} catch (e) {
			notifications.error('Обновление подписки: ' + errText(e));
		} finally {
			refreshingSub = false;
		}
	}
</script>

{#if loading || !config}
	<div class="wdtt-loading">Загрузка…</div>
{:else}
	<ProcessAlerts
		status={clientStatus}
		installAvailable={status?.installAvailable ?? false}
		installVersion={status?.installVersion}
		installedVersion={status?.installedVersion}
		updateAvailable={status?.updateAvailable ?? false}
		installing={installing || (status?.installing ?? false)}
		onInstall={install}
	/>

	<InstanceBar
		items={clientBarItems}
		selectedId={selectedClientId}
		onSelect={(id) => {
			selectedClientId = id;
			wgEnsureSettled.delete(id);
		}}
		onToggle={toggleClientInstance}
		onAdd={addClient}
		onDelete={deleteClient}
		onRename={renameClient}
	/>

	{#if selectedClient}
		{#key selectedClientId}
			<WdttClientSimple
				client={selectedClient.config}
				running={clientStatus?.running ?? false}
				status={clientStatus}
				routerClock={status?.routerClock}
				{saving}
				{importing}
				onSave={saveClientConfig}
				onRevert={revertClient}
				onToggle={(on) => toggleClientInstance(selectedClientId, on)}
				onImportPayload={applyImportPayload}
				onRefreshSubscription={refreshFromSubscription}
				refreshingSub={refreshingSub}
				subscriptionTick={subscriptionTick}
				onEnsureWg={ensureWgManual}
				ensuringWg={ensuringWg}
			/>
		{/key}
	{/if}
{/if}

<style>
	.wdtt-loading {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-secondary);
	}
</style>
