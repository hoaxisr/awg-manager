<script lang="ts">
	import { ArrowRight, Trash2 } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Badge, Button } from '$lib/components/ui';
	import type { WarpSwapEvent } from '$lib/types/warpscout';
	import { countryFlag, formatTime, humanizeSwapReason } from './warpscoutUi';

	interface Props {
		history: WarpSwapEvent[];
		onclear?: () => void;
	}

	let { history, onclear }: Props = $props();
</script>

<div class="timeline-section">
	<div class="timeline-head">
		<div class="timeline-title">
			<h4>{m.warpscout_recent_swaps()}</h4>
			{#if history.length > 0}
				<Badge variant="muted" size="xs">{history.length}</Badge>
			{:else}
				<span class="sub">{m.warpscout_no_swaps_yet()}</span>
			{/if}
		</div>
		{#if history.length > 0 && onclear}
			<Button size="sm" variant="ghost" onclick={onclear}>
				<Trash2 size={12} />
				<span>{m.warpscout_clear_history_btn()}</span>
			</Button>
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

<style>
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
	.timeline-title {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		flex-wrap: wrap;
	}
	.timeline-head h4 {
		margin: 0;
		font-size: 0.8rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}
	.sub {
		font-size: 0.74rem;
		color: var(--color-text-muted);
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
	.mono {
		font-family: var(--font-mono);
	}
	.muted {
		color: var(--color-text-muted);
	}
</style>
