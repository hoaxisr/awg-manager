<script lang="ts">
	import type { DnsRoute } from '$lib/types';
	import { ServiceIcon } from '$lib/components/dnsroutes';

	interface Props {
		rule: DnsRoute;
		broken?: boolean;
		onedit: () => void;
		ondelete: () => void;
		onicon?: () => void;
	}

	let { rule, broken = false, onedit, ondelete, onicon }: Props = $props();

	let counts = $derived.by(() => {
		const domains = rule.domains ?? [];
		const subnets = rule.subnets ?? [];
		const d = domains.filter((x) => !x.startsWith('geosite:')).length;
		const gs = domains.filter((x) => x.startsWith('geosite:')).length;
		const s = subnets.filter((x) => !x.startsWith('geoip:')).length;
		const gi = subnets.filter((x) => x.startsWith('geoip:')).length;
		return { d, s, gs, gi };
	});
</script>

<div class="hr-card" class:broken>
	<div class="card-main">
		{#if onicon}
			<button
				class="icon-btn"
				type="button"
				onclick={() => onicon()}
				aria-label="Сменить иконку"
				title="Сменить иконку"
			>
				<ServiceIcon name={rule.name} iconUrl={rule.iconUrl} size={36} />
			</button>
		{:else}
			<ServiceIcon name={rule.name} iconUrl={rule.iconUrl} size={36} />
		{/if}
		<div class="card-info">
			<div class="card-title">
				<span class="led" class:led-green={!broken} class:led-red={broken}></span>
				<h3>{rule.name}</h3>
				{#if broken}<span class="broken-badge">broken</span>{/if}
			</div>
			<div class="card-stats">
				{#if counts.d > 0}<span class="card-stat">{counts.d} доменов</span>{/if}
				{#if counts.s > 0}<span class="card-stat">{counts.s} CIDR</span>{/if}
				{#if counts.gs > 0}<span class="card-stat geo">{counts.gs} geosite</span>{/if}
				{#if counts.gi > 0}<span class="card-stat geo">{counts.gi} geoip</span>{/if}
			</div>
		</div>
	</div>
	<div class="card-actions">
		<button class="route-action-btn" title="Изменить" onclick={() => onedit()} aria-label="Edit">
			<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
				<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
			</svg>
		</button>
		<button class="route-action-btn danger" title="Удалить" onclick={() => ondelete()} aria-label="Delete">
			<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<polyline points="3 6 5 6 21 6" />
				<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
			</svg>
		</button>
	</div>
</div>

<style>
	.hr-card {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: center;
		gap: 12px;
		border-radius: 8px;
		padding: 12px 14px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		transition: border-color 0.2s;
		min-width: 0;
	}
	.hr-card:hover {
		border-color: var(--border-hover);
	}
	.hr-card.broken {
		border-color: var(--error);
	}

	.card-main {
		display: flex;
		gap: 12px;
		align-items: center;
		min-width: 0;
	}

	.card-info {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
		max-width: 100%;
	}

	.card-title {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		max-width: 100%;
	}

	.card-title h3 {
		margin: 0;
		font-size: 0.9375rem;
		color: var(--text-primary);
		font-weight: 600;
		min-width: 0;
		max-width: 100%;
		white-space: normal;
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.2;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.led-green {
		background: var(--success);
		box-shadow: 0 0 6px var(--success);
	}
	.led-red {
		background: var(--error);
		box-shadow: 0 0 6px var(--error);
	}

	.broken-badge {
		background: rgba(247, 118, 142, 0.15);
		color: var(--error);
		font-size: 0.6875rem;
		padding: 1px 6px;
		border-radius: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.card-stats {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
		margin-top: 2px;
		min-width: 0;
	}

	.card-stat {
		font-size: 0.75rem;
		color: var(--text-muted);
		white-space: normal;
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.card-stat.geo {
		color: var(--info);
	}

	.card-actions {
		display: flex;
		gap: 4px;
		align-items: center;
		justify-content: flex-end;
		flex-shrink: 0;
	}

	@media (max-width: 640px) {
		.hr-card {
			grid-template-columns: minmax(0, 1fr) auto;
			padding: 10px 12px;
			align-items: start;
		}
	}

	.icon-btn {
		padding: 0;
		background: none;
		border: 1px solid transparent;
		border-radius: 7px;
		cursor: pointer;
		transition: border-color 0.15s;
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
	}

	.icon-btn:hover {
		border-color: var(--border-hover);
	}

	.icon-btn:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}
</style>
