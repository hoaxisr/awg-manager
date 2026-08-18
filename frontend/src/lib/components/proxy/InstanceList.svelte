<script lang="ts">
	import { Badge, Button, StatusDot, Toggle } from '$lib/components/ui';
	import type { StatusDotVariant } from '$lib/components/ui';
	import { Pencil, Plus, X } from 'lucide-svelte';
	import { formatUptime } from '$lib/components/freeturn/uptime';
	import type { ProxyInstanceRow } from './rows';

	interface Props {
		/** LS-01 / LS-02 — подставляет страница. */
		title: string;
		rows: ProxyInstanceRow[];
		selectedKey: string | null;
		/** LS-03 / LS-04. */
		addLabel: string;
		/** LS-13 / LS-14 — фраза пустого списка. */
		emptyText: string;
		/** Ключи строк, у которых идёт мутация: тумблер на время запроса заперт. */
		busyKeys?: string[];
		onselect: (row: ProxyInstanceRow) => void;
		onadd: () => void;
		ontoggle: (row: ProxyInstanceRow, on: boolean) => void;
		onrename: (row: ProxyInstanceRow, name: string) => void;
		ondelete: (row: ProxyInstanceRow) => void;
	}

	let {
		title,
		rows,
		selectedKey,
		addLabel,
		emptyText,
		busyKeys = [],
		onselect,
		onadd,
		ontoggle,
		onrename,
		ondelete,
	}: Props = $props();

	let renamingKey = $state<string | null>(null);
	let renameDraft = $state('');
	let renameInput = $state<HTMLInputElement | undefined>();

	function startRename(row: ProxyInstanceRow) {
		renamingKey = row.key;
		renameDraft = row.name;
		queueMicrotask(() => renameInput?.focus());
	}

	function commitRename(row: ProxyInstanceRow) {
		const name = renameDraft.trim();
		renamingKey = null;
		if (name && name !== row.name) onrename(row, name);
	}

	function toggleRename(row: ProxyInstanceRow) {
		if (renamingKey === row.key) commitRename(row);
		else startRename(row);
	}

	function dotVariant(row: ProxyInstanceRow): StatusDotVariant {
		if (row.orphanedPid) return 'warning';
		if (row.state === 'running') return 'success';
		return row.state === 'error' ? 'error' : 'muted';
	}

	// LS-05..LS-09. Осиротевший процесс проверяется первым: startedAt у него нет,
	// поэтому строка «запущен · …» вышла бы без аптайма.
	//
	// Мета отдаётся частями, а не готовой строкой: в рейле 300px одна строка с
	// эллипсисом съедала хвост («PID 15…»), а по частям он переносится целиком.
	function meta(row: ProxyInstanceRow): string[] {
		if (row.orphanedPid) return ['устаревший процесс'];
		if (row.state === 'running') {
			return ['запущен', formatUptime(row.startedAt), row.pid ? `PID ${row.pid}` : ''].filter(
				Boolean,
			);
		}
		if (row.state === 'error') return ['не запускается'];
		return row.autostart ? ['автоподключение', 'остановлен'] : ['остановлен'];
	}

	// EX-55 / SH-83 — та же строка, что в модалке удаления: у раздачи своя.
	function deleteLabel(row: ProxyInstanceRow): string {
		return row.role === 'server'
			? `Удалить раздачу «${row.name}»?`
			: `Удалить инстанс «${row.name}»?`;
	}

	// LS-10..LS-12. У WDTT режим есть всегда: connMode клиента / relayMode сервера.
	function protocolBadge(row: ProxyInstanceRow): string {
		if (row.protocol === 'freeturn') return 'FreeTurn';
		return row.mode === 'raw' ? 'WDTT · Raw' : 'WDTT · WG';
	}
</script>

