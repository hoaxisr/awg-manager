<script lang="ts">
	// Блок «Абоненты» WDTT-сервера (ia.md §3.3 часть А, спека §4.4).
	// Владеет списком и его мутациями; решает матрицу кнопок чистый модуль
	// `serverClients.ts`, тексты — по ID микрокопии.
	import { untrack } from 'svelte';
	import { Badge, Button, ConfirmModal, FieldHint } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import type { WdttPanelUserEntry, WdttPanelUsersStatus, WdttServerConfig } from '$lib/types';
	import LinkPanel from './LinkPanel.svelte';
	import ServerClientAddModal from './ServerClientAddModal.svelte';
	import ServerClientRow from './ServerClientRow.svelte';
	import {
		CLIENT_TEXT,
		addErrorText,
		addedPassword,
		autoCreateAfterRemove,
		counterLabel,
		headerApplied,
		reissueName,
	} from './serverClients';

	/** WU-07: запрос списка не висит дольше 20 секунд. */
	const FETCH_TIMEOUT_MS = 20_000;

	interface Props {
		serverId: string;
		serverName: string;
		/** Конфиг сервера с бэкенда — из него берётся главный пароль. */
		server: WdttServerConfig;
		running: boolean;
		/** Общий замок мутаций сервера занят другой операцией. */
		busy?: boolean;
		/** Общий замок мутаций сервера (деталь «Раздача» владеет им). */
		locked: (fn: () => Promise<void>) => Promise<void>;
	}

	let { serverId, serverName, server, running, busy = false, locked }: Props = $props();

	let users = $state<WdttPanelUserEntry[]>([]);
	let lastReload = $state<WdttPanelUsersStatus['reload']>(undefined);
	let loading = $state(false);
	let loadError = $state('');
	let loadedFor = $state('');

	let linkUser = $state<WdttPanelUserEntry | undefined>();
	let removeTarget = $state<WdttPanelUserEntry | undefined>();
	let reissueTarget = $state<WdttPanelUserEntry | undefined>();

	let addOpen = $state(false);
	let addError = $state('');

	// WU-08: страж гонки ответов — побеждает последний применённый результат
	// (и чтение, и мутация). Счётчик летящих GET-ов отдельный: спиннер гасится
	// по их окончанию, а не по тому, чей результат победил.
	let seq = 0;
	let inflight = 0;

	const mainPassword = $derived((server.password ?? '').trim());
	const applied = $derived(headerApplied(lastReload, running));
	// TS-13: без главного пароля сервера абонента завести нельзя — форму даже
	// не открываем, причина написана под списком.
	const canAdd = $derived(!!mainPassword && !busy);

	function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
		return new Promise((resolve, reject) => {
			const timer = setTimeout(() => reject(new Error('таймаут запроса')), ms);
			p.then(
				(v) => {
					clearTimeout(timer);
					resolve(v);
				},
				(e) => {
					clearTimeout(timer);
					reject(e);
				},
			);
		});
	}

	function apiCode(e: unknown): string {
		return (e as { body?: { code?: string } })?.body?.code ?? '';
	}

	/**
	 * Ответ мутации несёт и состав, и судьбу SIGHUP этой мутации. Применённый
	 * ответ устаревает все летящие GET-ы (WU-08): иначе стартовый GET или
	 * «Обновить», запущенные до мутации, перезаписали бы её результат более
	 * старым составом.
	 */
	function applyStatus(st: WdttPanelUsersStatus) {
		seq += 1;
		users = st.users ?? [];
		lastReload = st.reload;
	}

	/**
	 * Чтение списка. `reload` у GET не приходит — бейдж шапки возвращается к
	 * признаку по `running`, если не сказано сохранить судьбу прошлой мутации.
	 */
	async function reload(preserveBadge = false) {
		const my = ++seq;
		inflight += 1;
		loading = true;
		loadError = '';
		try {
			const st = await withTimeout(api.getWdttServerPanelUsers(serverId), FETCH_TIMEOUT_MS);
			if (my !== seq) return;
			users = st.users ?? [];
			if (!preserveBadge) lastReload = st.reload;
		} catch (e) {
			if (my !== seq) return;
			// TS-17
			loadError = 'Не удалось получить список абонентов';
			notifications.error(errText(e));
		} finally {
			inflight -= 1;
			loading = inflight > 0;
		}
	}

	$effect(() => {
		const id = serverId;
		untrack(() => {
			if (id === loadedFor) return;
			loadedFor = id;
			void reload();
		});
	});

	function addUser(values: { comment: string; password: string; vkHash: string }) {
		if (!canAdd || !values.comment) return;
		const { comment, password, vkHash } = values;
		addError = '';
		void locked(async () => {
			try {
				const st = await withTimeout(
					api.addWdttServerPanelUser(serverId, {
						comment,
						password: password || undefined,
						vkHash: vkHash || undefined,
						mainPassword,
					}),
					FETCH_TIMEOUT_MS,
				);
				applyStatus(st);
				addOpen = false;
				// TS-05 / TS-06
				notifications.success(
					password
						? `Абонент «${comment}» добавлен`
						: `Абонент «${comment}» добавлен, пароль сгенерирован`,
				);
			} catch (e) {
				// Отказ остаётся в открытой модалке: он про то, что в полях, и
				// тостом за окном был бы оторван от них.
				addError = addErrorText(apiCode(e), errText(e));
				// ИА §5 п.4: после любого отказа список перечитывается.
				await reload();
			}
		});
	}

	function removeUser(user: WdttPanelUserEntry) {
		const name = user.comment || user.password;
		removeTarget = undefined;
		void locked(async () => {
			try {
				const st = await withTimeout(
					api.removeWdttServerPanelUser(serverId, user.password),
					FETCH_TIMEOUT_MS,
				);
				applyStatus(st);
				if (linkUser?.password === user.password) linkUser = undefined;
				// TS-07
				notifications.success(`Абонент «${name}» удалён`);
			} catch (e) {
				notifications.error(errText(e));
				await reload();
			}
		});
	}

	function renameUser(user: WdttPanelUserEntry, name: string) {
		void locked(async () => {
			try {
				await withTimeout(
					api.renameWdttServerPanelUser(serverId, user.password, name),
					FETCH_TIMEOUT_MS,
				);
			} catch (e) {
				notifications.error(errText(e));
			}
			// PATCH судьбу SIGHUP не заполняет: состав не менялся, бейдж шапки
			// остаётся признаком по `running`.
			await reload();
		});
	}

	/**
	 * Перевыпуск — составная операция ровно из четырёх шагов (ia.md §3.3):
	 * добавить с новым паролем и тем же именем → выдать ссылку → удалить старую
	 * запись → перечитать список. Атомарности нет: частичный отказ показывается
	 * текстом TS-09 и «перевыпущено» не утверждает.
	 */
	function reissueUser(user: WdttPanelUserEntry) {
		reissueTarget = undefined;
		void locked(async () => {
			const before = users;
			const name = reissueName(user, users);
			let created: WdttPanelUsersStatus;
			try {
				created = await withTimeout(
					api.addWdttServerPanelUser(serverId, {
						comment: name,
						vkHash: user.vkHash || undefined,
						mainPassword,
					}),
					FETCH_TIMEOUT_MS,
				);
			} catch (e) {
				notifications.error(addErrorText(apiCode(e), errText(e)));
				await reload();
				return;
			}
			applyStatus(created);
			const pass = addedPassword(before, created.users ?? []);
			linkUser = (created.users ?? []).find((u) => u.password === pass);
			try {
				const st = await withTimeout(
					api.removeWdttServerPanelUser(serverId, user.password),
					FETCH_TIMEOUT_MS,
				);
				applyStatus(st);
			} catch (e) {
				// TS-09
				notifications.error(
					`Новый абонент создан, старую запись удалить не удалось: ${errText(e)}`,
				);
				await reload(true);
				return;
			}
			await reload(true);
			// TS-08
			notifications.success(`Абонент «${name}» перевыпущен — выдайте новую ссылку`);
		});
	}
