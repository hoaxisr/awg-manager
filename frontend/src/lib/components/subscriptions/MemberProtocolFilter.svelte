<script lang="ts">
	import { tick } from 'svelte';
	import { m } from '$lib/i18n';
	import { ListFilter } from 'lucide-svelte';
	import { Button } from '$lib/components/ui';
	import type { ProtocolFilterOption } from './memberSelectors';

	interface Props {
		options: ProtocolFilterOption[];
		excluded: Set<string>;
		onToggle: (protocol: string) => void;
		onSelectAll?: () => void;
		onClearAll?: () => void;
	}
	let { options, excluded, onToggle, onSelectAll, onClearAll }: Props = $props();

	const activeCount = $derived(options.filter((item) => !excluded.has(item.protocol)).length);
	const filtered = $derived(activeCount < options.length);

	let open = $state(false);
	let wrapEl = $state<HTMLDivElement | null>(null);
	// Поповер — position: fixed: таблица серверов режет overflow, и меню,
	// позиционированное от кнопки, обрезалось бы её краем.
	let pos = $state({ top: 0, left: 0 });

	function updatePos(): void {
		if (!wrapEl) return;
		const rect = wrapEl.getBoundingClientRect();
		const maxLeft = Math.max(8, window.innerWidth - 240);
		pos = { top: rect.bottom + 4, left: Math.max(8, Math.min(rect.left, maxLeft)) };
	}

	async function toggle(e: MouseEvent): Promise<void> {
		e.stopPropagation();
		open = !open;
		if (!open) return;
		updatePos();
		await tick();
		updatePos();
	}

	// Слушатели живут только пока поповер открыт.
	$effect(() => {
		if (!open) return;
		const onPointerDown = (e: PointerEvent) => {
			if (wrapEl && !wrapEl.contains(e.target as Node)) open = false;
		};
		const onKeyDown = (e: KeyboardEvent) => {
			if (e.key === 'Escape') open = false;
		};

		window.addEventListener('pointerdown', onPointerDown);
		window.addEventListener('keydown', onKeyDown);
		window.addEventListener('resize', updatePos);
		window.addEventListener('scroll', updatePos, true);
		return () => {
			window.removeEventListener('pointerdown', onPointerDown);
			window.removeEventListener('keydown', onKeyDown);
			window.removeEventListener('resize', updatePos);
			window.removeEventListener('scroll', updatePos, true);
		};
	});
</script>

{#snippet filterIcon()}<ListFilter size={13} aria-hidden="true" />{/snippet}

<div class="proto-filter" bind:this={wrapEl} aria-haspopup="dialog" aria-expanded={open}>
	<Button
		variant={filtered || open ? 'outline-primary' : 'secondary'}
		size="sm"
		iconBefore={filterIcon}
		ariaLabel={m.subscriptions_preview_protocol_filter_aria()}
		onclick={toggle}
	>
		{m.subscriptions_members_filter_btn()}
		{#if filtered}
			<span class="proto-badge mono">{activeCount}/{options.length}</span>
		{/if}
	</Button>
	{#if open}
		<div
			class="proto-popover"
			role="dialog"
			aria-label={m.subscriptions_preview_protocol_filter_aria()}
			style="top: {pos.top}px; left: {pos.left}px;"
		>
			<div class="proto-actions">
				<Button variant="ghost" size="sm" onclick={() => onSelectAll?.()}>
					{m.subscriptions_members_filter_all()}
				</Button>
				<Button variant="ghost" size="sm" onclick={() => onClearAll?.()}>
					{m.subscriptions_members_filter_none()}
				</Button>
			</div>
			<div class="proto-options">
				{#each options as item (item.protocol)}
					<label class="proto-option">
						<input
							type="checkbox"
							class="proto-check"
							checked={!excluded.has(item.protocol)}
							onchange={() => onToggle(item.protocol)}
						/>
						<span class="proto-name">{item.label}</span>
						<span class="proto-count mono">{item.count}</span>
					</label>
				{/each}
			</div>
		</div>
	{/if}
</div>

<style>
	.mono { font-family: var(--font-mono, ui-monospace, monospace); }
	.proto-filter {
		position: relative;
		display: inline-flex;
	}
	.proto-badge {
		margin-left: 0.3rem;
		font-size: 0.72rem;
	}
	.proto-popover {
		position: fixed;
		z-index: 60;
		min-width: 220px;
		max-width: 280px;
		max-height: 280px;
		overflow-y: auto;
		padding: 0.45rem;
		border: 1px solid var(--color-border);
		border-radius: 8px;
		background: var(--color-bg-secondary);
		box-shadow: 0 10px 28px rgba(0, 0, 0, 0.35);
	}
	.proto-actions {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.35rem;
		padding-bottom: 0.4rem;
		margin-bottom: 0.35rem;
		border-bottom: 1px solid var(--color-border);
	}
	.proto-options {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}
	.proto-option {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.3rem 0.35rem;
		border-radius: var(--radius-sm);
		cursor: pointer;
		font-size: 0.82rem;
		color: var(--color-text-primary);
	}
	.proto-option:hover {
		background: var(--color-bg-tertiary);
	}
	.proto-check {
		width: 15px;
		height: 15px;
		accent-color: var(--color-accent);
		cursor: pointer;
		margin: 0;
	}
	.proto-name {
		flex: 1;
		min-width: 0;
	}
	.proto-count {
		font-size: 0.74rem;
		color: var(--color-text-muted);
	}
</style>
