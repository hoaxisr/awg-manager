<script lang="ts">
	import type { SubscriptionMember } from '$lib/types';
	import { Trash2 } from 'lucide-svelte';
	import SubscriptionMemberCard from './SubscriptionMemberCard.svelte';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';

	interface Props {
		members: SubscriptionMember[];
		effectiveActiveMember: string | null;
		switching: string | null;
		layout: SingboxLayoutMode;
		isInline: boolean;
		removingTag: string | null;
		minDelayMs: number | null;
		onpick: (tag: string) => void;
		onremove: (member: SubscriptionMember) => void;
	}
	let {
		members,
		effectiveActiveMember,
		switching,
		layout,
		isInline,
		removingTag,
		minDelayMs,
		onpick,
		onremove,
	}: Props = $props();
</script>

{#if layout === 'list'}
	<div class="awg-list-table member-list-table" class:with-inline-remove={isInline}>
		<div class="awg-list-table-track">
		<div
			class="sbx-member-list-row sbx-member-list-row--head">
			<span>Delay</span>
			<span>Сервер</span>
			<span>Протокол</span>
			<span>Ping</span>
			<span>Тег</span>
			<span>Статус</span>
			{#if isInline}<span class="h-rm" aria-hidden="true"></span>{/if}
		</div>
		<div class="member-list-meta-row mono">
			<span class="meta-lbl">Мин. delay</span>
			{#if minDelayMs !== null}
				<span class="meta-val"><strong>{minDelayMs} ms</strong></span>
				<span class="meta-hint">по последним проверкам среди серверов</span>
			{:else}
				<span class="meta-empty">—</span>
			{/if}
		</div>
		{#each members as member (member.tag)}
			<div
				class="member-list-line"
				class:with-inline-remove={isInline}
				class:active-line={member.tag === effectiveActiveMember}
				class:switching-line={switching === member.tag}
				class:is-disabled={switching !== null}
				role="button"
				tabindex={switching !== null ? -1 : 0}
				aria-pressed={member.tag === effectiveActiveMember}
				onclick={() => {
					if (switching !== null) return;
					onpick(member.tag);
				}}
				onkeydown={(e) => {
					if (switching !== null) return;
					if (e.key === 'Enter' || e.key === ' ') {
						e.preventDefault();
						onpick(member.tag);
					}
				}}
			>
				<SubscriptionMemberCard
					{member}
					active={member.tag === effectiveActiveMember}
					switching={switching === member.tag}
					disabled={switching !== null}
					onclick={() => onpick(member.tag)}
					layout="list"
				/>
				{#if isInline}
					<button
						type="button"
						class="member-remove-btn"
						title="Удалить сервер"
						aria-label="Удалить сервер {member.label || member.tag}"
						disabled={removingTag !== null}
						onclick={(e) => {
							e.stopPropagation();
							onremove(member);
						}}
					>
						<Trash2 size={14} aria-hidden="true" />
						Удалить
					</button>
				{/if}
			</div>
		{/each}
		</div>
	</div>
{:else}
<div class="grid">
	{#each members as member (member.tag)}
		<div
			class="member-slot"
			class:member-slot--inline={isInline}
			class:member-slot--active={member.tag === effectiveActiveMember}
		>
			<SubscriptionMemberCard
				{member}
				active={member.tag === effectiveActiveMember}
				switching={switching === member.tag}
				disabled={switching !== null}
				onclick={() => onpick(member.tag)}
			/>
			{#if isInline}
				<button
					type="button"
					class="member-remove-btn"
					title="Удалить сервер"
					aria-label="Удалить сервер {member.label || member.tag}"
					disabled={removingTag !== null}
					onclick={(e) => {
						e.stopPropagation();
						onremove(member);
					}}
				>
					<Trash2 size={14} aria-hidden="true" />
					Удалить
				</button>
			{/if}
		</div>
	{/each}
</div>
{/if}

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
		gap: 0.8rem;
		justify-items: stretch;
		align-items: stretch;
	}
	.mono { font-family: var(--font-mono, ui-monospace, monospace); }

	.member-slot {
		position: relative;
		min-width: 0;
	}
	.member-remove-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 4px;
		padding: 0.375rem 0.5rem;
		border: none;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--color-text-muted);
		font: inherit;
		font-size: var(--sbx-card-action);
		font-weight: 500;
		white-space: nowrap;
		cursor: pointer;
		flex-shrink: 0;
		transition: background var(--t-fast) ease, color var(--t-fast) ease;
	}
	.member-remove-btn:hover:not(:disabled) {
		color: var(--color-error);
		background: var(--color-error-tint);
	}
	.member-remove-btn:disabled {
		cursor: not-allowed;
		opacity: 0.5;
	}
	.member-remove-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}
	.member-slot .member-remove-btn {
		position: absolute;
		right: 6px;
		bottom: 6px;
		top: auto;
		z-index: 1;
	}

	@media (max-width: 640px) {
		.member-slot--inline {
			display: flex;
			flex-direction: column;
			border: 1px solid var(--color-border);
			border-radius: 10px;
			overflow: hidden;
			background: var(--color-bg-secondary);
		}

		.member-slot--inline.member-slot--active {
			border-color: #3fb950;
			background: rgba(63, 185, 80, 0.06);
		}

		.member-slot--inline :global(.card) {
			border: none;
			border-radius: 0;
			background: transparent;
			min-height: 0;
		}

		.member-slot--inline :global(.card.active) {
			border: none;
			background: transparent;
		}

		.member-slot--inline .member-remove-btn {
			position: static;
			width: 100%;
			border-top: 1px solid var(--color-border);
			border-radius: 0;
			padding: 0.5rem 0.75rem;
			justify-content: center;
		}
	}

	@media (max-width: 900px) {
		.grid {
			grid-template-columns: repeat(auto-fit, minmax(min(100%, 250px), 1fr));
		}
	}

	@media (max-width: 640px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}

	.member-list-table {
		border: 1px solid var(--color-border);
		border-radius: 12px;
		background: var(--color-bg-secondary);
		overflow-x: auto;
		overflow-y: hidden;
		margin-top: 0.25rem;
	}
	.member-list-table {
		--awg-list-min-width: 800px;
	}

	.member-list-table.with-inline-remove {
		--awg-list-min-width: 880px;
	}

	.sbx-member-list-row {
		display: grid;
		grid-template-columns:
			minmax(80px, 1fr)
			minmax(0, 1.35fr)
			minmax(0, 1fr)
			minmax(56px, 0.9fr)
			minmax(0, 0.95fr)
			minmax(88px, 1fr);
		gap: 0 1rem;
		align-items: center;
		padding: 0.65rem 1rem;
		border-bottom: 1px solid var(--color-border);
		min-width: max(100%, max(var(--awg-list-min-width, 0px), max-content));
	}
	.member-list-table.with-inline-remove .sbx-member-list-row {
		grid-template-columns:
			minmax(80px, 1fr)
			minmax(0, 1.35fr)
			minmax(0, 1fr)
			minmax(56px, 0.9fr)
			minmax(0, 0.95fr)
			minmax(88px, 1fr)
			minmax(72px, max-content);
	}
	.sbx-member-list-row--head {
		background: var(--color-bg-tertiary);
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		padding-top: 0.75rem;
		padding-bottom: 0.75rem;
	}
	.sbx-member-list-row--head .h-rm {
		display: block;
	}
	.member-list-meta-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.25rem 0.4rem;
		padding: 0.45rem 1rem;
		border-bottom: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
	}
	.member-list-meta-row .meta-lbl {
		text-transform: uppercase;
		letter-spacing: 0.04em;
		font-size: 0.65rem;
		font-weight: 700;
	}
	.member-list-meta-row .meta-val {
		color: var(--color-text-primary);
	}
	.member-list-meta-row .meta-val strong {
		color: #3fb950;
		font-weight: 600;
	}
	.member-list-meta-row .meta-empty {
		color: var(--color-text-muted);
	}
	.member-list-meta-row .meta-hint {
		font-size: 0.7rem;
		opacity: 0.85;
		margin-left: 0.25rem;
	}
	.member-list-line {
		padding: 0.65rem 1rem;
		border-bottom: 1px solid var(--color-border);
		cursor: pointer;
		min-width: max(100%, max(var(--awg-list-min-width, 0px), max-content));
	}
	.member-list-line:not(.with-inline-remove) {
		display: flex;
		align-items: center;
	}
	.member-list-line:not(.with-inline-remove) :global(.mbr-flatten) {
		flex: 1;
		min-width: 0;
		display: grid;
		grid-template-columns:
			minmax(80px, 1fr)
			minmax(0, 1.35fr)
			minmax(0, 1fr)
			minmax(56px, 0.9fr)
			minmax(0, 0.95fr)
			minmax(88px, 1fr);
		gap: 0 1rem;
		align-items: center;
	}
	.member-list-line.with-inline-remove {
		display: grid;
		grid-template-columns:
			minmax(80px, 1fr)
			minmax(0, 1.35fr)
			minmax(0, 1fr)
			minmax(56px, 0.9fr)
			minmax(0, 0.95fr)
			minmax(88px, 1fr)
			minmax(72px, max-content);
		gap: 0 1rem;
		align-items: center;
	}
	.member-list-line.with-inline-remove :global(.mbr-flatten) {
		display: contents;
	}
	.member-list-line.with-inline-remove .member-remove-btn {
		justify-self: end;
	}
	.member-list-line:last-child {
		border-bottom: none;
	}
	.member-list-line.active-line {
		background: rgba(63, 185, 80, 0.06);
	}
	.member-list-line.switching-line {
		opacity: 0.65;
		cursor: wait;
	}
	.member-list-line.is-disabled {
		cursor: not-allowed;
	}
</style>
