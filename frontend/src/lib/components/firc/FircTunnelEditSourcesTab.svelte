<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircTunnel, FircTunnelState } from '$lib/types/firc';
	import { formatFircSubError } from '$lib/utils/fircDiagnostics';
	import { Button, Badge, IconButton } from '$lib/components/ui';
	import { Plus, Trash2 } from 'lucide-svelte';
	import { SUB_INTERVALS, formatSubUsage, parseHeadersTextToMap } from './fircTunnelEdit';

	interface Props {
		draft: FircTunnel;
		liveState: FircTunnelState | undefined;
		happKeysCount: number;
		includeWarning: string;
		excludeWarning: string;
		onaddsource: (kind: 'subscription' | 'link') => void;
		onhappkeys: () => void;
		onremovesource: (id: string) => void;
	}

	let {
		draft = $bindable(),
		liveState,
		happKeysCount,
		includeWarning,
		excludeWarning,
		onaddsource,
		onhappkeys,
		onremovesource,
	}: Props = $props();

	// Заголовки подписки правятся в строке источника: раскрытое поле держит
	// свой текст, чтобы разбор на каждом вводе не переформатировал набранное.
	let headersOpenId = $state<string | null>(null);
	let headersText = $state('');

	function toggleHeaders(src: FircTunnel['sources'][number]) {
		if (headersOpenId === src.id) {
			headersOpenId = null;
			return;
		}
		headersText = Object.entries(src.headers ?? {})
			.map(([k, v]) => `${k}: ${v}`)
			.join('\n');
		headersOpenId = src.id;
	}
</script>

