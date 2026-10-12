<script lang="ts">
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { Copy } from 'lucide-svelte';

	interface Props {
		value: string;
		size?: number;
		title?: string;
	}

	let { value, size = 12, title }: Props = $props();

	async function copy() {
		const ok = await copyToClipboard(value);
		if (ok) notifications.success(m.system_files_copied_to_clipboard());
	}
</script>

<button type="button" class="btn-copy" onclick={copy} {title}>
	<Copy {size} />
</button>

<style>
	.btn-copy {
		background: none;
		border: none;
		cursor: pointer;
		padding: 0.2rem;
		color: var(--color-text-muted);
		border-radius: 4px;
		display: inline-flex;
		align-items: center;
	}
	.btn-copy:hover {
		color: var(--color-accent);
	}
</style>
