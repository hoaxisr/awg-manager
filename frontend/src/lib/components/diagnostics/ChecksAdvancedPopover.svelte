<script lang="ts">
	import { Settings } from 'lucide-svelte';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		includeRestart: boolean;
		running: boolean;
		onChangeIncludeRestart: (v: boolean) => void;
	}

	let { includeRestart, running, onChangeIncludeRestart }: Props = $props();

	let open = $state(false);
	let popover = $state<HTMLDivElement | undefined>(undefined);
	let trigger = $state<HTMLButtonElement | undefined>(undefined);

	function close() {
		open = false;
	}

	function handleClickOutside(e: MouseEvent) {
		if (!open) return;
		const target = e.target as Node | null;
		if (popover?.contains(target ?? null)) return;
		if (trigger?.contains(target ?? null)) return;
		close();
	}

	function handleKey(e: KeyboardEvent) {
		if (open && e.key === 'Escape') close();
	}

	$effect(() => {
		if (open) {
			document.addEventListener('mousedown', handleClickOutside);
			document.addEventListener('keydown', handleKey);
			return () => {
				document.removeEventListener('mousedown', handleClickOutside);
				document.removeEventListener('keydown', handleKey);
			};
		}
	});
</script>

<div class="wrap">
	<button
		bind:this={trigger}
		class="trigger"
		type="button"
		onclick={() => (open = !open)}
		class:active={open || includeRestart}
		aria-haspopup="dialog"
		aria-expanded={open}
		title="Расширенные параметры"
	>
		<Settings size={14} />
		{#if includeRestart}<span class="dot" aria-label="custom"></span>{/if}
	</button>

	{#if open}
		<div bind:this={popover} class="popover" role="dialog">
			<header class="head">Расширенные параметры</header>

			<label class="toggle-row">
				<Toggle
					checked={includeRestart}
					onchange={onChangeIncludeRestart}
					disabled={running}
				/>
				<div class="toggle-label">
					<span>Включая restart-цикл</span>
					<span class="hint">
						Перезапустит каждый запущенный туннель на 2-5&nbsp;сек для проверки
						stop/start цикла.
					</span>
				</div>
			</label>
		</div>
	{/if}
</div>

<style>
	.wrap {
		position: relative;
		display: inline-block;
	}

	.trigger {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 32px;
		height: 32px;
		padding: 0;
		background: transparent;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		transition: background var(--t-fast) ease, color var(--t-fast) ease,
			border-color var(--t-fast) ease;
	}
	.trigger:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}
	.trigger.active {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}

	.dot {
		position: absolute;
		top: 4px;
		right: 4px;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-accent);
	}

	.popover {
		position: absolute;
		top: calc(100% + 6px);
		right: 0;
		z-index: var(--z-page-overlay);
		min-width: 280px;
		padding: 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		box-shadow: 0 12px 32px rgba(0, 0, 0, 0.45);
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.head {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		padding-bottom: 6px;
		border-bottom: 1px solid var(--color-border);
	}

	.toggle-row {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		cursor: pointer;
	}

	.toggle-label {
		display: flex;
		flex-direction: column;
		gap: 4px;
		font-size: 13px;
		color: var(--color-text-primary);
	}

	.hint {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.4;
	}
</style>
