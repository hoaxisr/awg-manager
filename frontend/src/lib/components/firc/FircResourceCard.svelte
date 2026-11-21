<script lang="ts">
	import { untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { Eye, EyeOff, RefreshCw, RotateCw } from 'lucide-svelte';
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
	import { formatBitRate, formatBytes } from '$lib/utils/format';
	import {
		Badge,
		Button,
		Modal,
		Toggle,
		TrafficChart,
		TrafficSparkline,
		TunnelListActions,
	} from '$lib/components/ui';
	import {
		TunnelDelaySparkBars,
		TunnelListTrafficCell,
		TunnelMetaText,
		TunnelSingboxPingButton,
		TunnelTitleRow,
	} from '$lib/components/tunnels';
	import SubscriptionMemberPicker from '$lib/components/subscriptions/SubscriptionMemberPicker.svelte';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';

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

	const linkMeta = $derived.by(() => {
		const firstLink =
			tunnel.sources?.find((s) => s.kind === 'link' && s.link)?.link?.trim() ?? '';
		if (!firstLink) {
			return { proto: 'VLESS', security: 'Reality', transport: '' };
		}
		const lower = firstLink.toLowerCase();
		const isLocalFpBridge = /^socks5?:\/\/127\.0\.0\.1:19[1-4]\d\d\b/.test(lower);
		const isFiniteProto =
			lower.startsWith('fp://') ||
			lower.startsWith('finiteproto://') ||
			lower.includes('mode=finiteproto') ||
			lower.includes('type=finiteproto') ||
			isLocalFpBridge;
		let proto = 'VLESS';
		let security = '';
		let transport = '';
		if (isFiniteProto) {
			proto = 'VLESS';
			security = 'TLS';
			transport = 'FiniteProto';
		} else if (
			lower.startsWith('tt://') ||
			((lower.startsWith('https://') || lower.startsWith('http://')) && lower.includes('d='))
		) {
			proto = 'TrustTunnel';
			security = 'TLS';
		} else if (lower.startsWith('trojan://')) {
			proto = 'Trojan';
			security = 'TLS';
		} else if (lower.startsWith('hysteria2://') || lower.startsWith('hy2://')) {
			proto = 'Hysteria2';
			security = 'QUIC';
		} else if (lower.startsWith('ss://')) {
			proto = 'Shadowsocks';
		} else if (lower.startsWith('naive+https://') || lower.startsWith('naive://')) {
			proto = 'Naive';
			security = 'TLS';
		} else if (lower.startsWith('socks5://') || lower.startsWith('socks://')) {
			proto = 'SOCKS5';
		} else {
			proto = 'VLESS';
			if (lower.includes('security=reality') || lower.includes('pbk=')) {
				security = 'Reality';
			} else if (lower.includes('security=tls')) {
				security = 'TLS';
			}
		}
		if (!isFiniteProto) {
			if (lower.includes('type=xhttp') || lower.includes('type=splithttp')) {
				transport = 'XHTTP';
			} else if (lower.includes('type=ws')) {
				transport = 'WS';
			} else if (lower.includes('type=grpc')) {
				transport = 'gRPC';
			} else if (lower.includes('type=httpupgrade')) {
				transport = 'HTTPUpgrade';
			}
		}
		return { proto, security, transport };
	});

	const allNodes = $derived.by(() => {
		const liveNodes = live?.nodes ?? [];
		const byKey = new Map<string, { key: string; name: string; state: string }>();
		for (const ln of liveNodes) {
			byKey.set(ln.key, {
				key: ln.key,
				name: stripFircEmojis(ln.name || ln.key),
				state: ln.state,
			});
		}
		for (const key of tunnel.order ?? []) {
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

			if (running && tunnel.device) {
				const res = await api.fircProbeInterfaces([tunnel.device]);
				const row = res?.results?.[tunnel.device];
				if (row) {
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
					notifications.success(`${title}: ${best} мс`);
				} else {
					const detail = firstErr || ((rows ?? []).length === 0 ? diagnostic.reason : '');
					notifications.warning(
						`${title}: узел не ответил${detail ? ` (${detail})` : ''}`,
					);
				}
			}
		} catch (error) {
			localHistory = [...localHistory, 0].slice(-14);
			if (!silent) {
				notifications.error(
					error instanceof Error ? error.message : 'Не удалось проверить узлы FIRC',
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
				nextVal ? `Туннель ${tunnel.device} включён` : `Туннель ${tunnel.device} отключён`,
			);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка переключения туннеля');
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
			notifications.success(`Приоритетный узел переключён на ${picked?.name || nodeKey}`);
		} catch (error) {
			notifications.error(
				error instanceof Error ? error.message : 'Не удалось переключить узел',
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
			notifications.success(`Подписки ${tunnel.device} обновлены`);
		} catch (err: unknown) {
			notifications.error((err instanceof Error ? err.message : undefined) ?? 'Ошибка обновления подписки');
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
			notifications.success(`Туннель ${tunnel.device} перезапущен`);
		} catch (err: unknown) {
			notifications.error((err instanceof Error ? err.message : undefined) ?? 'Ошибка перезапуска туннеля');
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
				`Туннель ${tunnel.device} используется в группах: ${usedIn.join(', ')}. Сначала смените интерфейс в этих группах.`,
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
			notifications.success(`Туннель ${tunnel.device} удалён`);
		} catch (error) {
			notifications.error(
				error instanceof Error ? error.message : 'Не удалось удалить туннель FIRC',
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
		<button
			type="button"
			class="firc-extra-btn"
			disabled={Boolean(busyExtra)}
			onclick={handleRefresh}
			title="Обновить подписки и источники"
		>
			<RefreshCw size={12} class={busyExtra === 'refresh' ? 'spin' : ''} />
		</button>
	{/if}
	<button
		type="button"
		class="firc-extra-btn"
		disabled={Boolean(busyExtra)}
		onclick={handleRestart}
		title="Перезапустить туннель"
	>
		<RotateCw size={12} class={busyExtra === 'restart' ? 'spin' : ''} />
	</button>
{/snippet}

{#if renderMode === 'table'}
	<tr
		class="sbx-sub-active-row"
		class:ok={cardState === 'ok'}
		class:slow={cardState === 'slow'}
		class:fail={cardState === 'fail'}
	>
		<td class="tunnel-list-cell tunnel-list-cell--delay list-cell list-cell-delay">
			<div class="lc lc-delay">
				<TunnelSingboxPingButton
					layout="list"
					label={latText}
					state={cardState}
					{checking}
					onclick={() => triggerCheck()}
				/>
			</div>
		</td>
		<td class="tunnel-list-cell tunnel-list-cell--name list-cell list-cell-name">
			<div class="table-name">
				<TunnelTitleRow
					{title}
					dotVariant={statusVariant}
					dotPulse={running}
					onTitleClick={handleEdit}
				>
					{#snippet badges()}
						<Badge variant="accent" size="sm">FIRC</Badge>
					{/snippet}
				</TunnelTitleRow>
				<TunnelMetaText mono>
					<span>{tunnel.device}</span>
					{#if listenPortsText}
						<span class="meta-dot" aria-hidden="true">·</span><span>{listenPortsText}</span>
					{/if}
				</TunnelMetaText>
				{#if diagnostic.reason}
					<div
						class="firc-diag-inline"
						class:is-error={diagnostic.tone === 'error'}
						class:is-warning={diagnostic.tone === 'warning'}
						title={diagnostic.reason}
						data-testid="firc-card-diagnostic"
					>
						{diagnostic.reason}
					</div>
				{/if}
			</div>
		</td>
		{#if kind === 'proxy'}
			<td class="tunnel-list-cell list-cell list-cell-badges">
				<div class="badges">
					<span class="badge proto">{linkMeta.proto}</span>
					{#if linkMeta.security}
						<span class="badge reality">{linkMeta.security}</span>
					{/if}
					{#if linkMeta.transport}
						<span class="badge mode">{linkMeta.transport}</span>
					{/if}
				</div>
			</td>
			<td class="tunnel-list-cell list-cell list-cell-run">
				<span class="run-pill" class:run-on={running}>{running ? 'running' : 'stopped'}</span>
			</td>
		{:else}
			<td class="tunnel-list-cell list-cell" colspan="2" title={endpointText}>
				<div class="lc lc-endpoint">
					<div class="lc-endpoint-stack">
						<span class="lc-endpoint-name" title={preferredNodeName}>
							{liveActiveNodeName || preferredNodeName || '—'}
						</span>
						<span class="mono">{allNodes.length} серверов</span>
					</div>
				</div>
			</td>
		{/if}
		<td class="tunnel-list-cell tunnel-list-cell--traffic list-cell list-cell-traffic">
			<div class="lc lc-traffic">
				<TunnelListTrafficCell
					rxRate={inlineRxRate}
					txRate={inlineTxRate}
					rxData={trafficSparkSeries.rx}
					txData={trafficSparkSeries.tx}
				/>
			</div>
		</td>
		<td class="tunnel-list-cell tunnel-list-cell--ping list-cell list-cell-ping-mini">
			<div class="lc">
				<TunnelDelaySparkBars {history} state={cardState} layout="list" onclick={triggerCheck} />
			</div>
		</td>
		<td class="tunnel-list-cell tunnel-list-cell--actions list-cell list-cell-actions col-actions">
			<div class="lc lc-actions">
				<TunnelListActions
					onEdit={() => handleEdit()}
					editLabel="Изменить"
					onTest={() => (diagnosticsOpen = true)}
					testDisabled={!tunnel.device}
					testTitle="Тест скорости и диагностика «{title}»"
					onDelete={() => (confirmDeleteOpen = true)}
					{deleting}
					extra={fircExtraActions}
				/>
			</div>
		</td>
	</tr>
{:else if layout === 'dense' || renderMode === 'list-card'}
	<div
		class="card view-dense card-clickable"
		class:view-list={renderMode === 'list-card'}
		class:ok={cardState === 'ok'}
		class:slow={cardState === 'slow'}
		class:fail={cardState === 'fail'}
		class:unknown={cardState === 'unknown'}
		class:stopped={!enabled}
		role="button"
		tabindex="0"
		onclick={(e) => handleEdit(e)}
		onkeydown={(e) => { if ((e.key === 'Enter' || e.key === ' ') && e.target === e.currentTarget) { e.preventDefault(); handleEdit(e); } }}
	>
		<div class="header header-dense">
			<div class="header-dense-body">
				<div class="title-row-dense">
					<TunnelTitleRow {title} dotVariant={statusVariant} dotPulse={running} dense staticTitle>
						{#snippet badges()}
							<Badge variant="accent" size="sm">FIRC</Badge>
							{#if kind === 'subscription'}
								<Badge variant="accent" size="sm">подписка</Badge>
							{/if}
						{/snippet}
					</TunnelTitleRow>
				</div>
				<div class="meta-tags-dense">
					<span class="iface-dense mono">
						<span>{tunnel.device}</span>
						{#if listenPortsText}
							<span class="meta-dot" aria-hidden="true">·</span><span>{listenPortsText}</span>
						{/if}
					</span>
					<span class="badge proto">{linkMeta.proto}</span>
					{#if linkMeta.security}
						<span class="badge reality">{linkMeta.security}</span>
					{/if}
					{#if linkMeta.transport}
						<span class="badge mode">{linkMeta.transport}</span>
					{/if}
					<span class="badge mode">×{tunnel.active || 1}</span>
				</div>
			</div>
			<!-- svelte-ignore a11y_click_events_have_key_events -->
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div class="dense-toolbar" onclick={(e) => e.stopPropagation()}>
				<div class="dense-toolbar-bottom">
					<TunnelSingboxPingButton
						layout="dense"
						label={latText}
						state={cardState}
						{checking}
						onclick={() => triggerCheck()}
					/>
					<Toggle
						size="sm"
						spinner="none"
						checked={enabled}
						onchange={handleToggleEnable}
					/>
				</div>
			</div>
		</div>

		{#if diagnostic.reason}
			<div
				class="firc-diag-banner firc-diag-banner--dense"
				class:is-error={diagnostic.tone === 'error'}
				class:is-warning={diagnostic.tone === 'warning'}
				class:is-info={diagnostic.tone === 'info'}
				role="status"
				data-testid="firc-card-diagnostic"
			>
				{diagnostic.reason}
			</div>
		{/if}

		{#if renderMode !== 'list-card'}
			<div class="details">
				<div class="details-dense-cols">
					<div class="details-dense-col">
						<div class="kv-stacked-stat">
							<span class="kv-stacked-label">
								{kind === 'proxy' ? 'Сервер' : 'Активный сервер'}
							</span>
							<span class="kv-endpoint">
								<span class="kv-stacked-value" title={endpointText}>
									{showEndpoint || kind === 'subscription' ? endpointText : hiddenEndpointText}
								</span>
								<button
									type="button"
									class="eye-btn"
									onclick={(e) => {
										e.stopPropagation();
										showEndpoint = !showEndpoint;
									}}
									aria-label={showEndpoint ? 'Скрыть' : 'Показать'}
								>
									{#if showEndpoint}
										<Eye size={12} aria-hidden="true" />
									{:else}
										<EyeOff size={12} aria-hidden="true" />
									{/if}
								</button>
							</span>
						</div>
					</div>
				</div>
				<div class="dense-meta-line mono">
					<span>{allNodes.length} серверов</span>
					<span>источн.: {tunnel.sources.length}</span>
				</div>
			</div>
		{/if}

		<div class="actions">
			<TunnelListActions
				variant="labeled"
				onEdit={() => handleEdit()}
				editLabel="Изменить"
				onTest={() => (diagnosticsOpen = true)}
				testDisabled={!tunnel.device}
				testTitle="Тест скорости и диагностика «{title}»"
				onDelete={() => (confirmDeleteOpen = true)}
				{deleting}
				extra={fircExtraActions}
			/>
		</div>

		{#if renderMode !== 'list-card'}
			<div class="charts-dense">
				<div class="traffic-inline">
					<TrafficSparkline
						rxData={trafficSparkSeries.rx}
						txData={trafficSparkSeries.tx}
						responsive
						height={20}
					/>
					<div class="traffic-inline-rates">
						<span class="traffic-inline-rate rx">↓ {formatBitRate(inlineRxRate)}</span>
						<span class="traffic-inline-rate tx">↑ {formatBitRate(inlineTxRate)}</span>
					</div>
				</div>
				<div class="chart-inline delay-inline">
					<div class="chart-inline-head">
						<span class="chart-inline-label">Delay (5 мин)</span>
					</div>
					<TunnelDelaySparkBars
						{history}
						state={cardState}
						layout="dense"
						onclick={triggerCheck}
					/>
				</div>
			</div>
		{/if}
	</div>
{:else}
	<div
		class="card view-compact card-clickable"
		class:ok={cardState === 'ok'}
		class:slow={cardState === 'slow'}
		class:fail={cardState === 'fail'}
		class:unknown={cardState === 'unknown'}
		class:stopped={!enabled}
		role="button"
		tabindex="0"
		onclick={(e) => handleEdit(e)}
		onkeydown={(e) => { if ((e.key === 'Enter' || e.key === ' ') && e.target === e.currentTarget) { e.preventDefault(); handleEdit(e); } }}
	>
		<div class="tunnel-card-intro">
			<div class="title-row">
				<TunnelTitleRow {title} dotVariant={statusVariant} dotPulse={running} staticTitle>
					{#snippet badges()}
						<Badge variant="accent" size="sm">FIRC</Badge>
						{#if kind === 'subscription'}
							<Badge variant="accent" size="sm">подписка</Badge>
						{/if}
					{/snippet}
				</TunnelTitleRow>
				<TunnelSingboxPingButton
					layout="compact"
					label={latText}
					state={cardState}
					{checking}
					onclick={() => triggerCheck()}
				/>
				<!-- svelte-ignore a11y_click_events_have_key_events -->
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div class="toggle-stop" onclick={(e) => e.stopPropagation()}>
					<Toggle
						size="sm"
						spinner="none"
						checked={enabled}
						onchange={handleToggleEnable}
					/>
				</div>
			</div>
			<div class="iface">
				<span>{tunnel.device}</span>
				{#if listenPortsText}
					<span class="meta-dot" aria-hidden="true">·</span><span>SOCKS5 {listenPortsText}</span>
				{/if}
				<span class="meta-dot" aria-hidden="true">·</span><span>{stateLabel}</span>
			</div>
			<div class="badges">
				<span class="badge proto">{linkMeta.proto}</span>
				{#if linkMeta.security}
					<span class="badge reality">{linkMeta.security}</span>
				{/if}
				{#if linkMeta.transport}
					<span class="badge mode">{linkMeta.transport}</span>
				{/if}
				<span class="badge mode">
					{tunnel.by === 'connection'
						? 'По соед.'
						: tunnel.by === 'site'
							? 'По сайту'
							: 'Сайт+Клиент'} · ×{tunnel.active || 1}
				</span>
			</div>
		</div>

		{#if kind === 'subscription'}
			<div class="sub-meta">
				<div>
					<span>{allNodes.length} серверов</span>
					{#if liveActiveNodeName}
						<span>· активен <span class="mono">{liveActiveNodeName}</span></span>
					{/if}
				</div>
				<div>
					<span>источников: {tunnel.sources.length}</span>
					<span>· авторезерв</span>
				</div>
			</div>
		{/if}

		{#if diagnostic.reason}
			<div
				class="firc-diag-banner"
				class:is-error={diagnostic.tone === 'error'}
				class:is-warning={diagnostic.tone === 'warning'}
				class:is-info={diagnostic.tone === 'info'}
				role="status"
				data-testid="firc-card-diagnostic"
			>
				{diagnostic.reason}
			</div>
		{/if}

		{#if kind === 'proxy'}
			<div class="divider divider-dashed"></div>
			<div class="row">
				<span class="item-label">Сервер</span>
				<div class="server-row value">
					{#if showEndpoint}
						<span class="server-text mono">{endpointText}</span>
					{:else}
						<span class="server-hidden">●●●●●●●●</span>
					{/if}
					<button
						class="icon-btn"
						onclick={(e) => {
							e.stopPropagation();
							showEndpoint = !showEndpoint;
						}}
						aria-label={showEndpoint ? 'Скрыть' : 'Показать'}
					>
						{#if showEndpoint}
							<EyeOff size={12} aria-hidden="true" />
						{:else}
							<Eye size={12} aria-hidden="true" />
						{/if}
					</button>
				</div>
			</div>
			<div class="row">
				<span class="item-label">Пул</span>
				<span class="value mono">{allNodes.length} узл. · источн.: {tunnel.sources.length}</span>
			</div>
		{:else}
			<div class="server-section">
				<div class="server-row">
					<span class="item-label">Активный сервер</span>
					<div class="picker-anchor">
						<div class="server-control">
							<button
								class="server-btn"
								onclick={(e) => {
									e.stopPropagation();
									pickerOpen = !pickerOpen;
								}}
								aria-haspopup="listbox"
								aria-expanded={pickerOpen}
								title="Выбрать приоритетный сервер в пуле FIRC"
							>
								<span class="server-text" title={endpointText}>
									{endpointText}
								</span>
								<span class="caret" aria-hidden="true">▾</span>
							</button>
							<button
								type="button"
								class="eye-btn"
								onclick={(e) => {
									e.stopPropagation();
									showEndpoint = !showEndpoint;
								}}
								title={showEndpoint ? 'Скрыть' : 'Показать'}
								aria-label={showEndpoint ? 'Скрыть' : 'Показать'}
							>
								{#if showEndpoint}
									<Eye size={14} aria-hidden="true" />
								{:else}
									<EyeOff size={14} aria-hidden="true" />
								{/if}
							</button>
						</div>
						{#if pickerOpen}
							<SubscriptionMemberPicker
								members={subscriptionMembers}
								activeMemberTag={activeNodeKey}
								onPick={selectMember}
								onClose={() => (pickerOpen = false)}
							/>
						{/if}
					</div>
				</div>
			</div>
		{/if}

		<div class="actions actions--bar">
			<TunnelListActions
				variant="labeled"
				onEdit={() => handleEdit()}
				editLabel="Изменить"
				onTest={() => (diagnosticsOpen = true)}
				testDisabled={!tunnel.device}
				testTitle="Тест скорости и диагностика «{title}»"
				onDelete={() => (confirmDeleteOpen = true)}
				{deleting}
				extra={fircExtraActions}
			/>
		</div>

		<div class="chart-section">
			<div class="chart-body">
				<div class="chart-head"><span>Delay (5 мин)</span></div>
				<TunnelDelaySparkBars
					{history}
					state={cardState}
					layout="compact"
					onclick={triggerCheck}
				/>
				<div class="chart-head traffic-head">
					<span>Трафик</span>
					<span class="stats">
						↓ {formatBytes(rxTotal)} · ↑ {formatBytes(txTotal)}
					</span>
				</div>
				<TrafficChart
					{rxRates}
					{txRates}
					{rxTotal}
					{txTotal}
					height={56}
					onclick={handleEdit}
				/>
			</div>
		</div>
	</div>
{/if}

<Modal
	open={confirmDeleteOpen}
	title={kind === 'proxy' ? 'Удалить туннель FIRC?' : 'Удалить подписку FIRC?'}
	size="sm"
	onclose={() => {
		if (!deleting) confirmDeleteOpen = false;
	}}
>
	<p>Удалить <strong>{title}</strong> ({tunnel.device}) из конфигурации FIRC?</p>
	{#snippet actions()}
		<Button variant="ghost" disabled={deleting} onclick={() => (confirmDeleteOpen = false)}>
			Отмена
		</Button>
		<Button variant="danger" loading={deleting} onclick={remove}>Удалить</Button>
	{/snippet}
</Modal>

<TunnelDiagnosticsModal
	open={diagnosticsOpen}
	kind="firc"
	targetId={tunnel.id}
	displayName={title}
	subjectLabel={kind === 'subscription' ? 'подписку' : 'туннель'}
	iface={tunnel.device}
	unavailableReason={!running ? diagnostic.reason || 'Туннель не запущен' : undefined}
	onclose={() => (diagnosticsOpen = false)}
/>

<style>
	.card {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px 14px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		transition: border-color var(--t-fast) ease;
	}
	.card.ok {
		border-color: var(--color-success-border);
	}
	.card.slow {
		border-color: var(--color-warning-border);
	}
	.card.fail {
		border-color: var(--color-error-border);
	}
	.card.unknown {
		border-color: var(--color-border);
	}
	.card.stopped {
		opacity: 0.72;
	}

	.card.card-clickable {
		cursor: pointer;
	}
	.card.card-clickable:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.tunnel-card-intro {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}
	.title-row :global(.tunnel-title-row) {
		flex: 1;
		min-width: 0;
	}
	.title-row :global(.ping-btn) {
		flex-shrink: 0;
		margin-left: auto;
	}
	.title-row :global(.badge) {
		flex-shrink: 0;
	}
	.toggle-stop {
		display: inline-flex;
		align-items: center;
		flex-shrink: 0;
	}

	.iface {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	.meta-dot {
		margin: 0 0.35em;
		opacity: 0.75;
	}

	.badges {
		display: flex;
		gap: 0.4rem;
		flex-wrap: wrap;
	}
	.badge {
		font-size: var(--sbx-card-badge);
		padding: 2px 8px;
		border-radius: 10px;
		font-weight: 500;
	}
	.badge.proto {
		background: rgba(88, 166, 255, 0.15);
		color: var(--color-accent);
	}
	.badge.reality {
		background: rgba(210, 153, 34, 0.15);
		color: #d29922;
	}
	.badge.mode {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}

	.sub-meta {
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		line-height: 1.35;
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.firc-diag-banner {
		padding: 6px 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
		font-size: var(--sbx-card-meta);
		line-height: 1.35;
		overflow-wrap: anywhere;
	}
	.firc-diag-banner--dense {
		padding: 4px 6px;
		font-size: 10px;
	}
	.firc-diag-banner.is-error {
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
		color: var(--color-error);
	}
	.firc-diag-banner.is-warning {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}
	.firc-diag-banner.is-info {
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
		color: var(--color-info);
	}
	.firc-diag-inline {
		font-size: var(--sbx-card-note);
		line-height: 1.3;
		color: var(--color-text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 340px;
	}
	.firc-diag-inline.is-error {
		color: var(--color-error);
	}
	.firc-diag-inline.is-warning {
		color: var(--color-warning);
	}

	.divider {
		height: 0;
		border: none;
		margin: 0;
		background: none;
	}
	.divider-dashed {
		border-top: 1px dashed var(--color-border);
	}

	.row {
		display: flex;
		align-items: center;
		margin: 0;
	}
	.row .item-label {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		width: 60px;
		flex-shrink: 0;
	}
	.row .value {
		font-size: var(--sbx-card-value);
		color: var(--color-text-secondary);
		font-family: var(--font-mono, monospace);
	}
	.server-row {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: 1;
	}
	.server-hidden {
		color: var(--color-text-muted);
		letter-spacing: 2px;
	}
	.server-text {
		font-family: var(--font-mono, monospace);
	}
	.icon-btn {
		background: none;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 2px;
		display: inline-flex;
	}
	.icon-btn:hover {
		color: var(--color-text-primary);
	}

	.server-section {
		padding-top: 8px;
		border-top: 1px dashed var(--color-border);
	}
	.server-section .server-row {
		display: grid;
		grid-template-columns: max-content minmax(0, 1fr);
		gap: 0.45rem;
		align-items: center;
		margin: 0;
	}
	.server-section .item-label {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.picker-anchor {
		position: relative;
		min-width: 0;
	}
	.server-control {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		min-width: 0;
	}
	.server-btn {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		width: 100%;
		min-width: 0;
		padding: 0.4rem 0.55rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font: inherit;
		font-size: var(--sbx-card-value);
		color: var(--color-text-primary);
		cursor: pointer;
	}
	.server-btn:hover {
		border-color: var(--color-accent);
	}
	.server-text {
		font-size: var(--sbx-card-value);
		overflow: hidden;
		display: block;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.caret {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-note);
	}
	.eye-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex: 0 0 auto;
		padding: 0.35rem;
		border: none;
		background: none;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: color var(--t-fast) ease;
	}
	.eye-btn:hover {
		color: var(--color-text-secondary);
	}

	.actions--bar {
		margin-top: 0.15rem;
	}

	.firc-extra-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0.25rem 0.45rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition:
			background var(--t-fast) ease,
			border-color var(--t-fast) ease,
			color var(--t-fast) ease;
	}
	.firc-extra-btn:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}
	.firc-extra-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.chart-head {
		display: flex;
		justify-content: space-between;
		font-size: var(--sbx-card-label);
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.chart-head .stats {
		font-size: var(--sbx-card-value);
	}
	.chart-head.traffic-head .stats {
		font-size: 0.6875rem;
	}
	.stats {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	.chart-section {
		margin: 0 -14px -12px;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--color-bg-secondary);
		overflow: hidden;
	}
	.chart-body {
		padding: 8px 12px 10px;
	}
	.chart-body :global(.tunnel-delay-spark--compact) {
		height: 36px;
	}
	.traffic-head {
		margin-top: 8px;
	}

	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}

	/* Dense Mode */
	.card.view-dense {
		gap: 8px;
		padding: 10px 12px;
	}
	.header-dense {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: flex-start;
		gap: 6px;
	}
	.header-dense-body {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}
	.title-row-dense {
		display: flex;
		align-items: center;
		gap: 5px;
		min-width: 0;
	}
	.meta-tags-dense {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		margin-top: 3px;
		gap: 3px;
		min-width: 0;
	}
	.iface-dense {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0;
		font-size: 9px;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-muted);
		min-width: 0;
	}
	.dense-toolbar {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		flex-shrink: 0;
	}
	.dense-toolbar-bottom {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.card.view-dense .details {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 4px 0 8px;
		border-top: 1px solid var(--color-border);
		border-bottom: 1px solid var(--color-border);
	}
	.details-dense-cols {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 10px 10px;
		align-items: start;
	}
	.details-dense-col {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}
	.kv-stacked-stat {
		display: flex;
		flex-direction: row;
		align-items: center;
		gap: 0.35rem;
		min-width: 0;
	}
	.kv-stacked-label {
		font-size: 9px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		flex: 0 0 auto;
	}
	.kv-endpoint {
		display: flex;
		align-items: center;
		gap: 2px;
		flex: 1 1 auto;
		min-width: 0;
	}
	.kv-stacked-value {
		font-size: 10px;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.dense-meta-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem 0.75rem;
		min-width: 0;
		font-size: 9px;
		line-height: 1.25;
		color: var(--color-text-muted);
	}
	.charts-dense {
		display: flex;
		flex-direction: row;
		align-items: stretch;
		gap: 4px;
		width: 100%;
		min-width: 0;
	}
	.charts-dense > .delay-inline,
	.charts-dense > .traffic-inline {
		flex: 1 1 0;
		min-width: 0;
	}
	.chart-inline {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
		padding: 5px 6px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}
	.chart-inline.delay-inline {
		gap: 0;
		padding: 0;
		overflow: hidden;
	}
	.chart-inline.delay-inline .chart-inline-head {
		padding: 5px 6px 3px;
	}
	.charts-dense .traffic-inline {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		padding: 5px 4px 5px 5px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}
	.charts-dense .traffic-inline-rates {
		display: flex;
		flex-direction: column;
		gap: 0.06rem;
		flex: 0 0 auto;
		font-size: 9px;
		line-height: 1.1;
		font-family: var(--font-mono, monospace);
	}
	.charts-dense .traffic-inline-rate.rx {
		color: var(--color-accent);
	}
	.charts-dense .traffic-inline-rate.tx {
		color: var(--color-success);
	}
	.chart-inline-head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 6px;
		font-size: 9px;
		line-height: 1.2;
	}
	.chart-inline-label {
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		font-weight: 500;
	}

	/* Table Row */
	.sbx-sub-active-row {
		cursor: pointer;
	}
	.lc {
		display: flex;
		align-items: center;
		min-width: 0;
		font-size: var(--sbx-card-value);
		color: var(--color-text-secondary);
		vertical-align: middle;
	}
	.lc-delay {
		gap: 0.35rem;
		min-width: 0;
	}
	.lc-endpoint {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		min-width: 0;
		overflow: hidden;
	}
	.lc-endpoint-stack {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.12rem;
		min-width: 0;
		flex: 1;
	}
	.lc-endpoint-name {
		width: 100%;
		font-size: var(--sbx-card-value);
		font-weight: 500;
		color: var(--color-text-primary);
		line-height: 1.2;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.lc-actions {
		flex-wrap: nowrap;
		gap: 0.375rem;
		justify-content: center;
		align-items: center;
		white-space: nowrap;
	}
	.table-name {
		display: grid;
		gap: 2px;
	}
	.run-pill {
		display: inline-flex;
		border-radius: 999px;
		padding: 2px 7px;
		background: var(--color-muted-bg);
		font-size: 10px;
	}
	.run-pill.run-on {
		background: var(--color-success-bg);
		color: var(--color-success);
	}
</style>
