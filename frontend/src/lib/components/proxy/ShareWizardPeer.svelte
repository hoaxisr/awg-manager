<script lang="ts">
	// Выбор WG-сервера роутера и пира для FreeTurn-сервера (WS-22..WS-25, WS-28).
	// Механизм — тот же, что у виджета детали (`freeturn/ServerWgBind.svelte`):
	// каталог `serverPeerOptions`, `.conf` пира из API и подстановка локального
	// Endpoint. Свой компонент нужен ради подписей: у виджета детали и абзац, и
	// подпись порта, и предупреждение Keenetic — легаси-строки, а в мастере
	// каждая видимая строка обязана быть строкой микрокопии.
	import { onMount, untrack } from 'svelte';
	import { Dropdown, Input } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { servers, type ServersSnapshot } from '$lib/stores/servers';
	import {
		buildRunningServerPeerDropdownOptions,
		decodeServerPeerValue,
		patchWgConfEndpoint,
		resolveServerListenPort,
	} from '$lib/utils/serverPeerOptions';
	import ConfPasteBox from './ConfPasteBox.svelte';

	interface Props {
		/** Локальный порт FreeTurn-клиента этого роутера — дефолт Endpoint (F-18). */
		endpointPort: number;
		/** `-connect` выбранного сервера: `127.0.0.1:<listenPort>`. */
		onconnect: (addr: string) => void;
		/** `.conf` пира с подставленным Endpoint — он уезжает в ссылку абоненту. */
		onpeerconf: (conf: string) => void;
	}

	let { endpointPort, onconnect, onpeerconf }: Props = $props();

	let snap = $state<ServersSnapshot | null>(null);
	let selected = $state('');
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

	const options = $derived(buildRunningServerPeerDropdownOptions(snap));

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
		untrack(() => onpeerconf(src && ok ? patchWgConfEndpoint(src, localPort) : ''));
	});

	async function pick(value: string) {
		selected = value;
		manualConf = '';
		fetchedConf = '';
		if (!value || !snap) return;
		loading = true;
		try {
			const { kind, serverId, pubkey } = decodeServerPeerValue(value);
			const listen = resolveServerListenPort(snap, kind, serverId);
			if (!listen) {
				notifications.error('Не удалось определить listenPort сервера');
				return;
			}
			onconnect(`127.0.0.1:${listen}`);
			const peer = snap.servers
				?.find((s) => s.id === serverId)
				?.peers?.find((p) => p.publicKey === pubkey);
			keenetic = kind === 'system' && peer?.confAvailable !== true;
			if (keenetic) return;
			fetchedConf =
				kind === 'system'
					? await api.getSystemServerPeerConf(serverId, pubkey)
					: await api.getManagedPeerConf(serverId, pubkey);
		} catch (e) {
			notifications.error(errText(e));
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
