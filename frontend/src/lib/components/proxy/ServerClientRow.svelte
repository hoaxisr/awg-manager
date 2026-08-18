<script lang="ts">
	// Строка абонента: имя, укороченный пароль, бейджи SH-27..SH-31 и действия
	// по матрице спеки §4.4. Решает матрицу чистый модуль, строка её рисует.
	import { Pencil } from 'lucide-svelte';
	import { Badge, Button, FieldHint } from '$lib/components/ui';
	import type { WdttPanelUserEntry } from '$lib/types';
	import { rowActions, shortPassword } from './serverClients';

	interface Props {
		user: WdttPanelUserEntry;
		users: WdttPanelUserEntry[];
		busy?: boolean;
		onlink: (user: WdttPanelUserEntry) => void;
		onreissue: (user: WdttPanelUserEntry) => void;
		onremove: (user: WdttPanelUserEntry) => void;
		onrename: (user: WdttPanelUserEntry, name: string) => void;
	}

	let { user, users, busy = false, onlink, onreissue, onremove, onrename }: Props = $props();

	const actions = $derived(rowActions(user, users));

	let renaming = $state(false);
	let draft = $state('');
	let input = $state<HTMLInputElement | undefined>();

	function startRename() {
		if (renaming) {
			renaming = false;
			return;
		}
		draft = user.comment ?? '';
		renaming = true;
		queueMicrotask(() => input?.focus());
	}

	function commit() {
		if (!renaming) return;
		renaming = false;
		const name = draft.trim();
		if (name && name !== (user.comment ?? '')) onrename(user, name);
	}
</script>

<li class="row">
	<div class="row-main">
		{#if renaming}
			<input
				bind:this={input}
				class="rename-input"
				aria-label="Имя абонента"
				bind:value={draft}
				onkeydown={(e) => {
					if (e.key === 'Enter') commit();
					if (e.key === 'Escape') renaming = false;
				}}
				onblur={commit}
			/>
		{:else}
			<span class="row-name">{user.comment || '—'}</span>
			<code class="row-pass" title={user.password}>{shortPassword(user.password)}</code>
			{#if user.isExpired}
				<Badge size="xs" variant="warning">истёк</Badge>
			{/if}
			{#if user.isDeactivated}
				<Badge size="xs" variant="muted">отключён</Badge>
			{/if}
			{#if user.isMainPassword}
				<Badge size="xs" variant="accent">= главный пароль</Badge>
			{/if}
			{#if user.isAuto}
				<span class="row-auto">
					<Badge size="xs" variant="info">заведён автоматически</Badge>
					<FieldHint
						text="Сервер не запускается без единого рабочего пароля, поэтому абонент создан за вас."
						ariaLabel="Подсказка: заведён автоматически"
					/>
				</span>
			{/if}
		{/if}
	</div>

	<div class="row-actions">
		{#if actions.link !== 'hidden'}
			<Button
				variant="ghost"
				size="sm"
				disabled={busy || actions.link === 'blocked'}
				onclick={() => onlink(user)}
			>
				Ссылка
			</Button>
			{#if actions.linkHint}
				<FieldHint text={actions.linkHint} ariaLabel="Подсказка: ссылка недоступна" />
			{/if}
		{/if}
		{#if actions.reissue}
			<Button variant="ghost" size="sm" disabled={busy} onclick={() => onreissue(user)}>
				Перевыпустить
			</Button>
		{/if}
		<Button
			variant="ghost"
			size="sm"
			disabled={busy || actions.remove === 'blocked'}
			onclick={() => onremove(user)}
		>
			Удалить
		</Button>
		{#if actions.removeHint}
			<FieldHint text={actions.removeHint} ariaLabel="Подсказка: удаление недоступно" />
		{/if}
		<button
			type="button"
			class="row-action"
			class:active={renaming}
			aria-label="Переименовать абонента"
			title="Переименовать абонента"
			onmousedown={(e) => {
				if (renaming) e.preventDefault();
			}}
			onclick={startRename}
		>
			<Pencil size={14} />
		</button>
	</div>
</li>

<style>
	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg-primary);
		border-bottom: 1px solid var(--color-border);
	}

	.row:last-child {
		border-bottom: none;
	}

	.row-main {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.375rem 0.5rem;
		min-width: 0;
		flex: 1;
	}

	.row-name {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.row-pass {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.row-auto {
		display: inline-flex;
		align-items: center;
		gap: 0.15rem;
	}

	.rename-input {
		flex: 1;
		min-width: 8rem;
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
		gap: 0.15rem;
		flex-wrap: wrap;
	}

	.row-action {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.75rem;
		height: 1.75rem;
		border: none;
		border-radius: var(--radius-sm);
		background: none;
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.row-action:hover,
	.row-action.active {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}

	.row-action :global(svg) {
		display: block;
	}
</style>
