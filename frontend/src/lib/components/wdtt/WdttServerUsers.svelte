<script lang="ts">
	import { untrack } from 'svelte';
	import { Button, Input } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { pluralize } from '$lib/utils/pluralize';
	import type { WdttPanelUserEntry, WdttPanelUsersStatus } from '$lib/types';

	const FETCH_TIMEOUT_MS = 20_000;

	interface Props {
		serverInstanceId: string;
		serverMainPassword?: string;
		canManage?: boolean;
		onGenerateForUser?: (user: WdttPanelUserEntry) => void;
	}

	let {
		serverInstanceId,
		serverMainPassword = '',
		canManage = true,
		onGenerateForUser
	}: Props = $props();

	let loading = $state(false);
	let adding = $state(false);
	let removing = $state('');
	let loadError = $state('');
	let status = $state<WdttPanelUsersStatus | null>(null);
	let newComment = $state('');
	let newVkHash = $state('');
	let newPassword = $state('');

	let reloadSeq = 0;
	let loadedForId = $state('');

	function withTimeout<T>(promise: Promise<T>, ms: number): Promise<T> {
		return new Promise((resolve, reject) => {
			const timer = setTimeout(() => reject(new Error('таймаут запроса')), ms);
			promise.then(
				(v) => {
					clearTimeout(timer);
					resolve(v);
				},
				(e) => {
					clearTimeout(timer);
					reject(e);
				}
			);
		});
	}

	async function reload() {
		const seq = ++reloadSeq;
		loading = true;
		loadError = '';
		try {
			const result = await withTimeout(
				api.getWdttServerPanelUsers(serverInstanceId),
				FETCH_TIMEOUT_MS
			);
			if (seq !== reloadSeq) return;
			status = result;
		} catch (e) {
			if (seq !== reloadSeq) return;
			loadError = e instanceof Error ? e.message : String(e);
			notifications.error('Не удалось загрузить список клиентов: ' + loadError);
		} finally {
			if (seq === reloadSeq) loading = false;
		}
	}

	$effect(() => {
		const id = serverInstanceId;
		untrack(() => {
			if (id === loadedForId) return;
			loadedForId = id;
			void reload();
		});
	});

	async function addUser() {
		if (adding || !newComment.trim()) return;
		if (!serverMainPassword.trim()) {
			notifications.error('Сначала задайте пароль сервера на вкладке «Основное»');
			return;
		}
		adding = true;
		try {
			const hadCustomPass = !!newPassword.trim();
			status = await withTimeout(
				api.addWdttServerPanelUser(serverInstanceId, {
					comment: newComment.trim(),
					vkHash: newVkHash.trim(),
					password: newPassword.trim() || undefined,
					mainPassword: serverMainPassword.trim()
				}),
				FETCH_TIMEOUT_MS
			);
			newComment = '';
			newVkHash = '';
			newPassword = '';
			loadError = '';
			notifications.success(
				hadCustomPass
					? 'Клиент добавлен — используйте указанный пароль в ссылке wdtt://'
					: 'Клиент добавлен — пароль сгенерирован автоматически, он показан в списке ниже'
			);
		} catch (e) {
			notifications.error(
				'Не удалось добавить клиента: ' + (e instanceof Error ? e.message : String(e))
			);
		} finally {
			adding = false;
		}
	}

	async function removeUser(password: string) {
		removing = password;
		try {
			status = await withTimeout(
				api.removeWdttServerPanelUser(serverInstanceId, password),
				FETCH_TIMEOUT_MS
			);
			notifications.success('Клиент удалён');
		} catch (e) {
			notifications.error(
				'Не удалось удалить клиента: ' + (e instanceof Error ? e.message : String(e))
			);
		} finally {
			removing = '';
		}
	}

	function shortPass(pass: string): string {
		if (pass.length <= 16) return pass;
		return `${pass.slice(0, 8)}…${pass.slice(-6)}`;
	}

	const users = $derived(status?.users ?? []);
	const extraUsers = $derived(users.filter((u) => !u.isMainPassword));
	const panelMissing = $derived(!!status && !status.available);
</script>

