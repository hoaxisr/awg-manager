<script lang="ts">
	import { Play, Square, Radar, SlidersHorizontal } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Badge, Button, SegmentedControl } from '$lib/components/ui';
	import { EmptyState } from '$lib/components/layout';
	import RichText from '$lib/components/ui/RichText.svelte';
	import type { SegmentedOption } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import type {
		WarpAWGParams,
		WarpFilters,
		WarpHit,
		WarpObfuscationSource,
		WarpProtocol,
		WarpRouteRef,
		WarpScanStatus,
		WarpTunnelRef,
	} from '$lib/types/warpscout';
	import WarpCreateTunnelModal from './WarpCreateTunnelModal.svelte';
	import WarpFiltersEditor from './WarpFiltersEditor.svelte';
	import WarpHitsTable from './WarpHitsTable.svelte';
	import WarpSwapEndpointModal from './WarpSwapEndpointModal.svelte';
	import {
		LIMITS,
		defaultFilters,
		formatElapsed,
		parseGoTime,
		sanitizeInt,
		sortHits,
		warpErrorMessage,
	} from './warpscoutUi';

	interface Props {
		scan: WarpScanStatus;
		engineAvailable: boolean;
		hasAccount: boolean;
		accountAddressV4?: string;
		targets: WarpTunnelRef[];
		usedIpv4?: string[];
		routes?: WarpRouteRef[];
		obfuscationSources?: WarpObfuscationSource[];
		globalAwg?: WarpAWGParams;
		scanInterface?: string;
		routeSummary?: string;
		/** В автопилоте есть несохранённые правки — включать его из сканера нельзя. */
		autopilotLocked: boolean;
		autoRename?: boolean;
		findJunkRunning?: boolean;
		onchanged: () => void | Promise<void>;
		ontargetschanged: () => void | Promise<void>;
	}

	let {
		scan,
		engineAvailable,
		hasAccount,
		accountAddressV4 = '',
		targets,
		usedIpv4 = [],
		routes = [],
		obfuscationSources = [],
		globalAwg,
		scanInterface = '',
		routeSummary,
		autopilotLocked,
		autoRename = true,
		findJunkRunning = false,
		onchanged,
		ontargetschanged,
	}: Props = $props();

	let filters = $state<WarpFilters>(defaultFilters(0));
	let maxHitsInput = $state(String(LIMITS.maxHits.def));
	let starting = $state(false);
	let stopping = $state(false);
	let now = $state(Date.now());
	let mobileFiltersOpen = $state(false);

	const filterSummaryChips = $derived.by(() => {
		const out: string[] = [];
		if ((filters.excludeColos ?? []).length > 0) {
			out.push(m.warpscout_filters_summary_excl({ list: filters.excludeColos.join(', ') }));
		}
		if ((filters.allowedCountries ?? []).length > 0) {
			out.push(filters.allowedCountries.join(', '));
		}
		if ((filters.maxRttMs ?? 0) > 0) {
			out.push(m.warpscout_filters_summary_rtt({ ms: filters.maxRttMs }));
		}
		if (filters.requireTelegram) {
			out.push(m.warpscout_filters_summary_tg());
		}
		if (out.length === 0) out.push(m.warpscout_filters_none());
		return out;
	});

	function onMaxHitsInput(e: Event): void {
		const el = e.currentTarget as HTMLInputElement;
		const raw = el.value.trim();
		if (raw === '') {
			maxHitsInput = '';
			return;
		}
		const n = Math.trunc(Number(raw));
		if (!Number.isFinite(n)) {
			el.value = maxHitsInput;
			return;
		}
		const clamped = Math.min(LIMITS.maxHits.max, Math.max(LIMITS.maxHits.min, n));
		maxHitsInput = String(clamped);
		if (el.value !== maxHitsInput) el.value = maxHitsInput;
	}

	function onMaxHitsBlur(): void {
		maxHitsInput = String(sanitizeInt(maxHitsInput, LIMITS.maxHits));
	}

	let createHit = $state<WarpHit | null>(null);
	let swapHit = $state<WarpHit | null>(null);

	const effectiveRoute = $derived(routeSummary || m.warpscout_route_main_wan());
	const running = $derived(scan.running);
	const manualOwner = $derived(!scan.owner || scan.owner === 'manual');

	const headerProtocolOptions = $derived<SegmentedOption<WarpProtocol>[]>([
		{ value: 'awg', label: m.warpscout_proto_awg_short() },
		{ value: 'wg', label: 'WireGuard' },
	]);

	$effect(() => {
		if (!running) return;
		const t = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(t);
	});

	const elapsedMs = $derived.by(() => {
		if (scan.elapsedMs && !running) return scan.elapsedMs;
		const started = parseGoTime(scan.startedAt);
		if (running && started) return Math.max(0, now - started.getTime());
		return scan.elapsedMs ?? 0;
	});

	const progressPct = $derived(
		scan.total > 0 ? Math.min(100, Math.round((scan.done / scan.total) * 100)) : 0,
	);

	const hits = $derived(sortHits(scan.hits));
	const showTgCol = $derived(Boolean(filters.requireTelegram) || hits.some((h) => h.tgOk));

	async function startScan(): Promise<void> {
		const clampedHits = sanitizeInt(maxHitsInput, LIMITS.maxHits);
		maxHitsInput = String(clampedHits);
		starting = true;
		try {
			await api.warpscoutScanStart({
				filters: { ...filters, maxRttMs: sanitizeInt(filters.maxRttMs, LIMITS.maxRttMs) },
				maxHits: clampedHits,
				maxCandidates: 0,
				ifname: scanInterface.trim() || undefined,
			});
			await onchanged();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			starting = false;
		}
	}

	async function stopScan(): Promise<void> {
		stopping = true;
		try {
			await api.warpscoutScanStop();
			await onchanged();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			stopping = false;
		}
	}

	let exportingEp = $state('');

	async function applied(): Promise<void> {
		await Promise.all([onchanged(), ontargetschanged()]);
	}

	async function exportConf(h: WarpHit): Promise<void> {
		if (exportingEp) return;
		exportingEp = h.endpoint;
		try {
			const res = await api.warpscoutExport({
				endpoint: h.endpoint,
				colo: h.colo || undefined,
				protocol: filters.protocol,
			});
			const blob = new Blob([res.content], { type: 'text/plain;charset=utf-8' });
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = res.filename || 'warp.conf';
			document.body.appendChild(a);
			a.click();
			a.remove();
			URL.revokeObjectURL(url);
			notifications.success(m.warpscout_exported_conf_ok({ filename: res.filename || 'warp.conf' }));
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			exportingEp = '';
		}
	}
