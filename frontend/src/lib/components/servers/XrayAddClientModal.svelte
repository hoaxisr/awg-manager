<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';

	// Окно «Добавить устройство VLESS».
	interface Props {
		open: boolean;
		onadded: () => Promise<void> | void;
	}

	let { open = $bindable(false), onadded }: Props = $props();

	let newClientRemark = $state('');
	let addClientLoading = $state(false);

	async function handleAddClient() {
		if (!newClientRemark.trim()) {
			notifications.error(m.servers_xray_device_name_required());
			return;
		}
		addClientLoading = true;
		try {
			const client = await api.addXrayClient(newClientRemark.trim());
			notifications.success(m.servers_xray_device_added({ name: client.remark }));
			newClientRemark = '';
			open = false;
			await onadded();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_device_add_failed());
		} finally {
			addClientLoading = false;
		}
	}
</script>

<Modal bind:open title={m.servers_xray_add_device_title()} onclose={() => (open = false)} size="sm">
	<form onsubmit={(e) => { e.preventDefault(); void handleAddClient(); }} class="add-client-form">
		<label class="form-field">
			<span class="field-label">{m.servers_xray_device_name_label()}</span>
			<input
				type="text"
				bind:value={newClientRemark}
				placeholder={m.servers_xray_device_name_placeholder()}
				class="text-input"
				required
			/>
		</label>
		<p class="field-hint">
			{m.servers_xray_device_key_hint()}
		</p>
	</form>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)}>{m.common_cancel()}</Button>
		<Button variant="primary" onclick={handleAddClient} disabled={addClientLoading}>
			{addClientLoading ? m.servers_xray_creating() : m.common_add()}
		</Button>
	{/snippet}
</Modal>

<style>
	.add-client-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		gap: 4px;
	}

	.field-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.text-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
		outline: none;
	}
	.text-input:focus {
		border-color: var(--color-accent);
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}
</style>
