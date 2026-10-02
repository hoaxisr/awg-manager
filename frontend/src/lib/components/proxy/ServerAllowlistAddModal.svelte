<script lang="ts">
	// Модалка добавления абонента FreeTurn (Дополнение №4 п.1): та же форма, что
	// у WDTT, — окно по кнопке шапки, а не inline. Поля свои, решение об
	// отправке — у владельца списка.
	import { m } from '$lib/i18n';
	import { RefreshCw } from 'lucide-svelte';
	import { Button, IconButton, Input, Modal, Toggle } from '$lib/components/ui';
	import LinkBox from './LinkBox.svelte';
	import ShareWizardPeer, { NEW_PEER } from './ShareWizardPeer.svelte';

	/** Что уходит владельцу: пир либо NEW_PEER (создать под абонента, #871). */
	export interface AddClientValues {
		clientId: string;
		name: string;
		allow: boolean;
		peer: string;
		/** .conf выбранного пира с локальным Endpoint; у NEW_PEER пуст. */
		peerConf: string;
		/** Локальный порт FreeTurn-клиента для Endpoint создаваемого пира. */
		localPort: number;
	}

	interface Props {
		open: boolean;
		/** Общий замок мутаций сервера занят — отправлять нечего. */
		busy?: boolean;
		/** Отказ последней попытки: печатается здесь, у полей, которых он касается. */
		error?: string;
		/** listen-порт WG-сервера, на который смотрит `-connect` раздачи. */
		serverListenPort?: number;
		/**
		 * Адрес из настроек раздачи, который уедет в ссылку (#933). Пусто —
		 * бэкенд подставит внешний IP роутера. Показываем ДО выдачи: узнать,
		 * что в ссылке не тот адрес, после отправки её абоненту — поздно.
		 */
		linkPeer?: string;
		/**
		 * Ссылка абоненту: непустая — окно показывает её вместо формы (#919).
		 * Так она попадается на глаза там, где владелец и стоит, а не уезжает
		 * вниз страницы. Тем же экраном открывается «Ссылка» в строке списка.
		 */
		link?: string;
		/** Чья ссылка показана: имя абонента либо его Client ID. */
		linkFor?: string;
		onsubmit: (values: AddClientValues) => void;
		onclose: () => void;
	}

	let {
		open,
		busy = false,
		error = '',
		serverListenPort,
		linkPeer = '',
		link = '',
		linkFor = '',
		onsubmit,
		onclose,
	}: Props = $props();

	/** Client ID придумывает фронт: бэкенд в ответе лишь возвращает присланный. */
	function randomClientId(): string {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	let clientId = $state(randomClientId());
	let name = $state('');
	let allow = $state(true);
	// Виджет пира живёт внутри окна и пересоздаётся с каждым открытием: сброса не нужно.
	let peer = $state(NEW_PEER);
	let peerConf = $state('');
	let localPort = $state(0);
	let portUnknown = $state(false);
	let peerLoading = $state(false);
	let confError = $state('');

	// Существующий пир без .conf — ссылка ушла бы без WG (#871 — ровно про
	// неполные ссылки): ждём загрузку, у Keenetic-пира — вставку .conf.
	const confMissing = $derived(peer !== NEW_PEER && !peerConf.trim());
	const canSubmit = $derived(
		!!clientId.trim() && !busy && !portUnknown && localPort > 0 && !peerLoading && !confMissing,
	);

	// Закрытая модалка полей не хранит: следующее открытие начинается с чистой
	// формы и нового Client ID.
	$effect(() => {
		if (open) return;
		clientId = randomClientId();
		name = '';
		allow = true;
	});

	function submit() {
		if (!canSubmit) return;
		onsubmit({ clientId: clientId.trim(), name: name.trim(), allow, peer, peerConf, localPort });
	}
</script>

<!-- Клик по подложке форму не теряет: выход — «Отменить» или Esc. -->
<Modal
	{open}
	title={link ? m.proxy_allowlist_modal_link_title() : m.proxy_allowlist_modal_title()}
	size="sm"
	closeOnBackdrop={false}
	{onclose}
>
	{#if link}
		<div class="issued">
			<p class="issued-for">{m.proxy_link_panel_subscriber({ name: linkFor || '—' })}</p>
			<LinkBox {link} freeturn />
		</div>
	{:else}
		<div class="add-form">
			<div class="field-with-btn">
				<Input label="Client ID" bind:value={clientId} fullWidth />
				<IconButton
					size="sm"
					ariaLabel={m.proxy_allowlist_refresh_id()}
					onclick={() => (clientId = randomClientId())}
				>
					<RefreshCw size={14} />
				</IconButton>
			</div>
			<Input label={m.proxy_clients_name_label()} bind:value={name} fullWidth />
			<p class="link-peer-note">
				{#if linkPeer.trim()}
					{m.proxy_allowlist_link_peer()} <b>{linkPeer.trim()}</b>{#if !/:\d+$/.test(linkPeer.trim())}
						<!-- Порт дописывает бэкенд из listen раздачи: обещать точное
						     значение ссылки, не зная его, — полуправда. -->
						{m.proxy_allowlist_link_peer_port()}{/if}
				{:else}
					{m.proxy_allowlist_link_peer_default()}
				{/if}
			</p>
			<ShareWizardPeer
				endpointPort={9000}
				{serverListenPort}
				createLabel={m.proxy_allowlist_create_peer()}
				onconnect={() => {}}
				onpeerconf={(conf, err, unknown) => {
					peerConf = conf;
					confError = err;
					portUnknown = unknown;
				}}
				onpick={(v, port, loading) => {
					peer = v;
					localPort = port;
					peerLoading = loading;
				}}
			/>
			{#if portUnknown || localPort <= 0}
				<p class="add-error" role="alert">{m.proxy_allowlist_port_required()}</p>
			{:else if confMissing && !peerLoading}
				<p class="add-error" role="alert">
					{confError || m.proxy_allowlist_conf_missing()}
				</p>
			{/if}
			<Toggle
				label={m.proxy_share_wizard_allow()}
				checked={allow}
				onchange={(v) => (allow = v)}
			/>
			{#if !allow}
				<!-- Ссылку хранит запись списка (#919): без записи её негде показать
				     потом, и владелец должен знать это ДО отправки, а не после. -->
				<p class="add-note">{m.proxy_allowlist_no_entry_note()}</p>
			{/if}
			{#if error}
				<p class="add-error" role="alert">{error}</p>
			{/if}
		</div>
	{/if}
	{#snippet actions()}
		{#if link}
			<Button variant="primary" size="md" onclick={onclose}>{m.proxy_allowlist_done()}</Button>
		{:else}
			<Button variant="secondary" size="md" disabled={busy} onclick={onclose}>{m.proxy_common_cancel_action()}</Button>
			<Button variant="primary" size="md" disabled={!canSubmit} loading={busy} onclick={submit}>
				{m.proxy_common_add()}
			</Button>
		{/if}
	{/snippet}
</Modal>

<style>
	.link-peer-note {
		margin: 0;
		font-size: 0.8rem;
		color: var(--text-muted, #94a3b8);
	}

	.add-form,
	.issued {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.issued-for {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}


	.field-with-btn :global(svg) {
		display: block;
	}

	.add-error {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-error);
	}

	.add-note {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
</style>
