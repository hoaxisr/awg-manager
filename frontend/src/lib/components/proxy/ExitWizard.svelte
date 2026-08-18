<script lang="ts">
	// Мастер «Вывести трафик» (ia.md §2.3): источник → параметры → куда
	// направить трафик. Протокол спрашивают только при ручном создании, из
	// ссылки он выводится схемой (WE-08). Решения шагов — в exitWizard.ts.
	import { untrack } from 'svelte';
	import { Button, Card, Dropdown, FieldHint } from '$lib/components/ui';
	import { ExternalLink } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { detectProxyLinkScheme } from '$lib/utils/proxyLinkScheme';
	import { linkHasBundledWg } from '$lib/utils/serverPeerOptions';
	import ConfPasteBox from './ConfPasteBox.svelte';
	import WizardSteps from './WizardSteps.svelte';
	import ExitWizardParams from './ExitWizardParams.svelte';
	import ExitWizardSource from './ExitWizardSource.svelte';
	import { cloneConfig } from './exitConfig';
	import { listenPort } from './linkedTunnel';
	import {
		commitExitWizard,
		emptyFields,
		exitStep1Ready,
		exitStep2Ready,
		fieldsFromFtConfig,
		fieldsFromFtPayload,
		fieldsFromWdttConfig,
		fieldsFromWdttPayload,
		nextLocalListen,
		policyPermitOrder,
		proxyTunnelName,
		resolveExitInterface,
		type ExitMode,
		type ExitProtocol,
		type ExitSourceKind,
	} from './exitWizard';
	import type { ProxyInstanceRow } from './rows';
	import type {
		AccessPolicy,
		FreeTurnClientConfig,
		FreeTurnLinkPayload,
		WdttClientConfig,
		WdttImportPayload,
		WdttSubscriptionPreview,
	} from '$lib/types';

	interface Props {
		policies: AccessPolicy[];
		/** Занятые локальные порты по протоколам — подсказка порта нового клиента. */
		usedListens: { wdtt: string[]; freeturn: string[] };
		/** Инстанс, открытый кнопкой «Мастер»: мастер правит его, а не заводит новый. */
		row?: ProxyInstanceRow | null;
		wdttClient?: WdttClientConfig;
		ftClient?: FreeTurnClientConfig;
		/** WE-02: выход из мастера. */
		onclose: () => void;
		/** Инстанс настроен и запущен — страница уводит в его деталь. */
		ondone: (protocol: ExitProtocol, id: string) => Promise<void> | void;
	}

	let { policies, usedListens, row = null, wdttClient, ftClient, onclose, ondone }: Props =
		$props();

	const STEPS = ['Источник', 'Параметры', 'Куда направить трафик'];

	let step = $state(0);
	let busy = $state(false);

	// Инстанс, созданный этим мастером: после отказа на любом следующем шаге
	// повторное «Сохранить и запустить» правит его, а не заводит второй.
	let created = $state<{ id: string; config: WdttClientConfig | FreeTurnClientConfig } | null>(
		null,
	);

	// ─── Шаг 1: источник.

	let link = $state('');
	let manual = $state(false);
	let decodeSeq = 0;
	let debounce: ReturnType<typeof setTimeout> | undefined;
	// Недопечатанная ссылка отвергается бэкендом на каждом вводе — тост держим
	// до конца ввода (blur/Enter) и показываем, только если ссылка так и не
	// разобралась.
	let pendingLinkError = $state('');

	let wdttPayload = $state<WdttImportPayload | null>(null);
	let ftPayload = $state<FreeTurnLinkPayload | null>(null);
	let subscription = $state<WdttSubscriptionPreview | null>(null);
	let profileIdx = $state('0');

	// Протокол и режим — состояние мастера: их задаёт разбор ссылки, ручной
	// выбор или инстанс, ради которого мастер открыли.
	let protocol = $state<ExitProtocol>(untrack(() => row?.protocol ?? 'wdtt'));
	let mode = $state<ExitMode>(untrack(() => row?.mode ?? 'wg'));

	// Стартовое значение: дальше поля живут своей жизнью и обновляются разбором
	// ссылки, а не приходящим конфигом (мастер пересоздаётся при смене инстанса).
	let fields = $state(
		untrack(() =>
			wdttClient
				? fieldsFromWdttConfig(wdttClient, row?.name ?? '')
				: ftClient
					? fieldsFromFtConfig(ftClient, row?.name ?? '')
					: emptyFields(candidateListen(protocol), protocol),
		),
	);

	/** WE-49: клиентский .conf, вставленный руками на шаге 3 (FreeTurn без WG). */
	let manualWg = $state('');

	const scheme = $derived(detectProxyLinkScheme(link));
	const profile = $derived<WdttImportPayload | null>(
		subscription ? (subscription.profiles[Number(profileIdx)] ?? null) : wdttPayload,
	);
	// WE-49: у FreeTurn-ссылки без WG-конфига источником становится .conf,
	// вставленный руками на шаге 3 — обещание WE-26.
	const linkWg = $derived(protocol === 'wdtt' ? profile?.wg : ftPayload?.wg);
	const wgConf = $derived(linkHasBundledWg(linkWg) ? linkWg : manualWg);
	const hasWg = $derived(linkHasBundledWg(wgConf));
	/** Ссылка WG-конфиг не принесла — шаг 3 предлагает вставить его руками. */
	const needsManualWg = $derived(protocol === 'freeturn' && !linkHasBundledWg(linkWg));
	const subUrl = $derived(
		subscription?.subUrl || (scheme === 'subscription' ? link.trim() : profile?.subUrl) || '',
	);

	const detected = $derived<ExitSourceKind>(
		scheme === 'unknown' && link.trim()
			? 'unknown'
			: subscription
				? 'subscription'
				: ftPayload
					? 'freeturn'
					: wdttPayload
						? 'wdtt'
						: 'none',
	);
	const profileOptions = $derived(
		(subscription?.profiles ?? []).map((p, i) => ({
			value: String(i),
			label: p.name?.trim() || p.peer,
		})),
	);

	const step1Ready = $derived(
		exitStep1Ready({ manual, protocol, peer: fields.peer, password: fields.password }),
	);
	const step2Ready = $derived(exitStep2Ready({ protocol, ...fields }));

	function candidateListen(p: ExitProtocol): string {
		return nextLocalListen(p === 'wdtt' ? usedListens.wdtt : usedListens.freeturn, p);
	}

	/** Ручку разбора выбирает схема ссылки: wdtt.DecodeLink чужих схем не знает. */
	async function decode() {
		const text = link.trim();
		const kind = detectProxyLinkScheme(text);
		if (!text || kind === 'unknown') {
			wdttPayload = null;
			ftPayload = null;
			subscription = null;
			pendingLinkError = '';
			return;
		}
		const seq = ++decodeSeq;
		pendingLinkError = '';
		try {
			if (kind === 'freeturn') {
				const payload = await api.decodeFreeTurnLink(text);
				if (seq !== decodeSeq) return;
				wdttPayload = null;
				subscription = null;
				ftPayload = payload;
				protocol = 'freeturn';
				mode = 'wg';
				fields = fieldsFromFtPayload(payload, candidateListen('freeturn'));
				return;
			}
			const res = await api.decodeWdttLink(text);
			if (seq !== decodeSeq) return;
			ftPayload = null;
			protocol = 'wdtt';
			if (res.subscription?.profiles.length) {
				subscription = res.subscription;
				wdttPayload = null;
				profileIdx = '0';
				applyProfile(res.subscription.profiles[0], true);
			} else if (res.profile) {
				subscription = null;
				wdttPayload = res.profile;
				applyProfile(res.profile, false);
			}
		} catch (e) {
			pendingLinkError = errText(e);
		}
	}

	/** Показать отложенную ошибку разбора: ввод завершён, ссылка так и не читается. */
	function showLinkError() {
		if (!pendingLinkError) return;
		notifications.error(pendingLinkError);
		pendingLinkError = '';
	}

	function applyProfile(payload: WdttImportPayload, fromSub: boolean) {
		mode = payload.connMode === 'raw' ? 'raw' : 'wg';
		fields = fieldsFromWdttPayload(payload, candidateListen('wdtt'), fromSub);
	}

	function onLinkInput(v: string) {
		link = v;
		pendingLinkError = '';
		clearTimeout(debounce);
		debounce = setTimeout(() => void decode(), 350);
	}

	async function readFile(file: File | undefined) {
		if (!file) return;
		const text = (await file.text()).trim();
		if (!text) return;
		link = text;
		manual = false;
		await decode();
		showLinkError();
	}

	function toggleManual() {
		manual = !manual;
		if (!manual) return;
		link = '';
		wdttPayload = null;
		ftPayload = null;
		subscription = null;
		fields = emptyFields(candidateListen(protocol), protocol);
	}

	// ─── Шаг 3: политика и запуск.

	let policy = $state('');

	const policyOptions = $derived([
		{ value: '', label: 'Не заводить в политику (сделаю сам)' },
		...policies.map((p) => ({ value: p.name, label: p.description || p.name })),
	]);

	const tunnelName = $derived(proxyTunnelName(protocol, fields.name));
	const port = $derived(listenPort(fields.listen) ?? '');
	/** Интерфейс, который можно завести в политику: у Raw — свой, у WG — туннель. */
	const willHaveIface = $derived(protocol === 'wdtt' || hasWg);

	async function saveAndStart() {
		busy = true;
		try {
			const config = wdttClient ?? ftClient;
			const res = await commitExitWizard({
				protocol,
				mode,
				fields,
				wdttPayload: profile,
				ftPayload,
				subUrl: subUrl || undefined,
				wgConf,
				existing:
					created ?? (row && config ? { id: row.id, config: cloneConfig(config) } : undefined),
				oncreated: (c) => (created = c),
			});
			if (policy) {
				const iface = await resolveExitInterface({
					protocol,
					id: res.id,
					mode,
					listen: fields.listen,
					tunnelId: res.tunnelId,
				});
				if (iface) {
					await api.permitPolicyInterface(policy, iface, policyPermitOrder(policies, policy));
				} else {
					// TS-23: имя интерфейса так и не появилось — в политику его
					// добавить нечем, и молчать об этом нельзя.
					notifications.error(
						'Не удалось добавить интерфейс в политику — добавьте его на странице «Маршрутизация»',
					);
				}
			}
			await ondone(res.protocol, res.id);
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			busy = false;
		}
	}
