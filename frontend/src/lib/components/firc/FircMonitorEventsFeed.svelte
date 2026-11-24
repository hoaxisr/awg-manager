<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircMonitorSnapshot } from '$lib/types/firc';
	import { Badge } from '$lib/components/ui';
	import { IpInspectSection } from '$lib/components/diagnostics';
	import { Search, ArrowRight, Info } from 'lucide-svelte';
	import { formatTime, formatOutboundLabel } from './fircMonitorFormat';

	interface Props {
		snapshot: FircMonitorSnapshot | null;
		// Фильтры и раскрытое событие живут у панели: переживают переключение
		// разделов монитора, как и до выделения ленты.
		sourceFilter: string;
		searchFilter: string;
		inspectedEventSeq: number | null;
		onOpenQuickAdd: (target: { ip?: string; domain?: string; prefix?: string }) => void;
	}

	let {
		snapshot,
		sourceFilter = $bindable(),
		searchFilter = $bindable(),
		inspectedEventSeq = $bindable(),
		onOpenQuickAdd,
	}: Props = $props();

	let filteredMatches = $derived.by(() => {
		const list = snapshot?.recentMatches ?? [];
		const q = searchFilter.trim().toLowerCase();
		return list.filter((m) => {
			if (sourceFilter !== 'all' && m.sourceId !== sourceFilter) return false;
			if (q) {
				const hay = `${m.domain} ${m.clientIp} ${m.sourceName} ${m.groupName} ${m.outbound} ${m.fakeIp ?? ''} ${m.realIp ?? ''}`.toLowerCase();
				if (!hay.includes(q)) return false;
			}
			return true;
		});
	});
</script>

<!-- Section 3: Live Matched Feed -->
<div class="mon-card">
	<div class="mon-card-head">
		<span>{m.firc_monitor_feed_title()}</span>
		<div class="feed-filters">
			<div class="feed-search">
				<Search size={13} />
				<input
					type="text"
					placeholder={m.firc_monitor_feed_search()}
					bind:value={searchFilter}
				/>
			</div>
			<select class="feed-select" bind:value={sourceFilter}>
				<option value="all">{m.firc_monitor_feed_all_sources({ n: snapshot?.recentMatches?.length ?? 0 })}</option>
				{#each snapshot?.servers ?? [] as srv (srv.id)}
					<option value={srv.id}>{srv.name}</option>
				{/each}
			</select>
		</div>
	</div>

	{#if filteredMatches.length === 0}
		<div class="feed-empty">
			{m.firc_monitor_feed_empty()}
		</div>
	{:else}
		<div class="feed-list">
			{#each filteredMatches as ev (ev.seq)}
				<div class="feed-item">
					<div class="feed-row">
						<span class="feed-time mono">{formatTime(ev.at)}</span>
						<Badge variant={ev.sourceId === 'lan_br0' ? 'default' : 'accent'} size="xs">
							{ev.sourceName}
						</Badge>
						<span class="feed-client mono">{ev.clientIp}</span>
						<ArrowRight size={12} class="feed-arrow" />
						<span class="feed-domain mono">{ev.domain}</span>
						{#if ev.fakeIp}
							<span class="feed-fake mono">
								({ev.fakeIp}{ev.realIp ? ` → ${ev.realIp}` : ''})
							</span>
						{/if}
						<ArrowRight size={12} class="feed-arrow" />
						<Badge variant="success" size="xs">{ev.groupName || ev.groupId}</Badge>
						{#if ev.outbound}
							<ArrowRight size={12} class="feed-arrow" />
							<Badge variant="info" size="xs" mono>{formatOutboundLabel(ev.outbound, ev.outboundLabel)}</Badge>
						{/if}
						<!-- Переключатель с aria-expanded и подсветкой открытого: IconButton
						     не умеет ни того, ни другого, поэтому кнопка своя. -->
						<button
							type="button"
							class="feed-inspect-btn"
							class:active={inspectedEventSeq === ev.seq}
							aria-label={m.ip_inspect_toggle_aria()}
							aria-expanded={inspectedEventSeq === ev.seq}
							onclick={() => (inspectedEventSeq = inspectedEventSeq === ev.seq ? null : ev.seq)}
						>
							<Info size={12} aria-hidden="true" />
						</button>
					</div>
					{#if inspectedEventSeq === ev.seq}
						<div class="feed-inspect-wrap">
							<IpInspectSection
								ip={ev.realIp || ev.fakeIp || ''}
								domain={ev.domain}
								compact
								{onOpenQuickAdd}
							/>
						</div>
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.mon-card {
		display: flex;
		flex-direction: column;
		border-radius: 8px;
		background: var(--bg-primary, rgba(0, 0, 0, 0.14));
		border: 1px solid var(--border);
		overflow: hidden;
		min-width: 0;
		max-width: 100%;
	}

	.mon-card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.55rem 0.75rem;
		border-bottom: 1px solid var(--border);
		font-size: 0.8rem;
		font-weight: 600;
		color: var(--text-primary);
		flex-wrap: wrap;
	}

	.mono {
		font-family: var(--font-mono);
	}

	.feed-filters {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		flex-wrap: wrap;
		min-width: 0;
		max-width: 100%;
	}

	.feed-search {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0 0.5rem;
		height: 28px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-secondary);
		color: var(--text-muted);
		min-width: 0;
		max-width: 100%;
		flex: 1 1 180px;
	}

	.feed-search input {
		border: none;
		background: transparent;
		color: var(--text-primary);
		font-size: 0.75rem;
		outline: none;
		width: 100%;
		min-width: 0;
	}

	.feed-select {
		height: 28px;
		padding: 0 0.5rem;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-secondary);
		color: var(--text-primary);
		font-size: 0.75rem;
		min-width: 0;
		max-width: 100%;
	}

	.feed-empty {
		padding: 1rem;
		text-align: center;
		font-size: 0.78rem;
		color: var(--text-muted);
	}

	.feed-list {
		display: flex;
		flex-direction: column;
		max-height: 240px;
		overflow-y: auto;
	}

	.feed-row {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.38rem 0.75rem;
		border-bottom: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
		font-size: 0.76rem;
		flex-wrap: wrap;
	}

	.feed-row:last-child {
		border-bottom: none;
	}

	.feed-time {
		color: var(--text-muted);
		font-size: 0.72rem;
	}

	.feed-client {
		color: var(--text-secondary);
		font-size: 0.73rem;
	}

	.feed-domain {
		color: var(--text-primary);
		font-weight: 600;
	}

	.feed-fake {
		color: var(--text-muted);
		font-size: 0.71rem;
	}

	.feed-item {
		border-bottom: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
	}

	.feed-item:last-child {
		border-bottom: none;
	}

	.feed-item .feed-row {
		border-bottom: none;
	}

	.feed-inspect-btn {
		all: unset;
		margin-left: auto;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		border-radius: 4px;
		color: var(--text-muted);
		cursor: pointer;
	}

	.feed-inspect-btn:hover,
	.feed-inspect-btn.active {
		color: var(--text-primary);
		background: var(--color-bg-hover, rgba(255, 255, 255, 0.08));
	}

	.feed-inspect-wrap {
		padding: 0 0.75rem 0.6rem;
	}
</style>
