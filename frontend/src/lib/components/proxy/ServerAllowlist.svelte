<script lang="ts">
	// «Абоненты» FreeTurn-сервера: allowlist Client ID (ia.md §3.3 часть Б,
	// FA-01..FA-09) и связка «выдал ссылку — внёс получателя» (SH-46).
	// Перенесён из `freeturn/ServerAllowlist.svelte`; легаси-тексты заменены на
	// строки микрокопии там, где ID для них есть.
	import { untrack } from 'svelte';
	import { RefreshCw } from 'lucide-svelte';
	import { Button, ConfirmModal, IconButton, Input } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import type { FreeTurnAllowlistEntry, FreeTurnServerConfig } from '$lib/types';
	import LinkBox from './LinkBox.svelte';

	interface Props {
		serverId: string;
		serverName: string;
		server: FreeTurnServerConfig;
		/** .conf пира из секции «Сеть»: он вкладывается в ссылку абоненту. */
		peerConf?: string;
		busy?: boolean;
		/** Общий замок мутаций сервера (деталь «Раздача» владеет им). */
		locked: (fn: () => Promise<void>) => Promise<void>;
	}

	let { serverId, serverName, server, peerConf = '', busy = false, locked }: Props = $props();

	let entries = $state<FreeTurnAllowlistEntry[]>([]);
	let enabled = $state(false);
	let clientsFile = $state('');
	let loadedFor = $state('');
	let disableOpen = $state(false);

	let clientId = $state('');
	let comment = $state('');
	let link = $state('');

	async function reload() {
		try {
			const st = await api.getFreeTurnServerAllowlist(serverId);
			entries = st.clients ?? [];
			enabled = st.enabled;
			clientsFile = st.clientsFile ?? '';
		} catch (e) {
			notifications.error(errText(e));
		}
	}

	// FA-08: смена инстанса перечитывает список под выбранный сервер.
	$effect(() => {
		const id = serverId;
		untrack(() => {
			if (id === loadedFor) return;
			loadedFor = id;
			void reload();
		});
	});

	/** Client ID придумывает фронт: бэкенд в ответе лишь возвращает присланный. */
	function randomClientId() {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		clientId = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	/** SH-46: ссылка выдаётся и получатель сразу вносится в список. */
	function issueAndAllow() {
		if (busy) return;
		if (!clientId.trim()) randomClientId();
		void locked(async () => {
			try {
				const res = await api.generateFreeTurnLink({
					serverId,
					clientId: clientId.trim() || undefined,
					name: comment.trim() || serverName,
					peer: server.connect?.trim() || undefined,
					wg: peerConf.trim() || undefined,
				});
				link = res.link ?? '';
				const id = (res.clientId || clientId).trim();
				if (!id) return;
				clientId = id;
				const add = await api.addFreeTurnServerAllowlistClient(serverId, id, comment.trim());
				entries = add.clients ?? [];
				enabled = add.enabled;
				clientsFile = add.clientsFile ?? '';
				// TS-11 — только когда бэкенд сказал, что перезапуск нужен
				// (включение списка). На добавление записи тоста нет: сервер
				// подхватывает её сам (оговорка TS-11).
				if (add.needsRestart) {
					notifications.info('Список включён — перезапустите сервер, чтобы проверка заработала');
				} else {
					// TS-10
					notifications.success('Client ID внесён в список разрешённых');
				}
			} catch (e) {
				notifications.error(errText(e));
			}
		});
	}

	function removeEntry(id: string) {
		void locked(async () => {
			try {
				await api.removeFreeTurnServerAllowlistClient(serverId, id);
			} catch (e) {
				notifications.error(errText(e));
			}
			await reload();
		});
	}

	function disableList() {
		disableOpen = false;
		void locked(async () => {
			try {
				const res = await api.disableFreeTurnServerAllowlist(serverId);
				// TS-24 — как и на включении, тост только когда бэкенд сказал, что
				// перезапуск нужен: -clients-file читается при старте процесса, и
				// живой сервер до перезапуска продолжает проверять ID.
				if (res.needsRestart) {
					notifications.info(
						'Список выключен — перезапустите сервер, чтобы проверка отключилась',
					);
				}
			} catch (e) {
				notifications.error(errText(e));
			}
			await reload();
		});
	}

	function shortId(id: string): string {
		return id.length <= 20 ? id : `${id.slice(0, 10)}…${id.slice(-8)}`;
	}
</script>

<div class="allowlist">
	{#if enabled}
		<div class="head">
			<Button variant="ghost" size="sm" disabled={busy} onclick={() => (disableOpen = true)}>
				Выключить список
			</Button>
		</div>
	{/if}

	{#if entries.length}
		<ul class="list">
			{#each entries as entry (entry.clientId)}
				<li class="row">
					<div class="row-main">
						<span class="row-name">{entry.comment || '—'}</span>
						<code class="row-id" title={entry.clientId}>{shortId(entry.clientId)}</code>
					</div>
					<Button
						variant="ghost"
						size="sm"
						disabled={busy}
						onclick={() => removeEntry(entry.clientId)}
					>
						Удалить
					</Button>
				</li>
			{/each}
		</ul>
		<p class="counter">
			<span>Записей: {entries.length}</span>
			{#if clientsFile}
				<span aria-hidden="true">·</span>
				<code>{clientsFile}</code>
			{/if}
		</p>
	{:else}
		<!-- SH-89: пустой список — не «ничего не настроено», а «никто не пройдёт». -->
		<p class="empty">Список пуст — с включённой проверкой сервер не пропустит никого</p>
	{/if}

	<div class="form">
		<div class="field-with-btn">
			<Input label="Client ID" bind:value={clientId} fullWidth />
			<IconButton size="sm" ariaLabel="Обновить Client ID" onclick={randomClientId}>
				<RefreshCw size={14} />
			</IconButton>
		</div>
		<Input label="Комментарий" bind:value={comment} />
		<Button variant="secondary" size="sm" disabled={busy} onclick={issueAndAllow}>
			Ссылка · внести в список
		</Button>
	</div>

	{#if link}
		<LinkBox {link} freeturn />
	{/if}
</div>

<ConfirmModal
	open={disableOpen}
	title="Выключить проверку Client ID? Сервер будет принимать любые ID."
	message=""
	confirmLabel="Выключить список"
	onConfirm={disableList}
	onClose={() => (disableOpen = false)}
/>

<style>
	.allowlist {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.list {
		list-style: none;
		margin: 0;
		padding: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		overflow: hidden;
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg-primary);
		border-bottom: 1px solid var(--color-border);
	}

	.row:last-child {
		border-bottom: none;
	}

	.row-main {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.row-name {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.row-id,
	.counter code {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.empty {
		margin: 0;
		padding: 0.625rem 0.75rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
	}

	.counter {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		flex-wrap: wrap;
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.field-with-btn {
		display: flex;
		align-items: flex-end;
		gap: 0.375rem;
		min-width: 0;
	}

	.field-with-btn :global(svg) {
		display: block;
	}

	.form {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: 0.5rem;
		align-items: end;
	}
</style>
