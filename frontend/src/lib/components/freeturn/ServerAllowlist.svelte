<script lang="ts">
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { pluralize } from '$lib/utils/pluralize';
	import type { FreeTurnAllowlistEntry, FreeTurnServerConfig } from '$lib/types';

	interface Props {
		serverInstanceId: string;
		server: FreeTurnServerConfig;
	}

	let { serverInstanceId, server }: Props = $props();

	let loading = $state(false);
	let removing = $state('');
	let entries = $state<FreeTurnAllowlistEntry[]>([]);
	let enabled = $state(false);
	let clientsFile = $state('');

	async function reload() {
		loading = true;
		try {
			const st = await api.getFreeTurnServerAllowlist(serverInstanceId);
			entries = st.clients ?? [];
			enabled = st.enabled;
			clientsFile = st.clientsFile ?? '';
			if (st.clientsFile && server.clientsFile !== st.clientsFile) {
				server.clientsFile = st.clientsFile;
			}
		} catch (e) {
			notifications.error(
				'Не удалось загрузить список клиентов: ' + (e instanceof Error ? e.message : String(e))
			);
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		void reload();
	});

	$effect(() => {
		serverInstanceId;
		void reload();
	});

	export async function saveClient(clientId: string, comment: string): Promise<boolean> {
		const id = clientId.trim();
		if (!id) {
			notifications.error('Укажите Client ID');
			return false;
		}
		try {
			const res = await api.addFreeTurnServerAllowlistClient(serverInstanceId, id, comment.trim());
			entries = res.clients ?? [];
			enabled = res.enabled;
			clientsFile = res.clientsFile ?? '';
			if (res.clientsFile) {
				server.clientsFile = res.clientsFile;
			}
			if (res.needsRestart) {
				notifications.info(
					'Allowlist включён. Сохраните настройки сервера и перезапустите freeturn-server, чтобы проверка ID заработала.'
				);
			} else {
				notifications.success('Клиент добавлен в список');
			}
			return true;
		} catch (e) {
			notifications.error(
				'Не удалось сохранить клиента: ' + (e instanceof Error ? e.message : String(e))
			);
			return false;
		}
	}

	async function removeEntry(clientId: string) {
		removing = clientId;
		try {
			await api.removeFreeTurnServerAllowlistClient(serverInstanceId, clientId);
			await reload();
			notifications.success('Клиент удалён из списка');
		} catch (e) {
			notifications.error(
				'Не удалось удалить клиента: ' + (e instanceof Error ? e.message : String(e))
			);
		} finally {
			removing = '';
		}
	}

	async function disableAllowlist() {
		if (!confirm('Выключить проверку Client ID? Сервер будет принимать любые ID.')) return;
		try {
			await api.disableFreeTurnServerAllowlist(serverInstanceId);
			server.clientsFile = '';
			enabled = false;
			entries = [];
			clientsFile = '';
			notifications.info('Allowlist выключен — сохраните настройки и перезапустите сервер');
		} catch (e) {
			notifications.error(
				'Не удалось выключить allowlist: ' + (e instanceof Error ? e.message : String(e))
			);
		}
	}

	function shortId(id: string): string {
		if (id.length <= 20) return id;
		return `${id.slice(0, 10)}…${id.slice(-8)}`;
	}

	const summaryCount = $derived(entries.length);
</script>

<div class="ft-allowlist">
	<div class="ft-allowlist-head">
		<div>
			<div class="section-label">Список ID клиентов сервера</div>
			<p class="ft-hint">
				{#if enabled}
					Разрешены только ID из списка ниже — по одному на каждого получателя ссылки.
					Новые записи подхватываются без перезапуска; включение/выключение allowlist — после
					перезапуска сервера.
				{:else}
					Сейчас сервер принимает любой Client ID. Добавьте первого клиента кнопкой «В список» в
					блоке генерации ссылки — allowlist включится автоматически.
				{/if}
			</p>
		</div>
		{#if enabled}
			<Button variant="ghost" size="sm" onclick={disableAllowlist}>Выключить allowlist</Button>
		{/if}
	</div>

	{#if loading && !entries.length}
		<p class="ft-hint">Загрузка…</p>
	{:else if entries.length === 0}
		<p class="ft-empty">Список пуст — добавьте клиента из блока «Ссылка для клиента» выше.</p>
	{:else}
		<ul class="ft-allowlist-list">
			{#each entries as entry (entry.clientId)}
				<li class="ft-allowlist-item">
					<div class="ft-allowlist-main">
						<span class="ft-allowlist-name">{entry.comment || '—'}</span>
						<code class="ft-allowlist-id" title={entry.clientId}>{shortId(entry.clientId)}</code>
					</div>
					<Button
						variant="ghost"
						size="sm"
						loading={removing === entry.clientId}
						onclick={() => removeEntry(entry.clientId)}
					>
						Удалить
					</Button>
				</li>
			{/each}
		</ul>
		<p class="ft-hint ft-allowlist-foot">
			{pluralize(summaryCount, ['клиент', 'клиента', 'клиентов'])} в allowlist
			{#if clientsFile}
				· файл <code>{clientsFile}</code>
			{/if}
		</p>
	{/if}
</div>

<style>
	.ft-allowlist {
		grid-column: 1 / -1;
	}

	.ft-allowlist-head {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-bottom: 0.625rem;
	}

	.ft-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0.25rem 0 0;
	}

	.ft-empty {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		margin: 0;
		padding: 0.625rem 0.75rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
	}

	.ft-allowlist-list {
		list-style: none;
		margin: 0;
		padding: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		overflow: hidden;
	}

	.ft-allowlist-item {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg-primary);
		border-bottom: 1px solid var(--color-border);
	}

	.ft-allowlist-item:last-child {
		border-bottom: none;
	}

	.ft-allowlist-main {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.ft-allowlist-name {
		font-size: 0.875rem;
		font-weight: 500;
	}

	.ft-allowlist-id {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.ft-allowlist-foot {
		margin-top: 0.375rem;
	}
</style>
