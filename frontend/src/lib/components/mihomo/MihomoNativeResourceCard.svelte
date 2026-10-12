<script lang="ts">
	import { goto } from '$app/navigation';
	import { untrack } from 'svelte';
	import { Eye, EyeOff } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { isCardNestedInteraction } from '$lib/utils/cardClick';
	import { notifications } from '$lib/stores/notifications';
	import { mihomoNativeResources } from '$lib/stores/mihomoNative';
	import { singboxTraffic } from '$lib/stores/singbox';
	import { getTrafficRates, loadHistory, subscribeTraffic } from '$lib/stores/traffic';
	import type {
		MihomoNativeProxy,
		MihomoNativeSubscription,
		MihomoRuntimeProvider,
		MihomoRuntimeProxy,
	} from '$lib/types';
	import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
	import { formatBitRate, formatBytes, formatRelativeTime } from '$lib/utils/format';
	import { Badge, Button, Modal, TrafficChart, TrafficSparkline, TunnelListActions } from '$lib/components/ui';
	import {
		TunnelDelaySparkBars,
		TunnelListTrafficCell,
		TunnelMetaText,
		TunnelSingboxPingButton,
		TunnelTitleRow,
	} from '$lib/components/tunnels';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';
	import MihomoNativeEditModal from './MihomoNativeEditModal.svelte';
	import SubscriptionMemberPicker from '$lib/components/subscriptions/SubscriptionMemberPicker.svelte';

	interface Props {
		kind: 'proxy' | 'subscription';
		proxy?: MihomoNativeProxy;
		subscription?: MihomoNativeSubscription;
		runtimeProxies: Record<string, MihomoRuntimeProxy>;
		runtimeProviders: Record<string, MihomoRuntimeProvider>;
		layout?: SingboxLayoutMode;
		renderMode?: TunnelRenderMode;
		autoDelayCheckNonce?: number;
		autoDelayCheckDelayMs?: number;
	}

	let {
		kind,
		proxy,
		subscription,
		runtimeProxies,
		runtimeProviders,
		layout = 'compact',
		renderMode = 'compact',
		autoDelayCheckNonce = 0,
		autoDelayCheckDelayMs = 0,
	}: Props = $props();

	let checking = $state(false);
	let deleting = $state(false);
	let confirmDeleteOpen = $state(false);
	let editOpen = $state(false);
	let diagnosticsOpen = $state(false);
	let showEndpoint = $state(false);
	let localHistory = $state<number[]>([]);
	let switching = $state(false);
	let pickerOpen = $state(false);

	const title = $derived(kind === 'proxy' ? proxy?.name ?? 'Mihomo proxy' : subscription?.name ?? 'Mihomo subscription');
	const groupName = $derived(subscription?.groupName ?? '');
	const groupRuntime = $derived(groupName ? runtimeProxies[groupName] : undefined);
	const provider = $derived.by(() => {
		const name = subscription?.providerName;
		if (!name) return undefined;
		return runtimeProviders[name] ?? Object.values(runtimeProviders).find((item) => item.name === name);
	});
	const members = $derived.by(() => {
		const values = groupRuntime?.all?.length
			? groupRuntime.all
			: (provider?.proxies ?? []).map((item) => item.name);
		return [...new Set(values.filter(Boolean))];
	});
	const activeName = $derived(kind === 'proxy' ? proxy?.name ?? '' : groupRuntime?.now || members[0] || '');
	const activeRuntime = $derived.by(() => {
		if (!activeName) return undefined;
		return runtimeProxies[activeName] ?? provider?.proxies?.find((item) => item.name === activeName);
	});
	const enabled = $derived(kind === 'proxy' ? proxy?.enabled !== false : subscription?.enabled !== false);
	const bridge = $derived(kind === 'proxy' ? proxy?.bridge : subscription?.bridge);
	const proxyIface = $derived(bridge?.proxyInterface || (Number.isInteger(bridge?.proxyIndex) && (bridge?.proxyIndex ?? -1) >= 0 ? `Proxy${bridge?.proxyIndex}` : 'NDMS Proxy'));
	const kernelIface = $derived(bridge?.kernelInterface || '');
	const listenPort = $derived(bridge?.listenPort ? `:${bridge.listenPort}` : '');

	const diagnosticsUnavailable = $derived.by(() => {
		if (!enabled) return 'Ресурс выключен. Включите его и сохраните настройки перед тестированием.';
		if (!bridge?.kernelInterface) return 'Для ресурса ещё не создан kernel-интерфейс NDMS Proxy.';
		if (!activeRuntime || activeRuntime.alive === false) return 'Ресурс недоступен в работающем runtime Mihomo.';
		return undefined;
	});
	const running = $derived(Boolean(enabled && activeRuntime && activeRuntime.alive !== false));
	const runtimeHistory = $derived((activeRuntime?.history ?? []).map((item) => item.delay).filter((delay) => Number.isFinite(delay)));
	const history = $derived([...runtimeHistory, ...localHistory].slice(-14));
	const delayPresentation = $derived(singboxDelayFromHistory(history, { running }));
	const cardState = $derived(delayPresentation.state);
	const latText = $derived(delayPresentation.label);
	const statusVariant = $derived(running ? 'success' : enabled ? 'warning' : 'muted');

	const activeSubMember = $derived.by(() => {
		if (kind !== 'subscription' || !subscription?.members?.length) return undefined;
		return subscription.members.find((m) => m.tag === activeName || m.label === activeName);
	});

	const nativeConfig = $derived(proxy?.nativeConfig ?? {});
	const server = $derived.by(() => {
		if (kind === 'proxy') return String(nativeConfig.server ?? '');
		return activeSubMember?.server ?? '';
	});
	const port = $derived.by(() => {
		if (kind === 'proxy') return Number(nativeConfig.port ?? 0);
		return activeSubMember?.port ?? 0;
	});
	const sni = $derived.by(() => {
		if (kind === 'proxy') return String(nativeConfig.sni || nativeConfig.servername || '');
		return activeSubMember?.sni ?? '';
	});
	const protocol = $derived.by(() => {
		if (kind === 'proxy') {
			const p = proxy?.protocol || activeRuntime?.type || 'proxy';
			return p.toUpperCase();
		}
		const p = activeSubMember?.protocol || activeRuntime?.type || 'VLESS';
		return p.toUpperCase();
	});
	const transport = $derived.by(() => {
		if (kind === 'proxy') {
			const tr = proxy?.transport || '';
			return tr && tr.toLowerCase() !== 'tcp' ? tr.toUpperCase() : '';
		}
		const tr = activeSubMember?.transport || '';
		return tr && tr.toLowerCase() !== 'tcp' ? tr.toUpperCase() : '';
	});
	const isURLTest = $derived(subscription?.mode === 'url-test');
	const realityEnabled = $derived(Boolean(nativeConfig['reality-opts'] || activeSubMember?.security === 'reality' || activeName.toLowerCase().includes('reality')));
	const tlsEnabled = $derived(Boolean(nativeConfig.tls || activeSubMember?.security === 'tls' || activeName.toLowerCase().includes('tls')));
	const trafficKey = $derived(kind === 'proxy' ? proxy?.name ?? '' : groupName || activeName);
	const traffic = $derived(trafficKey ? $singboxTraffic.get(trafficKey) : undefined);

	const listActiveServerName = $derived(
		kind === 'proxy'
			? (proxy?.name ?? '')
			: (activeSubMember?.label?.trim() || activeSubMember?.tag?.trim() || activeName)
	);
	const endpointText = $derived.by(() => {
		if (kind === 'proxy') {
			return port > 0 ? `${server}:${port}` : server;
		}
		if (activeSubMember?.server) {
			return activeSubMember.port > 0 ? `${activeSubMember.server}:${activeSubMember.port}` : activeSubMember.server;
		}
		return activeName;
	});
	const hiddenEndpointText = $derived.by(() => {
		if (kind === 'proxy') {
			return port > 0 ? `••••••••:${port}` : '••••••••';
		}
		if (activeSubMember?.server) {
			return activeSubMember.port > 0 ? `••••••••:${activeSubMember.port}` : '••••••••';
		}
		return '••••••••';
	});
	const activeEndpointTitle = $derived(
		listActiveServerName ? `${listActiveServerName} · ${endpointText}` : endpointText
	);
	const subscriptionMembers = $derived.by(() => {
		if (subscription?.members?.length) return subscription.members;
		return members.map((name) => ({
			tag: name,
			label: name,
			protocol: 'vless',
			server: name,
			port: 0,
		}));
	});

	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	$effect(() => {
		const key = trafficKey;
		if (!key) return;
		const update = () => {
			const rates = getTrafficRates(key);
			rxRates = rates.rx;
			txRates = rates.tx;
		};
		update();
		return subscribeTraffic(update);
	});
	$effect(() => {
		const key = trafficKey;
		if (key) untrack(() => void loadHistory(key));
	});
	const inlineRxRate = $derived(rxRates.at(-1) ?? 0);
	const inlineTxRate = $derived(txRates.at(-1) ?? 0);
	const trafficSparkSeries = $derived.by(() => {
		const count = Math.min(rxRates.length, txRates.length);
		const start = Math.max(0, count - 36);
		return { rx: rxRates.slice(start, count), tx: txRates.slice(start, count) };
	});

	const lastFetchedHuman = $derived.by(() => {
		const raw = provider?.updatedAt || subscription?.lastFetched;
		if (!raw || raw.startsWith('0001')) return '—';
		return formatRelativeTime(raw);
	});

	async function triggerCheck(): Promise<void> {
		if (checking || !activeName) return;
		checking = true;
		try {
			const delayTarget = kind === 'subscription' && groupName ? groupName : activeName;
			const delay = await api.mihomoRuntimeDelay(delayTarget);
			localHistory = [...localHistory, delay].slice(-14);
		} catch (error) {
			localHistory = [...localHistory, 0].slice(-14);
			notifications.error(error instanceof Error ? error.message : 'Не удалось проверить Mihomo proxy');
		} finally {
			checking = false;
		}
	}

	let lastAutoDelayCheckNonce = 0;
	$effect(() => {
		const nonce = autoDelayCheckNonce;
		if (nonce <= 0 || nonce === lastAutoDelayCheckNonce || !running) return;
		lastAutoDelayCheckNonce = nonce;
		const timer = setTimeout(() => untrack(() => void triggerCheck()), autoDelayCheckDelayMs);
		return () => clearTimeout(timer);
	});

	async function selectMember(name: string): Promise<void> {
		if (!groupName || !name || name === activeName || switching) return;
		switching = true;
		try {
			await api.mihomoRuntimeSelect(groupName, name);
			await mihomoNativeResources.refetch();
			notifications.success(`Mihomo переключён на ${name}`);
		} catch (error) {
			notifications.error(error instanceof Error ? error.message : 'Не удалось переключить сервер');
		} finally {
			switching = false;
		}
	}

	async function remove(): Promise<void> {
		if (deleting) return;
		deleting = true;
		try {
			if (kind === 'proxy' && proxy) await api.mihomoNativeDeleteProxy(proxy.id);
			else if (subscription) await api.mihomoNativeDeleteSubscription(subscription.id);
			await mihomoNativeResources.refetch();
			confirmDeleteOpen = false;
			notifications.success(kind === 'proxy' ? 'Прокси-туннель Mihomo удалён' : 'Подписка Mihomo удалена');
		} catch (error) {
			notifications.error(error instanceof Error ? error.message : 'Не удалось удалить ресурс Mihomo');
		} finally {
			deleting = false;
		}
	}

	async function handleUpdated(): Promise<void> {
		await mihomoNativeResources.refetch();
	}

	function handleEdit(e?: MouseEvent | KeyboardEvent): void {
		if (e && isCardNestedInteraction(e)) return;
		if (kind === 'subscription' && subscription) {
			goto(`/subscriptions/${encodeURIComponent(subscription.id)}?engine=mihomo`);
			return;
		}
		editOpen = true;
	}
