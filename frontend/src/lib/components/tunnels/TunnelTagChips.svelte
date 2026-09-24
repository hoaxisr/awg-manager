<script lang="ts">
	interface Props {
		tags: string[];
		onAdd: (raw: string) => void;
		onRemove: (tag: string) => void;
		onSelect?: (tag: string) => void;
		activeTag?: string | null;
		/** Только чипы, без удаления и добавления. */
		readonly?: boolean;
	}

	let { tags, onAdd, onRemove, onSelect, activeTag = null, readonly = false }: Props = $props();

	let adding = $state(false);
	let draft = $state('');
	let inputEl = $state<HTMLInputElement | null>(null);

	$effect(() => {
		if (adding) inputEl?.focus();
	});

	function commitDraft() {
		const raw = draft.trim();
		if (raw) onAdd(raw);
		draft = '';
	}

	function handleInputKeydown(event: KeyboardEvent) {
		event.stopPropagation();
		if (event.key === 'Enter') {
			event.preventDefault();
			commitDraft();
		} else if (event.key === 'Escape') {
			event.preventDefault();
			draft = '';
			adding = false;
		}
	}

	function handleInputBlur() {
		// Blur коммитит непустой черновик (на тач-устройствах Enter недоступен).
		commitDraft();
		adding = false;
	}
</script>

<div class="tunnel-tag-chips">
	{#each tags as tag (tag)}
		<span class="tag-chip" class:active={tag === activeTag}>
			<button
				type="button"
				class="chip-label"
				title={onSelect ? `Фильтр по тегу «${tag}»` : tag}
				onclick={(event) => {
					event.stopPropagation();
					onSelect?.(tag);
				}}
			>
				{tag}
			</button>
			{#if !readonly}
				<button
					type="button"
					class="chip-remove"
					aria-label="Удалить тег «{tag}»"
					onclick={(event) => {
						event.stopPropagation();
						onRemove(tag);
					}}
				>
					&times;
				</button>
			{/if}
		</span>
	{/each}

	{#if !readonly}
		{#if adding}
			<input
				bind:this={inputEl}
				bind:value={draft}
				class="chip-input"
				type="text"
				placeholder="тег"
				maxlength="24"
				aria-label="Новый тег"
				onclick={(event) => event.stopPropagation()}
				onkeydown={handleInputKeydown}
				onblur={handleInputBlur}
			/>
		{:else}
			<button
				type="button"
				class="chip-add"
				aria-label="Добавить тег"
				onclick={(event) => {
					event.stopPropagation();
					adding = true;
				}}
			>
				+
			</button>
		{/if}
	{/if}
</div>

<style>
	.tunnel-tag-chips {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 3px;
		min-width: 0;
	}

	/* Не .chip: имя занято утилитой Skeleton и app.css — их свойства протекали сюда. */
	.tag-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.25rem 0.625rem;
		font-family: var(--font-mono);
		font-size: 11px;
		font-weight: 600;
		line-height: calc(1 / 0.75);
		color: var(--color-text-muted);
		white-space: nowrap;
		max-width: 100%;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		overflow: hidden;
		transition: background var(--t-fast) ease, color var(--t-fast) ease, border-color var(--t-fast) ease;
	}

	.tag-chip:hover {
		color: var(--color-text-primary);
		border-color: var(--color-border-hover);
	}

	.tag-chip.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.chip-label {
		border: none;
		background: transparent;
		padding: 1px 2px 1px 6px;
		font-size: 0.6875rem;
		line-height: 1.3;
		font-weight: 500;
		font-family: inherit;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
		cursor: pointer;
	}

	.tag-chip.active .chip-label {
		color: var(--color-accent);
	}

	.chip-label:hover {
		color: var(--color-text-primary);
	}

	/* readonly: без крестика справа — симметричный отступ */
	.chip-label:last-child {
		padding-right: 6px;
	}

	.chip-remove {
		border: none;
		background: transparent;
		padding: 0 5px 0 2px;
		font-size: 0.6875rem;
		line-height: 1.3;
		font-family: inherit;
		color: var(--color-text-muted);
		opacity: 0.7;
		cursor: pointer;
		flex-shrink: 0;
	}

	.chip-remove:hover {
		opacity: 1;
		color: var(--color-error);
	}

	.chip-add {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 18px;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		background: transparent;
		padding: 1px 0;
		font-size: 0.6875rem;
		line-height: 1.3;
		font-family: inherit;
		color: var(--color-text-muted);
		opacity: 0.7;
		cursor: pointer;
	}

	.chip-add:hover {
		opacity: 1;
		color: var(--color-text-primary);
		border-color: var(--color-text-muted);
	}

	.chip-input {
		width: 5.5rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		padding: 1px 6px;
		font-size: 0.6875rem;
		line-height: 1.3;
		font-family: inherit;
		color: var(--color-text-primary);
	}

	.chip-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.chip-input::placeholder {
		color: var(--color-text-muted);
		opacity: 0.7;
	}
</style>