</script>

<section class="radar-card">
	<!-- Шапка-пульт: название + протокол слева, лимит + старт/стоп справа -->
	<div class="radar-toolbar">
		<div class="toolbar-left">
			<h3>{m.warpscout_radar_title()}</h3>
			<div class="proto-seg">
				<SegmentedControl
					value={filters.protocol}
					options={headerProtocolOptions}
					ariaLabel={m.warpscout_proto_label()}
					disabled={running}
					onchange={(v) => (filters.protocol = v)}
				/>
			</div>
		</div>

		<div class="toolbar-right">
			<label class="limit-field" title={m.warpscout_limit_title()}>
				<span>{m.warpscout_limit_label()}</span>
				<input
					class="field-input compact limit-input"
					type="number"
					min={LIMITS.maxHits.min}
					max={LIMITS.maxHits.max}
					step="1"
					value={maxHitsInput}
					oninput={onMaxHitsInput}
					onblur={onMaxHitsBlur}
					disabled={running}
				/>
			</label>
			{#if running}
				<Button variant="danger" size="sm" loading={stopping} onclick={stopScan} disabled={!manualOwner}>
					{#snippet iconBefore()}<Square size={13} />{/snippet}
					{m.common_stop()}
				</Button>
			{:else}
				<Button
					variant="primary"
					size="sm"
					loading={starting}
					onclick={startScan}
					disabled={!engineAvailable || !hasAccount || findJunkRunning}
					title={!engineAvailable
						? m.warpscout_err_engine_missing()
						: !hasAccount
							? m.warpscout_scan_need_account_hint()
							: findJunkRunning
								? m.warpscout_err_busy()
								: undefined}
				>
					{#snippet iconBefore()}<Play size={13} />{/snippet}
					{m.warpscout_scan_btn()}
				</Button>
			{/if}
		</div>
	</div>

	<!-- Встроенная компактная полоса фильтров (на телефоне сворачивается в аккордеон) -->
	<div class="filter-strip">
		<button
			type="button"
			class="mobile-filters-toggle"
			aria-expanded={mobileFiltersOpen}
			onclick={() => (mobileFiltersOpen = !mobileFiltersOpen)}
		>
			<span class="mft-left">
				<SlidersHorizontal size={13} />
				<span class="mft-title">{m.warpscout_filters_toggle()}</span>
				{#each filterSummaryChips as chip (chip)}
					<span class="mft-chip">{chip}</span>
				{/each}
			</span>
			<span class="mono mft-sign">{mobileFiltersOpen ? '−' : '+'}</span>
		</button>
		<div class="filter-strip-body" class:mobile-open={mobileFiltersOpen}>
			<WarpFiltersEditor bind:filters hideProtocol disabled={running} idPrefix="scan" />
		</div>
	</div>

	<!-- Прогресс сканирования -->
	{#if running || scan.total > 0 || scan.error}
		<div class="progress-strip">
			<div
				class="scan-progress"
				role="progressbar"
				aria-label={m.warpscout_scan_progress_aria()}
				aria-valuemin={0}
				aria-valuemax={100}
				aria-valuenow={progressPct}
			>
				<div class="bar" class:running style:width={`${progressPct}%`}></div>
			</div>
			<div class="progress-meta">
				<span class="mono">{scan.done} / {scan.total}</span>
				<span><RichText text={m.warpscout_found_count({ count: scan.hits?.length ?? 0 })} /></span>
				<span>{formatElapsed(elapsedMs)}</span>
				{#if running && !manualOwner}
					<Badge variant="info" size="xs">{m.warpscout_badge_auto_scan()}</Badge>
				{:else if running}
					<Badge variant="info" size="xs">{m.warpscout_badge_scanning()}</Badge>
				{:else if scan.aborted}
					<Badge variant="warning" size="xs">{m.warpscout_badge_stopped()}</Badge>
				{:else if scan.total > 0 && !scan.error}
					<Badge variant="success" size="xs">{m.warpscout_badge_done()}</Badge>
				{/if}
			</div>
		</div>
		{#if scan.error}
			<div class="scan-err">{scan.error}</div>
		{/if}
	{/if}

	<!-- Область результатов / пустое состояние -->
	<div class="results-area">
		{#if !hasAccount}
			<EmptyState title={m.warpscout_empty_need_acct_title()} description={m.warpscout_empty_need_acct_desc()}>
				{#snippet icon()}<Radar />{/snippet}
			</EmptyState>
		{:else if hits.length > 0}
			<WarpHitsTable {hits} {showTgCol} oncreate={(h) => (createHit = h)} onswap={(h) => (swapHit = h)} onexport={exportConf} />
		{:else if !running && scan.total > 0 && !scan.error}
			<EmptyState
				title={m.warpscout_empty_no_hits_title()}
				description={m.warpscout_empty_no_hits_desc({ route: effectiveRoute })}
			>
				{#snippet icon()}<Radar />{/snippet}
			</EmptyState>
		{:else if !running}
			<EmptyState
				title={m.warpscout_empty_ready_title()}
				description={filters.protocol === 'awg'
					? m.warpscout_empty_ready_awg({ route: effectiveRoute })
					: m.warpscout_empty_ready_wg({ route: effectiveRoute })}
			>
				{#snippet icon()}<Radar />{/snippet}
				{#snippet action()}
					<Button
						variant="primary"
						size="sm"
						loading={starting}
						onclick={startScan}
						disabled={!engineAvailable || !hasAccount || findJunkRunning}
					>
						{#snippet iconBefore()}<Play size={13} />{/snippet}
						{m.warpscout_start_scan_btn()}
					</Button>
				{/snippet}
			</EmptyState>
		{/if}
	</div>
</section>

{#if createHit}
	<WarpCreateTunnelModal
		hit={createHit}
		{filters}
		{accountAddressV4}
		{targets}
		{usedIpv4}
		{routes}
		scanIface={scanInterface}
		{obfuscationSources}
		{globalAwg}
		{autopilotLocked}
		onclose={() => (createHit = null)}
		ondone={applied}
	/>
{/if}

{#if swapHit}
	<WarpSwapEndpointModal
		hit={swapHit}
		{targets}
		{filters}
		{globalAwg}
		{autoRename}
		{autopilotLocked}
		onclose={() => (swapHit = null)}
		ondone={applied}
	/>
{/if}

<style>
	.radar-card {
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		overflow: hidden;
		display: flex;
		flex-direction: column;
	}
	.radar-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.85rem;
		padding: 0.6rem 0.85rem;
		border-bottom: 1px solid var(--color-border);
		flex-wrap: wrap;
	}
	.toolbar-left {
		display: flex;
		align-items: center;
		gap: 0.5rem 0.75rem;
		flex-wrap: wrap;
		min-width: 0;
	}
	.toolbar-left h3 {
		margin: 0;
		font-size: 0.92rem;
		font-weight: 600;
		color: var(--color-text-primary);
		white-space: nowrap;
	}
	.proto-seg {
		flex-shrink: 0;
	}
	.toolbar-right {
		display: flex;
		align-items: center;
		gap: 0.55rem;
		margin-left: auto;
		flex-wrap: wrap;
	}
	.limit-field {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.76rem;
		color: var(--color-text-secondary);
		white-space: nowrap;
	}
	.limit-input {
		width: 3.85rem;
		font-family: var(--font-mono);
		text-align: center;
	}
	.filter-strip {
		padding: 0.6rem 0.85rem;
		background: color-mix(in srgb, var(--color-bg-tertiary) 55%, transparent);
		border-bottom: 1px solid var(--color-border);
	}
	.mobile-filters-toggle {
		display: none;
		width: 100%;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.15rem 0;
		border: 0;
		background: transparent;
		color: var(--color-text-primary);
		font: inherit;
		font-size: 0.78rem;
		cursor: pointer;
		text-align: left;
	}
	.mft-left {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		flex-wrap: wrap;
		min-width: 0;
		color: var(--color-text-secondary);
	}
	.mft-title {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.mft-chip {
		display: inline-flex;
		align-items: center;
		padding: 0.08rem 0.42rem;
		border-radius: 999px;
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-family: var(--font-mono);
		font-size: 0.69rem;
	}
	.mft-sign {
		font-size: 0.95rem;
		font-weight: 600;
		color: var(--color-text-muted);
		flex-shrink: 0;
		padding-left: 0.25rem;
	}
	.progress-strip {
		display: flex;
		align-items: center;
		gap: 0.65rem 0.9rem;
		padding: 0.45rem 0.85rem;
		background: var(--color-bg-primary);
		border-bottom: 1px solid var(--color-border);
		flex-wrap: wrap;
	}
	.scan-progress {
		flex: 1 1 8rem;
		height: 5px;
		background: var(--color-bg-tertiary);
		border-radius: 999px;
		overflow: hidden;
	}
	.bar {
		height: 100%;
		background: var(--color-accent);
		transition: width 0.35s ease;
	}
	.bar.running {
		background: linear-gradient(90deg, var(--color-accent), var(--color-info));
	}
	.progress-meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem 0.75rem;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}
	.scan-err {
		padding: 0.45rem 0.85rem;
		font-size: 0.76rem;
		color: var(--color-error);
		background: var(--color-error-tint);
		border-bottom: 1px solid var(--color-error-border);
	}
	.results-area {
		padding: 0.65rem 0.85rem;
	}
	.mono {
		font-family: var(--font-mono);
	}

	@media (max-width: 768px) {
		.mobile-filters-toggle {
			display: flex;
		}
		.filter-strip-body {
			display: none;
			margin-top: 0.6rem;
			padding-top: 0.6rem;
			border-top: 1px solid var(--color-border);
		}
		.filter-strip-body.mobile-open {
			display: block;
		}
	}

	@media (max-width: 640px) {
		.toolbar-right {
			width: 100%;
			justify-content: space-between;
			margin-left: 0;
		}
	}

	@media (max-width: 480px) {
		.toolbar-left,
		.proto-seg {
			width: 100%;
		}
	}

</style>
