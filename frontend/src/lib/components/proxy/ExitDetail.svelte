<script lang="ts">
	// Деталь вкладки «Выход» — одна колонка секций сверху вниз (ia.md §2.2).
	// Конфиг с прихода страницы — состояние сервера; правит пользователь
	// редактируемую копию, которая живёт здесь (W-22). Сохраняет её страница:
	// она владеет конфигами и статусами.
	import { untrack } from 'svelte';
	import { Badge, Button, Card, FieldHint } from '$lib/components/ui';
	import { ExternalLink } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { findPolicyForInterface } from '$lib/utils/accessPolicy';
	import { formatUptime } from '../freeturn/uptime';
	import type {
		AccessPolicy,
		FreeTurnClientConfig,
		FreeTurnProcessStatus,
		TunnelListItem,
		WdttClientConfig,
		WdttProcessStatus,
	} from '$lib/types';
	import AdvancedSection from './AdvancedSection.svelte';
	import CaptchaSection from './CaptchaSection.svelte';
	import DetailSection from './DetailSection.svelte';
	import ExitParamsSection from './ExitParamsSection.svelte';
	import LogSection from './LogSection.svelte';
	import RunBar from './RunBar.svelte';
	import SubscriptionSection from './SubscriptionSection.svelte';
	import { allowEnsure, markEnsured } from './ensureGuard';
	import { cloneConfig, type ExitConfig } from './exitConfig';
	import { findLinkedTunnel, listenPort } from './linkedTunnel';
	import type { ProxyInstanceRow } from './rows';

	interface Props {
		row: ProxyInstanceRow;
		status?: WdttProcessStatus | FreeTurnProcessStatus;
		/** Конфиг выбранного инстанса на сервере — ровно один из двух. */
		wdttClient?: WdttClientConfig;
		ftClient?: FreeTurnClientConfig;
		routerClock?: string;
		policies: AccessPolicy[];
		tunnels: TunnelListItem[];
		saving?: boolean;
		busy?: boolean;
		onstart: () => void;
		onstop: () => void;
		onwizard?: () => void;
		/** Сохранение: конфиг сервера после записи или null, если не сохранилось. */
		onsave: (config: ExitConfig) => Promise<ExitConfig | null>;
		onreload: () => Promise<void> | void;
	}

	let {
		row,
		status,
		wdttClient,
		ftClient,
		routerClock = '',
		policies,
		tunnels,
		saving = false,
		busy = false,
		onstart,
		onstop,
		onwizard,
		onsave,
		onreload,
	}: Props = $props();

	// Редактируемая копия конфига. Поллинг и перезагрузки страницы обновляют
	// prop-конфиг, а несохранённые правки живут здесь и не затираются (W-22).
	// Копия создаётся вместе с деталью, а деталь пересоздаётся при смене
	// инстанса ({#key} на странице, W-13).
	let wdttDraft = $state(untrack(() => (wdttClient ? cloneConfig(wdttClient) : undefined)));
	let ftDraft = $state(untrack(() => (ftClient ? cloneConfig(ftClient) : undefined)));

	const wdttStatus = $derived(row.protocol === 'wdtt' ? (status as WdttProcessStatus) : undefined);
	const raw = $derived(row.mode === 'raw');
	const running = $derived(row.state === 'running');
	const listen = $derived(wdttClient?.listen ?? ftClient?.listen ?? '');
	const port = $derived(listenPort(listen));

	// LS-10..12 — протокол и режим инстанса.
	const badge = $derived(
		row.protocol === 'freeturn' ? 'FreeTurn' : raw ? 'WDTT · Raw' : 'WDTT · WG',
	);

	// RB-06: локальный порт, аптайм, PID.
	const runMeta = $derived(
		[listen, formatUptime(row.startedAt), row.pid ? `PID ${row.pid}` : '']
			.filter(Boolean)
			.join(' · '),
	);

	const uptime = $derived(formatUptime(row.startedAt) || '—');

	// Правило имён ia.md §1.0: на странице NDMS-имя, kernel-имя — под (i).
	const rawNdms = $derived(wdttStatus?.ndmsIface?.trim() || wdttClient?.ndmsIface?.trim() || '');
	const rawKernel = $derived(wdttStatus?.rawIface?.trim() || wdttClient?.rawIface?.trim() || '');
	const tunnel = $derived(
		raw ? null : findLinkedTunnel(tunnels, listen, row.protocol === 'wdtt' ? row.id : undefined),
	);

	// Политика читается обратным поиском по составу политик: поля политики
	// в конфиге инстанса нет и не заводится (ia.md §2.2 п.4).
	const policyIface = $derived(raw ? rawNdms : (tunnel?.ndmsName ?? ''));
	const policy = $derived(policyIface ? findPolicyForInterface(policies, policyIface) : null);
	// Имя политики — как на «Маршрутизации» (accesspolicy/PolicyTable.svelte):
	// NDMS-имя Policy0 в веб-интерфейсе роутера пользователю не показывают.
	const policyLabel = $derived(policy ? policy.description || policy.name : '');

	const rawHint = $derived(
		'Режим Raw: клиент поднимает свой интерфейс, отдельный AWG-туннель не нужен. ' +
			'Интерфейс виден в AWG-туннелях с бейджем «WDTT Raw» и в «Маршрутизации».' +
			(rawKernel ? ` Kernel-имя: ${rawKernel}.` : ''),
	);

	// ─── Сохранение и откат правок.

	async function save(): Promise<boolean> {
		const draft = wdttDraft ?? ftDraft;
		if (!draft) return false;
		const sent = cloneConfig(draft);
		const stored = await onsave(sent);
		if (!stored) return false;
		// W-22: ответ ложится в копию, только если пользователь не правил поля
		// во время запроса — иначе его правки были бы затёрты.
		if (JSON.stringify(draft) === JSON.stringify(sent)) {
			if (wdttDraft) wdttDraft = cloneConfig(stored) as WdttClientConfig;
			else ftDraft = cloneConfig(stored) as FreeTurnClientConfig;
		}
		return true;
	}

	/** EX-24 «Отменить» — возврат к тому, что лежит на сервере. */
	function revert() {
		if (wdttClient) wdttDraft = cloneConfig(wdttClient);
		else if (ftClient) ftDraft = cloneConfig(ftClient);
	}

	/** EX-32: клиент стартует только после успешного сохранения. */
	async function saveAndStart() {
		if (await save()) onstart();
	}

	// ─── Автоповедения клиента (W-19, W-20): туннель заводится сам.

	let tunnelBusy = $state(false);

	async function ensureTunnel(manual = false) {
		// tunnelBusy держит и реентрантность: второй запрос ждать нечего.
		if (row.protocol !== 'wdtt' || tunnelBusy) return;
		const id = row.id;
		if (!allowEnsure(id, manual)) return;
		tunnelBusy = true;
		try {
			const res = raw ? await api.ensureWdttRawTunnel(id) : await api.ensureWdttWgTunnel(id);
			if (res.created) {
				markEnsured(id);
				// TS-01 — про туннель из журнала (режим WG). В raw заводится
				// запись «WDTT Raw», а не туннель, и говорит о ней бэкенд.
				if (raw) {
					if (res.message) notifications.success(res.message);
				} else {
					notifications.success(`Создан туннель «${res.tunnelName ?? ''}»`);
				}
				await onreload();
			} else if (res.tunnelId) {
				markEnsured(id);
			}
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			tunnelBusy = false;
		}
	}

	$effect(() => {
		const wg = wdttStatus?.wgConfig?.trim();
		const iface = wdttStatus?.rawIface?.trim() || wdttStatus?.ndmsIface?.trim();
		if (row.protocol !== 'wdtt' || !running) return;
		if (raw ? !iface : !wg) return;
		untrack(() => ensureTunnel());
	});

	async function importConf(conf: string) {
		tunnelBusy = true;
		try {
			const tun = await api.importConfig(
				conf,
				row.name,
				undefined,
				row.protocol === 'freeturn' ? row.id : undefined,
				row.protocol === 'wdtt' ? row.id : undefined,
			);
			markEnsured(row.id);
			notifications.success(`Создан туннель «${tun.name}»`);
			await onreload();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			tunnelBusy = false;
		}
	}
