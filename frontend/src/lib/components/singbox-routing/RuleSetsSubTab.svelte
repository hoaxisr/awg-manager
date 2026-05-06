<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxTunnels } from '$lib/stores/singbox';
	import {
		Button,
		IconButton,
		Badge,
		StatRow,
		ConfirmModal,
	} from '$lib/components/ui';
	import type { StatTile } from '$lib/components/ui';
	import type {
		AWGTagInfo,
		SingboxRouterRuleSet,
		SingboxTunnel,
	} from '$lib/types';
	import {
		buildOutboundOptions,
		RuleSetAddModal,
		RefreshSettingsModal,
	} from '$lib/components/routing/singboxRouter';

	const ruleSetsStore = singboxRouter.ruleSets;
	const outboundsStore = singboxRouter.outbounds;
	const settingsStore = singboxRouter.settings;
	const phase1Store = singboxTunnels;

	const ruleSets = $derived($ruleSetsStore);
	const outbounds = $derived($outboundsStore);
	const settings = $derived($settingsStore);
	const phase1Tunnels = $derived(($phase1Store.data ?? []) as SingboxTunnel[]);

	let awgTags = $state<AWGTagInfo[]>([]);

	async function loadAWGTags(): Promise<void> {
		try {
			awgTags = await api.getAWGTags();
		} catch {
			awgTags = [];
		}
	}

	async function refresh(): Promise<void> {
		await singboxRouter.loadAll();
	}

	onMount(() => {
		loadAWGTags();
	});

	const outboundOptions = $derived(
		buildOutboundOptions(awgTags, phase1Tunnels, outbounds, true),
	);

	type SourceFilter = 'all' | 'remote' | 'local' | 'inline';
	let sourceFilter = $state<SourceFilter>('all');

	const remoteCount = $derived(ruleSets.filter((r) => r.type === 'remote').length);
	const localCount = $derived(ruleSets.filter((r) => r.type === 'local').length);
	const inlineCount = $derived(ruleSets.filter((r) => r.type === 'inline').length);

	const visibleRuleSets = $derived(
		sourceFilter === 'all'
			? ruleSets
			: ruleSets.filter((r) => r.type === sourceFilter),
	);

	const statTiles = $derived<StatTile[]>([
		{ label: 'Наборов', value: ruleSets.length },
		{ label: 'Удалённых', value: remoteCount },
		{ label: 'Локальных', value: localCount },
		{ label: 'Inline', value: inlineCount },
	]);

	let addMode = $state(false);
	let editTag = $state<string | null>(null);
	let refreshSettingsOpen = $state(false);
	let refreshing = $state<Set<string>>(new Set());
	let deleteTag = $state<string | null>(null);
	let forceDeleteTag = $state<string | null>(null);
	let forceDeleteMessage = $state('');
	let busy = $state(false);

	async function refreshOne(tag: string): Promise<void> {
		const next = new Set(refreshing);
		next.add(tag);
		refreshing = next;
		try {
			await api.singboxRouterRefreshRuleSet(tag);
			await refresh();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			const cleaned = new Set(refreshing);
			cleaned.delete(tag);
			refreshing = cleaned;
		}
	}

	function requestDelete(tag: string): void {
		deleteTag = tag;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteTag === null) return;
		const tag = deleteTag;
		busy = true;
		try {
			await api.singboxRouterDeleteRuleSet(tag, false);
			deleteTag = null;
			await refresh();
		} catch (e) {
			const msg = (e as Error).message;
			deleteTag = null;
			if (msg.includes('referenced')) {
				forceDeleteMessage = msg;
				forceDeleteTag = tag;
			} else {
				notifications.error(msg);
			}
		} finally {
			busy = false;
		}
	}

	async function confirmForceDelete(): Promise<void> {
		if (forceDeleteTag === null) return;
		const tag = forceDeleteTag;
		busy = true;
		try {
			await api.singboxRouterDeleteRuleSet(tag, true);
			forceDeleteTag = null;
			forceDeleteMessage = '';
			await refresh();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			busy = false;
		}
	}

	function sourceLabel(rs: SingboxRouterRuleSet): string {
		if (rs.type === 'inline') {
			return `${rs.rules?.length ?? 0} правил`;
		}
		if (rs.type === 'remote') return rs.url ?? '';
		return rs.path ?? '';
	}

	function transportLabel(rs: SingboxRouterRuleSet): string {
		if (rs.type !== 'remote') return '';
		return rs.download_detour ?? '';
	}
