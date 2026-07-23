<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard as copyText } from '$lib/utils/clipboard';
	import { Tabs } from '$lib/components/ui';
	import type {
		FreeTurnClientConfig,
		FreeTurnClientInstance,
		FreeTurnConfig,
		FreeTurnGenerateLinkResult,
		FreeTurnServerConfig,
		FreeTurnServerInstance,
		FreeTurnStatus
	} from '$lib/types';
	import ProcessAlerts from './ProcessAlerts.svelte';
	import InstanceBar from './InstanceBar.svelte';
	import FreeTurnClientSimple from './FreeTurnClientSimple.svelte';
	import FreeTurnServerSimple from './FreeTurnServerSimple.svelte';
	import { parseLocalListenPort, patchWgConfEndpoint } from '$lib/utils/serverPeerOptions';

	type FtTab = 'client' | 'server';

	let ftTab: FtTab = $state('client');
	let loading = $state(true);
	let saving = $state(false);

	let config = $state<FreeTurnConfig | null>(null);
	let status = $state<FreeTurnStatus | null>(null);
	let savedConfig = $state<FreeTurnConfig | null>(null);

	let selectedClientId = $state('default');
	let selectedServerId = $state('default');

	let importing = $state(false);
	let importedWG: string | null = $state(null);
	let installing = $state(false);
	let checkingUpdates = $state(false);

	let genProvider = $state('vk');
	let genPeer = $state('');
	let genMTU = $state(1376);
	let genWG = $state('');
	let genClientId = $state('');
	let genName = $state('');
	let generating = $state(false);
	let generatedLink = $state('');
	let generatedPeer = $state('');
	let generatedClientId = $state('');

	let statusPoll: ReturnType<typeof setInterval> | undefined;

	const ftTabs = [
		{ id: 'client', label: 'Клиент' },
		{ id: 'server', label: 'Сервер' }
	];

	const selectedClient = $derived(
		config
			? (config.clients.find((c: FreeTurnClientInstance) => c.id === selectedClientId) ??
					config.clients[0] ??
					null)
			: null
	);
	const selectedServer = $derived(
		config
			? (config.servers.find((s: FreeTurnServerInstance) => s.id === selectedServerId) ??
					config.servers[0] ??
					null)
			: null
	);
	const savedClient = $derived(
		savedConfig
			? (savedConfig.clients.find((c: FreeTurnClientInstance) => c.id === selectedClientId) ??
					savedConfig.clients[0] ??
					null)
			: null
	);
	const savedServer = $derived(
		savedConfig
			? (savedConfig.servers.find((s: FreeTurnServerInstance) => s.id === selectedServerId) ??
					savedConfig.servers[0] ??
					null)
			: null
	);
	const clientStatus = $derived(
		status
			? (status.clients.find((c) => c.id === selectedClientId)?.status ?? status.client)
			: undefined
	);
	const serverStatus = $derived(
		status
			? (status.servers.find((s) => s.id === selectedServerId)?.status ?? status.server)
			: undefined
	);
	const defaultClientListenPort = $derived.by(() => {
		const listen = config?.clients.find((c) => c.id === selectedClientId)?.config.listen;
		const port = Number(listen?.split(':').pop());
		return port > 0 ? port : 9000;
	});

	function errText(e: unknown): string {
		return e instanceof Error ? e.message : String(e ?? '');
	}

	onMount(async () => {
		await Promise.all([loadConfig(), loadStatus(false)]);
		loading = false;
		statusPoll = setInterval(async () => {
			await loadStatus(false);
		}, 1000);
	});

	onDestroy(() => {
		if (statusPoll) clearInterval(statusPoll);
	});

	function normalizeClient(c: FreeTurnClientConfig): FreeTurnClientConfig {
		return {
			...c,
			peer: c.peer ?? '',
			links: c.links ?? '',
			turnHost: c.turnHost ?? '',
			obfKey: c.obfKey ?? '',
			dnsServers: c.dnsServers ?? '',
			clientId: c.clientId ?? '',
			sub: c.sub ?? '',
			browser:
				(c.browser as string) === 'chromium'
					? 'chrome'
					: c.browser === 'safari' || c.browser === 'firefox'
						? c.browser
						: 'chrome',
			streams: c.streams > 0 ? c.streams : 10,
			streamsPerCred: c.streamsPerCred > 0 ? c.streamsPerCred : 10,
			debug: !!c.debug
		};
	}

	function normalizeServer(s: FreeTurnServerConfig): FreeTurnServerConfig {
		return {
			...s,
			connect: s.connect ?? '',
			obfKey: s.obfKey ?? '',
			clientsFile: s.clientsFile ?? '',
			debug: !!s.debug
		};
	}

	function normalizeConfig(cfg: FreeTurnConfig): FreeTurnConfig {
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
			const norm = normalizeConfig(await api.getFreeTurnConfig());
			savedConfig = structuredClone(norm);
			config = norm;
			if (!norm.clients.some((c) => c.id === selectedClientId)) {
				selectedClientId = norm.clients[0]?.id ?? 'default';
			}
			if (!norm.servers.some((s) => s.id === selectedServerId)) {
				selectedServerId = norm.servers[0]?.id ?? 'default';
			}
		} catch (e) {
			notifications.error('Не удалось загрузить конфигурацию FreeTurn: ' + errText(e));
		}
	}

	async function loadStatus(forceRemote = false) {
		try {
			status = await api.getFreeTurnStatus(forceRemote);
		} catch {
			// Молча — как ping-бейджи списка туннелей.
		}
	}

	async function checkUpdates() {
		checkingUpdates = true;
		try {
			await loadStatus(true);
			if (status?.installedVersion && !status.updateAvailable && !status.remoteCheckError) {
				notifications.info('Установлена актуальная версия');
			}
		} finally {
			checkingUpdates = false;
		}
	}

	function patchClientInConfig(id: string, cfg: FreeTurnClientConfig) {
		if (!config || !savedConfig) return;
		const idx = config.clients.findIndex((c) => c.id === id);
		const sidx = savedConfig.clients.findIndex((c) => c.id === id);
		if (idx >= 0) config.clients[idx].config = structuredClone(cfg);
		if (sidx >= 0) savedConfig.clients[sidx].config = structuredClone(cfg);
	}

	function patchServerInConfig(id: string, cfg: FreeTurnServerConfig) {
		if (!config || !savedConfig) return;
		const idx = config.servers.findIndex((s) => s.id === id);
		const sidx = savedConfig.servers.findIndex((s) => s.id === id);
		if (idx >= 0) config.servers[idx].config = structuredClone(cfg);
		if (sidx >= 0) savedConfig.servers[sidx].config = structuredClone(cfg);
	}

	async function saveClientConfig(cfg: FreeTurnClientConfig) {
		if (!selectedClient) return;
		saving = true;
		try {
			const sent = $state.snapshot(cfg);
			const id = selectedClient.id;
			const norm = normalizeClient(
				id === 'default'
					? await api.updateFreeTurnClientConfig(cfg)
					: await api.updateFreeTurnClientInstance(id, cfg)
			);
			if (config && savedConfig) {
				const sidx = savedConfig.clients.findIndex((c) => c.id === id);
				if (sidx >= 0) savedConfig.clients[sidx].config = structuredClone(norm);
				const idx = config.clients.findIndex((c) => c.id === id);
				if (idx >= 0 && JSON.stringify($state.snapshot(config.clients[idx].config)) === JSON.stringify(sent)) {
					config.clients[idx].config = structuredClone(norm);
				}
			}
			notifications.success('Настройки клиента сохранены');
		} catch (e) {
			notifications.error('Не удалось сохранить: ' + errText(e));
		} finally {
			saving = false;
		}
	}

	async function saveServerConfig(cfg: FreeTurnServerConfig) {
		if (!selectedServer) return;
		saving = true;
		try {
			const sent = $state.snapshot(cfg);
			const id = selectedServer.id;
			const norm = normalizeServer(
				id === 'default'
					? await api.updateFreeTurnServerConfig(cfg)
					: await api.updateFreeTurnServerInstance(id, cfg)
			);
			if (config && savedConfig) {
				const sidx = savedConfig.servers.findIndex((s) => s.id === id);
				if (sidx >= 0) savedConfig.servers[sidx].config = structuredClone(norm);
				const idx = config.servers.findIndex((s) => s.id === id);
				if (idx >= 0 && JSON.stringify($state.snapshot(config.servers[idx].config)) === JSON.stringify(sent)) {
					config.servers[idx].config = structuredClone(norm);
				}
			}
			notifications.success('Настройки сервера сохранены');
		} catch (e) {
			notifications.error('Не удалось сохранить: ' + errText(e));
		} finally {
			saving = false;
		}
	}

	function revertClient() {
		if (!config || !savedClient) return;
		patchClientInConfig(selectedClientId, $state.snapshot(savedClient.config));
	}

	function revertServer() {
		if (!config || !savedServer) return;
		patchServerInConfig(selectedServerId, $state.snapshot(savedServer.config));
	}

	async function toggleClientInstance(id: string, on: boolean) {
		try {
			if (on) {
				await api.startFreeTurnClient(id);
				notifications.success('FreeTurn клиент запущен');
			} else {
				await api.stopFreeTurnClient(id);
				notifications.success('FreeTurn клиент остановлен');
			}
		} catch (e) {
			notifications.error(errText(e) || 'Не удалось переключить клиент');
		} finally {
			await loadStatus();
		}
	}

	async function toggleServerInstance(id: string, on: boolean) {
		try {
			if (on) {
				await api.startFreeTurnServer(id);
				notifications.success('FreeTurn сервер запущен');
			} else {
				await api.stopFreeTurnServer(id);
				notifications.success('FreeTurn сервер остановлен');
			}
		} catch (e) {
			notifications.error(errText(e) || 'Не удалось переключить сервер');
		} finally {
			await loadStatus();
		}
	}

	async function addClient() {
		try {
			const inst = await api.createFreeTurnClient();
			await loadConfig();
			selectedClientId = inst.id;
			notifications.success(`Клиент «${inst.name}» создан`);
		} catch (e) {
			notifications.error('Не удалось создать клиент: ' + errText(e));
		}
	}

	async function addServer() {
		try {
			const inst = await api.createFreeTurnServer();
			await loadConfig();
			selectedServerId = inst.id;
			notifications.success(`Сервер «${inst.name}» создан`);
		} catch (e) {
			notifications.error('Не удалось создать сервер: ' + errText(e));
		}
	}

	async function deleteClient(id: string) {
		const inst = config?.clients.find((c) => c.id === id);
		const name = inst?.name ?? id;
		const listen = inst?.config.listen?.trim();
		const confirmMsg = listen
			? `Удалить клиент «${name}»?\n\nБудут удалены настройки инстанса и AWG-туннели, созданные при импорте freeturn:// для этого клиента. Ручные туннели с тем же портом не затронуты.`
			: `Удалить клиент «${name}»?\n\nБудут удалены настройки инстанса и AWG-туннели, созданные при импорте freeturn:// для этого клиента.`;
		if (!confirm(confirmMsg)) return;
		try {
			const result = await api.deleteFreeTurnClient(id);
			await loadConfig();
			await loadStatus();
			let msg = 'Клиент удалён';
			const n = result.deletedTunnels?.length ?? 0;
			if (n > 0) {
				msg += `, AWG-туннелей удалено: ${n}`;
			}
			if (result.tunnelErrors?.length) {
				notifications.error(
					'Клиент удалён, но часть AWG-туннелей не удалось убрать: ' +
						result.tunnelErrors.join('; ')
				);
			} else {
				notifications.success(msg);
			}
		} catch (e) {
			notifications.error('Не удалось удалить: ' + errText(e));
		}
	}

	async function deleteServer(id: string) {
		const name = config?.servers.find((s) => s.id === id)?.name ?? id;
		if (!confirm(`Удалить сервер «${name}»? Настройки инстанса будут потеряны.`)) return;
		try {
			await api.deleteFreeTurnServer(id);
			await loadConfig();
			await loadStatus();
			notifications.success('Сервер удалён');
		} catch (e) {
			notifications.error('Не удалось удалить: ' + errText(e));
		}
	}

	async function renameClient(id: string, name: string) {
		try {
			await api.renameFreeTurnClient(id, name);
			await loadConfig();
		} catch (e) {
			notifications.error('Не удалось переименовать: ' + errText(e));
		}
	}

	async function renameServer(id: string, name: string) {
		try {
			await api.renameFreeTurnServer(id, name);
			await loadConfig();
		} catch (e) {
			notifications.error('Не удалось переименовать: ' + errText(e));
		}
	}

	async function install() {
		installing = true;
		try {
			await api.installFreeTurn();
			notifications.success('freeturn установлен (клиент + сервер)');
		} catch (e) {
			notifications.error('Не удалось установить freeturn: ' + errText(e));
		} finally {
			installing = false;
			await loadStatus();
		}
	}

	async function copy(text: string) {
		if (await copyText(text)) {
			notifications.success('Скопировано в буфер');
		} else {
			notifications.error('Не удалось скопировать');
		}
	}

	async function applyImportLink(link: string) {
		if (!link.trim() || !selectedClient || !config) return;
		importing = true;
		try {
			const payload = await api.decodeFreeTurnLink(link.trim());
			const c = selectedClient.config;
			c.peer = payload.peer ?? c.peer;
			c.provider = payload.provider || c.provider;
			if (payload.obf) {
				c.obfProfile = payload.obf as typeof c.obfProfile;
			}
			c.obfKey = payload.key ?? c.obfKey;
			if (payload.n && payload.n > 0) c.streams = payload.n;
			if (payload.spc && payload.spc > 0) c.streamsPerCred = payload.spc;
			if (payload.cid) c.clientId = payload.cid;
			if (payload.transport) c.transport = payload.transport as typeof c.transport;
			if (payload.mode) c.mode = payload.mode as typeof c.mode;
			if (typeof payload.bond === 'boolean') c.bond = payload.bond;
			const wg = payload.wg?.trim() ? payload.wg : null;
			importedWG = wg;

			let msg = 'Ссылка распознана, поля заполнены — не забудьте сохранить';
			if (payload.cid) {
				msg += `. В ссылке был Client ID — если у сервера включён allowlist (-clients-file), владелец сервера должен добавить именно этот ID туда`;
			}
			if (wg) {
				try {
					const listenPort =
						parseLocalListenPort(c.listen) ??
						parseLocalListenPort(payload.listen) ??
						9000;
					const wgForImport = patchWgConfEndpoint(wg, listenPort);
					const tunnel = await api.importConfig(
						wgForImport,
						`FreeTurn ${payload.peer}`.slice(0, 60),
						undefined,
						selectedClientId
					);
					msg += `. Создан туннель «${tunnel.name}» (Endpoint 127.0.0.1:${listenPort})`;
				} catch (e) {
					notifications.error('Поля заполнены, но не удалось создать туннель из конфига: ' + errText(e));
				}
			}
			notifications.success(msg);
		} catch (e) {
			notifications.error('Не удалось разобрать ссылку: ' + errText(e));
		} finally {
			importing = false;
		}
	}

	async function generateLink(
		provider: string,
		peer: string,
		mtu: number,
		wg: string,
		clientId: string,
		name: string
	): Promise<FreeTurnGenerateLinkResult | null> {
		if (!selectedServer) return null;
		generating = true;
		try {
			const listen = selectedServer.config.listen?.trim() ?? '';
			let peerParam = peer.trim();
			if (peerParam && !peerParam.includes(':')) {
				const port = listen.includes(':') ? listen.split(':').pop() : listen;
				if (port) peerParam = `${peerParam}:${port}`;
			}
			const result = await api.generateFreeTurnLink({
				provider,
				peer: peerParam || undefined,
				mtu,
				wg: wg.trim() || undefined,
				clientId: clientId.trim() || undefined,
				name: name.trim() || undefined,
				serverId: selectedServer.id
			});
			generatedLink = result.link;
			generatedPeer = result.peer;
			generatedClientId = result.clientId ?? '';
			return result;
		} catch (e) {
			notifications.error('Не удалось сгенерировать ссылку: ' + errText(e));
			return null;
		} finally {
			generating = false;
		}
	}

	const clientBarItems = $derived(
		(config ? config.clients : []).map((c: FreeTurnClientInstance) => {
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
	const serverBarItems = $derived(
		(config ? config.servers : []).map((s: FreeTurnServerInstance) => {
			const st = status?.servers.find((x) => x.id === s.id)?.status ?? status?.server;
			return {
				id: s.id,
				name: s.name,
				running: st?.running,
				startedAt: st?.startedAt,
				pid: st?.pid,
				binaryPresent: st?.binaryPresent
			};
		})
	);
</script>

<Tabs
	tabs={ftTabs}
	active={ftTab}
	onchange={(id) => {
		ftTab = id as FtTab;
	}}
	urlParam="ft"
	defaultTab="client"
/>

{#if loading || !config}
	<div class="ft-loading">Загрузка…</div>
{:else}
	<ProcessAlerts
		status={ftTab === 'client' ? clientStatus : serverStatus}
		installAvailable={status?.installAvailable ?? false}
		installVersion={status?.installVersion}
		installedVersion={status?.installedVersion}
		updateAvailable={status?.updateAvailable ?? false}
		remoteVersion={status?.remoteVersion}
		remoteCheckError={status?.remoteCheckError}
		installing={installing || (status?.installing ?? false)}
		{checkingUpdates}
		onInstall={install}
		onCheckUpdates={checkUpdates}
	/>

	{#if ftTab === 'client'}
		<InstanceBar
			items={clientBarItems}
			selectedId={selectedClientId}
			onSelect={(id) => {
				selectedClientId = id;
			}}
			onToggle={toggleClientInstance}
			onAdd={addClient}
			onDelete={deleteClient}
			onRename={renameClient}
		/>
		{#if selectedClient}
			<FreeTurnClientSimple
				client={selectedClient.config}
				running={clientStatus?.running ?? false}
				status={clientStatus}
				routerClock={status?.routerClock}
				{saving}
				onSave={saveClientConfig}
				onToggle={(on) => toggleClientInstance(selectedClientId, on)}
				onImportLink={applyImportLink}
			/>
		{/if}
	{:else}
		<InstanceBar
			items={serverBarItems}
			selectedId={selectedServerId}
			showDtls={false}
			onSelect={(id) => {
				selectedServerId = id;
			}}
			onToggle={toggleServerInstance}
			onAdd={addServer}
			onDelete={deleteServer}
			onRename={renameServer}
		/>
		{#if selectedServer}
			<FreeTurnServerSimple
				server={selectedServer.config}
				serverInstanceId={selectedServerId}
				running={serverStatus?.running ?? false}
				status={serverStatus}
				{saving}
				{generating}
				defaultClientListenPort={defaultClientListenPort}
				{generatedLink}
				{generatedPeer}
				bind:genProvider
				bind:genPeer
				bind:genMTU
				bind:genWG
				bind:genClientId
				bind:genName
				onSave={saveServerConfig}
				onToggle={(on) => toggleServerInstance(selectedServerId, on)}
				onGenerate={generateLink}
				onCopy={copy}
			/>
		{/if}
	{/if}
{/if}

<style>
	.ft-loading {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-secondary);
	}

</style>
