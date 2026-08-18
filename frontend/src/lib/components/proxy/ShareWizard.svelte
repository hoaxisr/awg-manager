<script lang="ts">
	// Мастер «Настроить раздачу» (ia.md §3.4): протокол → параметры сервера →
	// первый абонент → ссылка. Решения шагов — в `shareWizard.ts`, экраны здесь.
	import { untrack } from 'svelte';
	import { Badge, Button, Card, FieldHint, Input, Toggle } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { listenPortNumber } from '$lib/utils/listenPortUtils';
	import LinkBox from './LinkBox.svelte';
	import ShareWizardParams from './ShareWizardParams.svelte';
	import WizardSteps from './WizardSteps.svelte';
	import { cloneConfig } from './exitConfig';
	import { addErrorText, apiErrorCode } from './serverClients';
	import {
		commitShareWizard,
		nextSharePort,
		randomHex,
		shareStep2Ready,
		wdttCardBlock,
		DEFAULT_FT_PORT,
		DEFAULT_WDTT_PORT,
		type ShareWizardClient,
		type ShareWizardFields,
	} from './shareWizard';
	import type { ProxyInstanceRow, ProxyProtocol } from './rows';
	import type { FreeTurnServerConfig, WdttServerConfig } from '$lib/types';

	interface Props {
		/** WDTT-сервер на роутере уже есть: второй бэкенд не примет (W-10). */
		wdttServerExists: boolean;
		/** Собран ли wdtt-server под процессор роутера; undefined — статус не загружен (W-03). */
		serverSupported?: boolean;
		/** Локальный порт FreeTurn-клиента этого роутера — дефолт Endpoint (F-18). */
		ftClientPort: number;
		/** Занятые порты серверов раздачи — подсказка порта новому серверу. */
		usedPorts: number[];
		/** Сервер, открытый кнопкой «Мастер»: мастер настраивает его, а не заводит новый. */
		row?: ProxyInstanceRow | null;
		wdttServer?: WdttServerConfig;
		ftServer?: FreeTurnServerConfig;
		/** WS-02: выход из мастера. */
		onclose: () => void;
		/**
		 * Перечитать список инстансов: сервер, созданный прошлой попыткой, при
		 * смене протокола остаётся на бэкенде — в списке страницы он обязан быть
		 * виден, а занятая им карточка WDTT — закрыться (WS-11).
		 */
		onreload?: () => Promise<void> | void;
		/** Сервер настроен и запущен — страница уводит в его деталь. */
		ondone: (protocol: ProxyProtocol, id: string) => Promise<void> | void;
	}

	let {
		wdttServerExists,
		serverSupported,
		ftClientPort,
		usedPorts,
		row = null,
		wdttServer,
		ftServer,
		onclose,
		onreload,
		ondone,
	}: Props = $props();

	const STEPS = ['Протокол', 'Параметры сервера', 'Первый абонент', 'Ссылка'];

	let step = $state(0);
	let busy = $state(false);

	// Карточка WDTT занята своим же инстансом, если мастер открыт ради него:
	// он и есть тот единственный сервер, второго никто не заводит.
	const wdttBlock = $derived(
		wdttCardBlock({
			serverExists: wdttServerExists && row?.protocol !== 'wdtt',
			serverSupported,
		}),
	);

	let protocol = $state<ProxyProtocol>(
		untrack(() =>
			row?.protocol ??
			(wdttCardBlock({ serverExists: wdttServerExists, serverSupported }) ? 'freeturn' : 'wdtt'),
		),
	);

	let fields = $state<ShareWizardFields>(
		untrack(() => ({
			password: wdttServer?.password ?? '',
			port: String(
				wdttServer
					? listenPortNumber(wdttServer.listen ?? '', DEFAULT_WDTT_PORT)
					: ftServer
						? listenPortNumber(ftServer.listen ?? '', DEFAULT_FT_PORT)
						: nextSharePort(protocol, usedPorts),
			),
			firewall: (wdttServer ?? ftServer)?.openFirewall !== false,
			connect: ftServer?.connect ?? '',
			obfProfile: ftServer?.obfProfile ?? 'none',
			obfKey: ftServer?.obfKey ?? '',
		})),
	);

	/** Дефолт имени первого абонента — только у WDTT (ia.md §3.4). */
	const WDTT_CLIENT_NAME = 'Абонент 1';

	let client = $state<ShareWizardClient>(
		untrack(() => ({
			name: protocol === 'wdtt' ? WDTT_CLIENT_NAME : '',
			password: '',
			vkHash: '',
			clientId: randomHex(16),
			allow: true,
		})),
	);

	let peer = $state('');
	let peerConf = $state('');
	let wanBusy = $state(false);

	// Сервер, созданный этим мастером, и заведённый им абонент: повтор после
	// отказа правит их, а не плодит вторых. Протокол хранится вместе с сервером:
	// после смены протокола этот сервер — чужой, и слать в него правки нельзя.
	let created = $state<{
		id: string;
		protocol: ProxyProtocol;
		config: WdttServerConfig | FreeTurnServerConfig;
	} | null>(null);
	let addedClientPassword = $state('');

	let link = $state('');
	let linkQwdtt = $state('');
	/** WS-44: запуск прошёл, ссылка не собралась — адреса сервера нет. */
	let linkMissing = $state(false);

	const blocked = $derived(protocol === 'wdtt' && !!wdttBlock);
	const step2Ready = $derived(shareStep2Ready({ protocol, ...fields }));
	const canNext = $derived(step === 0 ? !blocked : step === 1 ? step2Ready : true);
	const hasLink = $derived(!!link || !!linkQwdtt);

	/** Мастер настраивает открытый инстанс, только если протокол не сменили. */
	function existing() {
		if (created) return created.protocol === protocol ? created : undefined;
		const cfg = protocol === 'wdtt' ? wdttServer : ftServer;
		if (!row || row.protocol !== protocol || !cfg) return undefined;
		return { id: row.id, config: cloneConfig(cfg) };
	}

	function pickProtocol(p: ProxyProtocol) {
		if (p === protocol) return;
		const orphan = created && created.protocol !== p;
		protocol = p;
		fields.port = String(nextSharePort(p, usedPorts));
		// Имя абонента правит пользователь: сбрасываем только нетронутый дефолт.
		if (!client.name.trim() || client.name === WDTT_CLIENT_NAME) {
			client.name = p === 'wdtt' ? WDTT_CLIENT_NAME : '';
		}
		// Сервер прошлой попытки остаётся на бэкенде — показываем его в списке
		// страницы, а не умалчиваем.
		if (orphan) void onreload?.();
	}

	async function fillWan() {
		wanBusy = true;
		try {
			peer = await api.getWANIP();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			wanBusy = false;
		}
	}

	async function run(withLink: boolean) {
		busy = true;
		linkMissing = false;
		try {
			// FreeTurn собирает ссылку из конфига пира: без него абонент получил
			// бы ссылку без WG-конфига и не понял бы, чего в ней не хватает.
			// Сервер запускаем, ссылку — нет (WS-44).
			const wantLink = withLink && (protocol === 'wdtt' || !!peerConf.trim());
			const res = await commitShareWizard({
				protocol,
				fields,
				client,
				withLink: wantLink,
				peer,
				peerConf,
				existing: existing(),
				oncreated: (c) => (created = { ...c, protocol }),
				addedClientPassword: addedClientPassword || undefined,
				onclientadded: (p) => (addedClientPassword = p),
			});
			if (!withLink) {
				await ondone(res.protocol, res.id);
				return;
			}
			link = res.link;
			linkQwdtt = res.linkQwdtt;
			linkMissing = !res.link && !res.linkQwdtt;
		} catch (e) {
			// Отказ этапа абонента говорит строками микрокопии (TS-13..TS-16,
			// SH-26); остальные этапы отдают текст бэкенда как есть.
			notifications.error(addErrorText(apiErrorCode(e), errText(e)));
		} finally {
			busy = false;
		}
	}

	/** Выход: если сервер уже заведён, уводим в его деталь, а не в пустоту. */
	function leave() {
		if (created) void ondone(created.protocol, created.id);
		else onclose();
	}
