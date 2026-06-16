<script lang="ts">
	import { get } from 'svelte/store';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { Button, ConfirmModal } from '$lib/components/ui';
	import { Trash2, Edit3, GripVertical } from 'lucide-svelte';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import type { SingboxRouterDNSRewrite } from '$lib/types';
	import DNSRewriteEditModal from './DNSRewriteEditModal.svelte';
	import { createReorderDrag } from '$lib/components/sb-router/reorderDrag.svelte';

	interface Props {
		rewrites: SingboxRouterDNSRewrite[];
		onChange: () => Promise<void> | void;
		/** Показывать встроенный заголовок (счётчик + кнопка «Добавить»). */
		showHeader?: boolean;
		hideColumnHeader?: boolean;
		/** Режим добавления — bindable, чтобы триггерить add из родителя. */
		addMode?: boolean;
	}
	let { rewrites, onChange, showHeader = true, hideColumnHeader = false, addMode = $bindable(false) }: Props = $props();

	let editIndex = $state<number | null>(null);
	let deleteIndex = $state<number | null>(null);
	let deleteBusy = $state(false);

	// ── Drag-reorder (то же pointer-ядро, что и route.rules) ──────────────
	let rowEls = $state<Array<HTMLElement | null>>([]);

	function reorder(list: SingboxRouterDNSRewrite[], from: number, to: number): SingboxRouterDNSRewrite[] {
		const next = list.slice();
		const [moved] = next.splice(from, 1);
		next.splice(to, 0, moved);
		return next;
	}

	const drag = createReorderDrag({
		getRowElement: (i) => rowEls[i] ?? null,
		count: () => rewrites.length,
		onCommit: async (from, to) => {
			const snapshot = get(singboxRouter.dnsRewrites);
			singboxRouter.applyDNSRewrites(reorder(snapshot, from, to));
			try {
				await api.singboxRouterMoveDNSRewrite(from, to);
				await onChange();
			} catch (e) {
				singboxRouter.applyDNSRewrites(snapshot);
				notifications.error(`Ошибка перемещения: ${e instanceof Error ? e.message : String(e)}`);
			}
		},
	});

	function requestEdit(i: number): void {
		editIndex = i;
	}

	function requestDelete(i: number): void {
		deleteIndex = i;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteIndex === null) return;
		deleteBusy = true;
		try {
			await api.singboxRouterDeleteDNSRewrite(deleteIndex);
			deleteIndex = null;
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			deleteBusy = false;
		}
	}
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