</script>

<Card padding="lg">
	<div class="head">
		<h2>{row.name}</h2>
		<Badge size="sm" variant={row.protocol === 'wdtt' ? 'accent' : 'purple'}>{badge}</Badge>
	</div>

	<RunBar
		state={row.state}
		meta={runMeta}
		{busy}
		{onstart}
		{onstop}
		{onwizard}
	/>

	<!-- EX-01: ошибка живёт, пока процесс не работает. -->
	{#if !running && status?.lastError}
		<div class="error-box">
			<p class="error-title">Ошибка последнего запуска</p>
			<pre>{status.lastError}</pre>
		</div>
	{/if}

	<DetailSection
		title="Нагрузка"
		hint="Истории трафика для прокси-процессов нет — менеджер её не собирает. Байты и скорость смотрите на карточке связанного AWG-туннеля."
	>
		<div class="stat-row">
			<div class="stat">
				<span class="stat-value">{status?.dtlsConnections ?? 0}</span>
				<span class="stat-label">соединений</span>
			</div>
			<div class="stat">
				<span class="stat-value">{uptime}</span>
				<span class="stat-label">в работе</span>
			</div>
		</div>
	</DetailSection>

	<!-- Секции нет, пока интерфейс клиента неизвестен: пустой заголовок ничего
	     не сообщает, а обещать «не заведён в политику» не о чем. -->
	{#if policyIface}
		<DetailSection title="Куда идёт трафик">
			{#if raw}
				<div class="line-row">
					<span class="line-label">В роутере:</span>
					<code>{rawNdms}</code>
					<FieldHint text={rawHint} ariaLabel="Подсказка: интерфейс клиента" />
				</div>
			{:else if tunnel && row.protocol === 'wdtt'}
				<!-- EX-09/EX-10 — строка режима WG, а режим есть только у WDTT. -->
				<div class="line-row">
					<span class="line-label">AWG-туннель:</span>
					<a class="link" href={`/tunnels/${tunnel.id}`}>{tunnel.name}<ExternalLink size={12} /></a>
					<FieldHint
						text={`Режим WG: клиент получает WireGuard-конфиг, из него создан AWG-туннель с Endpoint 127.0.0.1:${port ?? ''}.`}
						ariaLabel="Подсказка: AWG-туннель"
					/>
				</div>
			{/if}
			<div class="line-row">
				<span class="line-label">Политика доступа:</span>
				{#if policyLabel}
					<Badge size="sm" variant="success">{policyLabel}</Badge>
				{:else}
					<Badge size="sm" variant="muted">не заведён в политику</Badge>
				{/if}
				<FieldHint
					text="Запись в политике — кандидатура, а не назначение: интерфейс дописывается в конец её порядка, и трафик пойдёт через него, только если он окажется первым рабочим."
					ariaLabel="Подсказка: политика доступа"
				/>
				<Button variant="ghost" size="sm" href="/routing">
					Маршрутизация
					{#snippet iconAfter()}<ExternalLink size={12} />{/snippet}
				</Button>
			</div>
		</DetailSection>
	{/if}

	<ExitParamsSection
		bind:wdttClient={wdttDraft}
		bind:ftClient={ftDraft}
		{raw}
		{saving}
		onsave={save}
		onrevert={revert}
	/>

	<!-- Поллинг капчи не крутится у остановленного клиента: подтверждать
	     нечего, пока потоки не поднимаются. -->
	{#if row.protocol === 'freeturn' && running}
		<CaptchaSection clientId={row.id} />
	{/if}

	{#if wdttDraft?.sub?.trim()}
		<SubscriptionSection
			instanceId={row.id}
			bind:client={wdttDraft}
			onsaveandstart={saveAndStart}
			{onreload}
		/>
	{/if}

	<AdvancedSection
		bind:wdttClient={wdttDraft}
		bind:ftClient={ftDraft}
		{raw}
		wgConf={wdttStatus?.wgConfig ?? ''}
		ports={listen ? [{ listen }] : []}
		onensuretunnel={() => ensureTunnel(true)}
		onimportconf={importConf}
		busyTunnel={tunnelBusy}
	/>

	<LogSection
		log={status?.log}
		{routerClock}
		showDebug={row.protocol === 'freeturn'}
		debug={ftDraft?.debug ?? false}
		ondebug={(on) => {
			if (ftDraft) ftDraft.debug = on;
		}}
	/>
</Card>

<style>
	.head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		min-width: 0;
		margin-bottom: 0.75rem;
	}

	h2 {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.error-box {
		border: 1px solid var(--color-error-border);
		background: var(--color-error-tint);
		border-radius: var(--radius);
		padding: 0.625rem 0.75rem;
		margin-top: 0.75rem;
	}

	.error-title {
		margin: 0 0 0.375rem;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-error);
	}

	.error-box pre {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		white-space: pre-wrap;
		word-break: break-word;
	}

	.stat-row {
		display: flex;
		gap: 2rem;
		flex-wrap: wrap;
	}

	.stat {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.stat-value {
		font-size: 1.25rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.stat-label {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.line-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-bottom: 0.375rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.line-label {
		color: var(--color-text-muted);
	}

	.link {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		color: var(--color-accent);
		font-size: 0.8125rem;
		text-decoration: none;
	}

	.link:hover {
		text-decoration: underline;
	}

	code {
		font-family: var(--font-mono);
		font-size: 0.78em;
		background: var(--color-bg-tertiary);
		padding: 0.05rem 0.3rem;
		border-radius: 4px;
		word-break: break-all;
	}


</style>
