<script lang="ts">
	export interface ProxyPanelTab {
		id: string;
		label: string;
	}

	interface Props {
		tabs: ProxyPanelTab[];
		active: string;
		onchange: (id: string) => void;
	}

	let { tabs, active, onchange }: Props = $props();
</script>

<div class="proxy-panel-tabs" role="tablist">
	{#each tabs as tab (tab.id)}
		<button
			type="button"
			class="proxy-panel-tab"
			class:active={active === tab.id}
			role="tab"
			aria-selected={active === tab.id}
			onclick={() => onchange(tab.id)}
		>
			{tab.label}
		</button>
	{/each}
</div>

<style>
	.proxy-panel-tabs {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.proxy-panel-tab {
		font: inherit;
		font-size: 0.8125rem;
		padding: 0.375rem 0.75rem;
		border-radius: 999px;
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
	}

	.proxy-panel-tab:hover {
		color: var(--color-text-primary);
		border-color: var(--color-accent-border, var(--color-border));
	}

	.proxy-panel-tab.active {
		background: var(--color-accent-bg, rgba(59, 130, 246, 0.12));
		border-color: var(--accent-line, var(--color-accent-border));
		color: var(--color-text-primary);
		font-weight: 600;
	}
</style>
