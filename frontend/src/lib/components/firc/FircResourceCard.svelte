<script lang="ts">
	import { untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { RefreshCw, RotateCw } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { isCardNestedInteraction } from '$lib/utils/cardClick';
	import { notifications } from '$lib/stores/notifications';
	import { getTrafficRates, loadHistory, subscribeTraffic } from '$lib/stores/traffic';
	import {
		stripFircEmojis,
		type FircTunnel,
		type FircTunnelProbeRow,
	} from '$lib/types/firc';
	import {
		fircStatus,
		fircTunnels,
		fircTunnelsState,
		fircGroups,
		fircProbeResults,
		fircInterfaceProbeResults,
		openFircTunnelEditor,
		reloadFircAll,
	} from '$lib/stores/firc';
	import { getFircTunnelDiagnostic } from '$lib/utils/fircDiagnostics';
	import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
	import { Button, IconButton, Modal } from '$lib/components/ui';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';
	import { fircLinkMeta } from './fircLinkMeta';
	import FircResourceTableRow from './FircResourceTableRow.svelte';
	import FircResourceDenseCard from './FircResourceDenseCard.svelte';
	import FircResourceCompactCard from './FircResourceCompactCard.svelte';

	interface Props {
		kind: 'proxy' | 'subscription';
		tunnel: FircTunnel;
		layout?: SingboxLayoutMode;
		renderMode?: TunnelRenderMode;
		autoDelayCheckNonce?: number;
		autoDelayCheckDelayMs?: number;
	}

	let {
		kind,
		tunnel,
		layout = 'compact',
		renderMode = 'compact',
		autoDelayCheckNonce = 0,
		autoDelayCheckDelayMs = 0,
	}: Props = $props();

	let checking = $state(false);
	let deleting = $state(false);
	let confirmDeleteOpen = $state(false);
	let diagnosticsOpen = $state(false);
	let showEndpoint = $state(false);
	let localHistory = $state<number[]>([]);
	let switching = $state(false);
	let pickerOpen = $state(false);
	let busyExtra = $state<'' | 'refresh' | 'restart'>('');

	const title = $derived(stripFircEmojis(tunnel.description || tunnel.id));
	const enabled = $derived(tunnel.enable !== false);
	const live = $derived($fircTunnelsState.find((s) => s.id === tunnel.id));
	const running = $derived(Boolean(enabled && live?.status === 'up'));
	const diagnostic = $derived(
		getFircTunnelDiagnostic({ tunnel, live, status: $fircStatus }),
	);
	const hasSubscriptionSource = $derived(
		Boolean(tunnel.sources?.some((s) => s.kind === 'subscription')),
	);

	const linkMeta = $derived(
		fircLinkMeta(tunnel.sources?.find((s) => s.kind === 'link' && s.link)?.link?.trim() ?? ''),
	);

	const allNodes = $derived.by(() => {
		const liveNodes = live?.nodes ?? [];
		const hiddenKeys = new Set((live?.filteredNodes ?? []).map((n) => n.key));
		const byKey = new Map<string, { key: string; name: string; state: string }>();
		for (const ln of liveNodes) {
			byKey.set(ln.key, {
				key: ln.key,
				name: stripFircEmojis(ln.name || ln.key),
				state: ln.state,
			});
		}
		for (const key of tunnel.order ?? []) {
			if (hiddenKeys.has(key)) continue;
			if (!byKey.has(key)) {
				const rawName = key.includes(':') ? key.slice(key.indexOf(':') + 1) : key;
				let decoded = rawName;
				try {
					decoded = decodeURIComponent(rawName);
				} catch {
					// keep raw
				}
				byKey.set(key, {
					key,
					name: stripFircEmojis(decoded),
					state: (tunnel.exclude ?? []).includes(key) ? 'excluded' : 'reserve',
				});
			}
		}
		const orderIdx = new Map((tunnel.order ?? []).map((k, i) => [k, i]));
		return Array.from(byKey.values()).sort((a, b) => {
			const ia = orderIdx.get(a.key);
			const ib = orderIdx.get(b.key);
			if (ia !== undefined && ib !== undefined) return ia - ib;
			if (ia !== undefined) return -1;
			if (ib !== undefined) return 1;
			return a.name.localeCompare(b.name);
		});
	});

	const liveActiveNodeName = $derived.by(() => {
		if (!running) return '';
		if (live?.active && live.active.length > 0) {
			const first = stripFircEmojis(live.active[0]);
			return live.active.length > 1 ? `${first} (+${live.active.length - 1})` : first;
		}
		const act = (live?.nodes ?? []).filter((n) => n.state === 'active');
		if (act.length > 0) {
			const first = stripFircEmojis(act[0].name || act[0].key);
			return act.length > 1 ? `${first} (+${act.length - 1})` : first;
		}
		return '';
	});

	const preferredNodeName = $derived(liveActiveNodeName || allNodes[0]?.name || '');

	const activeNodeKey = $derived.by(() => {
		const act = (live?.nodes ?? []).find((n) => n.state === 'active');
		if (act) return act.key;
		return allNodes[0]?.key ?? '';
	});

	const subscriptionMembers = $derived(
		allNodes.map((n) => ({
			tag: n.key,
			label: n.name,
			protocol: 'vless',
			server: n.name,
			port: 0,
		})),
	);

	const probeHistory = $derived.by(() => {
		const map = $fircProbeResults[tunnel.id];
		if (!map) return [] as number[];
		const vals: number[] = [];
		for (const r of Object.values(map)) {
			if (r.ok) {
				const ms = r.firstByteMs ?? r.handshakeMs ?? 0;
				if (ms > 0) vals.push(ms);
			}
		}
		return vals;
	});

	const ifaceProbe = $derived(
		tunnel.device ? ($fircInterfaceProbeResults[tunnel.device] ?? null) : null,
	);

	const history = $derived.by(() => {
		if (localHistory.length > 0) return localHistory.slice(-14);
		if (ifaceProbe) {
			return [ifaceProbe.ok && ifaceProbe.latencyMs > 0 ? Math.round(ifaceProbe.latencyMs) : 0];
		}
		return probeHistory.slice(-14);
	});
	const delayPresentation = $derived(singboxDelayFromHistory(history, { running }));
	const cardState = $derived(
		!enabled
			? 'unknown'
			: diagnostic.tone === 'error'
				? 'fail'
				: history.length > 0
					? delayPresentation.state
					: running
						? 'ok'
						: 'slow',
	);
	const latText = $derived(delayPresentation.label);
	const statusVariant = $derived(
		running ? 'success' : !enabled ? 'muted' : diagnostic.tone === 'error' ? 'error' : 'warning',
	);

	const inboundStatuses = $derived.by(() => {
		const liveInbs = live?.inbounds ?? [];
		return (tunnel.inbounds ?? []).map((inb) => {
			const match = liveInbs.find((s) => s.port === inb.port);
			if (!enabled) {
				return { port: inb.port, label: m.firc_inbound_off(), why: '' };
			}
			const st = match?.state ?? '';
			if (st === 'up') {
				return { port: inb.port, label: m.firc_inbound_up(), why: match?.why ?? '' };
			}
			if (st === 'no-node') {
				return { port: inb.port, label: m.firc_inbound_no_node(), why: match?.why ?? '' };
			}
			if (st === 'busy') {
				return { port: inb.port, label: m.firc_inbound_busy(), why: match?.why ?? '' };
			}
			return { port: inb.port, label: m.firc_inbound_off(), why: match?.why ?? '' };
		});
	});
	const listenPortsText = $derived(
		inboundStatuses.length > 0
			? inboundStatuses.map((s) => `:${s.port} (${s.label})`).join(', ')
			: '',
	);
	const stateLabel = $derived(diagnostic.stateLabel);

	const endpointText = $derived(preferredNodeName || tunnel.device);
	const hiddenEndpointText = '••••••••';

	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	let rxTotal = $state(0);
	let txTotal = $state(0);

	$effect(() => {
		const key = tunnel.device;
		if (!key) return;
		const update = () => {
			const rates = getTrafficRates(key);
			if (rates.rx.length > 0 || rates.tx.length > 0) {
				rxRates = rates.rx;
				txRates = rates.tx;
			}
		};
		untrack(update);
		return subscribeTraffic(update);
	});

	let initialHistoryLoadedFor = '';
	$effect(() => {
		const key = tunnel.device;
		if (!key || initialHistoryLoadedFor === key) return;
		initialHistoryLoadedFor = key;
		untrack(() => void loadHistory(key));
	});

	$effect(() => {
		const currentLive = live;
		if (!currentLive) return;
		const rxBps = currentLive.rxBps ?? 0;
		const txBps = currentLive.txBps ?? 0;
		untrack(() => {
			const key = tunnel.device;
			const rates = key ? getTrafficRates(key) : { rx: [], tx: [] };
			if (rates.rx.length === 0 && rates.tx.length === 0) {
				rxRates = [...rxRates.slice(-27), rxBps * 8];
				txRates = [...txRates.slice(-27), txBps * 8];
			}
			rxTotal += rxBps * 2.5;
			txTotal += txBps * 2.5;
		});
	});

	const inlineRxRate = $derived(rxRates.at(-1) ?? (live?.rxBps ?? 0) * 8);
	const inlineTxRate = $derived(txRates.at(-1) ?? (live?.txBps ?? 0) * 8);
	const trafficSparkSeries = $derived.by(() => {
		const count = Math.min(rxRates.length, txRates.length);
		const start = Math.max(0, count - 36);
		return { rx: rxRates.slice(start, count), tx: txRates.slice(start, count) };
	});

	async function triggerCheck(silent = false): Promise<void> {
		if (checking) return;
		checking = true;
		try {
			let best: number | null = null;
			let firstErr = '';
			let probedCount = 0;

			if (running && tunnel.device) {
				const res = await api.fircProbeInterfaces([tunnel.device]);
				const row = res?.results?.[tunnel.device];
				if (row) {
					probedCount = 1;
					fircInterfaceProbeResults.update((all) => ({
						...all,
						[tunnel.device]: { ...row, checkedAt: res.checkedAt },
					}));
					if (row.ok && typeof row.latencyMs === 'number' && row.latencyMs > 0) {
						best = Math.round(row.latencyMs);
					} else if (row.error) {
						firstErr = row.error;
					}
				}
			} else {
				const rows = await api.fircProbeTunnel({ id: tunnel.id, tunnel });
				probedCount = (rows ?? []).length;
				const nextMap: Record<string, FircTunnelProbeRow> = {
					...($fircProbeResults[tunnel.id] ?? {}),
				};
				for (const r of rows ?? []) {
					nextMap[r.key] = r;
					if (r.ok) {
						const ms = r.firstByteMs ?? r.handshakeMs ?? null;
						if (ms !== null && (best === null || ms < best)) best = ms;
					} else if (!firstErr && r.why) {
						firstErr = r.why;
					}
				}
				fircProbeResults.update((all) => ({ ...all, [tunnel.id]: nextMap }));
			}

			localHistory = [...localHistory, best ?? 0].slice(-14);
			if (!silent) {
				if (best !== null) {
					notifications.success(m.firc_resource_check_ok({ title, ms: best }));
				} else {
					const detail = firstErr || (probedCount === 0 ? diagnostic.reason : '');
					notifications.warning(
						detail
							? m.firc_resource_check_no_reply_detail({ title, detail })
							: m.firc_resource_check_no_reply({ title }),
					);
				}
			}
		} catch (error) {
			localHistory = [...localHistory, 0].slice(-14);
			if (!silent) {
				notifications.error(
					error instanceof Error ? error.message : m.firc_resource_check_failed(),
				);
			}
		} finally {
			checking = false;
		}
	}

	let lastAutoDelayCheckNonce = 0;
	$effect(() => {
		const nonce = autoDelayCheckNonce;
		if (nonce <= 0 || nonce === lastAutoDelayCheckNonce || !running) return;
		lastAutoDelayCheckNonce = nonce;
		const timer = setTimeout(() => untrack(() => void triggerCheck(true)), autoDelayCheckDelayMs);
		return () => clearTimeout(timer);
	});

	async function handleToggleEnable(nextVal: boolean): Promise<void> {
		const nextList = $fircTunnels.map((t) =>
			t.id === tunnel.id ? { ...t, enable: nextVal } : t,
		);
		fircTunnels.set(nextList);
		try {
			await api.fircSaveTunnels(nextList);
			await reloadFircAll();
			notifications.success(
				nextVal
					? m.firc_resource_enabled({ device: tunnel.device })
					: m.firc_resource_disabled({ device: tunnel.device }),
			);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_resource_toggle_failed());
		}
	}

	async function selectMember(nodeKey: string): Promise<void> {
		if (!nodeKey || switching) return;
		switching = true;
		try {
			const currentKeys = allNodes.map((n) => n.key);
			const nextOrder = [nodeKey, ...currentKeys.filter((k) => k !== nodeKey)];
			const nextList = $fircTunnels.map((t) =>
				t.id === tunnel.id ? { ...t, order: nextOrder } : t,
			);
			fircTunnels.set(nextList);
			await api.fircSaveTunnels(nextList);
			await reloadFircAll();
			const picked = allNodes.find((n) => n.key === nodeKey);
			notifications.success(m.firc_resource_node_switched({ name: picked?.name || nodeKey }));
		} catch (error) {
			notifications.error(
				error instanceof Error ? error.message : m.firc_resource_node_switch_failed(),
			);
		} finally {
			switching = false;
			pickerOpen = false;
		}
	}

	async function handleRefresh(e: MouseEvent): Promise<void> {
		e.stopPropagation();
		if (busyExtra) return;
		busyExtra = 'refresh';
		try {
			await api.fircRefreshTunnel(tunnel.id);
			await reloadFircAll();
			notifications.success(m.firc_resource_refreshed({ device: tunnel.device }));
		} catch (err: unknown) {
			notifications.error((err instanceof Error ? err.message : undefined) ?? m.firc_resource_refresh_failed());
		} finally {
			busyExtra = '';
		}
	}

	async function handleRestart(e: MouseEvent): Promise<void> {
		e.stopPropagation();
		if (busyExtra) return;
		busyExtra = 'restart';
		try {
			if (!enabled) {
				const nextList = $fircTunnels.map((t) =>
					t.id === tunnel.id ? { ...t, enable: true } : t,
				);
				fircTunnels.set(nextList);
				await api.fircSaveTunnels(nextList);
			} else {
				await api.fircRestartTunnel(tunnel.id);
			}
			await reloadFircAll();
			notifications.success(m.firc_resource_restarted({ device: tunnel.device }));
		} catch (err: unknown) {
			notifications.error((err instanceof Error ? err.message : undefined) ?? m.firc_resource_restart_failed());
		} finally {
			busyExtra = '';
		}
	}

	async function remove(): Promise<void> {
		if (deleting) return;
		const usedIn = $fircGroups
			.filter((g) => g.interface === tunnel.device)
			.map((g) => g.name || g.id);
		if (usedIn.length > 0) {
			notifications.warning(
				m.firc_resource_in_use({ device: tunnel.device, groups: usedIn.join(', ') }),
			);
			confirmDeleteOpen = false;
			return;
		}
		deleting = true;
		try {
			const nextList = $fircTunnels.filter((t) => t.id !== tunnel.id);
			await api.fircSaveTunnels(nextList);
			await reloadFircAll();
			confirmDeleteOpen = false;
			notifications.success(m.firc_resource_deleted({ device: tunnel.device }));
		} catch (error) {
			notifications.error(
				error instanceof Error ? error.message : m.firc_resource_delete_failed(),
			);
		} finally {
			deleting = false;
		}
	}

	function handleEdit(e?: MouseEvent | KeyboardEvent): void {
		if (e && isCardNestedInteraction(e)) return;
		openFircTunnelEditor(tunnel.id, 'nodes');
	}
