<script lang="ts">
	import type {
		ASCParams,
		WireguardServer,
		WireguardServerConfig,
		WireguardServerPeer,
		ManagedPeer,
		ManagedPeerStats,
	} from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { formatBytes } from '$lib/utils/format';
	import { comparePeerFieldsDirected } from '$lib/utils/peerSort';
	import { peerSort } from '$lib/stores/peerSort';
	import { maskToPrefix, resolveNatMode } from '$lib/utils/network';
	import { countActiveSystemPeers } from '$lib/utils/serverPeerActivity';
	import { patchSystemServerEnabledInSnapshot, systemServerIsUp } from '$lib/utils/systemServerState';
	import {
		PeerSortControls,
		ManagedPeerTable,
		AddSystemPeerModal,
		EditSystemPeerModal,
		PeerConfModal,
		ConfGeneratorModal,
		ServerAccessPolicyDropdown,
		ServerEndpointSetting,
		ServerSettingsPanel,
	} from '$lib/components/servers';
	import { Toggle, Button, Stat, StatStrip } from '$lib/components/ui';
	import { Plus, RefreshCw, ExternalLink } from 'lucide-svelte';

	interface Props {
		server: WireguardServer;
		isMarked?: boolean;
		onUnmark?: (id: string) => void;
		ingressEnabled?: boolean;
		onToggleIngress?: (interfaceName: string, enabled: boolean) => Promise<void>;
	}

	let {
		server,
		isMarked = false,
		onUnmark,
		ingressEnabled = false,
		onToggleIngress = async () => {},
	}: Props = $props();

	let isBuiltIn = $derived(server.builtIn ?? server.description === 'Wireguard VPN Server');

	let addPeerOpen = $state(false);
	let editPeerOpen = $state(false);
	let confModalOpen = $state(false);
	let confGeneratorOpen = $state(false);
	let selectedPeer = $state<WireguardServerPeer | null>(null);
	let confPubkey = $state('');
	let confPeerName = $state('');
	let confPeerKey = $state('');
	let serverConfig = $state<WireguardServerConfig | null>(null);
	let ascParams = $state<ASCParams | null>(null);
	let wanIP = $state('');
	let searchQuery = $state('');
	let togglingEnabled = $state(false);
	let restartingServer = $state(false);
	let togglingIngress = $state(false);
	let togglingNAT = $state(false);
	let policyChanging = $state(false);
	let togglingPeerKeys = $state(new Set<string>());
	let builtinWanIP = $state('');
	let loadingBuiltinWanIP = $state(false);
	let builtinWanIPLoadedFor = $state('');

	let natMode = $derived(resolveNatMode(server.natMode, server.natEnabled));
	// Keenetic exposes NAT as on/off; internet-only is normalized to "on" in the UI.
	let natEnabled = $derived(natMode !== 'none');
	// When the backend couldn't read NAT/policy from NDMS, natMode/policy are a
	// fabricated 'none' — surface "unknown" and block edits instead of letting
	// the user act on it. Absent flags (legacy/managed) are treated as known.
	let natModeKnown = $derived(server.natModeKnown ?? true);
	let policyKnown = $derived(server.policyKnown ?? true);

	let serverName = $derived(server.description || server.id);
	let isUp = $derived(systemServerIsUp(server));
	let onlineCount = $derived(countActiveSystemPeers(server.peers));
	let totalPeers = $derived((server.peers ?? []).length);
	let totalRx = $derived((server.peers ?? []).reduce((sum, p) => sum + p.rxBytes, 0));
	let totalTx = $derived((server.peers ?? []).reduce((sum, p) => sum + p.txBytes, 0));

	function peerTunnelIP(p: WireguardServerPeer): string {
		const raw = p.allowedIPs?.find((ip) => ip.includes('/32')) || p.allowedIPs?.[0] || '';
		if (!raw) return '';
		return raw.includes('/') ? raw : `${raw}/32`;
	}

	function toManagedPeer(p: WireguardServerPeer): ManagedPeer {
		return {
			publicKey: p.publicKey,
			privateKey: '',
			presharedKey: '',
			description: p.description,
			tunnelIP: peerTunnelIP(p),
			enabled: p.enabled,
		};
	}

	function getPeerStats(publicKey: string): ManagedPeerStats | undefined {
		const p = (server.peers ?? []).find((peer) => peer.publicKey === publicKey);
		if (!p) return undefined;
		return {
			publicKey: p.publicKey,
			endpoint: p.endpoint || '-',
			rxBytes: p.rxBytes,
			txBytes: p.txBytes,
			lastHandshake: p.lastHandshake,
			online: p.online,
		};
	}

	let sortedPeers = $derived.by(() => {
		let peers = server.peers ?? [];
		if (searchQuery) {
			const q = searchQuery.toLowerCase();
			peers = peers.filter(
				(p) =>
					(p.description || '').toLowerCase().includes(q) ||
					peerTunnelIP(p).toLowerCase().includes(q)
			);
		}
		const sortBy = $peerSort.sortBy;
		if (sortBy === null) return peers.map(toManagedPeer);
		return [...peers]
			.sort((a, b) => {
				const sa = getPeerStats(a.publicKey);
				const sb = getPeerStats(b.publicKey);
				return comparePeerFieldsDirected(
					{
						name: a.description || a.publicKey,
						ip: peerTunnelIP(a),
						endpoint: sa?.endpoint || '-',
						rxBytes: sa?.rxBytes ?? null,
						txBytes: sa?.txBytes ?? null,
						online: sa?.online ?? null,
						lastHandshake: sa?.lastHandshake ?? null,
					},
					{
						name: b.description || b.publicKey,
						ip: peerTunnelIP(b),
						endpoint: sb?.endpoint || '-',
						rxBytes: sb?.rxBytes ?? null,
						txBytes: sb?.txBytes ?? null,
						online: sb?.online ?? null,
						lastHandshake: sb?.lastHandshake ?? null,
					},
					sortBy,
					$peerSort.sortAsc
				);
			})
			.map(toManagedPeer);
	});

	let managedPeerMap = $derived.by(() => {
		const map = new Map<string, WireguardServerPeer>();
		for (const p of server.peers ?? []) map.set(p.publicKey, p);
		return map;
	});

	async function handleToggleEnabled(enabled: boolean) {
		togglingEnabled = true;
		try {
			const fresh = await api.setWireguardServerEnabled(server.id, enabled);
			servers.applyMutationResponse(patchSystemServerEnabledInSnapshot(fresh, server.id, enabled));
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения');
		} finally {
			togglingEnabled = false;
		}
	}

	async function handleRestartOrStart() {
		if (restartingServer) return;
		restartingServer = true;
		try {
			await api.restartWireguardServer(server.id);
			notifications.success(isUp ? 'Команда рестарта отправлена' : 'Команда запуска отправлена');
			servers.invalidate();
		} catch {
			notifications.warning('Команда могла быть отправлена, соединение могло временно прерваться');
		} finally {
			restartingServer = false;
		}
	}

	async function handleToggleIngress() {
		togglingIngress = true;
		try {
			await onToggleIngress(server.interfaceName, !ingressEnabled);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения egress в sing-box');
		} finally {
			togglingIngress = false;
		}
	}

	async function handleToggleNAT(enabled: boolean) {
		if (enabled === natEnabled) return;
		togglingNAT = true;
		try {
			const fresh = await api.setWireguardServerNATEnabled(server.id, enabled);
			servers.applyMutationResponse(fresh);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения NAT');
		} finally {
			togglingNAT = false;
		}
	}

	async function handlePolicyChange(newPolicy: string) {
		if (newPolicy === (server.policy ?? 'none')) return;
		policyChanging = true;
		try {
			const fresh = await api.setWireguardServerPolicy(server.id, newPolicy);
			servers.applyMutationResponse(fresh);
			notifications.success('Политика обновлена');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения политики');
		} finally {
			policyChanging = false;
		}
	}

	function isPeerToggling(publicKey: string): boolean {
		return togglingPeerKeys.has(publicKey);
	}

	async function handleTogglePeer(peer: ManagedPeer) {
		if (togglingPeerKeys.has(peer.publicKey)) return;
		togglingPeerKeys = new Set(togglingPeerKeys).add(peer.publicKey);
		try {
			const fresh = await api.toggleSystemServerPeer(server.id, peer.publicKey, !peer.enabled);
			servers.applyMutationResponse(fresh);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		} finally {
			const next = new Set(togglingPeerKeys);
			next.delete(peer.publicKey);
			togglingPeerKeys = next;
		}
	}

	async function doDeletePeer(peer: ManagedPeer) {
		try {
			const fresh = await api.deleteSystemServerPeer(server.id, peer.publicKey);
			servers.applyMutationResponse(fresh);
			notifications.success('Клиент удалён');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка удаления');
			throw e;
		}
	}

	function openEditPeer(peer: ManagedPeer) {
		const raw = managedPeerMap.get(peer.publicKey);
		if (!raw) return;
		selectedPeer = raw;
		editPeerOpen = true;
	}

	function openConf(peer: ManagedPeer) {
		if (isMarked) {
			void openConfGenerator(peer.publicKey);
			return;
		}
		const raw = managedPeerMap.get(peer.publicKey);
		if (!raw?.confAvailable) {
			notifications.warning('Конфиг недоступен — клиент создан вне AWG Manager или через KeenDNS');
			return;
		}
		confPubkey = peer.publicKey;
		confPeerName = peer.description || 'peer';
		confModalOpen = true;
	}

	async function openConfGenerator(publicKey: string) {
		confPeerKey = publicKey;
		try {
			const [config, asc, ip] = await Promise.all([
				api.getServerConfig(server.id),
				api.getASCParams(server.id).catch(() => null),
				api.getWANIP().catch(() => ''),
			]);
			serverConfig = config;
			ascParams = asc;
			wanIP = ip;
			confGeneratorOpen = true;
		} catch {
			notifications.error('Не удалось загрузить конфигурацию');
		}
	}

	let confGeneratorPeer = $derived(
		serverConfig?.peers.find((p) => p.publicKey === confPeerKey) ?? null
	);

	let keenDnsHref = $derived(
		server.keenDnsDomain ? `https://${server.keenDnsDomain}` : ''
	);

	$effect(() => {
		if (!isBuiltIn) {
			builtinWanIP = '';
			loadingBuiltinWanIP = false;
			builtinWanIPLoadedFor = '';
			return;
		}
		if (builtinWanIPLoadedFor === server.id) return;
		builtinWanIPLoadedFor = server.id;
		loadingBuiltinWanIP = true;
		void api
			.getWANIP()
			.then((ip) => {
				builtinWanIP = ip;
			})
			.catch(() => {
				builtinWanIP = '';
			})
			.finally(() => {
				loadingBuiltinWanIP = false;
			});
	});
