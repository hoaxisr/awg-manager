<script module lang="ts">
	/** Значение опции «создать нового пира» (#871): создаёт владелец при отправке. */
	export const NEW_PEER = '\0new';
</script>

<script lang="ts">
	// Выбор WG-сервера роутера и пира для FreeTurn-сервера (WS-22..WS-25, WS-28).
	// Каталог `serverPeerOptions`, `.conf` пира из API и подстановка локального
	// Endpoint. Тот же виджет служит модалке «Добавить» абонента (#871): там
	// пиры отфильтрованы по серверу `-connect` и есть опция «создать нового».
	import { onMount, untrack } from 'svelte';
	import { Dropdown, Input } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { servers, type ServersSnapshot } from '$lib/stores/servers';
	import {
		buildRunningServerPeerDropdownOptions,
		decodeServerPeerValue,
		findServerByListenPort,
		patchWgConfEndpoint,
		resolveServerListenPort,
	} from '$lib/utils/serverPeerOptions';
	import ConfPasteBox from './ConfPasteBox.svelte';

	interface Props {
		/** Локальный порт FreeTurn-клиента этого роутера — дефолт Endpoint (F-18). */
		endpointPort: number;
		/** `-connect` выбранного сервера: `127.0.0.1:<listenPort>`. */
		onconnect: (addr: string) => void;
		/**
		 * `.conf` пира с подставленным Endpoint — он уезжает в ссылку абоненту;
		 * текст отказа, если `.conf` запросить не удалось, и признак «порт клиента
		 * неизвестен»: без них шаг 4 звал бы вставить конфиг там, где поля вставки
		 * нет либо вставка ничего не решает.
		 */
		onpeerconf: (conf: string, confError: string, portUnknown: boolean) => void;
		/** Показывать пиров только сервера с этим listen-портом (`-connect` раздачи). */
		serverListenPort?: number;
		/** Подпись опции «создать нового пира» — она первая и выбрана по умолчанию. */
		createLabel?: string;
		/** Выбранное значение (в т.ч. NEW_PEER), локальный порт Endpoint и «.conf ещё грузится». */
		onpick?: (value: string, localPort: number, loading: boolean) => void;
	}

	let { endpointPort, onconnect, onpeerconf, serverListenPort, createLabel, onpick }: Props =
		$props();

	let snap = $state<ServersSnapshot | null>(null);
	// svelte-ignore state_referenced_locally -- стартовое значение выбора
	let selected = $state(createLabel ? NEW_PEER : '');
	let loading = $state(false);
	// svelte-ignore state_referenced_locally -- стартовое значение поля порта
	// Порт неизвестен (клиента FreeTurn на роутере нет или их несколько) —
	// поле пустое: подставлять чужой порт молча нельзя.
	let port = $state(endpointPort > 0 ? String(endpointPort) : '');
	/** Пир заведён в Keenetic OS: приватного ключа у нас нет (WS-28). */
	let keenetic = $state(false);
	/** Конфиг, вставленный руками в ветке Keenetic. */
	let manualConf = $state('');
	/** .conf пира, полученный из API: он и есть источник для перепатчивания. */
	let fetchedConf = $state('');
	/** Отказ запроса .conf: пир не Keenetic, вставлять руками некуда. */
	let confError = $state('');
	/** listen сервера не определился: `-connect` этого пира собрать не из чего. */
	let listenUnknown = $state(false);

	const options = $derived.by(() => {
		const all = buildRunningServerPeerDropdownOptions(snap);
		const own = findServerByListenPort(snap, serverListenPort ?? 0);
		// Порт задан (в т.ч. 0 — `-connect` пуст), а сервер не поднят — пиров нет:
		// чужой сервер в ссылке хуже пустого списка.
		const scoped =
			serverListenPort !== undefined
				? all.filter((o) => {
					const { kind, serverId } = decodeServerPeerValue(o.value);
					return kind === own?.kind && serverId === own?.serverId;
				})
			: all;
		return createLabel ? [{ value: NEW_PEER, label: createLabel }, ...scoped] : scoped;
	});

	onMount(() => {
		const unsub = servers.subscribe((st) => (snap = st.data));
		void servers.refetch();
		return unsub;
	});

	// Единственное место, откуда .conf уходит наверх: и полученный из API, и
	// вставленный руками пересобираются здесь. Порт правится ПОСЛЕ выбора пира —
	// значит источник обязан храниться, иначе Endpoint в ссылке остаётся со
	// старым портом (в мастере конфиг невидим, и заметить это негде).
	$effect(() => {
		const localPort = Number(port.trim()) || endpointPort;
		const src = (keenetic ? manualConf : fetchedConf).trim();
		// Без порта Endpoint собрался бы как `127.0.0.1:0` — конфиг с таким
		// адресом абоненту отдавать нельзя, лучше не отдавать конфиг вовсе.
		const ok = Number.isInteger(localPort) && localPort > 0 && localPort <= 65535;
		const conf = src && ok ? patchWgConfEndpoint(src, localPort) : '';
		const err = confError;
		// WS-48: конфиг пира есть, а порта для Endpoint нет — либо его не ввели,
		// либо не определился listen сервера. Вставка .conf тут ничего не чинит.
		const portUnknown = listenUnknown || (!!src && !ok);
		const sel = selected;
		const busy = loading;
		untrack(() => {
			onpeerconf(conf, err, portUnknown);
			onpick?.(sel, ok ? localPort : 0, busy);
		});
	});

	async function pick(value: string) {
		selected = value;
		manualConf = '';
		fetchedConf = '';
		confError = '';
		listenUnknown = false;
		if (!value || value === NEW_PEER || !snap) return;
		loading = true;
		try {
			const { kind, serverId, pubkey } = decodeServerPeerValue(value);
			const listen = resolveServerListenPort(snap, kind, serverId);
			if (!listen) {
				// `onconnect` не звали: адрес остался от прошлого выбора, и ссылка
				// собралась бы на чужой сервер. Причину уносим наверх (WS-48).
				listenUnknown = true;
				notifications.error('Не удалось определить listenPort сервера');
				return;
			}
			onconnect(`127.0.0.1:${listen}`);
			const peer = snap.servers
				?.find((s) => s.id === serverId)
				?.peers?.find((p) => p.publicKey === pubkey);
			keenetic = kind === 'system' && peer?.confAvailable !== true;
			if (keenetic) return;
			// Endpoint всё равно станет 127.0.0.1 — WAN-адрес бэкенду искать незачем.
			fetchedConf =
				kind === 'system'
					? await api.getSystemServerPeerConf(serverId, pubkey, '127.0.0.1')
					: await api.getManagedPeerConf(serverId, pubkey, '127.0.0.1');
		} catch (e) {
			// Ветка не-Keenetic: `keenetic` здесь уже false, поля вставки .conf
			// на экране нет — причину обязан унести наверх сам компонент.
			confError = errText(e);
			notifications.error(confError);
		} finally {
			loading = false;
		}
	}