</script>

{#snippet fircExtraActions()}
	{#if hasSubscriptionSource}
		<IconButton
			disabled={Boolean(busyExtra)}
			onclick={handleRefresh}
			title={m.firc_resource_refresh_title()}
			ariaLabel={m.firc_resource_refresh_title()}
		>
			<RefreshCw size={12} class={busyExtra === 'refresh' ? 'spin' : ''} />
		</IconButton>
	{/if}
	<IconButton
		disabled={Boolean(busyExtra)}
		onclick={handleRestart}
		title={m.firc_resource_restart_title()}
		ariaLabel={m.firc_resource_restart_title()}
	>
		<RotateCw size={12} class={busyExtra === 'restart' ? 'spin' : ''} />
	</IconButton>
{/snippet}

{#if renderMode === 'table'}
	<FircResourceTableRow
		{kind}
		{tunnel}
		{title}
		{statusVariant}
		{running}
		{cardState}
		{latText}
		{checking}
		{history}
		{diagnostic}
		{linkMeta}
		{listenPortsText}
		{endpointText}
		{preferredNodeName}
		{liveActiveNodeName}
		nodeCount={allNodes.length}
		{inlineRxRate}
		{inlineTxRate}
		{trafficSparkSeries}
		{deleting}
		extra={fircExtraActions}
		onCheck={triggerCheck}
		onEdit={handleEdit}
		onTest={() => (diagnosticsOpen = true)}
		onDelete={() => (confirmDeleteOpen = true)}
	/>
{:else if layout === 'dense' || renderMode === 'list-card'}
	<FircResourceDenseCard
		{kind}
		{tunnel}
		{renderMode}
		{title}
		{statusVariant}
		{running}
		{enabled}
		{cardState}
		{latText}
		{checking}
		{history}
		{diagnostic}
		{linkMeta}
		{listenPortsText}
		{endpointText}
		{hiddenEndpointText}
		nodeCount={allNodes.length}
		{inlineRxRate}
		{inlineTxRate}
		{trafficSparkSeries}
		{deleting}
		bind:showEndpoint
		extra={fircExtraActions}
		onCheck={triggerCheck}
		onEdit={handleEdit}
		onTest={() => (diagnosticsOpen = true)}
		onDelete={() => (confirmDeleteOpen = true)}
		onToggle={handleToggleEnable}
	/>
{:else}
	<FircResourceCompactCard
		{kind}
		{tunnel}
		{title}
		{statusVariant}
		{running}
		{enabled}
		{cardState}
		{latText}
		{checking}
		{history}
		{diagnostic}
		{linkMeta}
		{listenPortsText}
		{stateLabel}
		{endpointText}
		{liveActiveNodeName}
		nodeCount={allNodes.length}
		{subscriptionMembers}
		{activeNodeKey}
		{rxRates}
		{txRates}
		{rxTotal}
		{txTotal}
		{deleting}
		bind:showEndpoint
		bind:pickerOpen
		extra={fircExtraActions}
		onCheck={triggerCheck}
		onEdit={handleEdit}
		onTest={() => (diagnosticsOpen = true)}
		onDelete={() => (confirmDeleteOpen = true)}
		onToggle={handleToggleEnable}
		onPickMember={selectMember}
	/>
{/if}

<Modal
	open={confirmDeleteOpen}
	title={kind === 'proxy' ? m.firc_resource_delete_tunnel_title() : m.firc_resource_delete_sub_title()}
	size="sm"
	onclose={() => {
		if (!deleting) confirmDeleteOpen = false;
	}}
>
	<p>{m.firc_resource_delete_confirm_prefix()} <strong>{title}</strong> {m.firc_resource_delete_confirm_suffix({ device: tunnel.device })}</p>
	{#snippet actions()}
		<Button variant="ghost" disabled={deleting} onclick={() => (confirmDeleteOpen = false)}>
			{m.common_cancel()}
		</Button>
		<Button variant="danger" loading={deleting} onclick={remove}>{m.common_delete()}</Button>
	{/snippet}
</Modal>

<TunnelDiagnosticsModal
	open={diagnosticsOpen}
	kind="firc"
	targetId={tunnel.id}
	displayName={title}
	subject={kind === 'subscription' ? 'subscription' : 'tunnel'}
	iface={tunnel.device}
	unavailableReason={!running ? diagnostic.reason || m.tunnel_test_err_not_running() : undefined}
	onclose={() => (diagnosticsOpen = false)}
/>
