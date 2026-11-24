<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Badge, StatusDot } from '$lib/components/ui';
	import { Play, Pause, Trash2, RefreshCw, Radio } from 'lucide-svelte';

	type JournalSubTab = 'events' | 'dns' | 'capture';

	interface Props {
		subTab: JournalSubTab;
		eventsCount: number;
		paused: boolean;
		dnsDomains: number;
		dnsQueries: number;
		dnsBlocked: number;
		dnsLoading: boolean;
		captureRunning: boolean;
		captureRemaining: number;
		captureBusy: boolean;
		onSelectTab: (tab: JournalSubTab) => void;
		onClearEvents: () => void;
		onRefreshDns: () => void;
		onToggleCapture: () => void;
	}

	let {
		subTab,
		eventsCount,
		paused = $bindable(),
		dnsDomains,
		dnsQueries,
		dnsBlocked,
		dnsLoading,
		captureRunning,
		captureRemaining,
		captureBusy,
		onSelectTab,
		onClearEvents,
		onRefreshDns,
		onToggleCapture,
	}: Props = $props();
</script>

<!-- Subtabs Header -->
<div class="journal-top-bar">
	<div class="journal-subtabs">
		<button
			type="button"
			class="subtab-btn"
			class:active={subTab === 'events'}
			onclick={() => onSelectTab('events')}
		>
			{m.firc_journal_tab_events({ n: eventsCount })}
		</button>
		<button
			type="button"
			class="subtab-btn"
			class:active={subTab === 'dns'}
			onclick={() => onSelectTab('dns')}
		>
			{m.firc_journal_tab_dns({ n: dnsDomains })}
		</button>
		<button
			type="button"
			class="subtab-btn"
			class:active={subTab === 'capture'}
			onclick={() => onSelectTab('capture')}
		>
			{m.firc_journal_tab_capture()}
			{#if captureRunning}
				<StatusDot variant="success" size="sm" pulse />
			{/if}
		</button>
	</div>

	{#if subTab === 'events'}
		<div class="journal-actions">
			<Button
				variant="secondary"
				size="sm"
				onclick={() => (paused = !paused)}
			>
				{#snippet iconBefore()}
					{#if paused}
						<Play size={13} />
					{:else}
						<Pause size={13} />
					{/if}
				{/snippet}
				{paused ? m.diag_logs_resume() : m.diag_logs_pause()}
			</Button>
			<Button
				variant="ghost"
				size="sm"
				onclick={onClearEvents}
			>
				{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
				{m.common_clear()}
			</Button>
		</div>
	{:else if subTab === 'dns'}
		<div class="journal-actions">
			<Badge variant="default" size="sm" mono>{m.firc_journal_dns_queries({ n: dnsQueries })}</Badge>
			{#if dnsBlocked > 0}
				<Badge variant="warning" size="sm" mono>{m.firc_journal_dns_blocked({ n: dnsBlocked })}</Badge>
			{/if}
			<Button
				variant="secondary"
				size="sm"
				loading={dnsLoading}
				onclick={onRefreshDns}
			>
				{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
				{m.common_refresh()}
			</Button>
		</div>
	{:else if subTab === 'capture'}
		<div class="journal-actions">
			{#if captureRunning}
				<Badge variant="warning" size="sm" mono>{m.firc_journal_capture_left({ n: captureRemaining })}</Badge>
			{/if}
			<Button
				variant={captureRunning ? 'danger' : 'primary'}
				size="sm"
				loading={captureBusy}
				onclick={onToggleCapture}
			>
				{#snippet iconBefore()}<Radio size={13} />{/snippet}
				{captureRunning ? m.firc_journal_capture_stop() : m.firc_journal_capture_start()}
			</Button>
		</div>
	{/if}
</div>

<style>
	.journal-top-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}

	.journal-subtabs {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 3px;
	}

	.subtab-btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 5px 12px;
		border: none;
		border-radius: 6px;
		background: transparent;
		color: var(--text-secondary);
		font-size: 12px;
		font-weight: 500;
		cursor: pointer;
		transition: all 0.15s;
	}

	.subtab-btn:hover {
		color: var(--text-primary);
	}

	.subtab-btn.active {
		background: var(--bg-tertiary);
		color: var(--accent);
		font-weight: 600;
	}

	.journal-actions {
		display: flex;
		align-items: center;
		gap: 8px;
	}
</style>
