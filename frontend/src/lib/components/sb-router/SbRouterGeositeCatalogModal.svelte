<!--
  Каталог SagerNet sing-geosite: полный список geosite-наборов из ветки
  rule-set (~1.5 тыс.) с поиском и добавлением remote rule-set'ов в один
  клик. Дополнение к курируемому каталогу пресетов, не замена: здесь нет
  описаний и иконок, зато есть всё, что публикует SagerNet.
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Badge, Button, Modal } from '$lib/components/ui';
	import { RefreshCw } from 'lucide-svelte';
	import type { SingboxGeositesData } from '$lib/types';

	interface Props {
		open: boolean;
		existingRuleSetTags: string[];
		submitting?: boolean;
		onclose: () => void;
		onconfirm: (names: string[], baseUrl: string) => void;
	}

	let { open = false, existingRuleSetTags, submitting = false, onclose, onconfirm }: Props = $props();

	// Больше строк за раз рендерить незачем: поиск сужает список быстрее,
	// чем его можно проскроллить.
	const RENDER_CAP = 300;

	let catalog = $state<SingboxGeositesData | null>(null);
	let loading = $state(false);
	let loadError = $state('');
	let query = $state('');
	let selected = $state(new Set<string>());

	const existingTagSet = $derived(new Set(existingRuleSetTags));

	const filtered = $derived.by(() => {
		const names = catalog?.names ?? [];
		const q = query.trim().toLowerCase();
		if (!q) return names;
		return names.filter((n) => n.toLowerCase().includes(q));
	});
	const rendered = $derived(filtered.slice(0, RENDER_CAP));

	async function load(refresh = false): Promise<void> {
		loading = true;
		loadError = '';
		try {
			const fresh = await api.singboxRouterListGeosites(refresh);
			catalog = fresh;
			if (refresh && fresh.stale) {
				// Бэкенд не смог обновиться и отдал сохранённую копию.
				notifications.warning(m.sb_router_catalog_stale());
			}
		} catch (e) {
			const msg = e instanceof Error ? e.message : String(e);
			if (catalog) {
				// Неудачное обновление не должно прятать уже загруженный список.
				notifications.error(m.sb_router_catalog_refresh_failed({ message: msg }));
			} else {
				loadError = msg;
			}
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		if (open && !catalog && !loading && !loadError) void load();
	});

	$effect(() => {
		if (!open) {
			// Выбор, поиск и ошибка не переживают закрытие (повторное открытие
			// снова пробует загрузку); загруженный список — да.
			selected = new Set();
			query = '';
			loadError = '';
		}
	});

	$effect(() => {
		// Успешно добавленные (частичный сбой оставляет модалку открытой)
		// выбывают из выбора: иначе счётчик врёт, а повторный confirm
		// рапортует «уже есть в конфиге» про только что добавленные.
		const remaining = [...selected].filter((n) => !existingTagSet.has(`geosite-${n}`));
		if (remaining.length !== selected.size) selected = new Set(remaining);
	});

	function isAdded(name: string): boolean {
		return existingTagSet.has(`geosite-${name}`);
	}

	function toggle(name: string): void {
		if (isAdded(name)) return;
		const next = new Set(selected);
		if (next.has(name)) next.delete(name);
		else next.add(name);
		selected = next;
	}

	function confirm(): void {
		if (submitting || selected.size === 0 || !catalog) return;
		onconfirm([...selected], catalog.baseUrl);
	}
</script>

