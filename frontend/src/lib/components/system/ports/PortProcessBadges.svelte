<script lang="ts">
	import { ShieldAlert } from 'lucide-svelte';

	interface Props {
		service?: string;
		isSelf?: boolean;
		isCritical?: boolean;
		selfLabel: string;
		selfTitle: string;
		criticalTitle: string;
	}

	let { service, isSelf, isCritical, selfLabel, selfTitle, criticalTitle }: Props = $props();
</script>

{#if service}
	<span class="svc-badge" title="Служба init.d">{service}</span>
{/if}
{#if isSelf}
	<span class="self-badge" title={selfTitle}>{selfLabel}</span>
{/if}
{#if isCritical}
	<span class="crit-badge" title={criticalTitle}><ShieldAlert size={12} /></span>
{/if}

<style>
	.svc-badge, .self-badge, .crit-badge {
		font-size: 0.72rem;
		padding: 0.1rem 0.35rem;
		border-radius: 4px;
	}
	.svc-badge {
		background: var(--color-success-tint, rgba(16, 185, 129, 0.15));
		color: var(--color-success, #34d399);
	}
	.self-badge {
		background: var(--color-warning-tint, rgba(234, 179, 8, 0.15));
		color: var(--color-warning, #facc15);
	}
	.crit-badge {
		background: var(--color-error-tint, rgba(239, 68, 68, 0.15));
		color: var(--color-error, #f87171);
		display: inline-flex;
		align-items: center;
	}
</style>
