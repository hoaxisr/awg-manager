<script lang="ts">
	// Деталь вкладки «Выход» — одна колонка секций сверху вниз (ia.md §2.2).
	// Конфиг инстанса правится на месте: владелец конфига и его сохранения —
	// страница, здесь живут композиция секций и автоповедения клиента.
	import { untrack } from 'svelte';
	import { Badge, Card, FieldHint } from '$lib/components/ui';
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
	import { findLinkedTunnel, listenPort } from './linkedTunnel';
	import type { ProxyInstanceRow } from './rows';

	interface Props {
		row: ProxyInstanceRow;
		status?: WdttProcessStatus | FreeTurnProcessStatus;
		/** Конфиг выбранного инстанса — ровно один из двух. */
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
		onsave: () => Promise<void> | void;
		onrevert: () => void;
		onsaveandstart: () => Promise<void> | void;
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
		onrevert,
		onsaveandstart,
		onreload,
	}: Props = $props();

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
	const tunnel = $derived(raw ? null : findLinkedTunnel(tunnels, listen));

	// Политика читается обратным поиском по составу политик: поля политики
	// в конфиге инстанса нет и не заводится (ia.md §2.2 п.4).
	const policyIface = $derived(raw ? rawNdms : (tunnel?.ndmsName ?? ''));
	const policy = $derived(policyIface ? findPolicyForInterface(policies, policyIface) : null);

	const rawHint = $derived(
		'Режим Raw: клиент поднимает свой интерфейс, отдельный AWG-туннель не нужен. ' +
			'Интерфейс виден в AWG-туннелях с бейджем «WDTT Raw» и в «Маршрутизации».' +
			(rawKernel ? ` Kernel-имя: ${rawKernel}.` : ''),
	);

	// ─── Автоповедения клиента (W-19, W-20): туннель заводится сам.

	let tunnelBusy = $state(false);
	let ensuring = false;
	const settled = new Set<string>();
	const cooldown = new Map<string, number>();

	async function ensureTunnel(manual = false) {
		if (row.protocol !== 'wdtt' || ensuring) return;
		const id = row.id;
		if (manual) cooldown.delete(id);
		else if (settled.has(id)) return;
		const now = Date.now();
		// Кулдаун 20 с: на ошибке id не помечается settled, иначе поллинг
		// долбил бы POST (и тост) каждые две секунды.
		if (!manual && now - (cooldown.get(id) ?? 0) < 20000) return;
		cooldown.set(id, now);
		ensuring = true;
		tunnelBusy = true;
		try {
			const res = raw ? await api.ensureWdttRawTunnel(id) : await api.ensureWdttWgTunnel(id);
			if (res.created) {
				settled.add(id);
				notifications.success(`Создан туннель «${res.tunnelName ?? ''}»`);
				await onreload();
			} else if (res.tunnelId) {
				settled.add(id);
			}
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			ensuring = false;
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
			settled.add(row.id);
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

	<DetailSection title="Куда идёт трафик">
		{#if raw}
			{#if rawNdms}
				<div class="line-row">
					<span class="line-label">В роутере:</span>
					<code>{rawNdms}</code>
					<FieldHint text={rawHint} ariaLabel="Подсказка: интерфейс клиента" />
				</div>
			{/if}
		{:else if tunnel}
			<div class="line-row">
				<span class="line-label">AWG-туннель:</span>
				<a class="link" href={`/tunnels/${tunnel.id}`}>{tunnel.name}<ExternalLink size={12} /></a>
				<FieldHint
					text={`Режим WG: клиент получает WireGuard-конфиг, из него создан AWG-туннель с Endpoint 127.0.0.1:${port ?? ''}.`}
					ariaLabel="Подсказка: AWG-туннель"
				/>
			</div>
		{/if}
		{#if policyIface}
			<div class="line-row">
				<span class="line-label">Политика доступа:</span>
				{#if policy}
					<Badge size="sm" variant="success">{policy}</Badge>
				{:else}
					<Badge size="sm" variant="muted">не заведён в политику</Badge>
				{/if}
				<FieldHint
					text="Запись в политике — кандидатура, а не назначение: интерфейс дописывается в конец её порядка, и трафик пойдёт через него, только если он окажется первым рабочим."
					ariaLabel="Подсказка: политика доступа"
				/>
				<a class="link" href="/routing">Маршрутизация<ExternalLink size={12} /></a>
			</div>
		{/if}
	</DetailSection>

	<ExitParamsSection {wdttClient} {ftClient} {saving} {onsave} {onrevert} />

	{#if row.protocol === 'freeturn'}
		<CaptchaSection clientId={row.id} />
	{/if}

	{#if wdttClient?.sub?.trim()}
		<SubscriptionSection
			instanceId={row.id}
			client={wdttClient}
			{onsaveandstart}
			{onreload}
		/>
	{/if}

	<AdvancedSection
		{wdttClient}
		{ftClient}
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
		debug={ftClient?.debug ?? false}
		ondebug={(on) => {
			if (ftClient) ftClient.debug = on;
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