<div class="wdtt-users">
	<div class="wdtt-users-head">
		<div>
			<div class="section-label">Клиенты сервера</div>
			<p class="wdtt-hint">
				По умолчанию все подключаются основным паролем сервера. Для отдельного клиента укажите
				имя и нажмите «Добавить» — пароль сгенерируется сам, или задайте свой. Кнопка «Ссылка»
				подставит пароль клиента в wdtt://.
			</p>
		</div>
		{#if !loading}
			<Button variant="ghost" size="sm" onclick={reload}>Обновить</Button>
		{/if}
	</div>

	{#if loading && !users.length && !loadError}
		<p class="wdtt-hint">Загрузка…</p>
	{:else if loadError && !users.length}
		<p class="wdtt-empty wdtt-empty-error">
			{loadError}
			<Button variant="secondary" size="sm" onclick={reload}>Повторить</Button>
		</p>
	{:else if users.length === 0}
		<p class="wdtt-empty">
			Задайте пароль сервера на вкладке «Основное» — он станет основным клиентом, а отдельные
			пароли можно добавить ниже.
		</p>
	{:else}
		{#if panelMissing}
			<p class="wdtt-empty wdtt-empty-warn">
				panel.db сейчас недоступна — показан список из настроек awgm. Он будет записан в
				panel.db при следующем запуске сервера.
			</p>
		{/if}
		<ul class="wdtt-users-list">
			{#each users as entry (entry.password)}
				<li class="wdtt-users-item">
					<div class="wdtt-users-main">
						<span class="wdtt-users-name">{entry.comment || '—'}</span>
						<code class="wdtt-users-pass" title={entry.password}>{shortPass(entry.password)}</code>
						{#if entry.isMainPassword}
							<span class="wdtt-users-badge wdtt-users-badge-main">основной</span>
						{/if}
						{#if entry.isDeactivated}
							<span class="wdtt-users-badge">отключён</span>
						{/if}
					</div>
					<div class="wdtt-users-actions">
						{#if onGenerateForUser}
							<Button variant="ghost" size="sm" onclick={() => onGenerateForUser?.(entry)}>
								Ссылка
							</Button>
						{/if}
						{#if !entry.isMainPassword}
							<Button
								variant="ghost"
								size="sm"
								loading={removing === entry.password}
								disabled={!canManage}
								onclick={() => removeUser(entry.password)}
							>
								Удалить
							</Button>
						{/if}
					</div>
				</li>
			{/each}
		</ul>
		<p class="wdtt-hint wdtt-users-foot">
			{#if extraUsers.length}
				{pluralize(extraUsers.length, ['клиент', 'клиента', 'клиентов'])} с отдельным паролем
			{:else}
				Отдельных паролей нет — основной подходит всем
			{/if}
		</p>
	{/if}

	{#if canManage}
		<div class="wdtt-users-add">
			<Input label="Имя / комментарий" bind:value={newComment} placeholder="Клиент Иван" />
			<Input
				label="Пароль (необязательно)"
				bind:value={newPassword}
				placeholder="авто — если пусто"
				type="password"
			/>
			<Input
				label="VK-хеш (необязательно)"
				bind:value={newVkHash}
				placeholder="vk.com/call/join/… — не пароль wdtt://"
			/>
			<Button
				variant="secondary"
				size="sm"
				loading={adding}
				disabled={!newComment.trim() || adding}
				onclick={addUser}
			>
				Добавить клиента
			</Button>
		</div>
	{/if}
</div>

<style>
	.wdtt-users {
		margin-top: 1rem;
		padding-top: 1rem;
		border-top: 1px solid var(--color-border);
	}

	.wdtt-users-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 0.625rem;
	}

	.wdtt-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0.25rem 0 0;
	}

	.wdtt-empty {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		margin: 0;
		padding: 0.625rem 0.75rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
	}

	.wdtt-empty-error {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}

	.wdtt-empty-warn {
		margin-bottom: 0.5rem;
		border-style: solid;
		border-color: var(--color-warning, #b45309);
		color: var(--color-warning, #b45309);
	}

	.wdtt-users-list {
		list-style: none;
		margin: 0;
		padding: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		overflow: hidden;
	}

	.wdtt-users-item {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg-primary);
		border-bottom: 1px solid var(--color-border);
	}

	.wdtt-users-item:last-child {
		border-bottom: none;
	}

	.wdtt-users-main {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.375rem 0.625rem;
		min-width: 0;
	}

	.wdtt-users-name {
		font-size: 0.875rem;
		font-weight: 500;
	}

	.wdtt-users-pass {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.wdtt-users-badge {
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
		padding: 0.125rem 0.375rem;
		border-radius: 4px;
		background: var(--color-surface-secondary, rgba(0, 0, 0, 0.04));
	}

	.wdtt-users-badge-main {
		color: var(--color-primary, #2563eb);
	}

	.wdtt-users-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
	}

	.wdtt-users-foot {
		margin-top: 0.375rem;
	}

	.wdtt-users-add {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: 0.5rem;
		align-items: end;
		margin-top: 0.75rem;
	}
</style>
