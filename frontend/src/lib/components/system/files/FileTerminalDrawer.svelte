<script lang="ts">
	import { Button, Card } from '$lib/components/ui';
	import { Terminal } from 'lucide-svelte';
	import SystemTerminal from '../SystemTerminal.svelte';

	interface Props {
		currentPath: string;
		onHide: () => void;
	}

	let { currentPath, onHide }: Props = $props();
</script>

<Card padding="sm">
	<div class="terminal-drawer-head">
		<div class="drawer-title">
			<Terminal size={15} />
			<span>Встроенная консоль (текущий каталог: <code>{currentPath}</code>)</span>
		</div>
		<Button size="sm" variant="ghost" onclick={onHide}>Скрыть</Button>
	</div>
	<SystemTerminal compact />
</Card>

<style>
	.terminal-drawer-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.4rem;
	}
	.drawer-title {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
</style>
