<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { Lock, Eye } from 'lucide-svelte';

	interface Props {
		loading?: boolean;
		onreveal: () => void;
	}

	let { loading = false, onreveal }: Props = $props();
</script>

<!-- Secret Isolation Gate: Reveal CTA -->
<div class="gate-card">
	<div class="gate-icon">
		<Lock size={28} />
	</div>
	<div class="gate-text">
		<h3 class="gate-title">{m.servers_wizard_result_gate_title()}</h3>
		<p class="gate-desc">
			{m.servers_wizard_result_gate_desc()}
		</p>
	</div>
	<Button variant="primary" onclick={onreveal} disabled={loading}>
		<Eye size={16} />
		{loading ? m.servers_wizard_result_revealing() : m.servers_wizard_result_reveal()}
	</Button>
</div>

<style>
	.gate-card {
		padding: 2rem;
		border-radius: var(--radius);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		text-align: center;
		gap: 1rem;
	}

	.gate-icon {
		width: 56px;
		height: 56px;
		border-radius: 50%;
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.gate-text {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		max-width: 24rem;
	}

	.gate-title {
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.gate-desc {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin: 0;
		line-height: 1.4;
	}
</style>