</script>

{#if renderMode === 'table'}
	<tr class="mihomo-table-row sbx-sub-active-row" class:ok={cardState === 'ok'} class:slow={cardState === 'slow'} class:fail={cardState === 'fail'}>
		<td class="lc lc-delay"><TunnelSingboxPingButton layout="list" label={latText} state={cardState} {checking} onclick={triggerCheck} /></td>
		<td>
			<div class="table-name">
				<TunnelTitleRow {title} dotVariant={statusVariant} dotPulse={running} onTitleClick={handleEdit} />
				<TunnelMetaText mono>
					{#if proxyIface}
						<span>{proxyIface}</span>
						{#if kernelIface}<span class="meta-dot" aria-hidden="true">·</span><span>{kernelIface}</span>{/if}
						{#if listenPort}<span class="meta-dot" aria-hidden="true">·</span><span>{listenPort}</span>{/if}
					{:else}
						<span>NDMS-мост создаётся при применении</span>
					{/if}
				</TunnelMetaText>
			</div>
		</td>
		{#if kind === 'proxy'}
			<td><div class="badges"><span class="badge proto">{protocol}</span>{#if transport}<span class="badge transport">{transport}</span>{/if}</div></td>
			<td><span class="run-pill" class:run-on={running}>{running ? 'running' : 'stopped'}</span></td>
		{:else}
			<td class="lc lc-endpoint" title={activeEndpointTitle}>
				<div class="lc-endpoint-stack">
					{#if listActiveServerName}
						<span class="lc-endpoint-name" title={listActiveServerName}>{listActiveServerName}</span>
					{/if}
					<span class="mono">{showEndpoint ? endpointText : hiddenEndpointText}</span>
				</div>
			</td>
		{/if}
		<td class="lc lc-traffic"><TunnelListTrafficCell rxRate={inlineRxRate} txRate={inlineTxRate} rxData={trafficSparkSeries.rx} txData={trafficSparkSeries.tx} /></td>
		<td class="lc"><TunnelDelaySparkBars {history} state={cardState} layout="list" onclick={triggerCheck} /></td>
		<td class="lc-actions col-actions"><TunnelListActions onEdit={() => handleEdit()} editLabel="Изменить" onTest={() => diagnosticsOpen = true} onDelete={() => confirmDeleteOpen = true} {deleting} /></td>
	</tr>
{:else if layout === 'dense' || renderMode === 'list-card'}
	<div class="card view-dense card-clickable" class:view-list={renderMode === 'list-card'} class:ok={cardState === 'ok'} class:slow={cardState === 'slow'} class:fail={cardState === 'fail'} class:unknown={cardState === 'unknown'} class:stopped={!enabled} role="button" tabindex="0" onclick={(e) => handleEdit(e)}>
		<div class="header header-dense">
			<div class="header-dense-body">
				<div class="title-row-dense">
					<TunnelTitleRow {title} dotVariant={statusVariant} dotPulse={running} dense staticTitle>
						{#snippet badges()}
							<Badge variant="accent" size="sm">Mihomo</Badge>
							{#if kind === 'subscription'}
								<Badge variant="accent" size="sm">{subscription?.format === 'share-links' ? 'группа' : 'подписка'}</Badge>
							{/if}
						{/snippet}
					</TunnelTitleRow>
				</div>
				<div class="meta-tags-dense">
					<span class="iface-dense mono">
						{#if proxyIface}
							<span>{proxyIface}</span>
							{#if kernelIface}<span class="meta-dot" aria-hidden="true">·</span><span>{kernelIface}</span>{/if}
							{#if listenPort}<span class="meta-dot" aria-hidden="true">·</span><span>{listenPort}</span>{/if}
						{:else}
							<span>NDMS-мост</span>
						{/if}
					</span>
					<span class="badge proto">{protocol}</span>
					{#if transport}<span class="badge transport">{transport}</span>{/if}
					{#if realityEnabled}
						<span class="badge reality">Reality</span>
					{:else if tlsEnabled}
						<span class="badge tls">TLS</span>
					{/if}
					{#if kind === 'subscription'}
						<span class="badge mode">{isURLTest ? 'URLTest' : 'Selector'}</span>
					{/if}
				</div>
			</div>
			<div class="dense-toolbar">
				<div class="dense-toolbar-bottom">
					<TunnelSingboxPingButton layout="dense" label={latText} state={cardState} {checking} onclick={triggerCheck} />
				</div>
			</div>
		</div>

		{#if renderMode !== 'list-card'}
			<div class="details">
				{#if subscription?.lastError}<div class="sub-error mono">{subscription.lastError}</div>{/if}
				<div class="details-dense-cols">
					<div class="details-dense-col">
						<div class="kv-stacked-stat">
							<span class="kv-stacked-label">{kind === 'proxy' ? 'Сервер' : (isURLTest ? 'Авто' : 'Активный сервер')}</span>
							<span class="kv-endpoint">
								<span class="kv-stacked-value" title={showEndpoint ? activeEndpointTitle : (listActiveServerName || hiddenEndpointText)}>
									{#if showEndpoint}
										{endpointText}
									{:else if listActiveServerName}
										{listActiveServerName}
									{:else}
										{hiddenEndpointText}
									{/if}
								</span>
								<button type="button" class="eye-btn" onclick={(e) => { e.stopPropagation(); showEndpoint = !showEndpoint; }} aria-label={showEndpoint ? 'Скрыть IP' : 'Показать IP'}>
									{#if showEndpoint}<Eye size={12} aria-hidden="true" />{:else}<EyeOff size={12} aria-hidden="true" />{/if}
								</button>
							</span>
						</div>
					</div>
				</div>
				{#if kind === 'subscription'}
					<div class="dense-meta-line mono">
						<span>{members.length} серверов</span>
						<span>обновлено: {lastFetchedHuman}</span>
					</div>
				{/if}
			</div>
		{/if}

		<div class="actions">
			<TunnelListActions variant="labeled" onEdit={() => handleEdit()} editLabel="Изменить" onTest={() => diagnosticsOpen = true} onDelete={() => confirmDeleteOpen = true} {deleting} />
		</div>

		{#if renderMode !== 'list-card'}
			<div class="charts-dense">
				<div class="traffic-inline">
					<TrafficSparkline rxData={trafficSparkSeries.rx} txData={trafficSparkSeries.tx} responsive height={20} />
					<div class="traffic-inline-rates">
						<span class="traffic-inline-rate rx">↓ {formatBitRate(inlineRxRate)}</span>
						<span class="traffic-inline-rate tx">↑ {formatBitRate(inlineTxRate)}</span>
					</div>
				</div>
				<div class="chart-inline delay-inline">
					<div class="chart-inline-head"><span class="chart-inline-label">Delay (5 мин)</span></div>
					<TunnelDelaySparkBars {history} state={cardState} layout="dense" onclick={triggerCheck} />
				</div>
			</div>
		{/if}
	</div>
{:else}
	<div class="card view-compact card-clickable" class:ok={cardState === 'ok'} class:slow={cardState === 'slow'} class:fail={cardState === 'fail'} class:unknown={cardState === 'unknown'} class:stopped={!enabled} role="button" tabindex="0" onclick={(e) => handleEdit(e)}>
		<div class="tunnel-card-intro">
			<div class="title-row">
				<TunnelTitleRow {title} dotVariant={statusVariant} dotPulse={running} staticTitle>
					{#snippet badges()}
						<Badge variant="accent" size="sm">Mihomo</Badge>
						{#if kind === 'subscription'}
							<Badge variant="accent" size="sm">{subscription?.format === 'share-links' ? 'группа' : 'подписка'}</Badge>
						{/if}
					{/snippet}
				</TunnelTitleRow>
				<TunnelSingboxPingButton layout="compact" label={latText} state={cardState} {checking} onclick={triggerCheck} />
			</div>
			<div class="iface">
				{#if proxyIface}
					<span>{proxyIface}</span>
					{#if kernelIface}<span class="meta-dot" aria-hidden="true">·</span><span>{kernelIface}</span>{/if}
				{:else}
					<span>NDMS-мост создаётся при применении</span>
				{/if}
				{#if listenPort}<span class="meta-dot" aria-hidden="true">·</span><span>{listenPort}</span>{/if}
			</div>
			<div class="badges">
				<span class="badge proto">{protocol}</span>
				{#if transport}<span class="badge transport">{transport}</span>{/if}
				{#if realityEnabled}
					<span class="badge reality">Reality</span>
				{:else if tlsEnabled}
					<span class="badge tls">TLS</span>
				{/if}
			</div>
		</div>

		{#if kind === 'subscription'}
			<div class="sub-meta">
				<div>
					<span>{members.length} серверов</span>
					{#if activeName}
						<span>· активен <span class="mono">{listActiveServerName || activeName}</span></span>
					{/if}
				</div>
				<div>
					<span>обновлено {lastFetchedHuman}</span>
					{#if (subscription?.refreshHours ?? 0) > 0}
						<span>· auto {subscription?.refreshHours}ч</span>
					{/if}
				</div>
			</div>
		{/if}

		{#if subscription?.lastError}<div class="sub-error mono">{subscription.lastError}</div>{/if}

		{#if kind === 'proxy'}
			<div class="divider divider-dashed"></div>
			<div class="row">
				<span class="label">Сервер</span>
				<div class="server-row value">
					{#if showEndpoint}
						<span class="server-text mono">{server || '—'}</span>
					{:else}
						<span class="server-hidden">●●●●●●●●</span>
					{/if}
					<button class="icon-btn" onclick={(e) => { e.stopPropagation(); showEndpoint = !showEndpoint; }} aria-label={showEndpoint ? 'Скрыть' : 'Показать'}>
						{#if showEndpoint}<EyeOff size={12} aria-hidden="true" />{:else}<Eye size={12} aria-hidden="true" />{/if}
					</button>
					{#if port > 0}<span class="port mono">:{port}</span>{/if}
				</div>
			</div>
			{#if sni}
				<div class="row">
					<span class="label">SNI</span>
					<span class="value mono">
						{#if showEndpoint}
							{sni}
						{:else}
							<span class="server-hidden">●●●●●●●●</span>
						{/if}
					</span>
				</div>
			{/if}
		{:else}
			<div class="server-section">
				<div class="server-row">
					<span class="label">{isURLTest ? 'Авто' : 'Активный сервер'}</span>
					<div class="picker-anchor">
						<div class="server-control">
							<button
								class="server-btn"
								class:server-btn-readonly={isURLTest}
								onclick={(e) => {
									e.stopPropagation();
									if (isURLTest) {
										notifications.info(
											'Включён автовыбор (URLTest). Чтобы выбирать сервер вручную, откройте подписку → вкладка «Настройки» → режим «Вручную».',
											{ duration: 9000 },
										);
										return;
									}
									pickerOpen = !pickerOpen;
								}}
								aria-haspopup={isURLTest ? undefined : 'listbox'}
								aria-expanded={isURLTest ? undefined : pickerOpen}
								title={isURLTest ? 'Mihomo выбирает самый быстрый сервер автоматически' : ''}
							>
								<span
									class="server-text"
									class:mono={showEndpoint || !listActiveServerName}
									title={showEndpoint ? activeEndpointTitle : (listActiveServerName || hiddenEndpointText)}
								>
									{#if showEndpoint}
										{endpointText}
									{:else if listActiveServerName}
										{listActiveServerName}
									{:else}
										{hiddenEndpointText}
									{/if}
								</span>
								{#if !isURLTest}
									<span class="caret" aria-hidden="true">▾</span>
								{/if}
							</button>
							<button
								type="button"
								class="eye-btn"
								onclick={(e) => {
									e.stopPropagation();
									showEndpoint = !showEndpoint;
								}}
								title={showEndpoint ? 'Скрыть IP' : 'Показать IP'}
								aria-label={showEndpoint ? 'Скрыть IP сервера' : 'Показать IP сервера'}
							>
								{#if showEndpoint}
									<Eye size={14} aria-hidden="true" />
								{:else}
									<EyeOff size={14} aria-hidden="true" />
								{/if}
							</button>
						</div>
						{#if pickerOpen && !isURLTest}
							<SubscriptionMemberPicker
								members={subscriptionMembers}
								activeMemberTag={activeName}
								onPick={selectMember}
								onClose={() => (pickerOpen = false)}
							/>
						{/if}
					</div>
				</div>
			</div>
		{/if}

		<div class="actions actions--bar">
			<TunnelListActions variant="labeled" onEdit={() => handleEdit()} editLabel="Изменить" onTest={() => diagnosticsOpen = true} onDelete={() => confirmDeleteOpen = true} {deleting} />
		</div>

		<div class="chart-section">
			<div class="chart-body">
				<div class="chart-head"><span>Delay (5 мин)</span></div>
				<TunnelDelaySparkBars {history} state={cardState} layout="compact" onclick={triggerCheck} />
				<div class="chart-head traffic-head">
					<span>Трафик</span>
					<span class="stats">↓ {formatBytes(traffic?.download ?? 0)} · ↑ {formatBytes(traffic?.upload ?? 0)}</span>
				</div>
				<TrafficChart {rxRates} {txRates} rxTotal={traffic?.download ?? 0} txTotal={traffic?.upload ?? 0} height={56} onclick={handleEdit} />
			</div>
		</div>
	</div>
{/if}

<Modal open={confirmDeleteOpen} title={kind === 'proxy' ? 'Удалить прокси-туннель?' : 'Удалить подписку?'} size="sm" onclose={() => { if (!deleting) confirmDeleteOpen = false; }}>
	<p>Удалить <strong>{title}</strong> из нативной конфигурации Mihomo?</p>
	{#snippet actions()}<Button variant="ghost" disabled={deleting} onclick={() => confirmDeleteOpen = false}>Отмена</Button><Button variant="danger" loading={deleting} onclick={remove}>Удалить</Button>{/snippet}
</Modal>

<MihomoNativeEditModal
	open={editOpen}
	{kind}
	resourceId={kind === 'proxy' ? proxy?.id ?? '' : subscription?.id ?? ''}
	onclose={() => editOpen = false}
	onupdated={handleUpdated}
/>

<TunnelDiagnosticsModal
	open={diagnosticsOpen}
	kind="mihomo"
	targetId={kind === 'proxy' ? proxy?.id ?? '' : subscription?.id ?? ''}
	resourceKind={kind}
	displayName={title}
	subject={kind === 'proxy' ? 'tunnel' : 'subscription'}
	iface={bridge?.kernelInterface}
	unavailableReason={diagnosticsUnavailable}
	onclose={() => diagnosticsOpen = false}
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
	.card.ok { border-color: var(--color-success-border); }
	.card.slow { border-color: var(--color-warning-border); }
	.card.fail { border-color: var(--color-error-border); }
	.card.unknown { border-color: var(--color-border); }
	.card.stopped { opacity: .72; }

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

	.badges { display: flex; gap: 0.4rem; flex-wrap: wrap; }
	.badge {
		font-size: var(--sbx-card-badge);
		padding: 2px 8px;
		border-radius: 10px;
		font-weight: 500;
	}
	.badge.proto    { background: rgba(88,166,255,0.15); color: var(--color-accent); }
	.badge.transport{ background: var(--color-bg-tertiary); color: var(--color-text-muted); }
	.badge.tls      { background: rgba(63,185,80,0.15); color: #3fb950; }
	.badge.reality  { background: rgba(210,153,34,0.15); color: #d29922; }
	.badge.mode     { background: var(--color-bg-tertiary); color: var(--color-text-muted); }

	.sub-meta {
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		line-height: 1.35;
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}
	.sub-error {
		font-size: var(--sbx-card-meta);
		color: #f85149;
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
	.row .label {
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
	.server-hidden { color: var(--color-text-muted); letter-spacing: 2px; }
	.server-text { font-family: var(--font-mono, monospace); }
	.icon-btn {
		background: none;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 2px;
		display: inline-flex;
	}
	.icon-btn:hover { color: var(--color-text-primary); }
	.port { color: var(--color-text-primary); margin-left: auto; font-variant-numeric: tabular-nums; }

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
	.server-section .label {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.picker-anchor { position: relative; min-width: 0; }
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
		min-width: 0;
	}
	.server-btn:hover { border-color: var(--color-accent); }
	.server-btn-readonly { cursor: default; }
	.server-btn-readonly:hover { border-color: var(--color-border); }
	.server-text {
		font-size: var(--sbx-card-value);
		overflow: hidden;
		display: block;
		text-overflow: ellipsis;
		white-space: nowrap;
		word-break: normal;
		overflow-wrap: normal;
		min-width: 0;
	}
	.server-text.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: var(--sbx-card-value);
	}
	.caret { color: var(--color-text-muted); font-size: var(--sbx-card-note); }
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
	.eye-btn:hover { color: var(--color-text-secondary); }

	.actions--bar {
		margin-top: 0.15rem;
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
	.stats { font-family: var(--font-mono, ui-monospace, monospace); }
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
	.traffic-head { margin-top: 8px; }

	.mono { font-family: var(--font-mono, ui-monospace, monospace); }

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
	.dense-toolbar-bottom { display: flex; align-items: center; }

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
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}
	.card.view-dense .details-dense-cols .kv-stacked-stat {
		flex-direction: row;
		align-items: center;
		gap: 0.35rem;
	}
	.card.view-dense .details-dense-cols .kv-stacked-label {
		flex: 0 0 auto;
	}
	.card.view-dense .details-dense-cols .kv-endpoint {
		flex: 1 1 auto;
		min-width: 0;
	}
	.card.view-dense .details-dense-cols .kv-stacked-value {
		min-width: 0;
	}
	.card.view-dense .kv-endpoint {
		display: flex;
		align-items: center;
		gap: 2px;
		min-width: 0;
	}
	.kv-stacked-label {
		font-size: 9px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
	}
	.kv-stacked-value {
		font-size: 10px;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.dense-meta-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: flex-start;
		gap: 0.35rem 0.75rem;
		min-width: 0;
		font-size: 9px;
		line-height: 1.25;
		color: var(--color-text-muted);
	}
	.dense-meta-line span {
		white-space: nowrap;
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
		width: auto;
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
		font: inherit;
		color: inherit;
		text-align: left;
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
		cursor: pointer;
		font: inherit;
		color: inherit;
		text-align: left;
	}
	.charts-dense .traffic-inline-rates {
		display: flex;
		flex-direction: column;
		gap: 0.06rem;
		min-width: 0;
		flex: 0 0 auto;
		font-size: 9px;
		line-height: 1.1;
		font-family: var(--font-mono, monospace);
	}
	.charts-dense .traffic-inline-rate.rx { color: var(--color-accent); }
	.charts-dense .traffic-inline-rate.tx { color: var(--color-success); }

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
	.sbx-sub-active-row:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: -2px;
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
