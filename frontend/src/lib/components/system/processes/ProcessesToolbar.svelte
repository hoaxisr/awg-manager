<script lang="ts">
	import { Button, Card } from '$lib/components/ui';
	import { RefreshCw, Search, Pause, Power, Check } from 'lucide-svelte';

	interface Props {
		enabled: boolean;
		loading: boolean;
		interval: number;
		showKernelThreads: boolean;
		searchQuery: string;
		processCount: number;
		ontoggleenabled: () => void;
		onrefresh: () => void;
		onintervalchange: (sec: number) => void;
		ontogglekernelthreads: () => void;
		onsearchchange: (value: string) => void;
	}

	let {
		enabled,
		loading,
		interval,
		showKernelThreads,
		searchQuery,
		processCount,
		ontoggleenabled,
		onrefresh,
		onintervalchange,
		ontogglekernelthreads,
		onsearchchange,
	}: Props = $props();
</script>

<Card padding="sm">
	<div class="proctop-master-bar">
		<div class="master-left">
			<button
				type="button"
				class="master-switch-btn"
				class:active={enabled}
				onclick={ontoggleenabled}
			>
				<Power size={14} />
				<span>{enabled ? 'Мониторинг активен' : 'Мониторинг выключен'}</span>
			</button>

			{#if enabled}
				<Button size="sm" variant="ghost" onclick={onrefresh} disabled={loading}>
					{#snippet iconBefore()}<RefreshCw size={14} class={loading ? 'spin' : ''} />{/snippet}
					Обновить
				</Button>

				<!-- Auto refresh interval picker -->
				<div class="interval-picker">
					<span class="picker-label">Интервал:</span>
					<button
						type="button"
						class="interval-btn"
						class:active={interval === 1}
						onclick={() => onintervalchange(1)}
					>
						1с
					</button>
					<button
						type="button"
						class="interval-btn"
						class:active={interval === 2}
						onclick={() => onintervalchange(2)}
					>
						2с
					</button>
					<button
						type="button"
						class="interval-btn"
						class:active={interval === 5}
						onclick={() => onintervalchange(5)}
					>
						5с
					</button>
					<button
						type="button"
						class="interval-btn"
						class:active={interval === 0}
						onclick={() => onintervalchange(0)}
						title="Приостановить опрос"
					>
						{#if interval === 0}
							<Pause size={12} /> Пауза
						{:else}
							Пауза
						{/if}
					</button>
				</div>
			{/if}
		</div>

		{#if enabled}
			<div class="master-right">
				<button
					type="button"
					class="filter-threads-btn"
					class:active={showKernelThreads}
					onclick={ontogglekernelthreads}
				>
					{#if showKernelThreads}
						<Check size={14} class="icon-inline" /> Все потоки ядра
					{:else}
						Показать потоки ядра
					{/if}
				</button>

				<div class="search-box">
					<Search size={13} class="search-icon" />
					<input
						type="text"
						placeholder="Поиск по PID, имени, аргументам…"
						value={searchQuery}
						oninput={(e) => onsearchchange(e.currentTarget.value)}
					/>
				</div>
				<span class="counter-badge">{processCount} процессов</span>
			</div>
		{/if}
	</div>
</Card>

<style>
	/* Master Bar */
	.proctop-master-bar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.65rem;
	}

	.master-left, .master-right {
		display: flex;
		align-items: center;
		gap: 0.55rem;
		flex-wrap: wrap;
	}

	.master-switch-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		padding: 0.3rem 0.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
		font-size: 0.8rem;
		font-weight: 600;
		cursor: pointer;
		transition: all 0.15s ease;
	}
	.master-switch-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.master-switch-btn.active {
		background: rgba(16, 185, 129, 0.12);
		border-color: rgba(16, 185, 129, 0.35);
		color: #059669;
	}
	:global(.dark) .master-switch-btn.active {
		background: rgba(16, 185, 129, 0.15);
		border-color: rgba(16, 185, 129, 0.35);
		color: #34d399;
	}

	.filter-threads-btn {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		padding: 0.25rem 0.55rem;
		border-radius: var(--radius-sm, 6px);
		font-size: 0.75rem;
		cursor: pointer;
		white-space: nowrap;
	}
	.filter-threads-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.filter-threads-btn.active {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.15));
		border-color: var(--color-accent);
		color: var(--color-accent);
		font-weight: 600;
	}

	/* Interval Picker */
	.interval-picker {
		display: flex;
		align-items: center;
		gap: 0.2rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		padding: 0.15rem;
	}

	.picker-label {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		padding: 0 0.35rem;
	}

	.interval-btn {
		background: none;
		border: none;
		padding: 0.2rem 0.45rem;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
		border-radius: 4px;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 0.2rem;
	}

	.interval-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}

	.interval-btn.active {
		background: var(--color-accent);
		color: #ffffff;
	}

	.search-box {
		position: relative;
		display: flex;
		align-items: center;
	}

	:global(.search-icon) {
		position: absolute;
		left: 0.55rem;
		color: var(--color-text-muted);
	}

	.search-box input {
		padding: 0.3rem 0.55rem 0.3rem 1.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.8rem;
		width: 200px;
	}

	.search-box input:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.counter-badge {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		white-space: nowrap;
	}
</style>