</script>

<div class="stat-row-wrap">
	<StatRow tiles={statTiles} columns={4} />
</div>

<div class="action-row">
	<div class="filter-chips" role="tablist" aria-label="Источник">
		<button
			type="button"
			role="tab"
			aria-selected={sourceFilter === 'all'}
			class:active={sourceFilter === 'all'}
			onclick={() => (sourceFilter = 'all')}
		>
			Все <span class="chip-count">{ruleSets.length}</span>
		</button>
		<button
			type="button"
			role="tab"
			aria-selected={sourceFilter === 'remote'}
			class:active={sourceFilter === 'remote'}
			onclick={() => (sourceFilter = 'remote')}
		>
			Remote <span class="chip-count">{remoteCount}</span>
		</button>
		<button
			type="button"
			role="tab"
			aria-selected={sourceFilter === 'local'}
			class:active={sourceFilter === 'local'}
			onclick={() => (sourceFilter = 'local')}
		>
			Local <span class="chip-count">{localCount}</span>
		</button>
		<button
			type="button"
			role="tab"
			aria-selected={sourceFilter === 'inline'}
			class:active={sourceFilter === 'inline'}
			onclick={() => (sourceFilter = 'inline')}
		>
			Inline <span class="chip-count">{inlineCount}</span>
		</button>
	</div>

	<div class="actions">
		<IconButton
			ariaLabel="Настройки автообновления"
			size="md"
			onclick={() => (refreshSettingsOpen = true)}
			disabled={!settings}
		>
			<svg
				viewBox="0 0 24 24"
				fill="none"
				stroke="currentColor"
				stroke-width="2"
				stroke-linecap="round"
				stroke-linejoin="round"
				aria-hidden="true"
			>
				<circle cx="12" cy="12" r="3" />
				<path
					d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"
				/>
			</svg>
		</IconButton>
		<Button variant="primary" size="sm" onclick={() => (addMode = true)}>+ Набор</Button>
	</div>
</div>

