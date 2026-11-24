<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import { AlertTriangle, Trash2 } from 'lucide-svelte';

	// Подтверждение сброса сервера VLESS к заводскому состоянию.
	interface Props {
		open: boolean;
		/** Сброс прошёл: владелец забывает прежнее состояние и перечитывает данные. */
		ondone: () => Promise<void> | void;
	}

	let { open = $bindable(false), ondone }: Props = $props();

	let resetDeleteKeenDNS = $state(false);
	let resetLoading = $state(false);

	// Каждое открытие начинается с невыбранного удаления KeenDNS.
	$effect(() => {
		if (open) resetDeleteKeenDNS = false;
	});

	async function executeFactoryReset() {
		resetLoading = true;
		try {
			const res = await api.resetVlessServer({ delete_keendns: resetDeleteKeenDNS });
			open = false;
			await ondone();
			notifications.success(m.server_reset_vless_success());
			if (res.warnings?.length) {
				notifications.warning(res.warnings.join('; '));
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.common_error());
		} finally {
			resetLoading = false;
		}
	}
</script>

<Modal
	bind:open
	title={m.server_reset_vless_modal_title()}
	onclose={() => { if (!resetLoading) open = false; }}
	size="sm"
>
	<div class="confirm-content">
		<AlertTriangle size={32} class="confirm-icon text-danger mb-3" />
		<p class="confirm-p">{m.server_reset_vless_modal_body()}</p>
		<label class="reset-keendns-check">
			<input type="checkbox" bind:checked={resetDeleteKeenDNS} class="checkbox-input" />
			<div class="reset-keendns-text">
				<span class="reset-keendns-label">{m.server_reset_vless_keendns_label()}</span>
				<span class="reset-keendns-hint">{m.server_reset_vless_keendns_hint()}</span>
			</div>
		</label>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)} disabled={resetLoading}>
			{m.common_cancel()}
		</Button>
		<Button variant="danger" onclick={executeFactoryReset} disabled={resetLoading}>
			<Trash2 size={14} class="mr-1" />
			{resetLoading ? m.server_reset_progress() : m.server_reset_confirm_button()}
		</Button>
	{/snippet}
</Modal>

<style>
	.checkbox-input {
		width: 16px;
		height: 16px;
		accent-color: var(--color-accent);
		cursor: pointer;
	}

	.confirm-content {
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
		padding: 0.5rem 0;
		gap: 10px;
	}

	.confirm-p {
		font-size: 13px;
		color: var(--color-text-primary);
		margin: 0;
		line-height: 1.45;
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