</script>

<div class="peer-block">
	<p class="block-label">WG-сервер роутера</p>
	<p class="block-hint">Сюда FreeTurn отдаст трафик</p>

	<div class="grid">
		<Dropdown
			label="Пир"
			value={selected}
			options={options}
			placeholder={options.length ? 'Выберите…' : 'Нет поднятых WG-серверов с пирами'}
			disabled={!options.length || loading}
			onchange={(v) => void pick(v)}
			fullWidth
		/>
		<Input
			label="Порт"
			type="number"
			value={port}
			oninput={(v) => (port = v)}
			hint="Локальный порт FreeTurn-клиента, который смотрит на этот сервер"
			fullWidth
		/>
	</div>

	{#if keenetic}
		<p class="warn">Приватный ключ пира недоступен — вставьте .conf вручную</p>
		<ConfPasteBox label="Вставить клиентский .conf" bind:value={manualConf} />
	{/if}
</div>

<style>
	.peer-block {
		margin-top: 0.75rem;
	}

	.block-label {
		margin: 0;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.block-hint {
		margin: 0.125rem 0 0;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
		margin-top: 0.5rem;
	}

	.warn {
		margin: 0.875rem 0 0.5rem;
		font-size: 0.8125rem;
		color: var(--color-text-primary);
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid color-mix(in srgb, var(--color-warning, #d97706) 45%, var(--color-border));
		background: color-mix(in srgb, var(--color-warning, #d97706) 8%, var(--color-bg-primary));
	}
</style>