</script>

<Card padding="lg">
	<div class="head">
		<h2>Настроить раздачу</h2>
		<Button variant="ghost" onclick={leave}>← К списку</Button>
	</div>

	<WizardSteps steps={STEPS} current={step} {canNext} ongo={(i) => (step = i)}>
		{#if step === 0}
			<div class="choices">
				<!-- Заблокированная карточка не выбирается: выбрать её означало бы
				     упереться в погасшую «Дальше» без объяснения. -->
				<button
					type="button"
					class="choice"
					class:selected={protocol === 'wdtt'}
					class:blocked={!!wdttBlock}
					disabled={!!wdttBlock}
					onclick={() => pickProtocol('wdtt')}
				>
					<span class="choice-head">
						<span class="choice-title">WDTT</span>
						{#if wdttBlock}
							<Badge size="sm" variant="warning">{wdttBlock.badge}</Badge>
						{/if}
					</span>
					<span class="choice-text">Абоненты подключаются по паролю</span>
				</button>

				<button
					type="button"
					class="choice"
					class:selected={protocol === 'freeturn'}
					onclick={() => pickProtocol('freeturn')}
				>
					<span class="choice-head">
						<span class="choice-title">FreeTurn</span>
					</span>
					<span class="choice-text">Абоненты подключаются по Client ID</span>
				</button>
			</div>

			{#if wdttBlock}
				<p class="note">
					{wdttBlock.note}<FieldHint
						text={wdttBlock.info}
						ariaLabel="Подсказка: раздача WDTT"
					/>
				</p>
			{/if}
		{:else if step === 1}
			<ShareWizardParams
				{protocol}
				bind:fields
				endpointPort={ftClientPort}
				onpeerconf={(conf) => (peerConf = conf)}
			/>
		{:else if step === 2}
			{#if protocol === 'wdtt'}
				<p class="lead">
					Ссылку получит этот абонент<FieldHint
						text="Ссылка выдаётся на пароль абонента, а не на главный пароль сервера. Без единого рабочего пароля сервер не запустится: если не завести абонента здесь, сервер заведёт «Абонент 1» сам."
						ariaLabel="Подсказка: первый абонент"
					/>
				</p>
				<div class="grid">
					<Input label="Имя абонента" bind:value={client.name} fullWidth />
					<Input
						label="Пароль абонента"
						hint="Пусто — сгенерируется"
						bind:value={client.password}
						fullWidth
					/>
					<Input label="VK-хеш" hint="По желанию" bind:value={client.vkHash} fullWidth />
				</div>
			{:else}
				<div class="grid">
					<Input label="Client ID" bind:value={client.clientId} fullWidth />
					<!-- SH-39: у абонента имя, а не комментарий (Дополнение №4 п.2). -->
					<Input label="Имя абонента" bind:value={client.name} fullWidth />
				</div>
				<div class="toggle-row">
					<Toggle
						label="Внести в список разрешённых"
						checked={client.allow}
						onchange={(v) => (client.allow = v)}
					/>
				</div>
			{/if}
		{:else}
			{#if !hasLink}
				<div class="grid">
					<Input
						label="Адрес сервера для абонента"
						hint="Пусто — подставится сам"
						bind:value={peer}
						fullWidth
					/>
				</div>
				<div class="btn-row">
					<Button variant="secondary" size="sm" loading={wanBusy} onclick={fillWan}>WAN IP</Button>
				</div>
				{#if linkMissing}
					<p class="warn">Ссылку соберём после запуска: не хватает адреса сервера</p>
				{/if}
			{:else}
				<div class="links">
					{#if linkQwdtt}
						<LinkBox link={linkQwdtt} title="Для приложения на телефоне" />
					{/if}
					{#if link}
						<LinkBox
							{link}
							title={protocol === 'wdtt' ? 'Для клиента на роутере' : ''}
							freeturn={protocol === 'freeturn'}
						/>
					{/if}
					<p class="note">
						Вставьте ссылку во вкладку «Выход» на другом роутере или в приложение на телефоне
					</p>
				</div>
			{/if}
		{/if}

		{#snippet finish()}
			{#if !hasLink}
				<Button variant="secondary" loading={busy} disabled={!step2Ready} onclick={() => run(false)}>
					Только запустить
				</Button>
				<Button variant="primary" loading={busy} disabled={!step2Ready} onclick={() => run(true)}>
					Запустить и выдать ссылку
				</Button>
			{/if}
		{/snippet}
	</WizardSteps>
</Card>

<style>
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 1rem;
		flex-wrap: wrap;
	}

	h2 {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.choices {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
		gap: 0.75rem;
	}

	.choice {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		padding: 0.875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-tertiary);
		text-align: left;
		cursor: pointer;
	}

	.choice.selected {
		border-color: var(--color-accent);
	}

	.choice.blocked {
		opacity: 0.7;
	}

	.choice:disabled {
		cursor: default;
	}

	.choice-head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.choice-title {
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.choice-text {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.lead {
		margin: 0 0 0.875rem;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		line-height: 1.6;
	}

	.note {
		margin: 0.75rem 0 0;
		font-size: 0.75rem;
		color: var(--color-text-muted);
		line-height: 1.6;
	}

	.warn {
		margin: 0.75rem 0 0;
		font-size: 0.8125rem;
		color: var(--color-warning, #d97706);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
	}

	.btn-row {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.625rem;
	}

	.toggle-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.875rem;
	}

	.links {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}
</style>
