<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import InstanceBar from '../freeturn/InstanceBar.svelte';
	import { ProcessAlerts, Tabs } from '$lib/components/ui';
	import WdttClientSimple from './WdttClientSimple.svelte';
	import WdttServerSimple from './WdttServerSimple.svelte';
	import ProxyPanelModeToggle from '../proxy-panel/ProxyPanelModeToggle.svelte';
	import { linkedTunnelListenPort, patchWgConfEndpoint } from '$lib/utils/serverPeerOptions';
	import { peersEqual } from '$lib/utils/wdttPeer';
	import { errText } from '$lib/utils/errorMessage';
	import { createSelfReschedulingPoll } from '$lib/utils/selfReschedulingPoll';
	import type {
		WdttClientConfig,
		WdttClientInstance,
		WdttConfig,
		WdttImportPayload,
		WdttServerConfig,
		WdttServerInstance,
		WdttStatus
	} from '$lib/types';

	type WdttTabId = 'client' | 'server';

	let wdttTab: WdttTabId = $state('client');
	let loading = $state(true);
	let loadError = $state('');
	let saving = $state(false);
	let importing = $state(false);
	let installing = $state(false);

	let config = $state<WdttConfig | null>(null);
	let status = $state<WdttStatus | null>(null);
	let savedConfig = $state<WdttConfig | null>(null);
	let selectedClientId = $state('default');
	let selectedServerId = $state('default');
	let genPeer = $state('');
	let genVKHashes = $state('');
	let generatedLink = $state('');
	let generatedLinkQwdtt = $state('');
	let generating = $state(false);

	// wdtt-server собирается не под все арки роутеров (нет mips/mipsel) — там вкладки нет.
	// Пока статус не загружен, показываем обе: серверный режим — частый случай.
	const serverSupported = $derived(status?.serverSupported !== false);
	const wdttTabs = $derived(
		serverSupported
			? [
					{ id: 'client', label: 'Клиент' },
					{ id: 'server', label: 'Сервер' }
				]
			: [{ id: 'client', label: 'Клиент' }]
	);
	const activeTab = $derived<WdttTabId>(serverSupported ? wdttTab : 'client');

	const statusPoll = createSelfReschedulingPoll(loadStatus);
	// Не реактивны (в шаблоне не читаются) — дедуп/кулдаун авто-ensure в поллинге.
	let wgEnsureSettled = new Set<string>();
	let rawEnsureSettled = new Set<string>();
	const wgEnsureCooldown = new Map<string, number>();
	const rawEnsureCooldown = new Map<string, number>();
	let ensuringWg = $state(false);
	let importingWgTunnel = $state(false);
	let refreshingSub = $state(false);
	let subscriptionTick = $state(0);
	/** Сбрасывает локальный UI импорта/подписки после удаления клиента (id «default» не меняется). */
	let clientUiEpoch = $state(0);
	let clientPanelTab = $state<'setup' | 'log'>('setup');
	let serverPanelTab = $state<'main' | 'links' | 'network' | 'log'>('main');

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
	// ?. на массивах: ответ без clients/servers (усечённый статус, мок) иначе
	// роняет вычисление, и вкладка перестаёт реагировать на переключение.
	const clientStatus = $derived(
		status
			? (status.clients?.find((c) => c.id === selectedClientId)?.status ?? status.client)
			: undefined
	);
	const selectedServer = $derived(
		config
			? (config.servers.find((s: WdttServerInstance) => s.id === selectedServerId) ??
					config.servers[0] ??
					null)
			: null
	);
	const savedServer = $derived(
		savedConfig
			? (savedConfig.servers.find((s: WdttServerInstance) => s.id === selectedServerId) ??
					savedConfig.servers[0] ??
					null)
			: null
	);
	const serverStatus = $derived(
		status
			? (status.servers?.find((s) => s.id === selectedServerId)?.status ?? status.server)
			: undefined
	);

	const clientBarItems = $derived(
		(config ? config.clients : []).map((c: WdttClientInstance) => {
			const st = status?.clients?.find((s) => s.id === c.id)?.status ?? status?.client;
			return {
				id: c.id,
				name: c.name,
				running: st?.running,
				autostart: c.config.enabled,
				startedAt: st?.startedAt,
				pid: st?.pid,
				dtlsConnections: st?.dtlsConnections,
				binaryPresent: st?.binaryPresent
			};
		})
	);

	const serverBarItems = $derived(
		(config ? config.servers : []).map((s: WdttServerInstance) => {
			const st = status?.servers?.find((x) => x.id === s.id)?.status ?? status?.server;
			return {
				id: s.id,
				name: s.name,
				running: st?.running,
				autostart: s.config.enabled,
				startedAt: st?.startedAt,
				pid: st?.pid,
				dtlsConnections: st?.dtlsConnections,
				binaryPresent: st?.binaryPresent
			};
		})
	);
	/** wdtt-server поднимает один общий интерфейс wdtt0 — второй инстанс создать нельзя. */
	const canAddWdttServer = $derived((config?.servers.length ?? 0) === 0);

	function wdttTunnelName(profileName?: string): string {
		const base = profileName?.trim() || 'WDTT';
		if (base.toLowerCase().endsWith(' wdtt')) return base.slice(0, 60);
		return `${base} wdtt`.slice(0, 60);
	}

	function normalizeClient(c: WdttClientConfig): WdttClientConfig {
		const raw = c.connMode === 'raw';
		const defaultWorkers = raw ? 24 : 24;
		const workers = Math.max(raw ? 1 : 12, c.workers > 0 ? c.workers : defaultWorkers);
		return {
			...c,
			peer: c.peer ?? '',
			password: c.password ?? '',
			vkHashes: c.vkHashes ?? '',
			listen: c.listen || '127.0.0.1:9000',
			workers,
			obfs: c.obfs || 'audio',
			fingerprint: c.fingerprint || 'chrome',
			captchaMode: c.captchaMode || 'rjs',
			deviceId: c.deviceId ?? '',
			sub: c.sub ?? '',
			connMode: c.connMode === 'raw' ? 'raw' : 'wg',
			debug: !!c.debug
		};
	}

	function normalizeServer(s: WdttServerConfig): WdttServerConfig {
		return {
			...s,
			listen: s.listen || '0.0.0.0:56002',
			wgPort: s.wgPort > 0 ? s.wgPort : 56001,
			password: s.password ?? '',
			configDir: s.configDir ?? '',
			adminId: s.adminId ?? '',
			botToken: s.botToken ?? '',
			natMode: s.natMode || 'full',
			policy: s.policy || 'none',
			lanSegments: s.lanSegments ?? [],
			ingressEnabled: !!s.ingressEnabled,
			relayMode: s.relayMode === 'raw' ? 'raw' : 'wg',
			rawListen: s.rawListen ?? '',
			debug: !!s.debug
		};
	}

	function normalizeConfig(cfg: WdttConfig): WdttConfig {
		return {
			...cfg,
			clients: (cfg.clients ?? []).map((c) => ({
				...c,
				config: normalizeClient(c.config)
			})),
			servers: (cfg.servers ?? []).map((s) => ({
				...s,
				config: normalizeServer(s.config)
			}))
		};
	}

	async function loadConfig() {
		try {
			const norm = normalizeConfig(await api.getWdttConfig());
			savedConfig = structuredClone(norm);
			config = norm;
			loadError = '';
			if (!norm.clients.some((c) => c.id === selectedClientId)) {
				selectedClientId = norm.clients[0]?.id ?? 'default';
			}
			if (!norm.servers.some((s) => s.id === selectedServerId)) {
				selectedServerId = norm.servers[0]?.id ?? 'default';
			}
			const srv = norm.servers.find((s) => s.id === selectedServerId) ?? norm.servers[0];
			if (!genPeer) genPeer = srv?.config.linkPeer ?? '';
			if (!genVKHashes) genVKHashes = srv?.config.linkVkHashes ?? '';
		} catch (e) {
			loadError = errText(e);
			notifications.error('WDTT: ' + loadError);
		}
	}

	async function loadStatus() {
		try {
			status = await api.getWdttStatus();
			await Promise.all([maybeEnsureWgFromLog(), maybeEnsureRawFromStatus()]);
		} catch {
			// polling — молча
		}
	}

	async function maybeEnsureRawFromStatus() {
		if (!status || ensuringWg) return;
		const id = selectedClientId;
		if (rawEnsureSettled.has(id)) return;
		const clientCfg = config?.clients.find((c) => c.id === id)?.config;
		if (clientCfg?.connMode !== 'raw') return;
		const st = status.clients?.find((c) => c.id === id)?.status ?? status.client;
		if (!st?.running) return;
		if (!st.rawIface?.trim() && !st.ndmsIface?.trim()) return;
		const now = Date.now();
		if (now - (rawEnsureCooldown.get(id) ?? 0) < 20000) return;
		rawEnsureCooldown.set(id, now);
		ensuringWg = true;
		try {
			const result = await api.ensureWdttRawTunnel(id);
			if (result.created) {
				rawEnsureSettled.add(id);
				notifications.success(result.message ?? `WDTT Raw «${result.tunnelName}» в AWG-туннелях`);
			} else if (result.tunnelId) {
				rawEnsureSettled.add(id);
			}
		} catch (e) {
			notifications.error('WDTT Raw в AWG: ' + errText(e));
		} finally {
			ensuringWg = false;
		}
	}

	async function maybeEnsureWgFromLog() {
		if (!status || ensuringWg) return;
		const id = selectedClientId;
		if (wgEnsureSettled.has(id)) return;
		const clientCfg = config?.clients.find((c) => c.id === id)?.config;
		if (clientCfg?.connMode === 'raw') return;
		const st = status.clients?.find((c) => c.id === id)?.status ?? status.client;
		if (!st?.running) return;
		const wg = st.wgConfig?.trim();
		if (!wg) return;
		const now = Date.now();
		// Не ретраить чаще раза в 20с: на ошибке/no-tunnel id не помечается settled,
		// иначе 1s-поллинг долбил бы POST (и тост) каждую секунду.
		if (now - (wgEnsureCooldown.get(id) ?? 0) < 20000) return;
		wgEnsureCooldown.set(id, now);
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
		wgEnsureCooldown.delete(selectedClient.id);
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
		} finally {
			loading = false;
		}
		statusPoll.start();
	});

	onDestroy(() => statusPoll.stop());

	function patchClientInConfig(id: string, cfg: WdttClientConfig) {
		if (!config || !savedConfig) return;
		const idx = config.clients.findIndex((c) => c.id === id);
		const sidx = savedConfig.clients.findIndex((c) => c.id === id);
		if (idx >= 0) config.clients[idx].config = structuredClone(cfg);
		if (sidx >= 0) savedConfig.clients[sidx].config = structuredClone(cfg);
	}

	async function saveClientConfig(cfg: WdttClientConfig, opts?: { silent?: boolean }) {
		if (!selectedClient) return;
		saving = true;
		try {
			const sent = $state.snapshot(cfg);
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
				// Перезаписываем рабочий config только если юзер не редактировал во время запроса.
				if (idx >= 0 && JSON.stringify($state.snapshot(config.clients[idx].config)) === JSON.stringify(sent)) {
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
				const inst = config?.clients.find((c) => c.id === id);
				if (inst) {
					await saveClientConfig(inst.config, { silent: true });
				}
				await api.startWdttClientInstance(id);
				notifications.success('WDTT клиент запущен');
			} else {
				await api.stopWdttClientInstance(id);
				notifications.success('WDTT клиент остановлен');
			}
		} catch (e) {
			notifications.error(errText(e) || 'Не удалось переключить клиент');
		} finally {
			await Promise.all([loadConfig(), loadStatus()]);
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
			clientUiEpoch++;
			subscriptionTick = 0;
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
			notifications.success('wdtt-client и wdtt-server установлены');
		} catch (e) {
			notifications.error('Не удалось установить wdtt: ' + errText(e));
		} finally {
			installing = false;
			await loadStatus();
		}
	}

	function patchServerInConfig(id: string, cfg: WdttServerConfig) {
		if (!config || !savedConfig) return;
		const idx = config.servers.findIndex((s) => s.id === id);
		const sidx = savedConfig.servers.findIndex((s) => s.id === id);
		if (idx >= 0) config.servers[idx].config = structuredClone(cfg);
		if (sidx >= 0) savedConfig.servers[sidx].config = structuredClone(cfg);
	}

	async function applyServerAccessConfig(id: string, cfg: WdttServerConfig) {
		const norm = normalizeServer(cfg);
		patchServerInConfig(id, norm);
		if (savedConfig) {
			const sidx = savedConfig.servers.findIndex((s) => s.id === id);
			if (sidx >= 0) savedConfig.servers[sidx].config = structuredClone(norm);
		}
	}

	async function saveServerConfig(cfg: WdttServerConfig) {
		if (!selectedServer) return;
		saving = true;
		try {
			const sent = $state.snapshot(cfg);
			const id = selectedServer.id;
			const result = await api.updateWdttServerInstance(id, cfg);
			const norm = normalizeServer(result.config);
			if (config && savedConfig) {
				const sidx = savedConfig.servers.findIndex((s) => s.id === id);
				if (sidx >= 0) savedConfig.servers[sidx].config = structuredClone(norm);
				const idx = config.servers.findIndex((s) => s.id === id);
				if (idx >= 0 && JSON.stringify($state.snapshot(config.servers[idx].config)) === JSON.stringify(sent)) {
					config.servers[idx].config = structuredClone(norm);
				}
			}
			notifications.success('Настройки WDTT-сервера сохранены');
		} catch (e) {
			notifications.error('Не удалось сохранить: ' + errText(e));
			throw e;
		} finally {
			saving = false;
		}
	}

	function revertServer() {
		if (!config || !savedServer) return;
		patchServerInConfig(selectedServerId, $state.snapshot(savedServer.config));
	}

	async function toggleServerInstance(id: string, on: boolean) {
		try {
			if (on) {
				await api.startWdttServerInstance(id);
				notifications.success('WDTT сервер запущен');
			} else {
				await api.stopWdttServerInstance(id);
				notifications.success('WDTT сервер остановлен');
			}
		} catch (e) {
			notifications.error(errText(e) || 'Не удалось переключить сервер');
		} finally {
			await Promise.all([loadConfig(), loadStatus()]);
		}
	}

	async function deleteServer(id: string) {
		const inst = config?.servers.find((s) => s.id === id);
		const name = inst?.name ?? id;
		if (!confirm(`Удалить сервер «${name}»?`)) return;
		try {
			await api.deleteWdttServer(id);
			await loadConfig();
			await loadStatus();
			notifications.success('Сервер удалён');
		} catch (e) {
			notifications.error('Не удалось удалить: ' + errText(e));
		}
	}

	async function addServer() {
		try {
			const inst = await api.createWdttServer();
			await loadConfig();
			selectedServerId = inst.id;
			notifications.success(`Сервер «${inst.name}» создан`);
		} catch (e) {
			notifications.error('Не удалось создать сервер: ' + errText(e));
		}
	}

	async function renameServer(id: string, name: string) {
		try {
			await api.renameWdttServer(id, name);
			await loadConfig();
		} catch (e) {
			notifications.error('Не удалось переименовать: ' + errText(e));
		}
	}

	// peer и VK-хеши ссылки живут в конфиге сервера: без них основную ссылку
	// после перезагрузки страницы пришлось бы собирать по памяти.
	async function persistLinkParams(peer: string, vkHashes: string[], forClient: boolean) {
		if (!selectedServer) return;
		const current = selectedServer.config;
		// Хеши клиента — его личные: в серверные параметры идут только те,
		// с которыми собрана ссылка на основном пароле.
		const hashes = forClient ? (current.linkVkHashes ?? '') : vkHashes.join(',');
		if ((current.linkPeer ?? '') === peer && (current.linkVkHashes ?? '') === hashes) return;
		const id = selectedServer.id;
		const cfg = { ...$state.snapshot(current), linkPeer: peer, linkVkHashes: hashes };
		try {
			const result = await api.updateWdttServerInstance(id, cfg);
			patchServerInConfig(id, normalizeServer(result.config));
		} catch {
			// не критично: ссылка уже показана, параметры допишутся при сохранении
		}
	}

	async function generateServerLink(
		peer: string,
		vkHashes: string[],
		opts?: { password?: string; name?: string }
	) {
		if (!selectedServer) return null;
		generating = true;
		try {
			const result = await api.generateWdttServerLink(selectedServer.id, {
				peer: peer || undefined,
				vkHashes: vkHashes.length ? vkHashes : undefined,
				name: opts?.name ?? selectedServer.name,
				password: opts?.password
			});
			generatedLink = result.link;
			genPeer = result.peer;
			generatedLinkQwdtt = result.linkQwdtt ?? '';
			void persistLinkParams(result.peer, vkHashes, !!opts?.password);
			return result;
		} catch (e) {
			notifications.error('Не удалось сгенерировать ссылку: ' + errText(e));
			return null;
		} finally {
			generating = false;
		}
	}

	async function importWgTunnelFromConf(wgRaw: string, tunnelLabel?: string) {
		if (!selectedClient || !config) return;
		const c = selectedClient.config;
		if ((c.connMode ?? 'wg') === 'raw') {
			notifications.info('Raw-режим — AWG-туннель не нужен');
			return;
		}
		const port = linkedTunnelListenPort(c.listen);
		if (port == null) {
			notifications.error('Укажите listen (127.0.0.1:порт) перед импортом WG');
			return;
		}
		importingWgTunnel = true;
		try {
			const wg = patchWgConfEndpoint(wgRaw.trim(), port);
			const tunnel = await api.importConfig(
				wg,
				wdttTunnelName(tunnelLabel || selectedClient.name),
				undefined,
				undefined,
				selectedClientId
			);
			wgEnsureSettled.delete(selectedClientId);
			notifications.success(`AWG-туннель «${tunnel.name}» создан (Endpoint 127.0.0.1:${port})`);
		} catch (e) {
			notifications.error('Не удалось создать AWG-туннель: ' + errText(e));
			throw e;
		} finally {
			importingWgTunnel = false;
		}
	}

	async function applyImportPayload(
		payload: WdttImportPayload,
		meta?: { subUrl?: string; clientName?: string; andStart?: boolean }
	) {
		if (!selectedClient || !config) return;
		importing = true;
		try {
			const c = selectedClient.config;
			const oldPeer = savedClient?.config.peer ?? '';
			const listenPort = linkedTunnelListenPort(selectedClient.config.listen);
			if (payload.peer) c.peer = payload.peer;
			if (payload.password) c.password = payload.password;
			if (payload.vkHashes?.length) c.vkHashes = payload.vkHashes.join(',');
			if (payload.workers && payload.workers > 0) c.workers = payload.workers;
			const subUrl = payload.subUrl || meta?.subUrl;
			// Подписка задаёт listenPort=9000 для всех стран — порт у каждого клиента свой.
			if (payload.listen && !subUrl && listenPort == null) c.listen = payload.listen;
			if (subUrl) c.sub = subUrl;
			if (payload.deviceId) c.deviceId = payload.deviceId;
			if (payload.connMode === 'raw' || payload.connMode === 'wg') {
				c.connMode = payload.connMode;
			}

			const clientName = meta?.clientName?.trim();
			if (clientName && clientName !== selectedClient.name) {
				await api.renameWdttClient(selectedClientId, clientName);
			}

			if (
				payload.peer &&
				!peersEqual(oldPeer, payload.peer) &&
				clientStatus?.running &&
				!meta?.andStart
			) {
				await api.stopWdttClientInstance(selectedClientId);
				await loadStatus();
			}

			let msg = 'Профиль импортирован';
			if (subUrl) msg += ' (URL подписки сохранён)';

			const wg = payload.wg?.trim();
			const useWgTunnel = (c.connMode ?? 'wg') !== 'raw';
			if (wg && useWgTunnel) {
				try {
					const portForTunnel =
						listenPort ?? linkedTunnelListenPort(c.listen, payload.listen);
					if (portForTunnel == null) {
						notifications.error(
							'Профиль импортирован, но не удалось определить listen-порт для AWG-туннеля'
						);
					} else {
						const wgForImport = patchWgConfEndpoint(wg, portForTunnel);
						const tunnel = await api.importConfig(
							wgForImport,
							wdttTunnelName(meta?.clientName || payload.name),
							undefined,
							undefined,
							selectedClientId
						);
						msg += `. Создан туннель «${tunnel.name}» (Endpoint 127.0.0.1:${portForTunnel})`;
					}
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

{#if loading}
	<div class="wdtt-loading">Загрузка…</div>
{:else if loadError && !config}
	<div class="wdtt-loading">
		<p>Не удалось загрузить настройки WDTT.</p>
		<p class="wdtt-load-error">{loadError}</p>
	</div>
{:else if config}
	<ProcessAlerts
		status={activeTab === 'client' ? clientStatus : serverStatus}
		installAvailable={status?.installAvailable ?? false}
		installVersion={status?.installVersion}
		installedVersion={status?.installedVersion}
		updateAvailable={status?.updateAvailable ?? false}
		installing={installing || (status?.installing ?? false)}
		onInstall={install}
		productName="wdtt"
		notFoundHint="нажмите «Установить» — бинари скачаются с зеркала."
	>
		{#snippet manualInstall(binary)}
			<span>
				Бинарь <code>{binary}</code> не найден. Кнопка «Установить» скачает
				{serverSupported ? 'клиент и сервер' : 'клиент'} с зеркала.
			</span>
		{/snippet}
	</ProcessAlerts>

	<Tabs
		tabs={wdttTabs}
		active={activeTab}
		onchange={(id) => {
			wdttTab = id as WdttTabId;
		}}
	/>

	<ProxyPanelModeToggle />

	{#if activeTab === 'client'}
	<InstanceBar
		items={clientBarItems}
		selectedId={selectedClientId}
		onSelect={(id) => {
			selectedClientId = id;
			clientPanelTab = 'setup';
			wgEnsureSettled.delete(id);
		}}
		onToggle={toggleClientInstance}
		onAdd={addClient}
		onDelete={deleteClient}
		onRename={renameClient}
	/>

	{#if selectedClient}
		{#key `${selectedClientId}:${clientUiEpoch}`}
			<WdttClientSimple
				client={selectedClient.config}
				running={clientStatus?.running ?? false}
				status={clientStatus}
				routerClock={status?.routerClock}
				{saving}
				{importing}
				instances={clientBarItems}
				selectedInstanceId={selectedClientId}
				bind:opsTab={clientPanelTab}
				onSelectInstance={(id) => {
					selectedClientId = id;
					wgEnsureSettled.delete(id);
				}}
				onSave={saveClientConfig}
				onRevert={revertClient}
				onToggle={(on) => toggleClientInstance(selectedClientId, on)}
				onImportPayload={applyImportPayload}
				onRefreshSubscription={refreshFromSubscription}
				refreshingSub={refreshingSub}
				subscriptionTick={subscriptionTick}
				onEnsureWg={ensureWgManual}
				ensuringWg={ensuringWg}
				onImportWgTunnel={importWgTunnelFromConf}
				importingWgTunnel={importingWgTunnel}
			/>
		{/key}
	{:else}
		<p class="wdtt-empty-hint">Нет выбранного клиента. Нажмите «+ Добавить», чтобы создать новый.</p>
	{/if}
	{:else}
	<InstanceBar
		items={serverBarItems}
		selectedId={selectedServerId}
		showDtls={false}
		onSelect={(id) => {
			selectedServerId = id;
			serverPanelTab = 'main';
		}}
		onToggle={toggleServerInstance}
		onAdd={canAddWdttServer ? addServer : undefined}
		onDelete={deleteServer}
		onRename={renameServer}
	/>

	{#if selectedServer}
		{#key selectedServerId}
			<WdttServerSimple
				server={selectedServer.config}
				running={serverStatus?.running ?? false}
				status={serverStatus}
				{saving}
				{generating}
				serverInstanceId={selectedServerId}
				{generatedLink}
				generatedLinkQwdtt={generatedLinkQwdtt}
				bind:genPeer
				bind:genVKHashes
				instances={serverBarItems}
				selectedInstanceId={selectedServerId}
				bind:opsTab={serverPanelTab}
				onSelectInstance={(id) => {
					selectedServerId = id;
				}}
				onSave={saveServerConfig}
				onAccessUpdated={(cfg) => applyServerAccessConfig(selectedServerId, cfg)}
				onToggle={(on) => toggleServerInstance(selectedServerId, on)}
				onGenerate={generateServerLink}
			/>
		{/key}
	{:else}
		<p class="wdtt-empty-hint">Нет выбранного сервера.</p>
	{/if}
	{/if}
{/if}

<style>
	.wdtt-loading {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-secondary);
	}

	.wdtt-load-error {
		font-size: 0.8125rem;
		color: var(--color-danger);
		margin-top: 0.5rem;
	}

	.wdtt-empty-hint {
		margin: 0.5rem 0 1rem;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}
</style>
