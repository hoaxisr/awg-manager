<script lang="ts">
	import { formatLocale, m } from '$lib/i18n';
	import type { ZapretHostlistContent, ZapretHostlistId, ZapretHostlistInfo } from '$lib/types';
	import { Button } from '$lib/components/ui';
	import { TriangleAlert, Lock } from 'lucide-svelte';
	import { zapretWarningText } from './warnings';

	interface Props {
		info: ZapretHostlistInfo;
		content: string;
		loading: boolean;
		loadError: boolean;
		saving: boolean;
		onload: () => void;
		onsave: (content: string) => Promise<boolean>;
	}

	let { info, content, loading, loadError, saving, onload, onsave }: Props = $props();

	let draft = $state('');

	// Черновик инициализируется из содержимого с диска, но перезагрузка
	// НЕ должна затирать несохранённую правку молча. Поэтому синхронизируем
	// только когда правки нет — иначе пользователь потерял бы её, нажав
	// «Перечитать» или дождавшись фонового обновления.
	let touched = $state(false);
	$effect(() => {
		if (!touched) draft = content;
	});

	function edit(next: string) {
		touched = true;
		draft = next;
	}

	// Смена открытого списка — это новое содержимое, а не продолжение
	// прежней правки, поэтому сбрасываем флаг.
	$effect(() => {
		info.id;
		touched = false;
	});

	function reload() {
		touched = false;
		onload();
	}

	let lines = $derived(draft.split('\n').filter((l) => l.trim() !== '' && !l.trim().startsWith('#')));
	let dirtyNow = $derived(draft !== content);
	let empty = $derived(lines.length === 0);
	let displayNote = $derived.by(() => {
		switch (info.id) {
			case 'user': return m.zapret_hostlist_note_user();
			case 'exclude': return m.zapret_hostlist_note_exclude();
			case 'auto': return m.zapret_hostlist_note_auto();
			case 'main': return m.zapret_hostlist_note_main();
		}
	});
</script>

<div class="panel" class:generated={info.generated}>
	<div class="head">
		<div class="head-text">
			<h3>{displayNote || info.path}</h3>
			<code>{info.path}</code>
			{#if info.exists}
				<span class="stats">
					{m.zapret_hostlist_entries_count({ count: info.entries, formatted: info.entries.toLocaleString(formatLocale()) })}
					· {m.zapret_hostlist_domains_count({ count: info.domains, formatted: info.domains.toLocaleString(formatLocale()) })}
					{#if info.ips > 0}· {m.zapret_hostlist_addresses_count({ count: info.ips, formatted: info.ips.toLocaleString(formatLocale()) })}{/if}
					{#if info.gzipped}· {m.zapret_hostlist_gzipped()}{/if}
				</span>
			{:else}
				<span class="stats">{m.zapret_hostlist_file_missing()}</span>
			{/if}
		</div>
		{#if info.editable}
			<div class="actions">
				<Button variant="secondary" size="sm" onclick={reload} disabled={loading || saving}>
					{m.common_refresh()}
				</Button>
				<Button
					variant="primary"
					size="sm"
					loading={saving}
					disabled={loading || !dirtyNow}
					onclick={async () => {
 if (await onsave(draft)) touched = false;
 }}
				>
					{m.common_save()}
				</Button>
			</div>
		{/if}
	</div>

	{#if info.warnings?.length}
		<ul class="notes">
			{#each info.warnings as w, index (`${w.code}-${index}`)}
				<li>
					<TriangleAlert size={13} />
					<span>{zapretWarningText(w)}</span>
				</li>
			{/each}
		</ul>
	{/if}

	{#if info.editable}
		{#if loading}
			<p class="hint" role="status">{m.zapret_loading_hostlists()}</p>
		{:else if loadError}
			<div class="empty-state-rich" role="alert">
				<p>{m.zapret_error_read_hostlist()}</p>
				<Button variant="secondary" size="sm" onclick={reload}>{m.common_refresh()}</Button>
			</div>
		{:else}
			<textarea
				value={draft}
				oninput={(e) => edit(e.currentTarget.value)}
				disabled={saving}
				spellcheck="false"
				aria-label={info.path}
				placeholder={m.zapret_hostlist_placeholder()}
			></textarea>
			<div class="foot">
				<span class="count">{m.zapret_hostlist_lines_to_write({ count: lines.length, formatted: lines.length.toLocaleString(formatLocale()) })}</span>
				{#if dirtyNow}
					<span class="dirty">{m.zapret_hostlist_unsaved_changes()}</span>
				{/if}
		</div>
		{/if}
	{:else}
		<div class="locked">
			<Lock size={14} />
			<span>
				{m.zapret_hostlist_generated_hint()}
			</span>
		</div>
	{/if}

	{#if empty && info.editable && !loading && !loadError}
		<p class="hint">
			{m.zapret_hostlist_empty_hint()}
		</p>
	{/if}
</div>

<style>
	.panel {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-secondary);
	}

	.panel.generated {
		border-style: dashed;
	}

	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 10px;
	}

	.head-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	h3 {
		margin: 0;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	code {
		font-family: var(--font-mono);
		font-size: 0.625rem;
		color: var(--text-muted);
		word-break: break-all;
	}

	.stats {
		font-size: 0.6875rem;
		color: var(--text-secondary);
	}

	.actions {
		display: flex;
		gap: 6px;
		flex-shrink: 0;
	}

	.notes {
		display: flex;
		flex-direction: column;
		gap: 4px;
		margin: 0;
		padding: 6px 8px;
		list-style: none;
		border-radius: 6px;
		background: color-mix(in srgb, var(--warning, #f59e0b) 12%, transparent);
	}

	.notes li {
		display: flex;
		align-items: flex-start;
		gap: 5px;
		font-size: 0.6875rem;
		line-height: 1.35;
		color: var(--text-primary);
	}

	.notes li :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
		color: var(--warning, #f59e0b);
	}

	textarea {
		box-sizing: border-box;
		width: 100%;
		min-height: 11rem;
		resize: vertical;
		padding: 8px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		line-height: 1.5;
	}

	textarea:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: -1px;
	}

	.foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		font-size: 0.625rem;
		color: var(--text-muted);
	}

	.dirty {
		color: var(--warning, #f59e0b);
	}

	.locked {
		display: flex;
		align-items: flex-start;
		gap: 6px;
		padding: 8px;
		border-radius: 6px;
		background: var(--bg-primary);
		font-size: 0.6875rem;
		line-height: 1.4;
		color: var(--text-muted);
	}

	.locked :global(svg) {
		flex-shrink: 0;
		margin-top: 2px;
	}

	.hint {
		margin: 0;
		font-size: 0.6875rem;
		line-height: 1.4;
		color: var(--text-muted);
	}
</style>
