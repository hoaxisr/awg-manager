<script lang="ts">
	import {
		Plus,
		Save,
		Trash2,
		RotateCcw,
		SlidersHorizontal,
		ChevronDown,
		ArrowRight,
		Activity,
	} from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Badge, Button, ConfirmModal, Dropdown, SegmentedControl, Toggle } from '$lib/components/ui';
	import RichText from '$lib/components/ui/RichText.svelte';
	import type { DropdownOption, SegmentedOption } from '$lib/components/ui';
	import type {
		WarpCheckSample,
		WarpConfig,
		WarpProfile,
		WarpProfileMode,
		WarpProfileStats,
		WarpSwapEvent,
		WarpTargetMode,
		WarpTunnelRef,
		WarpWatchStatus,
	} from '$lib/types/warpscout';
	import WarpFiltersEditor from './WarpFiltersEditor.svelte';
	import {
		countryFlag,
		formatSlaPct,
		formatTime,
		formatUptime,
		humanizeSwapReason,
		latencyTone,
		modeLabel,
		newProfile,
		parseGoTime,
		targetKey,
		targetOptionDescription,
		targetSelectable,
		topColos,
		watchStateView,
	} from './warpscoutUi';

	interface Props {
		config: WarpConfig;
		targets: WarpTunnelRef[];
		watch: WarpWatchStatus[];
		stats?: WarpProfileStats[];
		history: WarpSwapEvent[];
		dirty: boolean;
		saving: boolean;
		onsave: () => void | Promise<void>;
		onreset: () => void;
		onresetstats?: (profileId: string) => void | Promise<void>;
	}

	let {
		config = $bindable(),
		targets,
		watch,
		stats = [],
		history,
		dirty,
		saving,
		onsave,
		onreset,
		onresetstats,
	}: Props = $props();

	const modeOptions = $derived<SegmentedOption<WarpProfileMode>[]>([
		{ value: 'single', label: m.warpscout_mode_single() },
		{ value: 'multi', label: m.warpscout_mode_multi() },
	]);

	const MODE_ORDER: WarpTargetMode[] = ['kernel', 'native', 'system'];
	const SPARK_SLOTS = 32;

	let deleteId = $state<string | null>(null);
	let resetStatsId = $state<string | null>(null);
	let resettingStats = $state(false);
	let openIds = $state<Record<string, boolean>>({});

	const watchById = $derived(new Map((watch ?? []).map((w) => [w.profileId, w])));
	const statsById = $derived(new Map((stats ?? []).map((s) => [s.profileId, s])));

	function buildSparkBars(samples: WarpCheckSample[] | undefined): Array<WarpCheckSample | null> {
		const tail = (samples ?? []).slice(-SPARK_SLOTS);
		const pad = Math.max(0, SPARK_SLOTS - tail.length);
		const out: Array<WarpCheckSample | null> = [];
		for (let i = 0; i < pad; i++) out.push(null);
		for (const s of tail) out.push(s);
		return out;
	}

	function sampleBarHeight(s: WarpCheckSample, maxMs: number): number {
		if (!s.ok) return 100;
		const ms = s.latencyMs ?? 0;
		if (ms <= 0 || maxMs <= 0) return 55;
		return Math.min(100, Math.max(30, Math.round((ms / maxMs) * 100)));
	}

	function sampleTooltip(s: WarpCheckSample): string {
		const time = formatTime(s.at);
		if (!s.ok) {
			const reason = humanizeSwapReason(s.reason || m.warpscout_sample_check_fail());
			return s.swapped
				? m.warpscout_sample_tip_fail_swap({ time, reason })
				: m.warpscout_sample_tip_fail({ time, reason });
		}
		const loc = [s.colo, s.loc].filter(Boolean).join(' ');
		const baseLat = s.latencyMs && s.latencyMs > 0 ? m.warpscout_ms({ ms: s.latencyMs }) : 'ok';
		const latency = loc ? `${baseLat} · ${loc}` : baseLat;
		return s.swapped
			? m.warpscout_sample_tip_ok_swap({ time, latency })
			: m.warpscout_sample_tip_ok({ time, latency });
	}

	async function confirmResetStats(): Promise<void> {
		if (!resetStatsId || !onresetstats) {
			resetStatsId = null;
			return;
		}
		resettingStats = true;
		try {
			await onresetstats(resetStatsId);
		} finally {
			resettingStats = false;
			resetStatsId = null;
		}
	}

	function isExpanded(p: WarpProfile): boolean {
		if (!p.targetId) return true;
		return !!openIds[p.id];
	}

	function toggleExpand(p: WarpProfile): void {
		openIds[p.id] = !isExpanded(p);
	}

	function resolveTarget(p: WarpProfile): WarpTunnelRef | undefined {
		if (!p.targetId) return undefined;
		return targets.find((t) => t.mode === p.targetMode && t.id === p.targetId);
	}

	function targetOptions(p: WarpProfile): DropdownOption[] {
		const sorted = [...targets].sort(
			(a, b) => MODE_ORDER.indexOf(a.mode) - MODE_ORDER.indexOf(b.mode) || a.name.localeCompare(b.name),
		);
		const opts: DropdownOption[] = sorted.map((t) => ({
			value: targetKey(t.mode, t.id),
			label: `${t.name}${t.iface && t.iface !== t.name ? ` (${t.iface})` : ''}`,
			description: targetOptionDescription(t),
			disabled: !targetSelectable(t),
			group: modeLabel(t.mode),
		}));
		if (p.targetId && !targets.some((t) => t.mode === p.targetMode && t.id === p.targetId)) {
			opts.unshift({
				value: targetKey(p.targetMode, p.targetId),
				label: m.warpscout_target_missing_opt({ id: p.targetId }),
				description: m.warpscout_target_missing_desc(),
				group: modeLabel(p.targetMode),
			});
		}
		return opts;
	}

	function setTarget(p: WarpProfile, key: string): void {
		const ix = key.indexOf(':');
		if (ix <= 0) return;
		p.targetMode = key.slice(0, ix) as WarpTargetMode;
		p.targetId = key.slice(ix + 1);
	}

	function setEnabled(p: WarpProfile, on: boolean): void {
		if (config.profileMode === 'single') {
			for (const other of config.profiles) other.enabled = false;
			p.enabled = on;
			config.activeProfileId = on ? p.id : '';
		} else {
			p.enabled = on;
		}
	}

	function setProfileMode(mode: WarpProfileMode): void {
		config.profileMode = mode;
		if (mode === 'single') {
			const active =
				config.profiles.find((p) => p.id === config.activeProfileId && p.enabled) ??
				config.profiles.find((p) => p.enabled);
			for (const p of config.profiles) p.enabled = p === active;
			config.activeProfileId = active?.id ?? '';
		}
	}

	function addProfile(): void {
		const created = newProfile(config.profiles.length + 1);
		openIds[created.id] = true;
		config.profiles.push(created);
	}

	function removeProfile(id: string): void {
		config.profiles = config.profiles.filter((p) => p.id !== id);
		if (config.activeProfileId === id) config.activeProfileId = '';
		delete openIds[id];
		deleteId = null;
	}

	const deleteName = $derived(config.profiles.find((p) => p.id === deleteId)?.name ?? '');
	const resetStatsName = $derived(config.profiles.find((p) => p.id === resetStatsId)?.name ?? '');
