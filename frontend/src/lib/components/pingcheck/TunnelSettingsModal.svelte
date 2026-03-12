<script lang="ts">
	import type { TunnelPingCheck } from '$lib/types';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		tunnelName: string;
		editForm: Partial<TunnelPingCheck>;
		saving: boolean;
		hasChanges: boolean;
		onSave: () => void;
		onClose: () => void;
	}

	let { tunnelName, editForm = $bindable(), saving, hasChanges, onSave, onClose }: Props = $props();
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions a11y_interactive_supports_focus -->
<div class="modal-overlay" role="dialog" aria-modal="true" aria-labelledby="pingcheck-settings-title" tabindex="-1" onclick={onClose} onkeydown={(e) => e.key === 'Escape' && onClose()}>
	<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
	<div class="modal" role="document" onclick={(e) => e.stopPropagation()}>
		<div class="modal-header">
			<h3 id="pingcheck-settings-title">Настройки мониторинга</h3>
			<span class="modal-tunnel-name">{tunnelName}</span>
		</div>

		<div class="modal-body">
			<div class="form-group">
				<Toggle
					checked={editForm.useCustomSettings ?? false}
					onchange={(v) => { editForm.useCustomSettings = v; }}
					label="Индивидуальные настройки"
					hint="Если отключено, используются глобальные настройки по умолчанию"
				/>
			</div>

			{#if editForm.useCustomSettings}
				<div class="custom-settings">
					<div class="form-group">
						<label for="method">Метод проверки</label>
						<select id="method" bind:value={editForm.method}>
							<option value="http">HTTP 204 (Google)</option>
							<option value="icmp">ICMP Ping</option>
						</select>
					</div>

					{#if editForm.method === 'icmp'}
						<div class="form-group">
							<label for="target">IP-адрес для ping</label>
							<input
								type="text"
								id="target"
								bind:value={editForm.target}
								placeholder="8.8.8.8"
							/>
						</div>
					{/if}

					<div class="form-row">
						<div class="form-group">
							<label for="interval">Интервал (сек)</label>
							<input
								type="number"
								id="interval"
								bind:value={editForm.interval}
								min="10"
								max="300"
							/>
						</div>

						<div class="form-group">
							<label for="deadInterval">Интервал при ошибке (сек)</label>
							<input
								type="number"
								id="deadInterval"
								bind:value={editForm.deadInterval}
								min="30"
								max="600"
							/>
						</div>
					</div>

					<div class="form-group">
						<label for="failThreshold">Порог ошибок</label>
						<input
							type="number"
							id="failThreshold"
							bind:value={editForm.failThreshold}
							min="1"
							max="10"
						/>
						<p class="form-hint">Количество неудачных проверок до пометки туннеля как недоступного</p>
					</div>
				</div>
			{/if}
		</div>

		<div class="modal-footer">
			<button class="btn btn-secondary" onclick={onClose} disabled={saving}>
				Отмена
			</button>
			<button class="btn btn-primary" onclick={onSave} disabled={saving || !hasChanges}>
				{saving ? 'Сохранение...' : 'Сохранить'}
			</button>
		</div>
	</div>
</div>

<style>
	.modal-overlay {
		position: fixed;
		inset: 0;
		background: rgba(0, 0, 0, 0.6);
		display: flex;
		align-items: center;
		justify-content: center;
		z-index: 1000;
		padding: 1rem;
	}

	.modal {
		background: var(--bg-primary);
		border-radius: 12px;
		width: 100%;
		max-width: 480px;
		max-height: 90vh;
		overflow-y: auto;
	}

	.modal-header {
		padding: 1.25rem 1.5rem;
		border-bottom: 1px solid var(--border);
	}

	.modal-header h3 {
		margin: 0 0 0.25rem 0;
		font-size: 1.125rem;
	}

	.modal-tunnel-name {
		color: var(--text-muted);
		font-size: 0.875rem;
	}

	.modal-body {
		padding: 1.5rem;
	}

	.modal-footer {
		padding: 1rem 1.5rem;
		border-top: 1px solid var(--border);
		display: flex;
		justify-content: flex-end;
		gap: 0.75rem;
	}

	.btn {
		padding: 0.5rem 1rem;
		border: none;
		border-radius: 6px;
		font-size: 0.875rem;
		cursor: pointer;
		transition: opacity 0.2s;
	}

	.btn:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.btn-primary {
		background: var(--accent);
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		opacity: 0.9;
	}

	.btn-secondary {
		background: var(--bg-tertiary);
		color: var(--text-primary);
		border: 1px solid var(--border);
	}

	.btn-secondary:hover:not(:disabled) {
		background: var(--bg-secondary);
	}

	.form-group {
		margin-bottom: 1rem;
	}

	.form-group label {
		display: block;
		margin-bottom: 0.375rem;
		font-size: 0.875rem;
		color: var(--text-secondary);
	}

	.form-group input[type="text"],
	.form-group input[type="number"],
	.form-group select {
		width: 100%;
		padding: 0.5rem 0.75rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		font-size: 0.875rem;
	}

	.form-group input:focus,
	.form-group select:focus {
		outline: none;
		border-color: var(--accent);
	}

	.form-hint {
		margin-top: 0.375rem;
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.form-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
	}

	.custom-settings {
		margin-top: 1rem;
		padding-top: 1rem;
		border-top: 1px solid var(--border);
	}
</style>