</script>

<div class="card server-detail-card server-card" class:status-up={isUp}>
	<div class="card-header">
		<div class="header-info">
			<div class="title-row">
				<div class="title-main">
					<Toggle
						checked={isUp}
						controlled
						onchange={handleToggleEnabled}
						disabled={togglingEnabled || restartingServer}
						loading={togglingEnabled}
						size="sm"
						spinner="none"
					/>
					<h3 class="card-title">{serverName}</h3>
				</div>
				<div class="title-badges">
					{#if isBuiltIn}
						<span class="badge badge-builtin">Встроенный</span>
					{:else if isMarked}
						<span class="badge badge-system">Системный</span>
					{/if}
				</div>
			</div>
			<div class="server-meta">
				<span class="meta mono">{server.interfaceName}</span>
				<span class="meta mono">{server.address}/{maskToPrefix(server.mask)}</span>
				<span class="meta mono">:{server.listenPort}</span>
				{#if server.mtu}
					<span class="meta mono">MTU {server.mtu}</span>
				{/if}
			</div>
			{#if server.keenDnsDomain}
				<div class="keendns-row">
					<a class="keendns-link" href={keenDnsHref} target="_blank" rel="noopener noreferrer">
						<ExternalLink size={13} strokeWidth={2} aria-hidden="true" />
						<span>{server.keenDnsDomain}</span>
					</a>
				</div>
			{/if}
		</div>
		<div class="header-right">
			<div class="header-actions">
					<Button variant="secondary" size="sm" onclick={handleRestartOrStart} disabled={restartingServer || togglingEnabled} loading={restartingServer} iconBefore={restartIcon}>
						{isUp ? 'Рестарт' : 'Запуск'}
					</Button>
				</div>
		</div>
	</div>

	<StatStrip>
		<Stat value={formatBytes(totalRx)} label="RX" />
		<Stat value={formatBytes(totalTx)} label="TX" />
		<Stat value={`${onlineCount} / ${totalPeers}`} label="Клиенты" sub={onlineCount > 0 ? `${onlineCount} онлайн` : 'нет активных'} />
		<Stat value={`UDP :${server.listenPort}`} label="Listen" />
	</StatStrip>

	{#if isBuiltIn}
		<ServerSettingsPanel persistKey="awgm:servers:settingsCollapsed">
			<div class="setting-row setting-row-toggle">
				<div class="setting-copy">
					<span class="setting-title">NAT</span>
					{#if !natModeKnown}
						<span class="setting-description setting-description-warning">Не удалось прочитать состояние NAT с роутера — обновите страницу.</span>
					{:else if natEnabled}
						<span class="setting-description">Для доступа клиентов в интернет через NAT роутера.</span>
					{:else}
						<span class="setting-description">Без NAT — клиенты не выходят в интернет напрямую.</span>
					{/if}
					{#if natModeKnown && ingressEnabled && natEnabled}
						<span class="setting-description setting-description-warning">Интернет-трафик идёт через sing-box - NAT влияет только на видимость в LAN.</span>
					{/if}
				</div>
				<div class="setting-control setting-control-toggle">
					<Toggle
						checked={natEnabled}
						controlled
						onchange={handleToggleNAT}
						disabled={togglingNAT || !natModeKnown}
						loading={togglingNAT}
						spinner="before"
					/>
				</div>
			</div>

			<div class="setting-row setting-row-toggle">
				<div class="setting-copy">
					<span class="setting-title">Маршрутизация через sing-box</span>
					<span class="setting-description">Заворачивать интернет-трафик клиентов данного сервера в sing-box.</span>
				</div>
				<div class="setting-control setting-control-toggle">
					<Toggle
						checked={ingressEnabled}
						onchange={handleToggleIngress}
						disabled={togglingIngress}
						spinner="before"
					/>
				</div>
			</div>

			<ServerAccessPolicyDropdown
				policy={server.policy ?? 'none'}
				disabled={policyChanging || !policyKnown}
				onchange={handlePolicyChange}
			>
				{#snippet extra()}
					{#if !policyKnown}
						<span class="setting-description setting-description-warning">Не удалось прочитать политику доступа с роутера — обновите страницу.</span>
					{/if}
				{/snippet}
			</ServerAccessPolicyDropdown>

			<ServerEndpointSetting
				serverId={server.id}
				endpoint={server.endpoint}
				listenPort={server.listenPort}
				wanIP={builtinWanIP}
				keenDnsDomain={server.keenDnsDomain}
				loadingWanIP={loadingBuiltinWanIP}
			/>
		</ServerSettingsPanel>
	{/if}

	<div class="peers-section">
		<div class="peers-header">
			<span class="peers-title">Клиенты ({onlineCount}/{totalPeers} онлайн)</span>
			<div class="peers-controls">
				<PeerSortControls
					bind:searchQuery
					showSearch={(server.peers ?? []).length > 0}
					hideSortOnDesktop
				/>
				{#if isBuiltIn}
					<Button variant="secondary" size="sm" onclick={() => (addPeerOpen = true)} iconBefore={addPeerIcon}>
						Добавить клиента
					</Button>
				{/if}
			</div>
		</div>

		{#if (server.peers ?? []).length === 0}
			<div class="empty-peers">Нет клиентов. Добавьте первого.</div>
		{:else}
			<ManagedPeerTable
				peers={sortedPeers}
				{getPeerStats}
				showPeerDownload={isBuiltIn || isMarked}
				showPeerActions={isBuiltIn}
				showPeerToggle={isBuiltIn}
				onTogglePeer={handleTogglePeer}
				{isPeerToggling}
				onOpenConf={openConf}
				onOpenEditPeer={openEditPeer}
				onDeletePeer={doDeletePeer}
			/>
		{/if}
	</div>

	{#if !isBuiltIn && onUnmark}
		<div class="server-actions">
			<Button variant="ghost" size="sm" onclick={() => onUnmark?.(server.id)} {iconBefore}>
				Вернуть в туннели
			</Button>
		</div>
	{/if}
</div>

<AddSystemPeerModal
	bind:open={addPeerOpen}
	serverId={server.id}
	{server}
	onclose={() => (addPeerOpen = false)}
	onAdded={() => servers.invalidate()}
/>

{#if selectedPeer}
	<EditSystemPeerModal
		bind:open={editPeerOpen}
		serverId={server.id}
		peer={selectedPeer}
		onclose={() => { editPeerOpen = false; selectedPeer = null; }}
		onUpdated={() => servers.invalidate()}
	/>
{/if}

<PeerConfModal
	bind:open={confModalOpen}
	serverId={server.id}
	pubkey={confPubkey}
	peerName={confPeerName}
	kind="system"
	onclose={() => (confModalOpen = false)}
/>

{#if confGeneratorOpen && serverConfig && confGeneratorPeer}
	<ConfGeneratorModal
		bind:open={confGeneratorOpen}
		{serverConfig}
		peer={confGeneratorPeer}
		{ascParams}
		{wanIP}
		onclose={() => {
			confGeneratorOpen = false;
		}}
	/>
{/if}

{#snippet iconBefore()}
	<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<polyline points="15,3 21,3 21,9"/>
		<polyline points="9,21 3,21 3,15"/>
		<line x1="21" y1="3" x2="14" y2="10"/>
		<line x1="3" y1="21" x2="10" y2="14"/>
	</svg>
{/snippet}

{#snippet restartIcon()}
	<RefreshCw size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet addPeerIcon()}
	<Plus size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<style>
	.badge {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 11px;
		font-weight: 500;
		border-radius: 10px;
	}

	.badge-builtin {
		background: var(--color-success-tint);
		color: var(--success);
	}

	.badge-system {
		background: rgba(107, 114, 128, 0.15);
		color: var(--text-secondary);
	}

	.keendns-row {
		margin-top: 0.125rem;
	}

	.keendns-link {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.8125rem;
		color: var(--accent);
		text-decoration: none;
	}

	.keendns-link:hover {
		text-decoration: underline;
	}


	.server-actions {
		display: flex;
		gap: 0.5rem;
	}

</style>
