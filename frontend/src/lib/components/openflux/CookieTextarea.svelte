<script lang="ts">
	import { Badge, Button } from '$lib/components/ui';
	import { hasMailruLoginCookie } from './openfluxUrls';

	interface Props {
		label: string;
		value?: string;
		saved?: boolean;
		cleared?: boolean;
		placeholder?: string;
		hint?: string;
		rows?: number;
		oninput?: () => void;
		onclear?: () => void;
	}

	let {
		label,
		value = $bindable(''),
		saved = false,
		cleared = false,
		placeholder = 'Mpop=...; sdc=...  или Netscape cookies.txt / JSON',
		hint = '',
		rows = 3,
		oninput,
		onclear,
	}: Props = $props();

	let revealed = $state(false);
	const fieldId = `cookie-textarea-${Math.random().toString(36).slice(2, 8)}`;
	const hasValue = $derived(Boolean(value?.trim()));
	const hasMpop = $derived(hasMailruLoginCookie(value));
	const isSavedMasked = $derived(!hasValue && saved && !cleared);

	function handleClear() {
		value = '';
		revealed = false;
		onclear?.();
		oninput?.();
	}
</script>

<div class="cookie-field">
	<div class="cookie-head">
		<label class="cookie-label" for={fieldId}>{label}</label>
		<div class="cookie-badges">
			{#if hasValue}
				<Badge size="xs" variant={hasMpop ? 'success' : 'warning'}>
					{hasMpop ? 'Mpop найден' : 'Нет cookie Mpop'}
				</Badge>
			{:else if isSavedMasked}
				<Badge size="xs" variant="success">Сохранены на роутере</Badge>
			{:else if cleared}
				<Badge size="xs" variant="warning">Будут очищены при сохранении</Badge>
			{/if}
			{#if (hasValue || isSavedMasked) && revealed}
				<Button variant="secondary" size="sm" type="button" onclick={() => (revealed = false)}>
					Скрыть
				</Button>
			{/if}
		</div>
	</div>

	{#if (hasValue || isSavedMasked) && !revealed}
		<div class="cookie-masked-row">
			<span class="cookie-masked-value">
				{isSavedMasked
					? '•••••••••••• (сохранены — оставьте как есть, чтобы не менять)'
					: '•••••••••••• (cookies заданы)'}
			</span>
			<div class="cookie-masked-actions">
				<Button variant="secondary" size="sm" type="button" onclick={() => (revealed = true)}>
					{isSavedMasked ? 'Заменить' : 'Показать / Изменить'}
				</Button>
				<Button variant="secondary" size="sm" type="button" onclick={handleClear}>
					Очистить
				</Button>
			</div>
		</div>
	{:else}
		<textarea
			id={fieldId}
			class="cookie-textarea"
			bind:value
			{placeholder}
			{rows}
			spellcheck="false"
			autocomplete="off"
			oninput={() => oninput?.()}
		></textarea>
	{/if}

	{#if hint}
		<span class="cookie-hint">{hint}</span>
	{/if}
</div>

<style>
	.cookie-field {
		grid-column: 1 / -1;
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.cookie-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.cookie-badges {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.cookie-label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.cookie-masked-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
		padding: 0.5rem 0.7rem;
		border-radius: var(--radius);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}

	.cookie-masked-value {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.cookie-masked-actions {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}

	.cookie-textarea {
		width: 100%;
		min-height: 4.5rem;
		padding: 0.55rem 0.7rem;
		border-radius: var(--radius);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		line-height: 1.45;
		resize: vertical;
		box-sizing: border-box;
	}

	.cookie-textarea:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.cookie-hint {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		line-height: 1.4;
	}
</style>
