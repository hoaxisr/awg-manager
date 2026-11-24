<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { Button, Modal } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';

	// Владелец монтирует модалку на каждое открытие — поле ключей начинается пустым.
	interface Props {
		count: number;
		onclose: () => void;
	}

	let { count = $bindable(), onclose }: Props = $props();

	let text = $state('');
	let busy = $state(false);

	async function save() {
		if (!text.trim() || busy) return;
		busy = true;
		try {
			const res = await api.saveHappKeys(text.trim());
			count = res.count ?? 0;
			text = '';
			onclose();
			notifications.success(m.firc_tunnel_edit_happ_saved({ count }));
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_happ_save_error());
		} finally {
			busy = false;
		}
	}

	async function clear() {
		if (busy) return;
		busy = true;
		try {
			await api.clearHappKeys();
			count = 0;
			notifications.info(m.firc_tunnel_edit_happ_cleared());
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_happ_clear_error());
		} finally {
			busy = false;
		}
	}
</script>

<Modal
	open
	title={m.firc_tunnel_edit_happ_modal_title()}
	size="md"
	{onclose}
>
	<div class="modal-form">
		<span class="hint">
			{m.firc_tunnel_edit_happ_hint_before()} <code>happ://crypt</code>, <code>crypt2</code>, <code>crypt3</code>, <code>crypt4</code> {m.firc_tunnel_edit_happ_hint_after()} <strong>{count}</strong>.
		</span>
		<div class="field">
			<label class="lbl" for="happ-keys-inp">{m.firc_tunnel_edit_happ_keys_label()}</label>
			<textarea
				id="happ-keys-inp"
				class="inp mono"
				rows="6"
				placeholder="-----BEGIN PRIVATE KEY-----&#10;...&#10;-----END PRIVATE KEY-----"
				bind:value={text}
			></textarea>
		</div>
	</div>
	{#snippet actions()}
		{#if count > 0}
			<Button variant="danger" size="sm" loading={busy} onclick={clear}>
				{m.firc_tunnel_edit_happ_clear()}
			</Button>
		{/if}
		<Button variant="secondary" size="sm" onclick={onclose}>
			{m.common_close()}
		</Button>
		<Button
			variant="primary"
			size="sm"
			loading={busy}
			disabled={!text.trim()}
			onclick={save}
		>
			{m.firc_tunnel_edit_happ_save()}
		</Button>
	{/snippet}
</Modal>

<style>
	.modal-form {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.lbl {
		font-size: 11.5px;
		color: var(--text-secondary);
		font-weight: 500;
	}
	.hint {
		font-size: 11px;
		color: var(--text-muted);
	}
	.inp {
		padding: 5px 8px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		box-sizing: border-box;
	}
	.mono {
		font-family: var(--font-mono);
	}
</style>
