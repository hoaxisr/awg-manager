<script lang="ts">
	import type { DnsRoute } from '$lib/types';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		route: DnsRoute;
		ontoggle: (enabled: boolean) => void;
		toggleLoading?: boolean;
	}

	let { route, ontoggle, toggleLoading = false }: Props = $props();

	let cidrCount = $derived((route.domains ?? []).filter(d => d.includes('/')).length);
	let domainCount = $derived((route.domains?.length ?? 0) - cidrCount);

	let summary = $derived.by(() => {
		const subCount = route.subscriptions?.length ?? 0;
		const manualCount = route.manualDomains?.length ?? 0;

		const counts: string[] = [];
		if (domainCount > 0) counts.push(`${domainCount} доменов`);
		if (cidrCount > 0) counts.push(`${cidrCount} CIDR`);
		const countStr = counts.join(' + ') || '0 записей';

		if (subCount === 0 && manualCount === 0) {
			return countStr;
		}

		const parts: string[] = [];
		if (subCount > 0) {
			parts.push(`${subCount} подпис${subCount === 1 ? 'ка' : subCount < 5 ? 'ки' : 'ок'}`);
		}
		if (manualCount > 0) {
			parts.push(`${manualCount} вручную`);
		}
		return `${countStr} (${parts.join(' + ')})`;
	});

	let ledColor = $derived(route.enabled ? 'green' : 'gray');
</script>

<div class="header">
	<div class="header-left">
		<div class="header-title">
			<span
				class="led"
				class:led-green={ledColor === 'green'}
				class:led-gray={ledColor === 'gray'}
			></span>
			<h3 class="route-name">{route.name}</h3>
		</div>
		<span class="route-summary">{summary}</span>
	</div>
	<div class="header-right">
		<Toggle
			checked={route.enabled}
			onchange={(checked) => ontoggle(checked)}
			loading={toggleLoading}
			size="sm"
		/>
	</div>
</div>

<style>
	.header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 0.75rem;
	}

	.header-left {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		min-width: 0;
	}

	.header-title {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.route-name {
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--text-primary);
		margin: 0;
	}

	.route-summary {
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.header-right {
		flex-shrink: 0;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.led-green {
		background: var(--success, #10b981);
		box-shadow: 0 0 6px var(--success, #10b981);
	}

	.led-gray {
		background: var(--text-muted, #6b7280);
		box-shadow: none;
	}
</style>
