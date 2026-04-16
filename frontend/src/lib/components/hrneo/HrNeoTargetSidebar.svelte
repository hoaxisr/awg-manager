<script lang="ts" module>
	export type ServiceItem = 'geodata' | 'settings' | 'disabled-tags';

	export interface TargetEntry {
		name: string;
		kind: 'policy' | 'interface';
		ruleCount: number;
		broken?: boolean;
	}

	export type SidebarSelection =
		| { type: 'target'; name: string }
		| { type: 'service'; item: ServiceItem }
		| null;
</script>

<script lang="ts">
	interface Props {
		targets: TargetEntry[];
		selected: SidebarSelection;
		geoSiteCount: number;
		geoIPCount: number;
		oversizedCount: number;
		onselect: (sel: SidebarSelection) => void;
		onreorder: (order: string[]) => void;
		onnewrule: () => void;
	}

	let {
		targets,
		selected,
		geoSiteCount,
		geoIPCount,
		oversizedCount,
		onselect,
		onreorder,
		onnewrule,
	}: Props = $props();

	function move(idx: number, dir: -1 | 1) {
		const next = targets.map((t) => t.name);
		const j = idx + dir;
		if (j < 0 || j >= next.length) return;
		[next[idx], next[j]] = [next[j], next[idx]];
		onreorder(next);
	}

	function isTargetSelected(name: string): boolean {
		return selected?.type === 'target' && selected.name === name;
	}

	function isServiceSelected(item: ServiceItem): boolean {
		return selected?.type === 'service' && selected.item === item;
	}
</script>

<aside class="hr-sidebar">
	<div class="sidebar-section">
		<div class="section-label">Targets</div>

		{#if targets.length === 0}
			<div class="empty-hint">Пока нет правил</div>
		{/if}

		{#each targets as t, i (t.name)}
			<div
				role="button"
				tabindex="0"
				class="row"
				class:active={isTargetSelected(t.name)}
				class:broken={t.broken}
				onclick={() => onselect({ type: 'target', name: t.name })}
				onkeydown={(e) => {
					if (e.key === 'Enter' || e.key === ' ') {
						e.preventDefault();
						onselect({ type: 'target', name: t.name });
					}
				}}
			>
				<span class="row-num">{i + 1}</span>
				<div class="row-body">
					<div class="row-title">{t.name}</div>
					<div class="row-meta">
						<span class="kind kind-{t.kind}">{t.kind}</span>
						<span class="count">{t.ruleCount}</span>
					</div>
				</div>
				<div class="row-arrows">
					<button
						type="button"
						class="arrow-btn"
						disabled={i === 0}
						onclick={(e) => {
							e.stopPropagation();
							move(i, -1);
						}}
						aria-label="Move up"
					>
						<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="18 15 12 9 6 15" />
						</svg>
					</button>
					<button
						type="button"
						class="arrow-btn"
						disabled={i === targets.length - 1}
						onclick={(e) => {
							e.stopPropagation();
							move(i, 1);
						}}
						aria-label="Move down"
					>
						<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="6 9 12 15 18 9" />
						</svg>
					</button>
				</div>
			</div>
		{/each}

		<button type="button" class="add-row" onclick={onnewrule}>+ Новое правило</button>
	</div>

	<div class="sidebar-section">
		<div class="section-label">Служебное</div>

		<div
			role="button"
			tabindex="0"
			class="row svc"
			class:active={isServiceSelected('geodata')}
			onclick={() => onselect({ type: 'service', item: 'geodata' })}
			onkeydown={(e) => {
				if (e.key === 'Enter' || e.key === ' ') {
					e.preventDefault();
					onselect({ type: 'service', item: 'geodata' });
				}
			}}
		>
			<div class="row-body">
				<div class="row-title">Гео-данные</div>
				<div class="row-meta">
					<span class="kind kind-policy">geosite {geoSiteCount}</span>
					<span class="kind kind-interface">geoip {geoIPCount}</span>
				</div>
			</div>
		</div>

		<div
			role="button"
			tabindex="0"
			class="row svc"
			class:active={isServiceSelected('settings')}
			onclick={() => onselect({ type: 'service', item: 'settings' })}
			onkeydown={(e) => {
				if (e.key === 'Enter' || e.key === ' ') {
					e.preventDefault();
					onselect({ type: 'service', item: 'settings' });
				}
			}}
		>
			<div class="row-body">
				<div class="row-title">Настройки демона</div>
			</div>
		</div>

		{#if oversizedCount > 0}
			<div
				role="button"
				tabindex="0"
				class="row svc"
				class:active={isServiceSelected('disabled-tags')}
				onclick={() => onselect({ type: 'service', item: 'disabled-tags' })}
				onkeydown={(e) => {
					if (e.key === 'Enter' || e.key === ' ') {
						e.preventDefault();
						onselect({ type: 'service', item: 'disabled-tags' });
					}
				}}
			>
				<div class="row-body">
					<div class="row-title">Отключённые теги</div>
					<div class="row-meta">
						<span class="kind kind-warn">{oversizedCount}</span>
					</div>
				</div>
			</div>
		{/if}
	</div>
</aside>

<style>
	.hr-sidebar {
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding: 10px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.sidebar-section {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.section-label {
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
		padding: 4px 6px;
	}

	.empty-hint {
		color: var(--text-muted);
		font-style: italic;
		font-size: 0.8125rem;
		padding: 6px;
	}

	.row {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 10px;
		background: var(--bg-tertiary);
		border: 1px solid transparent;
		border-radius: 6px;
		cursor: pointer;
		transition: border-color 0.15s, background 0.15s;
	}
	.row:hover {
		border-color: var(--border-hover);
	}
	.row.active {
		background: var(--bg-hover);
		border-color: var(--accent);
	}
	.row.broken {
		border-color: var(--error);
	}

	.row-num {
		color: var(--accent);
		font-weight: 700;
		font-size: 0.8125rem;
		width: 18px;
		text-align: center;
		flex-shrink: 0;
	}

	.row-body {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.row-title {
		font-size: 0.875rem;
		color: var(--text-primary);
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.row-meta {
		display: flex;
		gap: 6px;
		align-items: center;
		font-size: 0.6875rem;
	}

	.kind {
		text-transform: uppercase;
		letter-spacing: 0.05em;
		font-weight: 600;
		padding: 1px 6px;
		border-radius: 8px;
	}
	.kind-policy {
		background: rgba(122, 162, 247, 0.15);
		color: var(--accent);
	}
	.kind-interface {
		background: rgba(125, 207, 255, 0.15);
		color: var(--info);
	}
	.kind-warn {
		background: rgba(224, 175, 104, 0.15);
		color: var(--warning);
	}

	.count {
		color: var(--text-muted);
	}

	.row-arrows {
		display: flex;
		flex-direction: column;
		flex-shrink: 0;
	}

	.arrow-btn {
		background: transparent;
		border: none;
		color: var(--text-muted);
		cursor: pointer;
		padding: 0 4px;
		line-height: 0;
		height: 12px;
	}
	.arrow-btn:hover:not(:disabled) {
		color: var(--accent);
	}
	.arrow-btn:disabled {
		opacity: 0.25;
		cursor: default;
	}

	.add-row {
		margin-top: 4px;
		padding: 8px;
		background: transparent;
		border: 1px dashed var(--border-hover);
		border-radius: 6px;
		color: var(--accent);
		font-family: inherit;
		font-size: 0.8125rem;
		cursor: pointer;
	}
	.add-row:hover {
		border-color: var(--accent);
		background: var(--bg-tertiary);
	}
</style>
