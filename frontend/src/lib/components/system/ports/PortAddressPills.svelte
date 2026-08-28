<script lang="ts">
	import type { PortAddress } from './types';

	interface Props {
		addresses: PortAddress[];
		label?: string;
		marginTop?: string;
	}

	let { addresses, label, marginTop }: Props = $props();
</script>

<div class="addr-chips" style={marginTop ? `margin-top: ${marginTop};` : undefined}>
	{#if label}
		<span class="addr-label">{label}</span>
	{/if}
	{#each addresses as a}
		<span class="addr-pill">
			<span class="pill-proto {a.proto.startsWith('udp') ? 'udp' : 'tcp'}">{a.proto.toUpperCase()}</span>
			<code>{a.ip}:{a.port}</code>
		</span>
	{/each}
</div>

<style>
	.addr-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		align-items: center;
		margin-top: 0.35rem;
	}
	.addr-label {
		font-size: 0.78rem;
		color: var(--color-text-muted);
		font-weight: 500;
	}
	.addr-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		padding: 0.15rem 0.45rem;
		border-radius: 4px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		font-size: 0.8rem;
	}
	.pill-proto {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.25rem;
		border-radius: 3px;
	}
	.pill-proto.tcp {
		background: var(--color-accent-tint, rgba(59, 130, 246, 0.2));
		color: var(--color-accent, #60a5fa);
	}
	.pill-proto.udp {
		background: rgba(168, 85, 247, 0.2);
		color: #c084fc;
	}
</style>
