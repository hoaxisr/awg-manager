<script lang="ts">
	import { m } from '$lib/i18n';
	import type { ZapretStatus } from '$lib/types';
	import ZapretStatusToggle from './ZapretStatusToggle.svelte';

	interface Props {
		status: ZapretStatus | null;
		loading: boolean;
		busy: boolean;
		oncontrol: (action: 'start' | 'stop') => void;
	}

	let { status, loading, busy, oncontrol }: Props = $props();
	let installed = $derived(status?.installed === true);
	let running = $derived(status?.running === true);
</script>

<section class="zap-status" aria-label={m.zapret_service_aria()}>
	<ZapretStatusToggle {status} {loading} {busy} {oncontrol} />
	<div class="identity">
		<strong>Zapret</strong>
		<span class="state">
			{#if loading}
				{m.settings_integrations_loading()}
			{:else if !installed}
				{m.zapret_state_not_installed()}
			{:else if running}
				{m.zapret_state_running()}
			{:else}
				{m.zapret_state_stopped()}
			{/if}
		</span>
	</div>
	{#if status?.lastError}
		<p class="error" role="status">{status.lastError}</p>
	{/if}
</section>

<style>
	.zap-status {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		min-height: 3.5rem;
		padding: 0.625rem 0.75rem;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-secondary);
	}
	.identity {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}
	.identity strong {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--text-primary);
	}
	.state {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}
	.error {
		margin: 0 0 0 auto;
		max-width: 55%;
		font-size: 0.6875rem;
		color: var(--error, #ef4444);
		word-break: break-word;
	}
	@media (max-width: 520px) {
		.zap-status { flex-wrap: wrap; }
		.error { flex-basis: 100%; max-width: none; margin-left: 0; }
	}
</style>