</script>

<section class="auto-card">
	<!-- Шапка автопилота -->
	<div class="auto-head">
		<div class="title-group">
			<h3>{m.warpscout_autopilot_title()}</h3>
		</div>
		<div class="head-controls">
			<div class="mode-seg">
				<SegmentedControl
					value={config.profileMode}
					options={modeOptions}
					ariaLabel={m.warpscout_profile_mode_aria()}
					onchange={setProfileMode}
				/>
			</div>
			<Button size="sm" variant="secondary" onclick={addProfile}>
				{#snippet iconBefore()}<Plus size={13} />{/snippet}
				{m.warpscout_add_profile()}
			</Button>
		</div>
	</div>

	<!-- Список профилей с акцентом на живую телеметрию -->
	<div class="profiles-body">
		{#if config.profiles.length === 0 && !dirty}
			<div class="empty-bar">
				<p class="note">{m.warpscout_empty_profiles()}</p>
				<Button size="sm" variant="primary" onclick={addProfile}>
					{#snippet iconBefore()}<Plus size={13} />{/snippet}
					{m.warpscout_create_profile()}
				</Button>
			</div>
		{:else}
			{#if config.profiles.length === 0}
				<p class="note">{m.warpscout_all_profiles_removed()}</p>
			{/if}

			{#each config.profiles as p, i (p.id)}
				{@const w = watchById.get(p.id)}
				{@const ps = statsById.get(p.id)}
				{@const view = watchStateView(w?.state)}
				{@const tgt = resolveTarget(p)}
				{@const expanded = isExpanded(p)}
				{@const trace = w?.trace}
				{@const ep = w?.endpoint || tgt?.endpoint || ps?.currentEndpoint || ''}
				{@const ledTone = !p.enabled ? 'muted' : view.tone}
				{@const hasStats = !!ps && (ps.checksTotal > 0 || ps.swapsOk > 0 || ps.swapsFail > 0)}
				{@const samples = ps?.samples ?? []}
				{@const maxSampleMs = Math.max(80, ...samples.map((s) => s.latencyMs ?? 0))}
				{@const coloDistribution = topColos(ps?.colos, 4)}
				<div class="profile" class:enabled={p.enabled} class:expanded>
					<!-- Верхняя телеметрическая полоса -->
					<div class="telemetry-row">
						<div class="tel-left">
							<span class="led {ledTone}" class:pulse={p.enabled && view.tone === 'ok'} title={view.label}></span>
							<span class="prof-name">{p.name || m.warpscout_unnamed()}</span>

							{#if tgt}
								<span class="tgt-pill">
									<span>{tgt.name}</span>
									{#if tgt.iface && tgt.iface !== tgt.name}
										<span class="mono muted">· {tgt.iface}</span>
									{/if}
								</span>
							{:else if p.targetId}
								<span class="tgt-pill warn">{m.warpscout_target_missing_pill({ id: p.targetId })}</span>
							{:else}
								<span class="tgt-pill warn">{m.warpscout_pick_tunnel_pill()}</span>
							{/if}

							{#if ep}
								<span class="ep-text mono">{ep}</span>
							{/if}
						</div>

						<div class="tel-right">
							{#if p.enabled && trace}
								{@const lTone = latencyTone(trace.latencyMs)}
								<div class="live-metric">
									{#if trace.latencyMs > 0}
										<span class="live-ms mono {lTone}">{m.warpscout_ms({ ms: trace.latencyMs })}</span>
									{/if}
									<span class="live-colo mono">
										{trace.colo || '—'}
										{#if trace.loc}· {countryFlag(trace.loc)} {trace.loc}{/if}
										{#if trace.warp}· warp={trace.warp}{/if}
									</span>
								</div>
							{:else if p.enabled || w}
								<Badge variant={view.variant} size="xs">{view.label}</Badge>
							{:else}
								<Badge variant="muted" size="xs">{m.warpscout_badge_off()}</Badge>
							{/if}

							<div class="row-controls">
								<Toggle
									checked={p.enabled}
									controlled
									size="sm"
									label={config.profileMode === 'single' ? m.warpscout_toggle_active() : m.warpscout_toggle_enabled()}
									onchange={(on) => setEnabled(p, on)}
								/>
								<button
									type="button"
									class="icon-btn danger"
									title={m.warpscout_delete_profile_aria()}
									aria-label={m.warpscout_delete_profile_aria()}
									onclick={() => (deleteId = p.id)}
								>
									<Trash2 size={14} />
								</button>
							</div>
						</div>
					</div>

					<!-- Строка сводки правил + кнопка «Настроить ▾» -->
					<div class="summary-row">
						<div class="summary-facts">
							<span><RichText text={m.warpscout_sum_interval({ sec: p.checkIntervalSec })} /></span>
							<span class="dot">·</span>
							<span><RichText text={m.warpscout_sum_threshold({ n: p.failThreshold })} /></span>
							<span class="dot">·</span>
							<span><RichText text={m.warpscout_sum_cooldown({ sec: p.cooldownSec })} /></span>
							{#if (p.filters.excludeColos ?? []).length > 0}
								<span class="dot">·</span>
								<span>{m.warpscout_sum_excl()} <span class="mono">{p.filters.excludeColos.join(', ')}</span></span>
							{/if}
							{#if (p.filters.allowedCountries ?? []).length > 0}
								<span class="dot">·</span>
								<span>ISO: <span class="mono">{p.filters.allowedCountries.join(', ')}</span></span>
							{/if}
							{#if w && w.fails > 0}
								<span class="dot">·</span>
								<span class="warn">{m.warpscout_sum_fails({ n: w.fails })}</span>
							{/if}
							{#if w?.message}
								<span class="dot">·</span>
								<span class="warn">{humanizeSwapReason(w.message)}</span>
							{/if}
							{#if w && parseGoTime(w.nextSwapAfter) && (parseGoTime(w.nextSwapAfter)?.getTime() ?? 0) > Date.now()}
								<span class="dot">·</span>
								<span class="muted">{m.warpscout_sum_cooldown_until({ time: formatTime(w.nextSwapAfter) })}</span>
							{/if}
							{#if w && parseGoTime(w.lastCheck)}
								<span class="dot">·</span>
								<span class="muted">{m.warpscout_sum_checked_at({ time: formatTime(w.lastCheck) })}</span>
							{/if}
						</div>

						{#if p.targetId}
							<button type="button" class="expand-btn" aria-expanded={expanded} onclick={() => toggleExpand(p)}>
								<SlidersHorizontal size={12} />
								<span>{expanded ? m.warpscout_collapse() : m.warpscout_configure()}</span>
								<span class="chev" class:open={expanded}><ChevronDown size={13} /></span>
							</button>
						{/if}
					</div>

					<!-- Мини-дашборд статистики автопилота (KPI + живая лента проверок + колокейшны) -->
					{#if p.enabled || hasStats}
						<div class="stats-panel">
							{#if !hasStats}
								<div class="stats-waiting">
									<Activity size={13} />
									<span>{m.warpscout_stats_waiting({ sec: p.checkIntervalSec })}</span>
								</div>
							{:else if ps}
								{@const slaPct = formatSlaPct(ps.checksOk, ps.checksTotal)}
								{@const slaRatio = ps.checksTotal > 0 ? ps.checksOk / ps.checksTotal : 1}
								{@const avgTone = latencyTone(ps.avgLatencyMs)}
								<div class="kpi-grid">
									<div
										class="kpi-tile"
										title={m.warpscout_kpi_uptime_tip({
											ok: ps.checksOk,
											total: ps.checksTotal,
											fail: ps.checksFail,
										})}
									>
										<span class="kpi-label">{m.warpscout_kpi_uptime()}</span>
										<span
											class="kpi-val mono"
											class:ok={slaRatio >= 0.97}
											class:warn={slaRatio < 0.97 && slaRatio >= 0.85}
											class:err={slaRatio < 0.85}
										>
											{slaPct}
										</span>
										<span class="kpi-sub">{m.warpscout_kpi_ok_ratio({ ok: ps.checksOk, total: ps.checksTotal })}</span>
									</div>

									<div class="kpi-tile" title={m.warpscout_kpi_ping_tip()}>
										<span class="kpi-label">{m.warpscout_kpi_avg_ping()}</span>
										<span class="kpi-val mono {avgTone}">
											{ps.avgLatencyMs && ps.avgLatencyMs > 0 ? m.warpscout_ms({ ms: ps.avgLatencyMs }) : '—'}
										</span>
										<span class="kpi-sub mono">
											{#if ps.minLatencyMs && ps.maxLatencyMs}
												{m.warpscout_ms_range({ min: ps.minLatencyMs, max: ps.maxLatencyMs })}
											{:else}
												{m.warpscout_kpi_no_samples()}
											{/if}
										</span>
									</div>

									<div
										class="kpi-tile"
										title={m.warpscout_kpi_swaps_tip({ ok: ps.swapsOk, fail: ps.swapsFail })}
									>
										<span class="kpi-label">{m.warpscout_kpi_swaps()}</span>
										<span class="kpi-val mono">
											{ps.swapsOk}
											{#if ps.swapsFail > 0}
												<span class="kpi-err-inline">{m.warpscout_kpi_err_short({ n: ps.swapsFail })}</span>
											{/if}
										</span>
										<span class="kpi-sub">
											{ps.checksFail > 0 ? m.warpscout_kpi_fails_count({ n: ps.checksFail }) : m.warpscout_kpi_no_fails()}
										</span>
									</div>

									<div
										class="kpi-tile"
										title={parseGoTime(ps.endpointSince)
											? m.warpscout_kpi_node_since_tip({ time: formatTime(ps.endpointSince) })
											: m.warpscout_kpi_node_time_tip()}
									>
										<span class="kpi-label">{m.warpscout_kpi_on_node()}</span>
										<span class="kpi-val mono">{formatUptime(ps.endpointSince || ps.startedAt)}</span>
										<span class="kpi-sub">
											{parseGoTime(ps.startedAt)
												? m.warpscout_kpi_since({ time: formatTime(ps.startedAt) })
												: m.warpscout_kpi_session()}
										</span>
									</div>
								</div>

								<div class="spark-bar-wrap">
									<div class="spark-header">
										<span class="spark-caption">
											{m.warpscout_spark_title()}
											{#if samples.length > 0}
												<span class="muted">({samples.length})</span>
											{:else}
												<span class="muted">{m.warpscout_spark_collecting()}</span>
											{/if}
										</span>

										{#if onresetstats}
											<button
												type="button"
												class="reset-stats-btn"
												title={m.warpscout_reset_stats_tip()}
												onclick={() => (resetStatsId = p.id)}
											>
												<RotateCcw size={11} />
												<span>{m.warpscout_reset_btn()}</span>
											</button>
										{/if}
									</div>

									{#if samples.length > 0}
										<div class="spark-strip" role="img" aria-label={m.warpscout_spark_aria()}>
											{#each buildSparkBars(samples) as s, idx (idx)}
												{#if !s}
													<span class="spark-col empty"><span class="spark-fill"></span></span>
												{:else}
													{@const barTone = !s.ok ? 'fail' : s.swapped ? 'swap' : latencyTone(s.latencyMs)}
													{@const hPct = sampleBarHeight(s, maxSampleMs)}
													<span class="spark-col {barTone}" title={sampleTooltip(s)}>
														<span class="spark-fill" style={`height:${hPct}%`}></span>
													</span>
												{/if}
											{/each}
										</div>
									{/if}

									{#if coloDistribution.length > 0}
										<div class="colo-dist">
											<span class="colo-dist-label">{m.warpscout_colos_label()}</span>
											{#each coloDistribution as item (item.colo)}
												<span
													class="colo-chip mono"
													title={m.warpscout_colo_chip_tip({
														colo: item.colo,
														count: item.count,
														pct: item.pct,
													})}
												>
													<b>{item.colo}</b>
													<span class="muted">{item.pct}%</span>
												</span>
											{/each}
										</div>
									{/if}
								</div>
							{/if}
						</div>
					{/if}

					<!-- Раскрывающийся редактор параметров профиля -->
					{#if expanded}
						<div class="editor-drawer">
							<div class="editor-top">
								<label class="field-col">
									<span>{m.warpscout_profile_name()}</span>
									<input
										class="text-input"
										type="text"
										bind:value={p.name}
										maxlength={64}
										placeholder={m.warpscout_profile_name_ph()}
									/>
								</label>
								<div class="target-col">
									<Dropdown
										label={m.warpscout_target_tunnel()}
										value={p.targetId ? targetKey(p.targetMode, p.targetId) : ''}
										options={targetOptions(p)}
										placeholder={m.warpscout_target_tunnel_ph()}
										fullWidth
										onchange={(v) => setTarget(p, v)}
									/>
								</div>
								<label class="num-field">
									<span>{m.warpscout_interval_sec()}</span>
									<input class="num" type="number" min="15" max="3600" bind:value={p.checkIntervalSec} />
								</label>
								<label class="num-field">
									<span>{m.warpscout_fail_threshold()}</span>
									<input class="num" type="number" min="1" max="20" bind:value={p.failThreshold} />
								</label>
								<label class="num-field" title={m.warpscout_cooldown_tip()}>
									<span>{m.warpscout_cooldown_sec()}</span>
									<input class="num" type="number" min="0" max="86400" bind:value={p.cooldownSec} />
								</label>
							</div>

							<div class="editor-filters">
								<WarpFiltersEditor bind:filters={p.filters} idPrefix={`profile-${i}`} />
							</div>
						</div>
					{/if}
				</div>
			{/each}

			{#if dirty}
				<div class="save-bar">
					<span class="dirty-msg">{m.warpscout_unsaved_changes()}</span>
					<div class="save-actions">
						<Button size="sm" variant="ghost" onclick={onreset} disabled={saving}>
							{#snippet iconBefore()}<RotateCcw size={13} />{/snippet}
							{m.warpscout_discard_btn()}
						</Button>
						<Button size="sm" variant="primary" loading={saving} onclick={onsave}>
							{#snippet iconBefore()}<Save size={13} />{/snippet}
							{m.common_save()}
						</Button>
					</div>
				</div>
			{/if}
		{/if}
	</div>

	<!-- Интегрированный журнал автоподмен -->
	<div class="timeline-section">
		<div class="timeline-head">
			<h4>{m.warpscout_recent_swaps()}</h4>
			{#if history.length > 0}
				<Badge variant="muted" size="xs">{history.length}</Badge>
			{:else}
				<span class="sub">{m.warpscout_no_swaps_yet()}</span>
			{/if}
		</div>

		{#if history.length > 0}
			<div class="timeline-list">
				{#each history as ev, i (`${ev.at}-${ev.profileId}-${i}`)}
					<div class="swap-item">
						<span class="swap-dot" class:ok={ev.ok} class:err={!ev.ok}></span>
						<div class="swap-main">
							<div class="swap-top">
								<span class="swap-prof">{ev.profileName || ev.profileId}</span>
								<span class="swap-reason" title={ev.reason || undefined}>
									{humanizeSwapReason(ev.reason)}
								</span>
								<span class="swap-time mono">{formatTime(ev.at)}</span>
							</div>
							<div class="swap-bottom">
								<span class="swap-eps mono">
									<span class="muted">{ev.oldEndpoint || '—'}</span>
									<ArrowRight size={11} />
									<span>{ev.newEndpoint || '—'}</span>
								</span>
								<span class="swap-meta">
									{#if ev.colo}<span class="mono">{ev.colo}</span>{/if}
									{#if ev.loc}<span>{countryFlag(ev.loc)} <span class="mono">{ev.loc}</span></span>{/if}
									{#if ev.pingMs > 0}<span class="muted">· {m.warpscout_ms({ ms: ev.pingMs })}</span>{/if}
									{#if ev.ok}
										<Badge variant="success" size="xs">ok</Badge>
									{:else}
										<Badge variant="error" size="xs" title={ev.error}>{m.warpscout_badge_err()}</Badge>
									{/if}
								</span>
							</div>
							{#if !ev.ok && ev.error}
								<div class="swap-err clamp-1" title={ev.error}>{humanizeSwapReason(ev.error)}</div>
							{/if}
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>
</section>

<ConfirmModal
	open={deleteId !== null}
	title={m.warpscout_confirm_del_profile_title()}
	message={m.warpscout_confirm_del_profile_msg({ name: deleteName })}
	confirmLabel={m.common_delete()}
	variant="danger"
	onConfirm={() => {
		if (deleteId) removeProfile(deleteId);
	}}
	onClose={() => (deleteId = null)}
/>

<ConfirmModal
	open={resetStatsId !== null}
	title={m.warpscout_confirm_reset_stats_title()}
	message={m.warpscout_confirm_reset_stats_msg({ name: resetStatsName })}
	secondary={m.warpscout_confirm_reset_stats_sec()}
	confirmLabel={m.warpscout_reset_btn()}
	variant="danger"
	busy={resettingStats}
	onConfirm={confirmResetStats}
	onClose={() => (resetStatsId = null)}
/>

<style>
	.auto-card {
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		box-shadow: var(--shadow);
		overflow: hidden;
		display: flex;
		flex-direction: column;
	}
	.auto-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.85rem;
		padding: 0.6rem 0.85rem;
		border-bottom: 1px solid var(--color-border);
		flex-wrap: wrap;
	}
	.title-group {
		display: flex;
		align-items: baseline;
		gap: 0.2rem 0.6rem;
		flex-wrap: wrap;
		min-width: 0;
	}
	.auto-head h3 {
		margin: 0;
		font-size: 0.92rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.sub {
		font-size: 0.74rem;
		color: var(--color-text-muted);
	}
	.head-controls {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-left: auto;
		flex-wrap: wrap;
	}
	.mode-seg {
		flex-shrink: 0;
	}
	.profiles-body {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		padding: 0.65rem 0.85rem;
	}
	.empty-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.6rem 1rem;
		padding: 0.55rem 0.75rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--color-bg-primary) 65%, transparent);
		flex-wrap: wrap;
	}
	.profile {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		overflow: hidden;
		transition: border-color 0.15s ease;
	}
	.profile.enabled {
		border-color: var(--color-accent-border);
	}
	.telemetry-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.85rem;
		padding: 0.55rem 0.75rem 0.4rem;
		flex-wrap: wrap;
	}
	.tel-left {
		display: flex;
		align-items: center;
		gap: 0.4rem 0.55rem;
		flex-wrap: wrap;
		min-width: 0;
	}
	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-text-muted);
	}
	.led.ok {
		background: var(--color-success);
	}
	.led.pulse {
		box-shadow: 0 0 0 3px var(--color-success-tint);
	}
	.led.warn {
		background: var(--color-warning);
	}
	.led.err {
		background: var(--color-error);
	}
	.led.info {
		background: var(--color-info);
	}
	.prof-name {
		font-size: 0.86rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.tgt-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		padding: 0.1rem 0.45rem;
		font-size: 0.73rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}
	.tgt-pill.warn {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}
	.ep-text {
		font-size: 0.74rem;
		color: var(--color-text-muted);
	}
	.tel-right {
		display: flex;
		align-items: center;
		gap: 0.65rem;
		margin-left: auto;
		flex-wrap: wrap;
	}
	.live-metric {
		display: inline-flex;
		align-items: baseline;
		gap: 0.45rem;
		padding: 0.15rem 0.55rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
	}
	.live-ms {
		font-size: 0.82rem;
		font-weight: 700;
	}
	.live-ms.ok {
		color: var(--latency-color-ok, var(--color-success));
	}
	.live-ms.slow {
		color: var(--latency-color-slow, var(--color-warning));
	}
	.live-ms.fail {
		color: var(--latency-color-fail, var(--color-error));
	}
	.live-colo {
		font-size: 0.73rem;
		color: var(--color-text-secondary);
	}
	.row-controls {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
	}
	.summary-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.4rem 0.75rem;
		padding: 0.25rem 0.75rem 0.5rem;
		flex-wrap: wrap;
	}
	.summary-facts {
		display: flex;
		align-items: center;
		gap: 0.2rem 0.4rem;
		flex-wrap: wrap;
		font-size: 0.73rem;
		color: var(--color-text-secondary);
	}
	.summary-facts :global(strong) {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.dot {
		color: var(--color-text-muted);
	}
	.expand-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		padding: 0.18rem 0.5rem;
		font-size: 0.73rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		cursor: pointer;
		margin-left: auto;
	}
	.expand-btn:hover {
		color: var(--color-text-primary);
		border-color: var(--color-border-hover);
	}
	.chev {
		display: inline-flex;
		transition: transform 0.15s ease;
	}
	.chev.open {
		transform: rotate(180deg);
	}
	.editor-drawer {
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
		padding: 0.65rem 0.75rem;
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-secondary) 75%, transparent);
	}
	.editor-top {
		display: grid;
		grid-template-columns: repeat(6, minmax(0, 1fr));
		gap: 0.5rem;
		align-items: end;
	}
	.field-col {
		grid-column: span 3;
	}
	.target-col {
		grid-column: span 3;
	}
	.num-field {
		grid-column: span 2;
	}
	.field-col,
	.num-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.73rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		min-width: 0;
	}
	.field-col span,
	.num-field span {
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.text-input,
	.num {
		width: 100%;
		height: 34px;
		box-sizing: border-box;
		padding: 0.3rem 0.5rem;
		font-size: 0.8125rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
	}
	.num {
		font-family: var(--font-mono);
	}
	.editor-filters {
		padding-top: 0.55rem;
		border-top: 1px dashed var(--color-border);
	}
	.save-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.85rem;
		padding: 0.45rem 0.7rem;
		border-radius: var(--radius-sm);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		flex-wrap: wrap;
	}
	.dirty-msg {
		font-size: 0.76rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}
	.save-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		margin-left: auto;
	}
	.timeline-section {
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-tertiary) 40%, transparent);
		padding: 0.55rem 0.85rem 0.65rem;
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
	}
	.timeline-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.timeline-head h4 {
		margin: 0;
		font-size: 0.8rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}
	.timeline-list {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		max-height: 13.5rem;
		overflow: auto;
	}
	.swap-item {
		display: flex;
		align-items: flex-start;
		gap: 0.55rem;
		padding: 0.42rem 0.6rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
	}
	.swap-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		margin-top: 0.32rem;
		flex-shrink: 0;
		background: var(--color-text-muted);
	}
	.swap-dot.ok {
		background: var(--color-success);
	}
	.swap-dot.err {
		background: var(--color-error);
	}
	.swap-main {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
	}
	.swap-top {
		display: flex;
		align-items: baseline;
		gap: 0.45rem;
		flex-wrap: wrap;
		font-size: 0.76rem;
	}
	.swap-prof {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.swap-reason {
		color: var(--color-text-secondary);
	}
	.swap-time {
		margin-left: auto;
		font-size: 0.71rem;
		color: var(--color-text-muted);
	}
	.swap-bottom {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.35rem 0.75rem;
		flex-wrap: wrap;
		font-size: 0.73rem;
	}
	.swap-eps {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		color: var(--color-text-primary);
	}
	.swap-meta {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		margin-left: auto;
	}
	.swap-err {
		font-size: 0.71rem;
		color: var(--color-error);
	}
	.clamp-1 {
		display: -webkit-box;
		-webkit-line-clamp: 1;
		line-clamp: 1;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	.icon-btn {
		display: inline-flex;
		border: 0;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 0.2rem;
	}
	.icon-btn.danger:hover {
		color: var(--color-error);
	}
	.mono {
		font-family: var(--font-mono);
	}
	.muted {
		color: var(--color-text-muted);
	}
	.warn {
		color: var(--color-warning);
	}
	.note {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-text-secondary);
	}
	.stats-panel {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.5rem 0.75rem 0.6rem;
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-secondary) 55%, transparent);
	}
	.stats-waiting {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.73rem;
		color: var(--color-text-muted);
	}
	.kpi-grid {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: 0.4rem;
	}
	.kpi-tile {
		display: flex;
		flex-direction: column;
		gap: 0.08rem;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		min-width: 0;
	}
	.kpi-label {
		font-size: 0.67rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.025em;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.kpi-val {
		font-size: 0.84rem;
		font-weight: 700;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.kpi-val.ok {
		color: var(--latency-color-ok, var(--color-success));
	}
	.kpi-val.slow,
	.kpi-val.warn {
		color: var(--latency-color-slow, var(--color-warning));
	}
	.kpi-val.fail,
	.kpi-val.err {
		color: var(--latency-color-fail, var(--color-error));
	}
	.kpi-err-inline {
		font-size: 0.7rem;
		font-weight: 600;
		color: var(--color-error);
		margin-left: 0.2rem;
	}
	.kpi-sub {
		font-size: 0.68rem;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.spark-bar-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.32rem;
	}
	.spark-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		font-size: 0.7rem;
	}
	.spark-caption {
		color: var(--color-text-secondary);
		font-weight: 500;
	}
	.reset-stats-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.22rem;
		padding: 0.1rem 0.38rem;
		font: inherit;
		font-size: 0.69rem;
		color: var(--color-text-muted);
		background: transparent;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		cursor: pointer;
	}
	.reset-stats-btn:hover {
		color: var(--color-text-primary);
		border-color: var(--color-border);
		background: var(--color-bg-primary);
	}
	.spark-strip {
		display: grid;
		grid-template-columns: repeat(32, minmax(0, 1fr));
		gap: 2px;
		height: 20px;
		align-items: end;
		padding: 2px 3px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
	}
	.spark-col {
		height: 100%;
		display: flex;
		align-items: flex-end;
		min-width: 0;
	}
	.spark-fill {
		width: 100%;
		border-radius: 1.5px;
		transition: height 0.2s ease;
	}
	.spark-col.empty .spark-fill {
		height: 22%;
		background: color-mix(in srgb, var(--color-border) 55%, transparent);
	}
	.spark-col.ok .spark-fill,
	.spark-col.none .spark-fill {
		background: var(--color-success);
	}
	.spark-col.slow .spark-fill {
		background: var(--color-warning);
	}
	.spark-col.fail .spark-fill {
		background: var(--color-error);
	}
	.spark-col.swap .spark-fill {
		background: var(--color-info);
	}
	.colo-dist {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		flex-wrap: wrap;
		font-size: 0.7rem;
	}
	.colo-dist-label {
		color: var(--color-text-muted);
		margin-right: 0.1rem;
	}
	.colo-chip {
		display: inline-flex;
		align-items: baseline;
		gap: 0.25rem;
		padding: 0.08rem 0.4rem;
		border-radius: 999px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		font-size: 0.7rem;
	}
	.colo-chip b {
		font-weight: 600;
		color: var(--color-text-primary);
	}

	@media (max-width: 640px) {
		.kpi-grid {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
		.head-controls {
			width: 100%;
			justify-content: space-between;
			margin-left: 0;
		}
		.field-col,
		.target-col {
			grid-column: 1 / -1;
		}
		.tel-right {
			width: 100%;
			justify-content: space-between;
			margin-left: 0;
		}
	}
</style>