</script>

<div class="clients">
	<div class="head">
		<Badge size="sm" variant={applied ? 'success' : 'warning'}>
			{applied ? 'применено сейчас' : 'применится при следующем запуске'}
		</Badge>
		<Button
			variant="secondary"
			size="sm"
			disabled={!canAdd}
			onclick={() => {
				addError = '';
				addOpen = true;
			}}
		>
			Добавить
		</Button>
		<Button variant="ghost" size="sm" loading={loading} onclick={() => reload()}>Обновить</Button>
	</div>

	{#if loadError && !users.length}
		<p class="empty">
			{loadError}
			<Button variant="secondary" size="sm" onclick={() => reload()}>Повторить</Button>
		</p>
	{:else if users.length}
		<ul class="list">
			{#each users as user (user.password)}
				<ServerClientRow
					{user}
					{users}
					{busy}
					onlink={(u) => (linkUser = u)}
					onreissue={(u) => (reissueTarget = u)}
					onremove={(u) => (removeTarget = u)}
					onrename={renameUser}
				/>
			{/each}
		</ul>
	{/if}

	{#if linkUser}
		<!--
			Панель — снимок абонента: её стартовые значения (peer, VK-хеши) и обе
			собранные ссылки считаются один раз, на пароль абонента. Смена абонента
			при открытой панели обязана пересоздать компонент, иначе заголовок
			покажет нового, а ссылки останутся собранными на пароль прежнего.
		-->
		{#key linkUser.password}
			<LinkPanel
				{serverId}
				{serverName}
				{server}
				user={linkUser}
				onclose={() => (linkUser = undefined)}
			/>
		{/key}
	{/if}

	{#if users.length}
		<p class="counter">
			<span>{counterLabel(users)}</span>
			<!-- SH-88 -->
			<FieldHint
				text="Отключённый абонент пишется в файл сервера и считается рабочим"
				ariaLabel="Подсказка: счётчик рабочих"
			/>
		</p>
	{/if}

	{#if !mainPassword}
		<p class="note">{CLIENT_TEXT.mainPasswordUnset}</p>
	{/if}
</div>

<ServerClientAddModal
	open={addOpen}
	{busy}
	error={addError}
	onsubmit={addUser}
	onclose={() => (addOpen = false)}
/>

<ConfirmModal
	open={removeTarget !== undefined}
	title="Удалить абонента?"
	message={`Абонент «${removeTarget?.comment || removeTarget?.password || ''}» будет удалён с сервера.`}
	secondary={removeTarget && autoCreateAfterRemove(removeTarget, users)
		? 'Ссылка этого абонента перестанет работать сразу после удаления. После удаления сервер заведёт абонента «Абонент 1» автоматически — иначе он не запустится.'
		: 'Ссылка этого абонента перестанет работать сразу после удаления.'}
	confirmLabel="Удалить"
	onConfirm={() => removeTarget && removeUser(removeTarget)}
	onClose={() => (removeTarget = undefined)}
/>

<ConfirmModal
	open={reissueTarget !== undefined}
	title="Перевыпустить абонента?"
	message={`Абонент «${reissueTarget?.comment || reissueTarget?.password || ''}» получит новый пароль и новую ссылку, старая запись будет удалена.`}
	secondary="Старая ссылка перестанет работать."
	confirmLabel="Перевыпустить"
	variant="primary"
	onConfirm={() => reissueTarget && reissueUser(reissueTarget)}
	onClose={() => (reissueTarget = undefined)}
/>

<style>
	.clients {
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

	.empty {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
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
		gap: 0.15rem;
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.note {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}
</style>