<Modal {open} title={m.sb_router_catalog_geosite_title()} size="lg" bodyLayout="fill" {onclose}>
	<div class="geosite-body">
		<div class="toolbar">
			<input
				type="search"
				class="search"
				placeholder={m.sb_router_catalog_geosite_search({ count: catalog?.names.length ?? 0 })}
				bind:value={query}
				disabled={loading || !!loadError}
			/>
			<Button
				variant="ghost"
				size="sm"
				onclick={() => void load(true)}
				disabled={loading}
				title={m.sb_router_catalog_refresh_title()}
			>
				<RefreshCw size={14} aria-hidden="true" />
			</Button>
		</div>

		{#if loading}
			<div class="state">{m.sb_router_catalog_loading()}</div>
		{:else if loadError}
			<div class="state state-error">
				<div>{loadError}</div>
				<Button variant="secondary" size="sm" onclick={() => void load()}>{m.sb_router_catalog_retry()}</Button>
			</div>
		{:else if filtered.length === 0}
			<div class="state">{m.sb_router_catalog_nothing_found({ query })}</div>
		{:else}
			<div class="list">
				{#each rendered as name (name)}
					{@const added = isAdded(name)}
					<button
						type="button"
						class="row"
						class:selected={selected.has(name)}
						class:added
						aria-pressed={selected.has(name)}
						disabled={added}
						onclick={() => toggle(name)}
					>
						<span class="row-name">{name}</span>
						{#if added}
							<Badge variant="default" size="sm">{m.sb_router_catalog_added()}</Badge>
						{:else if selected.has(name)}
							<span class="row-check" aria-hidden="true">✓</span>
						{/if}
					</button>
				{/each}
			</div>
			{#if filtered.length > RENDER_CAP}
				<div class="cap-hint">
					{m.sb_router_catalog_cap_hint({ shown: RENDER_CAP, total: filtered.length })}
				</div>
			{/if}
		{/if}

		<p class="hint">
			{m.sb_router_catalog_geosite_hint_pre()} <code>{m.sb_router_catalog_geosite_code()}</code> {m.sb_router_catalog_geosite_hint_post()}
		</p>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={onclose} disabled={submitting}>{m.common_cancel()}</Button>
		<Button
			variant="primary"
			onclick={confirm}
			disabled={submitting || selected.size === 0}
			loading={submitting}
		>
			{selected.size > 0 ? m.sb_router_catalog_add_sets_count({ count: selected.size }) : m.sb_router_catalog_add_sets()}
		</Button>
	{/snippet}
</Modal>

<style>
	.geosite-body {
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
		min-height: 0;
		height: 100%;
		padding: 0.25rem 0;
	}
	.toolbar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.search {
		flex: 1;
		min-width: 0;
		padding: 0.45rem 0.7rem;
		background: var(--color-bg-tertiary, var(--bg-tertiary));
		border: 1px solid var(--color-border, var(--border));
		border-radius: var(--radius-sm, 8px);
		color: var(--text-primary);
		font-size: 0.875rem;
	}
	.list {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		display: flex;
		flex-direction: column;
		border: 1px solid var(--color-border, var(--border));
		border-radius: var(--radius-sm, 8px);
	}
	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.4rem 0.75rem;
		background: transparent;
		border: 0;
		border-bottom: 1px solid color-mix(in srgb, var(--color-border, var(--border)) 55%, transparent);
		color: var(--text-primary);
		cursor: pointer;
		text-align: left;
	}
	.row:last-child {
		border-bottom: 0;
	}
	@media (hover: hover) {
		.row:hover:not(:disabled) {
			background: color-mix(in srgb, var(--bg-hover) 70%, transparent);
		}
	}
	.row.selected {
		background: color-mix(in srgb, var(--color-accent, var(--accent)) 12%, transparent);
	}
	.row.added {
		cursor: default;
		color: var(--text-muted);
	}
	.row-name {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.row-check {
		color: var(--color-accent, var(--accent));
		font-weight: 700;
		flex-shrink: 0;
	}
	.state {
		padding: 1.5rem 0.5rem;
		color: var(--text-muted);
		font-size: 0.875rem;
		text-align: center;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.75rem;
	}
	.state-error {
		color: var(--color-error, #e06a5a);
		overflow-wrap: anywhere;
	}
	.cap-hint {
		font-size: 0.75rem;
		color: var(--text-muted);
	}
	.hint {
		margin: 0;
		font-size: 0.75rem;
		color: var(--text-muted);
		line-height: 1.4;
	}
	.hint code {
		font-family: var(--font-mono);
	}
</style>