<div class="list">
	<p class="list-title">{title}</p>

	{#if rows.length === 0}
		<p class="list-empty">{emptyText}</p>
	{:else}
		<div class="rows">
			{#each rows as row (row.key)}
				<div class="row" class:selected={row.key === selectedKey}>
					<button type="button" class="row-main" onclick={() => onselect(row)}>
						<span class="row-top">
							<StatusDot variant={dotVariant(row)} size="sm" pulse={row.state === 'running'} />
							{#if renamingKey !== row.key}
								<span class="row-name">{row.name}</span>
								<Badge size="xs" variant={row.protocol === 'wdtt' ? 'accent' : 'purple'}>
									{protocolBadge(row)}
								</Badge>
							{/if}
						</span>
						{#if renamingKey !== row.key}
							<span class="row-meta">
								{#each meta(row) as part, i (part)}
									{#if i > 0}<span class="row-meta-sep">·</span>{/if}
									<span class="row-meta-part">{part}</span>
								{/each}
							</span>
						{/if}
					</button>

					{#if renamingKey === row.key}
						<input
							bind:this={renameInput}
							class="row-rename-input"
							aria-label="Имя инстанса"
							placeholder="Имя инстанса"
							bind:value={renameDraft}
							onkeydown={(e) => {
								if (e.key === 'Enter') commitRename(row);
								if (e.key === 'Escape') renamingKey = null;
							}}
							onblur={() => commitRename(row)}
						/>
					{/if}

					<div class="row-actions">
						<span
							class="row-toggle"
							title={row.binaryPresent ? undefined : 'Установите бинарь, чтобы запустить инстанс'}
						>
							<Toggle
								checked={row.state === 'running'}
								onchange={(on) => ontoggle(row, on)}
								disabled={!row.binaryPresent || busyKeys.includes(row.key)}
								controlled
								size="sm"
								label=""
								ariaLabel="{row.state === 'running' ? 'Остановить' : 'Запустить'} {row.name}"
							/>
						</span>
						<button
							type="button"
							class="row-action"
							class:active={renamingKey === row.key}
							aria-label="Имя инстанса"
							title="Имя инстанса"
							onmousedown={(e) => {
								if (renamingKey === row.key) e.preventDefault();
							}}
							onclick={() => toggleRename(row)}
						>
							<Pencil size={14} />
						</button>
						<button
							type="button"
							class="row-action danger"
							aria-label={deleteLabel(row)}
							title={deleteLabel(row)}
							onclick={() => ondelete(row)}
						>
							<X size={14} />
						</button>
					</div>
				</div>
			{/each}
		</div>
	{/if}

	<Button variant="primary" fullWidth onclick={onadd}>
		{#snippet iconBefore()}<Plus size={15} strokeWidth={2.5} />{/snippet}
		{addLabel}
	</Button>
</div>

<style>
	.list {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		min-width: 0;
	}

	.list-title {
		margin: 0;
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.list-empty {
		margin: 0;
		padding: 1rem 0.75rem;
		text-align: center;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
	}

	.rows {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.row {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.5rem 0.5rem 0.5rem 0.75rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		min-width: 0;
	}

	.row:hover {
		background: var(--color-bg-hover);
	}

	.row.selected {
		border-color: var(--color-accent);
		background: var(--color-bg-hover);
	}

	.row-main {
		display: flex;
		flex-direction: column;
		align-items: stretch;
		gap: 0.1875rem;
		flex: 1;
		min-width: 0;
		padding: 0;
		background: none;
		border: none;
		color: inherit;
		cursor: pointer;
		text-align: left;
	}

	.row-top {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}

	.row-name {
		flex: 1;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.row-meta {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 0 0.3rem;
		font-size: 0.6875rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
	}

	.row-meta-part,
	.row-meta-sep {
		white-space: nowrap;
	}

	.row-rename-input {
		flex: 2;
		min-width: 0;
		font-size: 0.8125rem;
		padding: 0.25rem 0.5rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
	}

	.row-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.125rem;
		flex-shrink: 0;
	}

	.row-toggle {
		display: inline-flex;
		align-items: center;
	}

	.row-action {
		display: inline-flex;
		align-items: center;
		background: none;
		border: none;
		color: var(--color-text-secondary);
		cursor: pointer;
		padding: 0.1875rem 0.3125rem;
		line-height: 1;
		border-radius: var(--radius-sm);
	}

	/* lucide-svelte v1 рисует <svg> внутри компонента — правило контейнер-scoped. */
	.row-action :global(svg) {
		display: block;
	}

	.row-action:hover {
		color: var(--color-text-primary);
	}

	.row-action.active {
		color: var(--color-accent);
		background: color-mix(in srgb, var(--color-accent) 14%, transparent);
	}

	.row-action.danger:hover {
		color: var(--color-error);
	}

</style>
