<script lang="ts">
	// Подтверждение сброса сервера Telegram к заводским настройкам.
	import { m } from '$lib/i18n';
	import { Button, Modal } from '$lib/components/ui';
	import { AlertTriangle, Trash2 } from 'lucide-svelte';

	interface Props {
		open: boolean;
		loading: boolean;
		/** Удалить заодно домены KeenDNS 4-го уровня Telegram. */
		deleteKeenDNS: boolean;
		onconfirm: () => void;
	}

	let { open = $bindable(false), loading, deleteKeenDNS = $bindable(false), onconfirm }: Props = $props();
</script>

<Modal
	bind:open
	title={m.server_reset_tg_modal_title()}
	onclose={() => { if (!loading) open = false; }}
	size="sm"
>
	<div class="confirm-content">
		<AlertTriangle size={32} class="confirm-icon text-danger mb-3" />
		<p class="confirm-p">{m.server_reset_tg_modal_body()}</p>
		<label class="reset-keendns-check">
			<input type="checkbox" bind:checked={deleteKeenDNS} class="checkbox-input" />
			<div class="reset-keendns-text">
				<span class="reset-keendns-label">{m.server_reset_tg_keendns_label()}</span>
				<span class="reset-keendns-hint">{m.server_reset_tg_keendns_hint()}</span>
			</div>
		</label>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)} disabled={loading}>
			{m.common_cancel()}
		</Button>
		<Button variant="danger" onclick={onconfirm} disabled={loading}>
			<Trash2 size={14} class="mr-1" />
			{loading ? m.server_reset_progress() : m.server_reset_confirm_button()}
		</Button>
	{/snippet}
</Modal>

<style>
	.confirm-content {
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
		padding: 0.5rem 0;
	}

	.confirm-p {
		font-size: 13px;
		color: var(--color-text-primary);
		margin: 0 0 8px 0;
	}

	.checkbox-input {
		width: 18px;
		height: 18px;
		flex-shrink: 0;
		margin-top: 2px;
		accent-color: var(--color-accent);
		cursor: pointer;
	}

	.reset-keendns-check {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		width: 100%;
		text-align: left;
		padding: 10px 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		cursor: pointer;
	}

	.reset-keendns-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
		flex: 1 1 0;
		min-width: 0;
	}

	.reset-keendns-label {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.reset-keendns-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}
</style>
