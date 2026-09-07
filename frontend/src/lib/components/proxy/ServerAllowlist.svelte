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
	import { servers, type ServersSnapshot } from '$lib/stores/servers';
	import {
		findServerByListenPort,
		hostIP,
		parseLocalListenPort,
		patchWgConfEndpoint,
		suggestNextPeerIP,
	} from '$lib/utils/serverPeerOptions';
	import type { FreeTurnAllowlistEntry, FreeTurnServerConfig } from '$lib/types';
	import LinkBox from './LinkBox.svelte';
	import ServerAllowlistAddModal, { type AddClientValues } from './ServerAllowlistAddModal.svelte';
	import { NEW_PEER } from './ShareWizardPeer.svelte';

	interface Props {
		serverId: string;
		serverName: string;
		server: FreeTurnServerConfig;
		busy?: boolean;
		/** Общий замок мутаций сервера (деталь «Раздача» владеет им). */
		locked: (fn: () => Promise<void>) => Promise<void>;
	}

	let { serverId, serverName, server, busy = false, locked }: Props = $props();

	let entries = $state<FreeTurnAllowlistEntry[]>([]);
	let enabled = $state(false);
	let clientsFile = $state('');
	let loadedFor = $state('');
	let disableOpen = $state(false);

	let addOpen = $state(false);
	let addError = $state('');
	let link = $state('');

	// Пир абоненту заводится на WG-сервере, куда смотрит `-connect` (#871).
	let snap = $state<ServersSnapshot | null>(null);
	$effect(() => servers.subscribe((st) => (snap = st.data)));
	const serverListenPort = $derived(parseLocalListenPort(server.connect) ?? 0);

	type CreatedPeer = { kind: 'managed' | 'system'; serverId: string; pubkey: string };

	/** Создаёт пира под абонента: его .conf с локальным Endpoint и адрес для отката. */
	async function createPeerConf(
		description: string,
		localPort: number,
	): Promise<{ conf: string; peer: CreatedPeer }> {
		const own = findServerByListenPort(snap, serverListenPort);
		if (!own) {
			throw new Error('WG-сервер раздачи не поднят или не выбран в настройках «Сеть»');
		}
		const tunnelIP = suggestNextPeerIP(own.address, own.peerIPs);
		if (!tunnelIP) throw new Error('Не удалось подобрать адрес пира на WG-сервере');
		let conf: string;
		let pubkey: string;
		if (own.kind === 'managed') {
			pubkey = (await api.addManagedPeer(own.serverId, { description, tunnelIP })).publicKey;
			// Endpoint всё равно станет 127.0.0.1 — WAN-адрес бэкенду искать незачем.
			conf = await api.getManagedPeerConf(own.serverId, pubkey, '127.0.0.1');
		} else {
			// Ответ — снимок серверов, а не пир. Новый узнаётся по адресу, который
			// отправили мы: бэкенд держит его уникальным. Разница снимков «до/после»
			// подсунула бы чужого пира, добавленного параллельно.
			const fresh = await api.addSystemServerPeer(own.serverId, { description, tunnelIP });
			const want = hostIP(tunnelIP);
			const found = fresh.servers
				?.find((s) => s.id === own.serverId)
				?.peers?.find((p) => (p.allowedIPs ?? []).some((a) => hostIP(a) === want))?.publicKey;
			if (!found) throw new Error('Пир создан, но в ответе сервера не найден');
			pubkey = found;
			conf = await api.getSystemServerPeerConf(own.serverId, pubkey, '127.0.0.1');
		}
		void servers.refetch();
		return {
			conf: patchWgConfEndpoint(conf, localPort),
			peer: { kind: own.kind, serverId: own.serverId, pubkey },
		};
	}

	/** Откат пира, созданного под абонента, если ссылка или список не удались. */
	async function dropPeer(p: CreatedPeer): Promise<void> {
		if (p.kind === 'managed') await api.deleteManagedPeer(p.serverId, p.pubkey);
		else await api.deleteSystemServerPeer(p.serverId, p.pubkey);
		void servers.refetch();
	}

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
	function addClient(values: AddClientValues) {
		if (busy) return;
		addError = '';
		void locked(async () => {
			// Пир, созданный под этого абонента: отказ ссылки или списка откатывает
			// его, иначе на сервере копились бы сироты от неудачных попыток.
			let created: CreatedPeer | null = null;
			try {
				let wg = values.peerConf;
				if (values.peer === NEW_PEER) {
					const made = await createPeerConf(values.name || values.clientId, values.localPort);
					wg = made.conf;
					created = made.peer;
				}
				// peer не шлётся: внешний адрес подставит бэкенд. `server.connect` —
				// это локальный WG-сервер, абоненту он не адрес (#871).
				const res = await api.generateFreeTurnLink({
					serverId,
					clientId: values.clientId || undefined,
					name: values.name || serverName,
					wg: wg.trim() || undefined,
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
				if (created) {
					try {
						await dropPeer(created);
					} catch (dropErr) {
						addError += `. Созданный пир не удалён: ${errText(dropErr)}`;
					}
				}
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
			{#if !enabled}
				<!-- Выключенный список файл не теряет: его записи получат доступ при включении. -->
				<span aria-hidden="true">·</span>
				<span>проверка выключена — записи начнут действовать после её включения</span>
			{/if}
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
	{serverListenPort}
	onsubmit={addClient}
	onclose={() => (addOpen = false)}
/>

<ConfirmModal
	open={disableOpen}
	title="Выключить список разрешённых? Сервер будет принимать любой Client ID."
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
