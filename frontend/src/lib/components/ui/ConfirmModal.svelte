<script lang="ts">
	import Modal from './Modal.svelte';
	import Button from './Button.svelte';
	import { m } from '$lib/i18n';

	interface Props {
		open: boolean;
		title: string;
		/** Primary message (single line or short paragraph). */
		message: string;
		/** Optional secondary text shown under message in muted style. */
		secondary?: string;
		/** Full filesystem path shown as a selectable monospace line. */
		filePath?: string;
		/** По умолчанию «Удалить» на языке интерфейса. */
		confirmLabel?: string;
		/** По умолчанию «Отмена» на языке интерфейса. */
		cancelLabel?: string;
		/** 'danger' uses the red destructive Button variant; 'primary' uses the accent. */
		variant?: 'danger' | 'primary';
		busy?: boolean;
		onConfirm: () => void | Promise<void>;
		onClose: () => void;
	}

	let {
		open,
		title,
		message,
		secondary,
		filePath,
		// Дефолты подписей — в шаблоне, а не здесь: fallback в $props()
		// вычисляется один раз и не обновился бы при смене языка.
		confirmLabel,
		cancelLabel,
		variant = 'danger',
		busy = false,
		onConfirm,
		onClose,
	}: Props = $props();

	// Пустое сообщение = вопроса в заголовке достаточно; тело модалки при этом
	// не рендерится совсем, иначе его паддинг оставляет пустую полосу.
	const hasBody = $derived(Boolean(message || filePath || secondary));
</script>

{#snippet body()}
	{#if message}
		<p class="confirm-message">{message}</p>
	{/if}
	{#if filePath}
		<p class="confirm-file-label">{m.confirm_modal_file_on_disk()}</p>
		<code class="confirm-file-path">{filePath}</code>
	{/if}
	{#if secondary}
		<p class="confirm-secondary">{secondary}</p>
	{/if}
{/snippet}

<Modal {open} {title} size="sm" onclose={onClose} children={hasBody ? body : undefined}>
	{#snippet actions()}
		<Button variant="secondary" size="md" onclick={onClose} disabled={busy}>
			{cancelLabel ?? m.common_cancel()}
		</Button>
		<Button
			variant={variant === 'danger' ? 'outline-danger' : 'outline-primary'}
			size="md"
			onclick={onConfirm}
			disabled={busy}
		>
			{busy ? m.common_in_progress() : (confirmLabel ?? m.common_delete())}
		</Button>
	{/snippet}
</Modal>

<style>
	.confirm-message {
		margin: 0 0 0.5rem;
		line-height: 1.4;
	}
	.confirm-file-label {
		margin: 0.5rem 0 0.25rem;
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--muted-text, var(--color-text-muted));
	}
	.confirm-file-path {
		display: block;
		margin: 0 0 0.5rem;
		padding: 8px 10px;
		font-size: 0.8125rem;
		line-height: 1.35;
		word-break: break-all;
		user-select: all;
		background: var(--bg-secondary, var(--color-bg-secondary));
		border: 1px solid var(--border, var(--color-border));
		border-radius: 6px;
		color: var(--text-primary, var(--color-text-primary));
	}
	.confirm-secondary {
		margin: 0;
		font-size: 0.875rem;
		color: var(--muted-text, var(--color-text-muted));
		line-height: 1.4;
	}
</style>
