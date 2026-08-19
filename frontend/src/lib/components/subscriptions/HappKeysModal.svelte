<script lang="ts">
	import { api } from '$lib/api/client';
	import { Modal, Button } from '$lib/components/ui';
	import { Key, Upload, Trash2, CheckCircle2, AlertCircle } from 'lucide-svelte';

	interface Props {
		open: boolean;
		onclose: () => void;
		onsaved?: () => void;
	}

	let { open = $bindable(false), onclose, onsaved }: Props = $props();

	let keysText = $state('');
	let configured = $state(false);
	let keysCount = $state(0);
	let loading = $state(false);
	let saving = $state(false);
	let error = $state('');
	let successMsg = $state('');
	let fileInput = $state<HTMLInputElement | null>(null);

	async function loadStatus(): Promise<void> {
		loading = true;
		error = '';
		try {
			const res = await api.getHappKeysInfo();
			configured = res.configured;
			keysCount = res.count;
		} catch {
			// silent fallback
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		if (open) {
			error = '';
			successMsg = '';
			keysText = '';
			void loadStatus();
		}
	});

	async function handleSave(): Promise<void> {
		error = '';
		successMsg = '';
		if (!keysText.trim()) {
			error = 'Вставьте ключи RSA в поле ввода';
			return;
		}
		saving = true;
		try {
			const res = await api.saveHappKeys(keysText);
			configured = res.configured;
			keysCount = res.count;
			successMsg = `Сохранено ключей: ${res.count}`;
			keysText = '';
			onsaved?.();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось сохранить ключи';
		} finally {
			saving = false;
		}
	}

	async function handleClear(): Promise<void> {
		saving = true;
		error = '';
		successMsg = '';
		try {
			await api.clearHappKeys();
			configured = false;
			keysCount = 0;
			successMsg = 'Ключи успешно удалены';
			keysText = '';
			onsaved?.();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Ошибка удаления ключей';
		} finally {
			saving = false;
		}
	}

	function handleFileUpload(e: Event): void {
		const target = e.target as HTMLInputElement;
		const file = target.files?.[0];
		if (!file) return;
		const reader = new FileReader();
		reader.onload = (evt) => {
			keysText = String(evt.target?.result ?? '');
		};
		reader.readAsText(file);
	}
</script>

<Modal
	bind:open
	title="RSA-ключи расшифровки Happ"
	size="md"
	{onclose}
>
	<div class="happ-keys-content">
		<div class="status-banner" class:configured>
			{#if configured}
				<CheckCircle2 size={16} class="text-success" />
				<span>Ключи установлены (активно: {keysCount} шт.)</span>
			{:else}
				<AlertCircle size={16} class="text-muted" />
				<span>Ключи не установлены. Ссылки <code>happ://crypt</code> не будут расшифровываться.</span>
			{/if}
		</div>

		<div class="keys-field">
			<div class="field-header">
				<label for="happ-keys-input" class="lbl">Вставить ключи (JSON, PEM или Base64):</label>
				<Button
					size="sm"
					variant="secondary"
					onclick={() => fileInput?.click()}
				>
					<Upload size={13} />
					<span>Загрузить .json</span>
				</Button>
				<input
					type="file"
					bind:this={fileInput}
					accept=".json,.txt,.pem"
					class="hidden-file-input"
					onchange={handleFileUpload}
				/>
			</div>

			<textarea
				id="happ-keys-input"
				class="keys-textarea inp"
				bind:value={keysText}
				placeholder={`[
  "MIICXwIBAAKBgQC...",
  "MIIJKQIBAAKCAgE...",
  "MIIJJwIBAAKCAgE...",
  "MIIJKQIBAAKCAgE..."
]`}
				rows="7"
			></textarea>

			<span class="field-hint">
				Поддерживается JSON-массив строк, блоки PEM (<code>-----BEGIN RSA PRIVATE KEY-----</code>) или строки Base64 (PKCS#1).
			</span>
		</div>

		{#if error}
			<div class="alert-box alert-error">
				{error}
			</div>
		{/if}

		{#if successMsg}
			<div class="alert-box alert-success">
				{successMsg}
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<div class="modal-footer-row">
			{#if configured}
				<Button
					variant="outline-danger"
					size="sm"
					disabled={saving}
					onclick={handleClear}
				>
					<Trash2 size={14} />
					<span>Очистить</span>
				</Button>
			{/if}
			<div class="footer-actions">
				<Button variant="secondary" size="sm" onclick={onclose}>
					Закрыть
				</Button>
				<Button
					variant="primary"
					size="sm"
					disabled={saving || !keysText.trim()}
					onclick={handleSave}
				>
					{saving ? 'Сохранение...' : 'Сохранить ключи'}
				</Button>
			</div>
		</div>
	{/snippet}
</Modal>

<style>
	.happ-keys-content {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.status-banner {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		padding: 0.625rem 0.875rem;
		background: var(--color-bg-secondary, #27272a);
		border: 1px solid var(--color-border, #3f3f46);
		border-radius: 6px;
		font-size: 0.8125rem;
		color: var(--color-text-secondary, #a1a1aa);
	}

	.status-banner.configured {
		background: rgba(16, 185, 129, 0.08);
		border-color: rgba(16, 185, 129, 0.25);
		color: var(--color-success, #10b981);
	}

	.status-banner code {
		background: rgba(0, 0, 0, 0.2);
		padding: 0.1rem 0.3rem;
		border-radius: 3px;
		font-family: monospace;
	}

	.keys-field {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.field-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.lbl {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-primary, #f4f4f5);
	}

	.hidden-file-input {
		display: none;
	}

	.keys-textarea {
		width: 100%;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 0.75rem;
		line-height: 1.4;
		padding: 0.625rem;
		resize: vertical;
		min-height: 140px;
	}

	.alert-box {
		padding: 0.625rem 0.875rem;
		border-radius: 6px;
		font-size: 0.8125rem;
	}

	.alert-error {
		background: rgba(239, 68, 68, 0.1);
		color: var(--color-danger, #ef4444);
		border: 1px solid rgba(239, 68, 68, 0.25);
	}

	.alert-success {
		background: rgba(16, 185, 129, 0.1);
		color: var(--color-success, #10b981);
		border: 1px solid rgba(16, 185, 129, 0.25);
	}

	.modal-footer-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		width: 100%;
		gap: 0.5rem;
	}

	.footer-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-left: auto;
	}
</style>
