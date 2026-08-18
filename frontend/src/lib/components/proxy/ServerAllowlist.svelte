<script lang="ts">
	// «Абоненты» FreeTurn-сервера: allowlist Client ID (ia.md §3.3 часть Б,
	// FA-01..FA-09) и связка «выдал ссылку — внёс получателя» (SH-46).
	// Перенесён из `freeturn/ServerAllowlist.svelte`; легаси-тексты заменены на
	// строки микрокопии там, где ID для них есть.
	import { untrack } from 'svelte';
	import { Button, ConfirmModal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import type { FreeTurnAllowlistEntry, FreeTurnServerConfig } from '$lib/types';
	import LinkBox from './LinkBox.svelte';
	import ServerAllowlistAddModal from './ServerAllowlistAddModal.svelte';

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

	let addOpen = $state(false);
	let addError = $state('');
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

	/**
	 * SH-46: ссылка выдаётся и получатель сразу вносится в список. Форма живёт
	 * в модалке (Дополнение №4 п.1), решение «вносить ли» — галка WS-38.
	 */
	function addClient(values: { clientId: string; name: string; allow: boolean }) {
		if (busy) return;
		addError = '';
		void locked(async () => {
			try {
				const res = await api.generateFreeTurnLink({
					serverId,
					clientId: values.clientId || undefined,
					name: values.name || serverName,
					peer: server.connect?.trim() || undefined,
					wg: peerConf.trim() || undefined,
				});
				const id = (res.clientId || values.clientId).trim();
				if (id && values.allow) {
					const add = await api.addFreeTurnServerAllowlistClient(serverId, id, values.name);
					entries = add.clients ?? [];
					enabled = add.enabled;
					clientsFile = add.clientsFile ?? '';
					// TS-11 — только когда бэкенд сказал, что перезапуск нужен
					// (включение списка). На добавление записи тоста нет: сервер
					// подхватывает её сам (оговорка TS-11).
					if (add.needsRestart) {
						notifications.info(
							'Список включён — перезапустите сервер, чтобы проверка заработала',
						);
					} else {
						// TS-10
						notifications.success('Client ID внесён в список разрешённых');
					}
				}
				// Окно закрывается только полным успехом: ссылка под списком
				// иначе относилась бы к невнесённому абоненту.
				link = res.link ?? '';
				addOpen = false;
			} catch (e) {
				// Отказ остаётся в открытой модалке: он про то, что в полях.
				addError = errText(e);
				await reload();
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
	<div class="head">
		<!-- SH-43: форма ушла в окно, в шапке осталась кнопка. -->
		<Button
			variant="secondary"
			size="sm"
			disabled={busy}
			onclick={() => {
				addError = '';
				addOpen = true;
			}}
		>
			Добавить
		</Button>
		{#if enabled}
			<Button variant="ghost" size="sm" disabled={busy} onclick={() => (disableOpen = true)}>
				Выключить список
			</Button>
		{/if}
	</div>

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

	{#if link}
		<LinkBox {link} freeturn />
	{/if}
</div>

<ServerAllowlistAddModal
	open={addOpen}
	{busy}
	error={addError}
	onsubmit={addClient}
	onclose={() => (addOpen = false)}
/>

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

</style>
