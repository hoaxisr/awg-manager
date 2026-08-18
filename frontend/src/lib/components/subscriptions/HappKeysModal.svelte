<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
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

	function cleanB64(s: string): string {
		return s.trim().replace(/^["',\[\]]+|["',\[\]]+$/g, '').replace(/\s+/g, '');
	}

	function parseKeys(text: string): {
		count: number;
		valid: boolean;
		tokens: string[];
		errorMsg: string;
	} {
		const trimmed = text.trim();
		if (!trimmed) return { count: 0, valid: false, tokens: [], errorMsg: '' };

		let tokens: string[] = [];

		// 1. JSON array parsing
		try {
			const parsed = JSON.parse(trimmed);
			if (Array.isArray(parsed)) {
				tokens = parsed.map((x) => cleanB64(String(x))).filter((x) => x.length > 50);
			}
		} catch {}

		// 2. PEM blocks parsing
		if (tokens.length === 0) {
			const pemRegex = /-----BEGIN[^-]+-----[\s\S]+?-----END[^-]+-----/g;
			const pemMatches = trimmed.match(pemRegex);
			if (pemMatches && pemMatches.length > 0) {
				tokens = pemMatches.map((x) => x.trim());
			}
		}

		// 3. Smart Base64 RSA extraction (split on whitespace or lines)
		if (tokens.length === 0) {
			const words = trimmed.split(/[\s,]+/);
			let cur = '';
			for (const w of words) {
				const cleaned = cleanB64(w);
				if (!cleaned) continue;
				if (cleaned.startsWith('MII') && cur.length > 500) {
					tokens.push(cur);
					cur = '';
				}
				cur += cleaned;
			}
			if (cur.length > 50) tokens.push(cur);
		}

		let errorMsg = '';
		for (let i = 0; i < tokens.length; i++) {
			const t = tokens[i];
			const is1024 = i === 0;
			const minLen = is1024 ? 700 : 2800;
			const name = i === 0 ? 'crypt' : `crypt${i + 1}`;

			if (!t.startsWith('MII') && !t.startsWith('-----BEGIN')) {
				errorMsg = `Ключ #${i + 1} (${name}) повреждён: должен начинаться с MII...`;
				break;
			}
			if (t.length < minLen) {
				errorMsg = `Ключ #${i + 1} (${name}) обрезан: ${t.length} симв. (требуется ~${is1024 ? '816' : '3132'})`;
				break;
			}
		}

		if (tokens.length > 0 && tokens.length < 4 && !errorMsg) {
			errorMsg = `Обнаружено ключей: ${tokens.length} из 4. Для работы Happ crypt4 требуются все 4 ключа.`;
		}

		const valid = tokens.length === 4 && errorMsg === '';

		return {
			count: tokens.length,
			valid,
			tokens,
			errorMsg,
		};
	}

	let validation = $derived(parseKeys(keysText));

	async function loadStatus(): Promise<void> {
		loading = true;
		error = '';
		try {
			const res = await api.getHappKeysInfo();
			configured = res.configured;
			keysCount = res.count;
		} catch (e) {
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
		if (!validation.valid) {
			error = validation.errorMsg || 'Вставьте все 4 валидных ключа RSA';
			return;
		}

		saving = true;
		try {
			const res = await api.saveHappKeys({ keys: validation.tokens });
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

{#if open}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="modal-overlay" onclick={() => onclose()}>
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
		<div class="modal-content" role="dialog" tabindex="-1" onclick={(e) => e.stopPropagation()}>
			<div class="modal-header">
				<div class="title-with-icon">
					<Key size={18} class="title-icon" />
					<h3>RSA-ключи расшифровки Happ</h3>
				</div>
				<button type="button" class="btn-close" aria-label="Закрыть" onclick={() => onclose()}>✕</button>
			</div>

			<div class="modal-body">
				<div class="status-banner" class:configured>
					{#if configured}
						<CheckCircle2 size={16} class="text-success" />
						<span>Ключи установлены (активно: {keysCount} шт.)</span>
					{:else}
						<AlertCircle size={16} class="text-muted" />
						<span>Ключи не установлены. Ссылки <code>happ://crypt</code> не будут расшифровываться.</span>
					{/if}
				</div>

				<div class="form-group">
					<div class="field-header">
						<label for="happ-keys-input" class="lbl">Вставить ключи (JSON, PEM или Base64):</label>
						<button
							type="button"
							class="btn-file-upload"
							onclick={() => fileInput?.click()}
						>
							<Upload size={13} />
							<span>Загрузить .json</span>
						</button>
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
						class="keys-textarea"
						bind:value={keysText}
						placeholder={`[
  "MIICXwIBAAKBgQC...",
  "MIIJKQIBAAKCAgE...",
  "MIIJJwIBAAKCAgE...",
  "MIIJKQIBAAKCAgE..."
]`}
						rows="7"
					></textarea>

					{#if keysText.trim()}
						<div
							class="validation-badge"
							class:val-success={validation.valid}
							class:val-error={!validation.valid}
						>
							{#if validation.valid}
								<CheckCircle2 size={14} />
								<span>Ключи валидны: обнаружены все 4 ключа (crypt, crypt2, crypt3, crypt4)</span>
							{:else}
								<AlertCircle size={14} />
								<span>{validation.errorMsg}</span>
							{/if}
						</div>
					{/if}

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

			<div class="modal-footer">
				{#if configured}
					<button
						type="button"
						class="btn-danger-outline"
						disabled={saving}
						onclick={handleClear}
					>
						<Trash2 size={14} />
						<span>Очистить</span>
					</button>
				{/if}
				<div class="footer-actions">
					<button type="button" class="btn-cancel" onclick={() => onclose()}>Закрыть</button>
					<button
						type="button"
						class="btn-primary"
						disabled={saving || !validation.valid}
						onclick={handleSave}
					>
						{saving ? 'Сохранение...' : 'Сохранить ключи'}
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}

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
		backdrop-filter: blur(2px);
	}
	.modal-content {
		background: var(--color-bg-primary, #18181b);
		border: 1px solid var(--color-border, #27272a);
		border-radius: 8px;
		width: 100%;
		max-width: 540px;
		box-shadow: 0 10px 25px rgba(0, 0, 0, 0.5);
		display: flex;
		flex-direction: column;
	}
	.modal-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 1rem 1.25rem;
		border-bottom: 1px solid var(--color-border, #27272a);
	}
	.title-with-icon {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.title-with-icon h3 {
		margin: 0;
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary, #f4f4f5);
	}
	:global(.title-icon) {
		color: var(--accent, #3b82f6);
	}
	.btn-close {
		background: transparent;
		border: none;
		color: var(--color-text-muted, #71717a);
		font-size: 1rem;
		cursor: pointer;
		padding: 0.25rem;
		border-radius: 4px;
		line-height: 1;
	}
	.btn-close:hover {
		color: var(--color-text-primary, #f4f4f5);
	}
	.modal-body {
		padding: 1.25rem;
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.status-banner {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		padding: 0.6rem 0.8rem;
		border-radius: 6px;
		background: var(--color-bg-secondary, #27272a);
		border: 1px solid var(--color-border, #3f3f46);
		font-size: 0.85rem;
		color: var(--color-text-secondary, #a1a1aa);
	}
	.status-banner.configured {
		background: rgba(34, 197, 94, 0.08);
		border-color: rgba(34, 197, 94, 0.2);
		color: var(--color-success, #10b981);
	}
	:global(.text-success) {
		color: var(--color-success, #10b981);
		flex-shrink: 0;
	}
	:global(.text-muted) {
		color: #71717a;
		flex-shrink: 0;
	}
	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
	}
	.field-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.lbl {
		font-size: 0.85rem;
		font-weight: 500;
		color: var(--color-text-secondary, #d4d4d8);
	}
	.btn-file-upload {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.75rem;
		padding: 0.25rem 0.5rem;
		border-radius: 4px;
		background: var(--color-bg-secondary, #27272a);
		border: 1px solid var(--color-border, #3f3f46);
		color: var(--color-text-secondary, #d4d4d8);
		cursor: pointer;
	}
	.btn-file-upload:hover {
		background: var(--color-bg-tertiary, #3f3f46);
		color: #fff;
	}
	.hidden-file-input {
		display: none;
	}
	.keys-textarea {
		width: 100%;
		font-family: var(--font-mono, monospace);
		font-size: 0.78rem;
		line-height: 1.4;
		padding: 0.6rem;
		border-radius: 4px;
		border: 1px solid var(--color-border, #3f3f46);
		background: var(--color-bg-secondary, #18181b);
		color: var(--color-text-primary, #f4f4f5);
		resize: vertical;
	}
	.keys-textarea:focus {
		border-color: var(--accent, #3b82f6);
		outline: none;
	}
	.field-hint {
		font-size: 0.75rem;
		color: var(--color-text-muted, #71717a);
	}
	.field-hint code {
		background: rgba(255, 255, 255, 0.06);
		padding: 0.05rem 0.25rem;
		border-radius: 3px;
	}
	.validation-badge {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.78rem;
		padding: 0.4rem 0.6rem;
		border-radius: 4px;
		line-height: 1.35;
	}
	.val-success {
		background: rgba(34, 197, 94, 0.08);
		border: 1px solid rgba(34, 197, 94, 0.2);
		color: var(--color-success, #10b981);
	}
	.val-error {
		background: rgba(239, 68, 68, 0.08);
		border: 1px solid rgba(239, 68, 68, 0.2);
		color: #ef4444;
	}
	.alert-box {
		padding: 0.6rem 0.8rem;
		border-radius: 6px;
		font-size: 0.82rem;
	}
	.alert-error {
		background: rgba(239, 68, 68, 0.08);
		border: 1px solid rgba(239, 68, 68, 0.2);
		color: #ef4444;
	}
	.alert-success {
		background: rgba(34, 197, 94, 0.08);
		border: 1px solid rgba(34, 197, 94, 0.2);
		color: var(--color-success, #10b981);
	}
	.modal-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0.85rem 1.25rem;
		border-top: 1px solid var(--color-border, #27272a);
		gap: 0.5rem;
	}
	.footer-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-left: auto;
	}
	.btn-primary {
		padding: 0.45rem 0.9rem;
		background: var(--accent, #3b82f6);
		color: #fff;
		border: none;
		border-radius: 4px;
		font-size: 0.85rem;
		font-weight: 500;
		cursor: pointer;
	}
	.btn-primary:hover:not(:disabled) {
		opacity: 0.9;
	}
	.btn-primary:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.btn-cancel {
		padding: 0.45rem 0.8rem;
		background: transparent;
		color: var(--color-text-secondary, #d4d4d8);
		border: 1px solid var(--color-border, #3f3f46);
		border-radius: 4px;
		font-size: 0.85rem;
		cursor: pointer;
	}
	.btn-cancel:hover {
		background: var(--color-bg-secondary, #27272a);
	}
	.btn-danger-outline {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.45rem 0.75rem;
		background: transparent;
		color: #ef4444;
		border: 1px solid rgba(239, 68, 68, 0.3);
		border-radius: 4px;
		font-size: 0.82rem;
		cursor: pointer;
	}
	.btn-danger-outline:hover:not(:disabled) {
		background: rgba(239, 68, 68, 0.08);
	}
</style>
