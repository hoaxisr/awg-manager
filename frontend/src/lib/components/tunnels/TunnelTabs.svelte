<script lang="ts">
	export type TunnelTab = 'awg' | 'singbox';

	interface Props {
		active: TunnelTab;
		awgCount: number;
		singboxCount: number;
		onchange?: (tab: TunnelTab) => void;
	}

	let { active = $bindable('awg'), awgCount, singboxCount, onchange }: Props = $props();

	function select(tab: TunnelTab): void {
		active = tab;
		onchange?.(tab);
	}
</script>

<nav class="tunnel-tabs" role="tablist">
	<button
		type="button"
		role="tab"
		aria-selected={active === 'awg'}
		class="tunnel-tab"
		class:active={active === 'awg'}
		onclick={() => select('awg')}
	>
		<span>AmneziaWG</span>
		<span class="count">{awgCount}</span>
	</button>
	<button
		type="button"
		role="tab"
		aria-selected={active === 'singbox'}
		class="tunnel-tab"
		class:active={active === 'singbox'}
		onclick={() => select('singbox')}
	>
		<span>Sing-box</span>
		<span class="count">{singboxCount}</span>
	</button>
</nav>

<style>
	.tunnel-tabs {
		display: flex;
		gap: 2px;
		border-bottom: 1px solid var(--border);
		margin-bottom: 1rem;
	}
	.tunnel-tab {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 8px 16px;
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 13px;
		font-family: inherit;
		cursor: pointer;
		border-bottom: 2px solid transparent;
		transition: color 0.15s, border-color 0.15s;
	}
	.tunnel-tab:hover {
		color: var(--text);
	}
	.tunnel-tab.active {
		color: var(--primary, #60a5fa);
		border-bottom-color: var(--primary, #60a5fa);
	}
	.count {
		background: var(--bg-tertiary, rgba(100, 100, 100, 0.2));
		color: var(--text-muted);
		padding: 1px 7px;
		border-radius: 10px;
		font-size: 11px;
		font-variant-numeric: tabular-nums;
	}
	.tunnel-tab.active .count {
		background: rgba(96, 165, 250, 0.15);
		color: var(--primary, #60a5fa);
	}
</style>
