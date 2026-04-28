<script lang="ts">
	import Modal from './Modal.svelte';
	import Button from './Button.svelte';

	interface Props {
		open: boolean;
		title: string;
		/** Primary message (single line or short paragraph). */
		message: string;
		/** Optional secondary text shown under message in muted style. */
		secondary?: string;
		confirmLabel?: string;
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
		confirmLabel = 'Удалить',
		cancelLabel = 'Отмена',
		variant = 'danger',
		busy = false,
		onConfirm,
		onClose,
	}: Props = $props();
</script>

<Modal {open} {title} size="sm" onclose={onClose}>
	<p class="confirm-message">{message}</p>
	{#if secondary}
		<p class="confirm-secondary">{secondary}</p>
	{/if}
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} disabled={busy}>
			{cancelLabel}
		</Button>
		<Button {variant} size="md" onclick={onConfirm} disabled={busy}>
			{busy ? 'Выполнение…' : confirmLabel}
		</Button>
	{/snippet}
</Modal>

<style>
	.confirm-message {
		margin: 0 0 0.5rem;
		line-height: 1.4;
	}
	.confirm-secondary {
		margin: 0;
		font-size: 0.875rem;
		color: var(--muted-text, var(--color-text-muted));
		line-height: 1.4;
	}
</style>
