<script lang="ts">
	import type { DnsRoute } from '$lib/types';
	import { Toggle } from '$lib/components/ui';
	import { ServiceIcon } from '$lib/components/dnsroutes';
	import { SquarePen, Trash2 } from 'lucide-svelte';

	interface Props {
		rule: DnsRoute;
		broken?: boolean;
		ontoggle?: (enabled: boolean) => void;
		onedit: () => void;
		ondelete: () => void;
		onicon?: () => void;
		toggleLoading?: boolean;
	}

	let {
		rule,
		broken = false,
		ontoggle,
		onedit,
		ondelete,
		onicon,
		toggleLoading = false,
	}: Props = $props();

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

<div class="hr-card" class:enabled={rule.enabled} class:broken>
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
				<span
					class="led"
					class:led-green={rule.enabled && !broken}
					class:led-gray={!rule.enabled && !broken}
					class:led-red={broken}
				></span>
				<h3 lang="ru" title={rule.name}>{rule.name}</h3>
				{#if broken}<span class="broken-badge">broken</span>{/if}
			</div>
			{#if counts.d > 0}<span class="card-stat">{counts.d} доменов</span>{/if}
			{#if counts.s > 0}<span class="card-stat">{counts.s} CIDR</span>{/if}
			{#if counts.gs > 0}<span class="card-stat geo">{counts.gs} geosite</span>{/if}
			{#if counts.gi > 0}<span class="card-stat geo">{counts.gi} geoip</span>{/if}
		</div>
	</div>
	<div class="card-actions">
		{#if ontoggle}
			<Toggle
				checked={rule.enabled}
				onchange={(checked) => ontoggle(checked)}
				loading={toggleLoading}
				disabled={broken}
				size="sm"
			/>
		{/if}
		<div class="action-row">
			<button class="route-action-btn" title="Изменить" onclick={() => onedit()} aria-label="Edit">
				<SquarePen size={15} />
			</button>
			<button class="route-action-btn danger" title="Удалить" onclick={() => ondelete()} aria-label="Delete">
				<Trash2 size={15} />
			</button>
		</div>
	</div>
</div>

<style>
	.hr-card {
		display: flex;
		justify-content: space-between;
		border-radius: 8px;
		padding: 14px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		transition: border-color 0.2s;
		min-width: 0;
	}
	.hr-card:hover {
		border-color: var(--border-hover);
	}
	.hr-card:not(.enabled) {
		opacity: 0.4;
	}
	.hr-card.broken {
		border-color: var(--error);
	}

	.card-main {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		min-width: 0;
	}

	.card-info {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
		max-width: 100%;
	}

	.card-title {
		display: flex;
		align-items: flex-start;
		gap: 6px;
		min-width: 0;
		max-width: 100%;
	}

	.card-title h3 {
		margin: 0;
		flex: 1;
		font-size: 0.875rem;
		line-height: 1.25;
		color: var(--text-primary);
		font-weight: 600;
		min-width: 0;
		max-width: 100%;
		text-wrap: pretty;
		overflow-wrap: normal;
		word-break: normal;
		hyphens: auto;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
		margin-top: 0.2em;
	}
	.led-green {
		background: var(--success);
		box-shadow: 0 0 6px var(--success);
	}
	.led-gray {
		background: var(--text-muted);
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
		flex-shrink: 0;
	}

	.card-stat {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}
	.card-stat.geo {
		color: var(--info);
	}

	.card-actions {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 8px;
		flex-shrink: 0;
		margin-left: 8px;
		align-self: stretch;
	}

	.action-row {
		display: flex;
		gap: 4px;
		align-items: center;
		margin-top: auto;
	}

	.icon-btn {
		padding: 0;
		background: none;
		border: 1px solid transparent;
		border-radius: 7px;
		cursor: pointer;
		transition: border-color 0.15s;
		display: flex;
		align-items: flex-start;
		justify-content: center;
		flex-shrink: 0;
		align-self: flex-start;
	}

	.icon-btn:hover {
		border-color: var(--border-hover);
	}

	.icon-btn:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}
</style>
