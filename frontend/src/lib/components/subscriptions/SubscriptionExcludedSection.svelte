<script lang="ts">
	import type { SubscriptionMember } from '$lib/types';
	import { ChevronDown, ChevronRight, RotateCcw } from 'lucide-svelte';
	import { Button } from '$lib/components/ui';

	interface Props {
		members: SubscriptionMember[];
		restoring: boolean;
		onrestore: (tags: string[]) => void;
	}
	let { members, restoring, onrestore }: Props = $props();

	let open = $state(true);
	let selected = $state<Set<string>>(new Set());

	function toggleSel(tag: string): void {
		const next = new Set(selected);
		if (next.has(tag)) next.delete(tag);
		else next.add(tag);
		selected = next;
	}

	function restoreOne(tag: string): void {
		if (restoring) return;
		onrestore([tag]);
	}

	function restoreSelected(): void {
		if (restoring || selected.size === 0) return;
		onrestore([...selected]);
		selected = new Set();
	}

	function tagSuffix(tag: string): string {
		return tag.length > 8 ? tag.slice(-8) : tag;
	}
</script>

{#if members.length > 0}
	<section class="excluded">
		<div class="excluded-head">
			<button
				type="button"
				class="excluded-toggle"
				aria-expanded={open}
				onclick={() => (open = !open)}
			>
				{#if open}
					<ChevronDown size={14} aria-hidden="true" />
				{:else}
					<ChevronRight size={14} aria-hidden="true" />
				{/if}
				<span class="lbl accent">Исключённые ({members.length})</span>
			</button>
			{#if open && selected.size > 0}
				<Button
					variant="ghost"
					size="sm"
					disabled={restoring}
					loading={restoring}
					iconBefore={restoreIcon}
					onclick={restoreSelected}
				>
					{restoring ? 'Возвращаем...' : `Вернуть выбранные (${selected.size})`}
				</Button>
			{/if}
		</div>
		{#if open}
			<div class="hint">
				Эти серверы вы исключили вручную. Они не материализуются и не участвуют в выборе,
				пока вы их не вернёте. Набор переживает обновление подписки.
			</div>
			<div class="grid">
				{#each members as member (member.tag)}
					<div class="excluded-card">
						<label class="excluded-sel">
							<input
								type="checkbox"
								checked={selected.has(member.tag)}
								disabled={restoring}
								onchange={() => toggleSel(member.tag)}
							/>
						</label>
						<div class="excluded-main">
							<div class="excluded-title">{member.label || `${member.server}:${member.port}`}</div>
							<div class="excluded-meta mono">
								{member.protocol} · {member.server}:{member.port} · {tagSuffix(member.tag)}
							</div>
						</div>
						<Button
							variant="ghost"
							size="sm"
							disabled={restoring}
							iconBefore={restoreIcon}
							onclick={() => restoreOne(member.tag)}
						>
							Вернуть
						</Button>
					</div>
				{/each}
			</div>
		{/if}
	</section>
{/if}

{#snippet restoreIcon()}
	<RotateCcw size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<style>
	.excluded {
		margin-top: 1.5rem;
		padding-top: 1rem;
		border-top: 1px solid var(--color-border);
	}
	.excluded-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		margin-bottom: 0.6rem;
	}
	.excluded-toggle {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		padding: 0;
		border: none;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
	}
	.lbl {
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.5px;
	}
	.lbl.accent {
		color: var(--color-accent);
	}
	.hint {
		color: var(--color-text-muted);
		font-size: 0.82rem;
		margin-bottom: 0.8rem;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
		gap: 0.8rem;
		justify-items: stretch;
		align-items: stretch;
	}
	.excluded-card {
		display: flex;
		align-items: flex-start;
		gap: 0.6rem;
		padding: 12px 14px;
		border: 1px dashed var(--color-border);
		border-radius: 10px;
	}
	.excluded-sel {
		display: flex;
		align-items: center;
		padding-top: 0.15rem;
		flex-shrink: 0;
	}
	.excluded-main {
		flex: 1 1 auto;
		min-width: 0;
	}
	.excluded-title {
		font-size: 0.88rem;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.excluded-meta {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin-top: 0.25rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	@media (max-width: 640px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