{#if showHeader}
	<div class="header">
		<div class="hint">{rewrites.length} перезаписей</div>
		<Button variant="primary" size="sm" onclick={() => (addMode = true)} iconBefore={createIcon}>
			Добавить
		</Button>
	</div>
{/if}

{#if rewrites.length === 0}
	<div class="empty">
		Нет перезаписей. «Перезапись» возвращает заданный IP для домена/паттерна.
	</div>
{:else}
	{#if !hideColumnHeader}
		<div class="col-header">
			<div></div>
			<div>Шаблон</div>
			<div></div>
			<div>IP-адреса</div>
			<div class="actions-head">Действия</div>
		</div>
	{/if}
	<div class="rows" class:is-dragging={drag.draggingIndex !== null}>
		{#each rewrites as rw, i (i)}
			<div class="row-shell" class:drop-before={drag.dropBefore(i)}>
			<div
				class="row"
				class:dragging={drag.draggingIndex === i}
				bind:this={rowEls[i]}
				role="button"
				tabindex="0"
				onclick={() => requestEdit(i)}
				onkeydown={(e) => {
					if (e.target !== e.currentTarget) return;

					if (e.key === 'Enter' || e.key === ' ') {
						e.preventDefault();
						requestEdit(i);
					}
				}}
				aria-label={`Редактировать DNS-перезапись ${rw.pattern}`}
				title={`Редактировать DNS-перезапись «${rw.pattern}»`}
			>
				<button
					type="button"
					class="grip"
					class:is-busy={drag.busy}
					aria-label={`Перетащить DNS-перезапись ${rw.pattern}`}
					title="Перетащить для изменения порядка"
					onpointerdown={drag.busy ? undefined : (e) => drag.handlePointerDown(i, e)}
					onclick={(e) => e.stopPropagation()}
				>
					<GripVertical size={15} />
				</button>
				<code class="pat mono" title={rw.pattern}>{rw.pattern}</code>
				<span class="arrow">→</span>
				<span class="ips-line">
					<span class="mobile-arrow">→</span>
					<span class="ips mono" title={rw.ips.join(', ')}>{rw.ips.join(', ')}</span>
				</span>
				<div class="row-actions">
					<button
						type="button"
						class="route-action-btn"
						onclick={(e) => {
							e.stopPropagation();
							requestEdit(i);
						}}
						aria-label={`Редактировать DNS-перезапись ${rw.pattern}`}
						title={`Редактировать DNS-перезапись «${rw.pattern}»`}
					>
						<Edit3 size={15} />
					</button>
					<button
						type="button"
						class="route-action-btn danger"
						onclick={(e) => {
							e.stopPropagation();
							requestDelete(i);
						}}
						aria-label={`Удалить DNS-перезапись ${rw.pattern}`}
						title={`Удалить DNS-перезапись «${rw.pattern}»`}
					>
						<Trash2 size={15} />
					</button>
				</div>
			</div>
			</div>
		{/each}
		<div class="end-drop" class:drop-before={drag.dropAtEnd()}></div>
	</div>
{/if}

{#if addMode}
	<DNSRewriteEditModal
		onClose={() => (addMode = false)}
		onSave={async (rw) => {
			await api.singboxRouterAddDNSRewrite(rw);
			addMode = false;
			await onChange();
		}}
	/>
{/if}

{#if editIndex !== null}
	{@const idx = editIndex}
	<DNSRewriteEditModal
		rewrite={rewrites[idx]}
		onClose={() => (editIndex = null)}
		onSave={async (rw) => {
			await api.singboxRouterUpdateDNSRewrite(idx, rw);
			editIndex = null;
			await onChange();
		}}
	/>
{/if}

<ConfirmModal
	open={deleteIndex !== null}
	title="Удалить перезапись"
	message={deleteIndex !== null ? `Удалить перезапись «${rewrites[deleteIndex]?.pattern ?? ''}»?` : ''}
	busy={deleteBusy}
	onConfirm={confirmDelete}
	onClose={() => { if (!deleteBusy) deleteIndex = null; }}
/>

<style>
	.header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.75rem;
	}
	.hint {
		color: var(--muted-text);
		font-size: 0.85rem;
	}
	.empty {
		padding: 14px;
		color: var(--text-muted);
		text-align: center;
		font-size: 12px;
	}
	.col-header {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) 16px minmax(0, 1fr) auto;
		gap: 0.4rem;
		padding: 0.25rem 0.75rem;
		font-size: 0.65rem;
		letter-spacing: 0.5px;
		text-transform: uppercase;
		color: var(--muted-text);
	}
	.actions-head {
		text-align: right;
		white-space: nowrap;
	}
	.rows {
		display: grid;
		gap: 0.2rem;
		min-width: 0;
	}
	.rows.is-dragging {
		user-select: none;
	}
	.row-shell {
		position: relative;
		min-width: 0;
	}
	.row-shell.drop-before::before,
	.end-drop.drop-before::before {
		content: '';
		position: absolute;
		left: 0;
		right: 0;
		top: -1px;
		height: 2px;
		border-radius: 999px;
		background: var(--color-accent, var(--accent));
		box-shadow: 0 0 10px color-mix(in srgb, var(--color-accent, var(--accent)) 45%, transparent);
		z-index: 2;
		pointer-events: none;
	}
	.end-drop {
		position: relative;
		height: 0;
	}
	.row {
		transition: background-color 0.15s ease;
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) 16px minmax(0, 1fr) auto;
		gap: 0.4rem;
		align-items: center;
		min-width: 0;
		background: var(--surface-bg);
		padding: 0.5rem 0.75rem;
		border-radius: 4px;
		cursor: pointer;
	}
	.row.dragging {
		border: 1px solid color-mix(in srgb, var(--color-accent, var(--accent)) 55%, var(--border));
		opacity: 0.7;
	}

	.row:focus-visible {
		outline: 2px solid var(--color-accent, var(--accent));
		outline-offset: 2px;
	}

	.grip {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: transparent;
		border: none;
		padding: 2px;
		color: var(--text-muted);
		cursor: grab;
		touch-action: none;
		border-radius: 4px;
		opacity: 0.6;
		flex-shrink: 0;
	}
	.grip:hover {
		color: var(--text-primary);
		opacity: 1;
	}
	.grip:active {
		cursor: grabbing;
	}
	.grip.is-busy {
		cursor: wait;
		opacity: 0.35;
		pointer-events: none;
	}

	.row-actions {
		display: flex;
		flex-wrap: nowrap;
		align-items: center;
		justify-content: flex-end;
		gap: 4px;
		flex-shrink: 0;
	}
	.ips-line {
		min-width: 0;
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
	}
	.mobile-arrow {
		display: none;
	}

	.mono {
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
	}
	.pat {
		color: var(--text);
		min-width: 0;
		white-space: normal;
		overflow: visible;
		text-overflow: initial;
		overflow-wrap: anywhere;
		word-break: normal;
		line-height: 1.35;
	}
	.ips {
		color: var(--success, #22c55e);
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	@media (hover: hover) and (pointer: fine) {
		.row:hover {
			background: color-mix(in srgb, var(--bg-hover) 70%, transparent);
		}
	}

	@media (max-width: 720px) {
		.col-header {
			display: none;
		}

		.rows {
			gap: 0;
		}

		.row {
			grid-template-columns: auto minmax(0, 1fr) auto;
			grid-template-areas:
				'grip pattern actions'
				'grip ips actions';
			gap: 0.5rem 0.625rem;
			padding: 0.75rem 0.875rem;
			border: 0;
			border-radius: 0;
			border-bottom: 1px solid var(--border);
			background: transparent;
		}

		.row:last-child {
			border-bottom: 0;
		}

		.grip { grid-area: grip; align-self: center; }
		.pat { grid-area: pattern; }
		.ips-line {
			grid-area: ips;
			display: inline-flex;
			align-items: center;
			gap: 0.35rem;
			min-width: 0;
		}
		.pat {
			white-space: normal;
			overflow: visible;
			text-overflow: initial;
			overflow-wrap: anywhere;
			word-break: normal;
		}
		.arrow { display: none; }
		.mobile-arrow {
			display: inline;
			flex: 0 0 auto;
			color: var(--muted-text);
			line-height: 1;
			opacity: 0.85;
		}
		.ips {
			min-width: 0;
			white-space: normal;
			overflow-wrap: anywhere;
		}
		.row-actions {
			grid-area: actions;
			align-self: center;
		}
	}

	:global(body.reorder-dragging) {
		user-select: none;
		cursor: grabbing;
	}
</style>
