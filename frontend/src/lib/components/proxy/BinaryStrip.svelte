<script lang="ts">
	import { Badge, Button } from '$lib/components/ui';
	import { Download } from 'lucide-svelte';

	interface BinaryState {
		/** Имя бинаря: PG-04 «wdtt» / PG-05 «freeturn». */
		name: string;
		binaryPresent: boolean;
		/** Есть ли что ставить (зеркало/пин доступны). */
		installAvailable: boolean;
		installing: boolean;
		/**
		 * У бэкенда флаг истинен И когда бинаря нет (оговорка PG-07), поэтому
		 * бейдж обновления показывается только вместе с binaryPresent.
		 */
		updateAvailable: boolean;
		installedVersion?: string;
		installVersion?: string;
		oninstall: () => void;
	}

	interface Props {
		binaries: BinaryState[];
	}

	let { binaries }: Props = $props();

	// Полоса пуста, когда всё установлено и обновлений нет (ia.md §1).
	const shown = $derived(binaries.filter((b) => !b.binaryPresent || b.updateAvailable));
</script>

{#if shown.length > 0}
	<div class="binaries">
		{#each shown as b (b.name)}
			<div class="bin">
				<span class="bin-name">{b.name}</span>

				{#if b.binaryPresent}
					{#if b.installedVersion}
						<Badge size="xs" variant="success">установлен {b.installedVersion}</Badge>
					{/if}
					{#if b.installVersion}
						<Badge size="xs" variant="warning">доступно обновление {b.installVersion}</Badge>
					{/if}
				{:else}
					<Badge size="xs" variant="error">не установлен</Badge>
				{/if}

				{#if b.installAvailable}
					<Button
						variant={b.binaryPresent ? 'secondary' : 'primary'}
						loading={b.installing}
						disabled={b.installing}
						onclick={b.oninstall}
					>
						{#snippet iconBefore()}<Download size={14} strokeWidth={2.5} />{/snippet}
						{b.binaryPresent ? 'Обновить' : 'Установить'}
					</Button>
				{/if}
			</div>
		{/each}
	</div>
{/if}

<style>
	.binaries {
		display: flex;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-bottom: 1rem;
	}

	.bin {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
	}

	.bin-name {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
</style>
