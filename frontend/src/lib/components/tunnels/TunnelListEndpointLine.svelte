<script lang="ts">
	import { Eye, EyeOff } from 'lucide-svelte';

	interface Props {
		host: string;
		port: number;
		show?: boolean;
		onToggle?: () => void;
		class?: string;
	}

	let { host, port, show = $bindable(false), onToggle, class: className = '' }: Props = $props();
</script>

<div class="tunnel-list-endpoint-line mono {className}">
	<span class="tunnel-list-endpoint-host" class:tunnel-list-endpoint-host--muted={!show}>
		{show ? host : '••••••••'}
	</span>
	<button
		type="button"
		class="tunnel-list-endpoint-eye"
		onclick={(e) => {
			e.stopPropagation();
			show = !show;
			onToggle?.();
		}}
		aria-label={show ? 'Скрыть' : 'Показать'}
		title={show ? 'Скрыть' : 'Показать'}
	>
		{#if show}
			<Eye size={12} aria-hidden="true" />
		{:else}
			<EyeOff size={12} aria-hidden="true" />
		{/if}
	</button>
	<span class="tunnel-list-endpoint-port">:{port}</span>
</div>
