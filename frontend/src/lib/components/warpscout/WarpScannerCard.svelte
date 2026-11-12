<script lang="ts">
	import { Copy, Play, Square, Plus, Repeat, Radar } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Badge, Button, Dropdown, Modal, SegmentedControl } from '$lib/components/ui';
	import RichText from '$lib/components/ui/RichText.svelte';
	import type { DropdownOption, SegmentedOption } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type {
		WarpFilters,
		WarpHit,
		WarpProtocol,
		WarpScanStatus,
		WarpTargetMode,
		WarpTunnelRef,
	} from '$lib/types/warpscout';
	import WarpFiltersEditor from './WarpFiltersEditor.svelte';
	import {
		countryFlag,
		defaultFilters,
		formatDuration,
		latencyTone,
		modeLabel,
		parseGoTime,
		targetKey,
		targetOptionDescription,
		targetSelectable,
		warpErrorMessage,
	} from './warpscoutUi';

	interface Props {
		scan: WarpScanStatus;
		engineAvailable: boolean;
		hasAccount: boolean;
		targets: WarpTunnelRef[];
		scanInterface?: string;
		routeSummary?: string;
		onchanged: () => void | Promise<void>;
		ontargetschanged: () => void | Promise<void>;
	}

	let {
		scan,
		engineAvailable,
		hasAccount,
		targets,
		scanInterface = '',
		routeSummary,
		onchanged,
		ontargetschanged,
	}: Props = $props();

	let filters = $state<WarpFilters>(defaultFilters(0));
	let maxHits = $state(20);
	let starting = $state(false);
	let stopping = $state(false);
	let now = $state(Date.now());

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

	const hits = $derived.by((): WarpHit[] => {
		const list = [...(scan.hits ?? [])];
		const key = (h: WarpHit) => (h.pingMs > 0 ? h.pingMs : Number.MAX_SAFE_INTEGER);
		list.sort((a, b) => key(a) - key(b) || a.handshakeMs - b.handshakeMs);
		return list;
	});

	async function startScan(): Promise<void> {
		starting = true;
		try {
			await api.warpscoutScanStart({
				filters: { ...filters, maxRttMs: Math.max(0, Number(filters.maxRttMs) || 0) },
				maxHits: Math.min(100, Math.max(1, Number(maxHits) || 20)),
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

	async function copyEndpoint(ep: string): Promise<void> {
		const ok = await copyToClipboard(ep);
		if (ok) notifications.success(m.warpscout_copied_ep({ endpoint: ep }));
		else notifications.error(m.warpscout_copy_failed());
	}

	// ── Создание туннеля ──
	const modeOptions = $derived<SegmentedOption<WarpTargetMode>[]>([
		{ value: 'kernel', label: 'Kernel AWG' },
		{ value: 'native', label: 'Native WG' },
		{ value: 'system', label: m.warpscout_mode_system_short() },
	]);
	const protocolOptions = $derived<SegmentedOption<WarpProtocol>[]>([
		{ value: 'awg', label: 'AmneziaWG' },
		{ value: 'wg', label: 'WG' },
	]);

	let createHit = $state<WarpHit | null>(null);
	let createName = $state('');
	let createMode = $state<WarpTargetMode>('kernel');
	let createProtocol = $state<WarpProtocol>('awg');
	let createAutopilot = $state(false);
	let applying = $state(false);

	function openCreate(h: WarpHit): void {
		createHit = h;
		createName = `WARP ${h.colo || ''}`.trim();
		createMode = 'kernel';
		createProtocol = filters.protocol;
		createAutopilot = false;
	}

	async function submitCreate(): Promise<void> {
		if (!createHit) return;
		applying = true;
		try {
			const res = await api.warpscoutApply({
				action: 'create',
				endpoint: createHit.endpoint,
				mode: createMode,
				name: createName.trim() || undefined,
				protocol: createProtocol,
				enableAutopilot: createAutopilot,
				filters: createAutopilot ? { ...filters, protocol: createProtocol } : undefined,
			});
			const name = res.tunnel?.name || res.tunnel?.id || createName;
			notifications.success(
				createAutopilot && res.profileId
					? m.warpscout_created_with_auto({ name })
					: m.warpscout_created_ok({ name }),
			);
			createHit = null;
			await Promise.all([onchanged(), ontargetschanged()]);
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			applying = false;
		}
	}

	// ── Подмена в существующем туннеле ──
	let swapHit = $state<WarpHit | null>(null);
	let swapTarget = $state('');
	let swapAutopilot = $state(false);

	const swapOptions = $derived.by((): DropdownOption[] =>
		[...targets]
			.sort((a, b) => Number(targetSelectable(b)) - Number(targetSelectable(a)))
			.map((t) => ({
				value: targetKey(t.mode, t.id),
				label: `${t.name}${t.iface ? ` (${t.iface})` : ''}`,
				description: targetOptionDescription(t),
				disabled: !targetSelectable(t),
				group: modeLabel(t.mode),
			})),
	);
	const anySwapTarget = $derived(targets.some(targetSelectable));

	function openSwap(h: WarpHit): void {
		swapHit = h;
		const first = targets.find(targetSelectable);
		swapTarget = first ? targetKey(first.mode, first.id) : '';
		swapAutopilot = false;
	}

	async function submitSwap(): Promise<void> {
		if (!swapHit || !swapTarget) return;
		const t = targets.find((x) => targetKey(x.mode, x.id) === swapTarget);
		if (!t) return;
		applying = true;
		try {
			await api.warpscoutApply({
				action: 'swap',
				endpoint: swapHit.endpoint,
				mode: t.mode,
				targetId: t.id,
				protocol: filters.protocol,
				enableAutopilot: swapAutopilot,
				filters: swapAutopilot ? { ...filters } : undefined,
			});
			notifications.success(m.warpscout_swapped_ok({ name: t.name, endpoint: swapHit.endpoint }));
			swapHit = null;
			await Promise.all([onchanged(), ontargetschanged()]);
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			applying = false;
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
				<input class="num" type="number" min="1" max="100" bind:value={maxHits} disabled={running} />
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
					disabled={!engineAvailable || !hasAccount}
					title={!engineAvailable
						? m.warpscout_err_engine_missing()
						: !hasAccount
							? m.warpscout_scan_need_account_hint()
							: undefined}
				>
					{#snippet iconBefore()}<Play size={13} />{/snippet}
					{m.warpscout_scan_btn()}
				</Button>
			{/if}
		</div>
	</div>

	<!-- Встроенная компактная полоса фильтров -->
	<div class="filter-strip">
		<WarpFiltersEditor bind:filters hideProtocol disabled={running} idPrefix="scan" />
	</div>

	<!-- Прогресс сканирования -->
	{#if running || scan.total > 0 || scan.error}
		<div class="progress-strip">
			<div
				class="scan-progress"
				role="progressbar"
				aria-valuemin={0}
				aria-valuemax={100}
				aria-valuenow={progressPct}
			>
				<div class="bar" class:running style:width={`${progressPct}%`}></div>
			</div>
			<div class="progress-meta">
				<span class="mono">{scan.done} / {scan.total}</span>
				<span><RichText text={m.warpscout_found_count({ count: scan.hits?.length ?? 0 })} /></span>
				<span>{formatDuration(elapsedMs)}</span>
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
			<div class="empty-state">
				<div class="empty-icon"><Radar size={20} /></div>
				<div>
					<div class="empty-title">{m.warpscout_empty_need_acct_title()}</div>
					<p class="note">{m.warpscout_empty_need_acct_desc()}</p>
				</div>
			</div>
		{:else if hits.length > 0}
			<div class="hits-table-wrap">
				<table class="hits">
					<thead>
						<tr>
							<th>Endpoint</th>
							<th>{m.warpscout_col_location()}</th>
							<th class="r">{m.warpscout_col_latency()}</th>
							<th class="c hide-xs">TG</th>
							<th></th>
						</tr>
					</thead>
					<tbody>
						{#each hits as h, i (h.endpoint)}
							{@const tone = latencyTone(h.pingMs)}
							<tr class:top-hit={i === 0}>
								<td class="mono">
									<span class="ep">
										{#if i === 0}<span class="top-badge">TOP</span>{/if}
										<span>{h.endpoint}</span>
										<button
											type="button"
											class="icon-btn"
											title={m.common_copy()}
											aria-label={m.warpscout_copy_ep_aria()}
											onclick={() => copyEndpoint(h.endpoint)}
										>
											<Copy size={12} />
										</button>
									</span>
								</td>
								<td>
									<span class="geo">
										{#if h.loc}<span class="flag">{countryFlag(h.loc)}</span>{/if}
										<span class="mono colo-cell">{h.colo || h.loc || '—'}</span>
										{#if h.colo && h.loc}<span class="mono muted">· {h.loc}</span>{/if}
									</span>
								</td>
								<td
									class="r mono"
									title={h.handshakeMs > 0 ? m.warpscout_handshake_tip({ ms: h.handshakeMs }) : undefined}
								>
									{#if h.pingMs > 0}
										<span class="lat-pill {tone}">
											<span class="lat-dot"></span>
											{m.warpscout_ms({ ms: h.pingMs })}
										</span>
									{:else}
										—
									{/if}
								</td>
								<td class="c hide-xs">
									{#if h.tgOk}<span class="ok">✓</span>{:else}<span class="muted">—</span>{/if}
								</td>
								<td class="actions-cell">
									<div class="actions">
										<Button
											size="sm"
											variant="outline-primary"
											title={m.warpscout_create_tunnel_tip()}
											onclick={() => openCreate(h)}
										>
											{#snippet iconBefore()}<Plus size={12} />{/snippet}
											{m.warpscout_tunnel_btn()}
										</Button>
										<Button
											size="sm"
											variant="ghost"
											title={m.warpscout_swap_ep_tip()}
											onclick={() => openSwap(h)}
										>
											{#snippet iconBefore()}<Repeat size={12} />{/snippet}
											{m.warpscout_swap_btn()}
										</Button>
									</div>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{:else if !running && scan.total > 0 && !scan.error}
			<div class="empty-state">
				<div class="empty-icon"><Radar size={20} /></div>
				<div>
					<div class="empty-title">{m.warpscout_empty_no_hits_title()}</div>
					<p class="note">{m.warpscout_empty_no_hits_desc({ route: effectiveRoute })}</p>
				</div>
			</div>
		{:else if !running}
			<div class="empty-state">
				<div class="empty-icon"><Radar size={20} /></div>
				<div>
					<div class="empty-title">{m.warpscout_empty_ready_title()}</div>
					<p class="note">
						<RichText
							text={filters.protocol === 'awg'
								? m.warpscout_empty_ready_awg({ route: effectiveRoute })
								: m.warpscout_empty_ready_wg({ route: effectiveRoute })}
						/>
					</p>
				</div>
			</div>
		{/if}
	</div>
</section>

<Modal
	open={createHit !== null}
	title={m.warpscout_modal_create_title()}
	size="md"
	onclose={() => (createHit = null)}
>
	{#if createHit}
		<div class="modal-body">
			<p class="note">
				{m.warpscout_modal_node()} <span class="mono">{createHit.endpoint}</span>
				{#if createHit.colo}· {createHit.colo}{/if}
				{#if createHit.loc}{countryFlag(createHit.loc)} {createHit.loc}{/if}
			</p>
			<label class="stack">
				<span class="ws-label">{m.warpscout_field_name()}</span>
				<input class="text" type="text" bind:value={createName} maxlength={64} />
			</label>
			<div class="stack">
				<span class="ws-label">{m.warpscout_field_tunnel_mode()}</span>
				<SegmentedControl
					value={createMode}
					options={modeOptions}
					ariaLabel={m.warpscout_field_tunnel_mode()}
					onchange={(v) => (createMode = v)}
				/>
			</div>
			<div class="stack">
				<span class="ws-label">{m.warpscout_field_protocol()}</span>
				<SegmentedControl
					value={createProtocol}
					options={protocolOptions}
					ariaLabel={m.warpscout_field_protocol()}
					onchange={(v) => (createProtocol = v)}
				/>
			</div>
			<label class="check">
				<input type="checkbox" bind:checked={createAutopilot} />
				<span>{m.warpscout_enable_auto_create()}</span>
			</label>
		</div>
	{/if}
	{#snippet actions()}
		<Button variant="ghost" onclick={() => (createHit = null)} disabled={applying}>
			{m.common_cancel()}
		</Button>
		<Button variant="primary" loading={applying} onclick={submitCreate}>
			{m.common_create()}
		</Button>
	{/snippet}
</Modal>

<Modal
	open={swapHit !== null}
	title={m.warpscout_modal_swap_title()}
	size="md"
	onclose={() => (swapHit = null)}
>
	{#if swapHit}
		<div class="modal-body">
			<p class="note">
				{m.warpscout_modal_new_ep()} <span class="mono">{swapHit.endpoint}</span>
				{#if swapHit.colo}· {swapHit.colo}{/if}
				{#if swapHit.loc}{countryFlag(swapHit.loc)} {swapHit.loc}{/if}
			</p>
			{#if targets.length === 0}
				<p class="note warn">{m.warpscout_no_tunnels()}</p>
			{:else}
				<Dropdown
					label={m.warpscout_tunnel_btn()}
					value={swapTarget}
					options={swapOptions}
					fullWidth
					onchange={(v) => (swapTarget = v)}
				/>
				{#if !anySwapTarget}
					<p class="note warn">{m.warpscout_no_warp_tunnels()}</p>
				{/if}
			{/if}
			<label class="check">
				<input type="checkbox" bind:checked={swapAutopilot} />
				<span>{m.warpscout_enable_auto_swap()}</span>
			</label>
		</div>
	{/if}
	{#snippet actions()}
		<Button variant="ghost" onclick={() => (swapHit = null)} disabled={applying}>
			{m.common_cancel()}
		</Button>
		<Button variant="primary" loading={applying} disabled={!swapTarget || !anySwapTarget} onclick={submitSwap}>
			{m.warpscout_swap_btn()}
		</Button>
	{/snippet}
</Modal>

<style>
	.radar-card {
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		box-shadow: var(--shadow);
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
	.num,
	.text {
		height: 30px;
		box-sizing: border-box;
		padding: 0.25rem 0.45rem;
		font-size: 0.8125rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
	}
	.num {
		width: 3.85rem;
		font-family: var(--font-mono);
		text-align: center;
	}
	.text {
		width: 100%;
		height: 34px;
	}
	.filter-strip {
		padding: 0.6rem 0.85rem;
		background: color-mix(in srgb, var(--color-bg-tertiary) 55%, transparent);
		border-bottom: 1px solid var(--color-border);
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
	.empty-state {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.85rem 0.9rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--color-bg-primary) 65%, transparent);
	}
	.empty-icon {
		width: 2.25rem;
		height: 2.25rem;
		border-radius: 999px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: var(--color-accent-tint);
		color: var(--color-accent);
		flex-shrink: 0;
	}
	.empty-title {
		font-size: 0.82rem;
		font-weight: 600;
		color: var(--color-text-primary);
		margin-bottom: 0.15rem;
	}
	.hits-table-wrap {
		max-height: 22rem;
		overflow: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}
	.hits {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.78rem;
	}
	.hits th {
		position: sticky;
		top: 0;
		z-index: 1;
		text-align: left;
		font-weight: 500;
		font-size: 0.7rem;
		color: var(--color-text-muted);
		padding: 0.38rem 0.55rem;
		background: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
		white-space: nowrap;
	}
	.hits td {
		padding: 0.32rem 0.55rem;
		border-bottom: 1px solid var(--color-border);
		color: var(--color-text-primary);
		white-space: nowrap;
		vertical-align: middle;
	}
	.hits tr:last-child td {
		border-bottom: 0;
	}
	.hits tr.top-hit td {
		background: color-mix(in srgb, var(--color-accent-tint) 45%, transparent);
	}
	.hits tr:hover td {
		background: var(--color-bg-hover);
	}
	.top-badge {
		display: inline-flex;
		align-items: center;
		padding: 0.05rem 0.32rem;
		border-radius: 4px;
		font-family: var(--font-sans);
		font-size: 0.62rem;
		font-weight: 700;
		letter-spacing: 0.03em;
		background: var(--color-accent-tint);
		color: var(--color-accent);
		border: 1px solid var(--color-accent-border);
	}
	.colo-cell {
		font-weight: 600;
	}
	.geo {
		display: inline-flex;
		align-items: center;
		gap: 0.28rem;
	}
	.lat-pill {
		display: inline-flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.32rem;
		font-weight: 600;
	}
	.lat-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-text-muted);
	}
	.lat-pill.ok {
		color: var(--latency-color-ok, var(--color-success));
	}
	.lat-pill.ok .lat-dot {
		background: var(--latency-color-ok, var(--color-success));
	}
	.lat-pill.slow {
		color: var(--latency-color-slow, var(--color-warning));
	}
	.lat-pill.slow .lat-dot {
		background: var(--latency-color-slow, var(--color-warning));
	}
	.lat-pill.fail {
		color: var(--latency-color-fail, var(--color-error));
	}
	.lat-pill.fail .lat-dot {
		background: var(--latency-color-fail, var(--color-error));
	}
	.r {
		text-align: right !important;
	}
	.c {
		text-align: center !important;
	}
	.ep {
		display: inline-flex;
		align-items: center;
		gap: 0.32rem;
	}
	.actions-cell {
		width: 1%;
	}
	.actions {
		display: inline-flex;
		gap: 0.25rem;
		justify-content: flex-end;
	}
	.icon-btn {
		display: inline-flex;
		border: 0;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 0.1rem;
	}
	.icon-btn:hover {
		color: var(--color-accent);
	}
	.ok {
		color: var(--color-success);
		font-weight: 600;
	}
	.muted {
		color: var(--color-text-muted);
	}
	.mono {
		font-family: var(--font-mono);
	}
	.note {
		margin: 0;
		font-size: 0.76rem;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}
	.note.warn {
		color: var(--color-warning);
	}
	.modal-body {
		display: flex;
		flex-direction: column;
		gap: 0.8rem;
	}
	.stack {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}
	.ws-label {
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.check {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		font-size: 0.8125rem;
		color: var(--color-text-primary);
		cursor: pointer;
	}

	@media (max-width: 640px) {
		.toolbar-right {
			width: 100%;
			justify-content: space-between;
			margin-left: 0;
		}
		.empty-state {
			align-items: flex-start;
		}
	}

	@media (max-width: 580px) {
		.hide-xs {
			display: none;
		}
	}
</style>