<div class="grid">
	{#if visibleRuleSets.length === 0}
		<div class="empty">
			{#if ruleSets.length === 0}
				Пусто. Нажмите "+ Набор" или примените пресет.
			{:else}
				Нет наборов с фильтром «{sourceFilter}».
			{/if}
		</div>
	{:else}
		{#each visibleRuleSets as rs (rs.tag)}
			<div class="card">
				<div class="card-head">
					<div class="tag mono" title={rs.tag}>{rs.tag}</div>
					<Badge
						variant={rs.type === 'remote' ? 'accent' : 'info'}
						size="sm"
						uppercase
						mono
					>
						{rs.type}
					</Badge>
				</div>

				<div class="meta-row">
					<div class="meta-label">Формат</div>
					<div class="meta-value mono">
						{rs.format}
					</div>
				</div>

				<div class="meta-row">
					<div class="meta-label">
						{rs.type === 'remote' ? 'URL' : rs.type === 'local' ? 'Путь' : 'Содержимое'}
					</div>
					<div class="meta-value mono src" title={sourceLabel(rs)}>
						{sourceLabel(rs) || '—'}
					</div>
				</div>

				{#if rs.type === 'remote'}
					<div class="meta-row">
						<div class="meta-label">Интервал</div>
						<div class="meta-value mono">{rs.update_interval ?? '—'}</div>
					</div>

					<div class="meta-row">
						<div class="meta-label">HTTP client</div>
						<div class="meta-value mono">
							{#if transportLabel(rs)}
								<Badge variant="muted" size="sm" mono>{transportLabel(rs)}</Badge>
							{:else}
								<span class="dim">автоматически</span>
							{/if}
						</div>
					</div>
				{/if}

				<div class="card-actions">
					<Button
						variant="ghost"
						size="sm"
						onclick={() => (editTag = rs.tag)}
					>
						Изменить
					</Button>
					{#if rs.type === 'remote'}
						<Button
							variant="ghost"
							size="sm"
							onclick={() => refreshOne(rs.tag)}
							disabled={refreshing.has(rs.tag)}
						>
							{refreshing.has(rs.tag) ? '...' : 'Обновить'}
						</Button>
					{/if}
					<IconButton
						ariaLabel="Удалить"
						size="sm"
						variant="danger"
						onclick={() => requestDelete(rs.tag)}
					>
						<svg
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
							stroke-linecap="round"
							stroke-linejoin="round"
							aria-hidden="true"
						>
							<path
								d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"
							/>
						</svg>
					</IconButton>
				</div>
			</div>
		{/each}
	{/if}
</div>

{#if addMode}
	<RuleSetAddModal
		{outboundOptions}
		onClose={() => (addMode = false)}
		onSave={async (rs) => {
			await api.singboxRouterAddRuleSet(rs);
			addMode = false;
			await refresh();
		}}
	/>
{/if}

{#if editTag !== null}
	{@const current = ruleSets.find((rs) => rs.tag === editTag)}
	{#if current}
		<RuleSetAddModal
			ruleSet={current}
			{outboundOptions}
			onClose={() => (editTag = null)}
			onSave={async (rs) => {
				await api.singboxRouterUpdateRuleSet(current.tag, rs);
				editTag = null;
				await refresh();
			}}
		/>
	{/if}
{/if}

{#if refreshSettingsOpen && settings}
	<RefreshSettingsModal
		{settings}
		onClose={() => (refreshSettingsOpen = false)}
		onSaved={refresh}
	/>
{/if}

<ConfirmModal
	open={deleteTag !== null}
	title="Удалить rule set"
	message={deleteTag !== null ? `Удалить rule set «${deleteTag}»?` : ''}
	{busy}
	onConfirm={confirmDelete}
	onClose={() => {
		if (!busy) deleteTag = null;
	}}
/>

<ConfirmModal
	open={forceDeleteTag !== null}
	title="Удалить с потерей ссылок?"
	message={forceDeleteMessage}
	secondary="Удалить всё равно? Правила, ссылающиеся на этот rule set, станут orphan."
	confirmLabel="Удалить принудительно"
	{busy}
	onConfirm={confirmForceDelete}
	onClose={() => {
		if (!busy) {
			forceDeleteTag = null;
			forceDeleteMessage = '';
		}
	}}
/>

<style>
	.stat-row-wrap {
		margin-bottom: 1rem;
	}
	.action-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.875rem;
		flex-wrap: wrap;
	}
	.filter-chips {
		display: inline-flex;
		gap: 0.25rem;
		padding: 0.2rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
	}
	.filter-chips button {
		background: transparent;
		border: none;
		padding: 0.35rem 0.75rem;
		border-radius: calc(var(--radius) - 2px);
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		transition: background 0.15s ease, color 0.15s ease;
	}
	.filter-chips button:hover {
		color: var(--color-text-primary);
	}
	.filter-chips button.active {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-weight: 600;
	}
	.chip-count {
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.7rem;
		color: var(--color-text-muted);
		background: var(--color-bg-tertiary);
		padding: 0.05rem 0.4rem;
		border-radius: 999px;
	}
	.filter-chips button.active .chip-count {
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
	}
	.actions {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
		gap: 0.75rem;
	}
	.card {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
	}
	.card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.tag {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.meta-row {
		display: grid;
		grid-template-columns: 80px 1fr;
		gap: 0.5rem;
		align-items: center;
		font-size: 0.8125rem;
	}
	.meta-label {
		color: var(--color-text-muted);
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}
	.meta-value {
		color: var(--color-text-secondary);
		min-width: 0;
	}
	.src {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.dim {
		color: var(--color-text-muted);
		font-style: italic;
	}
	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	.card-actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.4rem;
		margin-top: 0.25rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.empty {
		grid-column: 1 / -1;
		padding: 1.5rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.875rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
	}
</style>
