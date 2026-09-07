<script lang="ts">
	// Модалка добавления абонента FreeTurn (Дополнение №4 п.1): та же форма, что
	// у WDTT, — окно по кнопке шапки, а не inline. Поля свои, решение об
	// отправке — у владельца списка.
	import { RefreshCw } from 'lucide-svelte';
	import { Button, IconButton, Input, Modal, Toggle } from '$lib/components/ui';
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
		onsubmit: (values: AddClientValues) => void;
		onclose: () => void;
	}

	let { open, busy = false, error = '', serverListenPort, onsubmit, onclose }: Props = $props();

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
<Modal {open} title="Новый абонент" size="sm" closeOnBackdrop={false} {onclose}>
	<div class="add-form">
		<div class="field-with-btn">
			<Input label="Client ID" bind:value={clientId} fullWidth />
			<IconButton
				size="sm"
				ariaLabel="Обновить Client ID"
				onclick={() => (clientId = randomClientId())}
			>
				<RefreshCw size={14} />
			</IconButton>
		</div>
		<Input label="Имя абонента" bind:value={name} fullWidth />
		<ShareWizardPeer
			endpointPort={9000}
			{serverListenPort}
			createLabel="Создать нового под абонента"
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
			<p class="add-error" role="alert">Укажите локальный порт FreeTurn-клиента абонента</p>
		{:else if confMissing && !peerLoading}
			<p class="add-error" role="alert">
				{confError || 'Конфиг пира не получен — без него ссылка абоненту неполная'}
			</p>
		{/if}
		<Toggle
			label="Внести в список разрешённых"
			checked={allow}
			onchange={(v) => (allow = v)}
		/>
		{#if error}
			<p class="add-error" role="alert">{error}</p>
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="secondary" size="md" disabled={busy} onclick={onclose}>Отменить</Button>
		<Button variant="primary" size="md" disabled={!canSubmit} loading={busy} onclick={submit}>
			Добавить
		</Button>
	{/snippet}
</Modal>

<style>
	.add-form {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
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

	.add-error {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-error);
	}
</style>
