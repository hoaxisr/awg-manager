<script lang="ts">
	import { untrack } from 'svelte';
	import { ChevronDown, SlidersHorizontal, Trash2 } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Badge, Button, IconButton, Toggle } from '$lib/components/ui';
	import RichText from '$lib/components/ui/RichText.svelte';
	import type { WarpProfile, WarpProfileStats, WarpTunnelRef, WarpWatchStatus } from '$lib/types/warpscout';
	import WarpProfileEditor from './WarpProfileEditor.svelte';
	import WarpProfileStatsPanel from './WarpProfileStats.svelte';
	import { countryFlag, formatTime, humanizeSwapReason, latencyTone, parseGoTime, watchStateView } from './warpscoutUi';

	interface Props {
		profile: WarpProfile;
		/** Режим одного активного профиля — меняет подпись переключателя. */
		single: boolean;
		targets: WarpTunnelRef[];
		watch?: WarpWatchStatus;
		stats?: WarpProfileStats;
		idPrefix: string;
		onenable: (on: boolean) => void;
		ondelete: () => void;
		onresetstats?: () => void;
	}

	let {
		profile = $bindable(),
		single,
		targets,
		watch,
		stats,
		idPrefix,
		onenable,
		ondelete,
		onresetstats,
	}: Props = $props();

	// Новый профиль (туннель ещё не выбран) открыт сразу и остаётся открытым
	// после выбора туннеля; без туннеля редактор не сворачивается вовсе.
	let open = $state(untrack(() => !profile.targetId));
	const expanded = $derived(!profile.targetId || open);

	const view = $derived(watchStateView(watch?.state));
	const tgt = $derived(
		profile.targetId
			? targets.find((t) => t.mode === profile.targetMode && t.id === profile.targetId)
			: undefined,
	);
	const trace = $derived(watch?.trace);
	const ep = $derived(watch?.endpoint || tgt?.endpoint || stats?.currentEndpoint || '');
	const ledTone = $derived(!profile.enabled ? 'muted' : view.tone);
	const hasStats = $derived(!!stats && (stats.checksTotal > 0 || stats.swapsOk > 0 || stats.swapsFail > 0));
</script>

<div class="profile" class:enabled={profile.enabled}>
	<!-- Верхняя телеметрическая полоса -->
	<div class="telemetry-row">
		<div class="tel-left">
			<span class="led {ledTone}" class:pulse={profile.enabled && view.tone === 'ok'} title={view.label}></span>
			<span class="prof-name">{profile.name || m.warpscout_unnamed()}</span>

			{#if tgt}
				<span class="tgt-pill">
					<span>{tgt.name}</span>
					{#if tgt.iface && tgt.iface !== tgt.name}
						<span class="mono muted">· {tgt.iface}</span>
					{/if}
				</span>
			{:else if profile.targetId}
				<span class="tgt-pill warn">{m.warpscout_target_missing_pill({ id: profile.targetId })}</span>
			{:else}
				<span class="tgt-pill warn">{m.warpscout_pick_tunnel_pill()}</span>
			{/if}

			{#if ep}
				<span class="ep-text mono">{ep}</span>
			{/if}
		</div>

		<div class="tel-right">
			{#if profile.enabled && trace}
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
			{:else if profile.enabled || watch}
				<Badge variant={view.variant} size="xs">{view.label}</Badge>
			{:else}
				<Badge variant="muted" size="xs">{m.warpscout_badge_off()}</Badge>
			{/if}

			<div class="row-controls">
				<Toggle
					checked={profile.enabled}
					controlled
					size="sm"
					label={single ? m.warpscout_toggle_active() : m.warpscout_toggle_enabled()}
					onchange={onenable}
				/>
				<IconButton
					variant="danger"
					ariaLabel={m.warpscout_delete_profile_aria()}
					title={m.warpscout_delete_profile_aria()}
					onclick={ondelete}
				>
					<Trash2 size={14} />
				</IconButton>
			</div>
		</div>
	</div>

	<!-- Строка сводки правил + кнопка «Настроить ▾» -->
	<div class="summary-row">
		<div class="summary-facts">
			<span><RichText text={m.warpscout_sum_interval({ sec: profile.checkIntervalSec })} /></span>
			<span class="dot">·</span>
			<span><RichText text={m.warpscout_sum_threshold({ n: profile.failThreshold })} /></span>
			<span class="dot">·</span>
			<span><RichText text={m.warpscout_sum_cooldown({ sec: profile.cooldownSec })} /></span>
			{#if (profile.filters.excludeColos ?? []).length > 0}
				<span class="dot">·</span>
				<span>{m.warpscout_sum_excl()} <span class="mono">{profile.filters.excludeColos.join(', ')}</span></span>
			{/if}
			{#if (profile.filters.allowedCountries ?? []).length > 0}
				<span class="dot">·</span>
				<span>ISO: <span class="mono">{profile.filters.allowedCountries.join(', ')}</span></span>
			{/if}
			{#if watch && watch.fails > 0}
				<span class="dot">·</span>
				<span class="warn">{m.warpscout_sum_fails({ n: watch.fails })}</span>
			{/if}
			{#if watch?.message}
				<span class="dot">·</span>
				<span class="warn">{humanizeSwapReason(watch.message)}</span>
			{/if}
			{#if watch && (parseGoTime(watch.nextSwapAfter)?.getTime() ?? 0) > Date.now()}
				<span class="dot">·</span>
				<span class="muted">{m.warpscout_sum_cooldown_until({ time: formatTime(watch.nextSwapAfter) })}</span>
			{/if}
			{#if watch && parseGoTime(watch.lastCheck)}
				<span class="dot">·</span>
				<span class="muted">{m.warpscout_sum_checked_at({ time: formatTime(watch.lastCheck) })}</span>
			{/if}
		</div>

		{#if profile.targetId}
			<div class="expand-wrap">
				<Button size="sm" variant="ghost" onclick={() => (open = !expanded)}>
					{#snippet iconBefore()}<SlidersHorizontal size={12} />{/snippet}
					{expanded ? m.warpscout_collapse() : m.warpscout_configure()}
					{#snippet iconAfter()}
						<span class="chev" class:open={expanded}><ChevronDown size={13} /></span>
					{/snippet}
				</Button>
			</div>
		{/if}
	</div>

	{#if profile.enabled || hasStats}
		<WarpProfileStatsPanel {stats} checkIntervalSec={profile.checkIntervalSec} onreset={onresetstats} />
	{/if}

	{#if expanded}
		<WarpProfileEditor bind:profile {targets} {idPrefix} />
	{/if}
</div>

<style>
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
	.expand-wrap {
		margin-left: auto;
	}
	.chev {
		display: inline-flex;
		transition: transform 0.15s ease;
	}
	.chev.open {
		transform: rotate(180deg);
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

	@media (max-width: 640px) {
		.tel-right {
			width: 100%;
			justify-content: space-between;
			margin-left: 0;
		}
	}
</style>