</script>

<Card padding="lg">
	<div class="head">
		<h2>Вывести трафик</h2>
		<Button variant="ghost" onclick={onclose}>← К списку</Button>
	</div>

	<WizardSteps
		steps={STEPS}
		current={step}
		canNext={step === 0 ? step1Ready : step2Ready}
		ongo={(i) => (step = i)}
	>
		{#if step === 0}
			<ExitWizardSource
				{link}
				{manual}
				{detected}
				{protocol}
				{mode}
				profiles={profileOptions}
				{profileIdx}
				ftClientId={ftPayload?.cid ?? ''}
				ftHasWg={hasWg}
				oninput={onLinkInput}
				oncommit={() => {
					clearTimeout(debounce);
					void decode().then(showLinkError);
				}}
				onfile={(f) => void readFile(f)}
				ontogglemanual={toggleManual}
				onprotocol={(p) => {
					protocol = p;
					// Подсказки порта и потоков — правила выбранного протокола.
					const blank = emptyFields(candidateListen(p), p);
					fields.listen = blank.listen;
					fields.workers = blank.workers;
				}}
				onmode={(m) => (mode = m)}
				onprofile={(idx) => {
					profileIdx = idx;
					const picked = subscription?.profiles[Number(idx)];
					if (picked) applyProfile(picked, true);
				}}
			/>
		{:else if step === 1}
			<ExitWizardParams {protocol} {mode} bind:fields />
		{:else}
			{#if needsManualWg}
				<!-- WE-26 обещает вставку .conf именно здесь. -->
				<ConfPasteBox label="Вставить клиентский .conf" bind:value={manualWg} />
			{/if}
			{#if willHaveIface}
				<div class="explain">
					{#if protocol === 'wdtt' && mode === 'raw'}
						<p>
							Клиент поднимет свой интерфейс в роутере.
							<FieldHint
								text="Режим Raw: отдельный AWG-туннель не нужен. Интерфейс виден в AWG-туннелях с бейджем «WDTT Raw» и в «Маршрутизации», переключатель в AWG управляет этим клиентом."
								ariaLabel="Подсказка: режим Raw"
							/>
						</p>
					{:else}
						<p>
							Будет создан AWG-туннель «{tunnelName}».
							<FieldHint
								text={`Режим WG: клиент получит WireGuard-конфиг, из него создастся AWG-туннель с Endpoint 127.0.0.1:${port}.`}
								ariaLabel="Подсказка: режим WG"
							/>
						</p>
					{/if}
				</div>

				<Dropdown
					label="Политика доступа"
					value={policy}
					options={policyOptions}
					onchange={(v) => (policy = v)}
					fullWidth
				/>
				{#if policy}
					<p class="note">
						Интерфейс будет добавлен в конец выбранной политики<FieldHint
							text="Запись в политике — кандидатура, а не назначение: интерфейс дописывается в конец её порядка, и трафик пойдёт через него, только если он окажется первым рабочим."
							ariaLabel="Подсказка: политика доступа"
						/>
					</p>
				{/if}
				<div class="routing-link">
					<Button variant="ghost" size="sm" href="/routing">
						Маршрутизация
						{#snippet iconAfter()}<ExternalLink size={12} />{/snippet}
					</Button>
				</div>
			{/if}
		{/if}

		{#snippet finish()}
			<Button variant="primary" loading={busy} disabled={!step2Ready} onclick={saveAndStart}>
				Сохранить и запустить
			</Button>
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

	.explain {
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-tertiary);
		margin-bottom: 1rem;
	}

	.explain p {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.6;
	}

	.note {
		margin: 0.625rem 0 0;
		font-size: 0.75rem;
		color: var(--color-text-muted);
		line-height: 1.6;
	}

	.routing-link {
		margin-top: 0.625rem;
	}
</style>