<div class="subtab-panel">
	<div class="sources-header">
		<span class="hint">
			{m.firc_tunnel_edit_sources_hint()}
		</span>
		<div class="sources-actions">
			<Button
				variant="secondary"
				size="sm"
				onclick={() => onaddsource('subscription')}
			>
				{#snippet iconBefore()}<Plus size={13} />{/snippet}
				{m.firc_tunnel_edit_add_subscription()}
			</Button>
			<Button
				variant="secondary"
				size="sm"
				onclick={() => onaddsource('link')}
			>
				{#snippet iconBefore()}<Plus size={13} />{/snippet}
				{m.firc_tunnel_edit_add_link()}
			</Button>
			<Button
				variant="ghost"
				size="sm"
				onclick={onhappkeys}
			>
				{happKeysCount > 0
					? m.firc_tunnel_edit_happ_keys_btn_count({ count: happKeysCount })
					: m.firc_tunnel_edit_happ_keys_btn()}
			</Button>
		</div>
	</div>

	{#if draft.sources.length === 0}
		<div class="sub-empty">{m.firc_tunnel_edit_sources_empty()}</div>
	{:else}
		<div class="sources-list">
			{#each draft.sources as src (src.id)}
				{@const subLive =
					src.kind === 'subscription'
						? (liveState?.subscriptions?.find((s) => s.name === src.name) ??
							(liveState?.subscriptions?.length === 1
								? liveState.subscriptions[0]
								: undefined))
						: undefined}
				<div class="source-row">
					<div class="source-main">
						<div class="source-title-line">
							<Badge
								variant={src.kind === 'subscription' ? 'accent' : 'default'}
								size="xs"
							>
								{src.kind === 'subscription'
									? m.firc_tunnel_edit_kind_subscription()
									: m.firc_tunnel_edit_kind_link()}
							</Badge>
							<input
								class="inp compact-inp"
								type="text"
								bind:value={src.name}
								placeholder={m.firc_tunnel_edit_source_name_placeholder()}
							/>
							{#if src.kind === 'subscription'}
								<button
									type="button"
									class="badge-btn"
									aria-expanded={headersOpenId === src.id}
									onclick={() => toggleHeaders(src)}
								>
									<Badge variant="info" size="xs">
										{m.firc_tunnel_edit_headers_badge({ count: Object.keys(src.headers ?? {}).length })}
									</Badge>
								</button>
							{/if}
							{#if src.kind === 'subscription'}
								{@const usageText = formatSubUsage(subLive?.usage ?? undefined)}
								{#if subLive?.fetching}
									<Badge variant="info" size="xs">{m.firc_sub_fetching_badge()}</Badge>
								{/if}
								{#if subLive && subLive.nodes > 0}
									<Badge variant="default" size="xs" mono>{m.firc_sub_nodes_badge({ count: subLive.nodes })}</Badge>
								{/if}
								{#if usageText}
									<Badge variant="default" size="xs" mono title={m.firc_tunnel_edit_usage_title()}>
										{usageText}
									</Badge>
								{/if}
							{/if}
						</div>
						{#if src.kind === 'subscription'}
							<input
								class="inp mono compact-inp"
								type="text"
								bind:value={src.url}
								placeholder={m.firc_tunnel_edit_sub_url_placeholder()}
							/>
							{#if headersOpenId === src.id}
								<textarea
									class="inp mono"
									rows="3"
									aria-label={m.firc_tunnel_edit_headers_label()}
									placeholder="User-Agent: Happ/3.6.1&#10;X-HWID: ..."
									bind:value={headersText}
									oninput={() => (src.headers = parseHeadersTextToMap(headersText))}
								></textarea>
							{/if}
							{#if subLive?.error}
								<div
									class="source-error-note"
									role="alert"
									title={subLive.error}
									data-testid="firc-source-error"
								>
									{formatFircSubError(subLive.error)}
								</div>
							{/if}
						{:else}
							<input
								class="inp mono compact-inp"
								type="text"
								bind:value={src.link}
								placeholder="vless://... | vmess://... | hy2://... | anytls://... | mieru://... | tt://..."
							/>
						{/if}
					</div>
					<div class="source-side">
						{#if src.kind === 'subscription'}
							<select
								class="inp compact-select"
								value={src.interval ?? 21600}
								onchange={(e) => {
									src.interval = Number((e.currentTarget as HTMLSelectElement).value);
								}}
							>
								{#each SUB_INTERVALS as opt}
									<option value={opt.value}>{opt.label()}</option>
								{/each}
							</select>
						{/if}
						<IconButton
							variant="danger"
							title={m.firc_tunnel_edit_remove_source()}
							ariaLabel={m.firc_tunnel_edit_remove_source()}
							onclick={() => onremovesource(src.id)}
						>
							<Trash2 size={14} />
						</IconButton>
					</div>
				</div>
			{/each}
		</div>
	{/if}

	<div class="filter-section">
		<div class="filter-section-title">{m.firc_filter_section_title()}</div>
		<div class="field-grid-2">
			<div class="field">
				<label class="lbl" for="firc-filter-include">{m.firc_filter_include_label()}</label>
				<input
					id="firc-filter-include"
					class="inp mono"
					type="text"
					bind:value={draft.filter}
					placeholder="DE|NL|🇩🇪"
					autocomplete="off"
					spellcheck="false"
				/>
				{#if includeWarning}
					<span class="filter-warn">{includeWarning}</span>
				{/if}
			</div>
			<div class="field">
				<label class="lbl" for="firc-filter-exclude">{m.firc_filter_exclude_label()}</label>
				<input
					id="firc-filter-exclude"
					class="inp mono"
					type="text"
					bind:value={draft.filterExclude}
					placeholder={m.firc_filter_exclude_example()}
					autocomplete="off"
					spellcheck="false"
				/>
				{#if excludeWarning}
					<span class="filter-warn">{excludeWarning}</span>
				{/if}
			</div>
		</div>
		<div class="filter-hint">
			{m.firc_filter_hint_before()}<code class="mono">(?!...)</code>{m.firc_filter_hint_mid()}<code class="mono">{m.firc_filter_exclude_example()}</code>{m.firc_filter_hint_after()}
		</div>
	</div>
</div>

<style>
	.subtab-panel {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.sources-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}
	.sources-actions {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}
	.sources-list {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.source-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 8px 10px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
	}
	.source-main {
		display: flex;
		flex-direction: column;
		gap: 6px;
		flex: 1;
		min-width: 0;
	}
	.source-title-line {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.source-side {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.field-grid-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.lbl {
		font-size: 11.5px;
		color: var(--text-secondary);
		font-weight: 500;
	}
	.hint {
		font-size: 11px;
		color: var(--text-muted);
	}
	.inp {
		padding: 5px 8px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		box-sizing: border-box;
	}
	.badge-btn {
		padding: 0;
		border: 0;
		background: none;
		cursor: pointer;
	}
	select.inp {
		padding-right: 1.75rem;
		text-overflow: ellipsis;
	}
	.compact-select {
		width: auto;
	}
	.mono {
		font-family: var(--font-mono);
	}
	.sub-empty {
		padding: 20px;
		text-align: center;
		color: var(--text-muted);
		font-size: 12px;
		border: 1px dashed var(--border);
		border-radius: 8px;
	}
	.source-error-note {
		padding: 6px 8px;
		border-radius: 6px;
		border: 1px solid var(--color-error-border, rgba(247, 118, 142, 0.4));
		background: var(--color-error-tint, rgba(247, 118, 142, 0.14));
		color: var(--color-error, #f7768e);
		font-size: 11.5px;
		line-height: 1.35;
		overflow-wrap: anywhere;
	}
	.filter-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
	}
	.filter-section-title {
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}
	.filter-warn {
		font-size: 11px;
		color: var(--color-warning, #e0af68);
	}
	.filter-hint {
		font-size: 11px;
		color: var(--text-muted);
		line-height: 1.45;
	}
</style>
