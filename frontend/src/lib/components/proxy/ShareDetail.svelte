<script lang="ts">
	// Деталь вкладки «Раздача» — одна колонка секций сверху вниз (ia.md §3.2).
	// Конфиг с прихода страницы — состояние сервера; правит пользователь
	// редактируемую копию, которая живёт здесь (W-22). Сохраняет её страница:
	// она владеет конфигами и статусами.
	import { onMount, untrack } from 'svelte';
	import { Badge, Card, FieldHint, Toggle } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { createIngressMutationLock } from '$lib/utils/ingressMutation';
	import { formatUptime } from '../freeturn/uptime';
	import type { NatMode } from '$lib/utils/network';
	import type {
		AccessPolicy,
		FreeTurnProcessStatus,
		FreeTurnServerConfig,
		WdttProcessStatus,
		WdttServerConfig,
	} from '$lib/types';
	import DetailSection from './DetailSection.svelte';
	import LogSection from './LogSection.svelte';
	import RunBar from './RunBar.svelte';
	import ShareAdvancedSection from './ShareAdvancedSection.svelte';
	import ShareNetworkSection from './ShareNetworkSection.svelte';
	import Topology, { type TopologyInbound } from './Topology.svelte';
	import { cloneConfig } from './exitConfig';
	import { freeTurnServerPorts, natModeLabel, wdttServerPorts, type ShareConfig } from './shareConfig';
	import { ingressOn, nextIngressInterfaces, wdttIngressRefs } from './shareIngress';
	import type { ProxyInstanceRow } from './rows';

	interface Props {
		row: ProxyInstanceRow;
		status?: WdttProcessStatus | FreeTurnProcessStatus;
		/** Конфиг выбранного сервера на бэкенде — ровно один из двух. */
		wdttServer?: WdttServerConfig;
		ftServer?: FreeTurnServerConfig;
		routerClock?: string;
		/** Каталог политик роутера — для строки схемы «политика: …». */
		policies: AccessPolicy[];
		saving?: boolean;
		busy?: boolean;
		onstart: () => void;
		onstop: () => void;
		onwizard?: () => void;
		/** Перезапуск сервера формой: смена режима server.log (SH-68). */
		onrestart: () => Promise<void>;
		/** Сохранение: конфиг сервера после записи или null, если не сохранилось. */
		onsave: (config: ShareConfig) => Promise<ShareConfig | null>;
		onreload: () => Promise<void> | void;
	}

	let {
		row,
		status,
		wdttServer,
		ftServer,
		routerClock = '',
		policies,
		saving = false,
		busy = false,
		onstart,
		onstop,
		onwizard,
		onrestart,
		onsave,
		onreload,
	}: Props = $props();

	// Редактируемая копия конфига: поллинг обновляет prop, а несохранённые
	// правки живут здесь и не затираются (W-22). Деталь пересоздаётся при смене
	// инстанса ({#key} на странице).
	let wdttDraft = $state(untrack(() => (wdttServer ? cloneConfig(wdttServer) : undefined)));
	let ftDraft = $state(untrack(() => (ftServer ? cloneConfig(ftServer) : undefined)));

	// Общий замок мутаций сервера: одна операция за раз. Мутации ingress вдобавок
	// сериализуются своим замком — чтение-правка-запись настроек sing-box иначе
	// теряет запись (WS-22).
	const withIngressLock = createIngressMutationLock();
	let mutating = $state(false);

	let lanOptions = $state<{ value: string; label: string }[]>([]);
	let ingress = $state(false);

	const wdttStatus = $derived(row.protocol === 'wdtt' ? (status as WdttProcessStatus) : undefined);
	const running = $derived(row.state === 'running');
	const ports = $derived(
		wdttDraft ? wdttServerPorts(wdttDraft) : ftDraft ? freeTurnServerPorts(ftDraft) : [],
	);

	// RB-07: порты раздачи, аптайм, PID.
	const runMeta = $derived(
		[
			...ports.map((p) => (p.label ? `${p.label} :${p.port}` : p.listen)),
			formatUptime(row.startedAt),
			row.pid ? `PID ${row.pid}` : '',
		]
			.filter(Boolean)
			.join(' · '),
	);

	// Правило имён ia.md §1.0: на странице NDMS-имена, kernel-имена — под (i).
	const ndmsIface = $derived(wdttStatus?.ndmsIface?.trim() || '');
	const rawNdmsIface = $derived(wdttStatus?.rawNdmsIface?.trim() || '');
	const ndmsNames = $derived([ndmsIface, rawNdmsIface].filter(Boolean).join(' · '));
	// SH-19 рассказывает про обе половины: со старым бинарём NDMS-имя raw пусто,
	// и рассказывать не о чем — (i) не показывается.
	const ndmsHint = $derived(
		ndmsIface && rawNdmsIface
			? `Оба интерфейса сервера зарегистрированы в роутере: ${ndmsIface} — WireGuard-половина, ` +
					`${rawNdmsIface} — Raw-половина. Kernel-имена: ${wdttDraft?.wgIface ?? ''} и ` +
					`${wdttStatus?.rawIface ?? ''}.`
			: '',
	);

	const policyName = $derived(wdttDraft?.policy?.trim() ?? '');
	const policyLabel = $derived.by(() => {
		if (!policyName || policyName === 'none') return '';
		const p = policies.find((x) => x.name === policyName);
		return p ? p.description || p.name : policyName;
	});

	const lanLabel = $derived(
		(wdttDraft?.lanSegments ?? [])
			.map((s) => lanOptions.find((o) => o.value === s)?.label || s)
			.join(', '),
	);

	const inbound = $derived<TopologyInbound[]>(
		wdttDraft
			? ports
					.filter((p) => p.label === 'DTLS' || p.label === 'Raw')
					.map((p) => ({
						who: p.label === 'DTLS' ? 'Приложение на телефоне' : 'Клиент на роутере',
						how: `${p.label} :${p.port}`,
					}))
			: ports.map((p) => ({ who: 'FreeTurn-клиент', how: `:${p.port}` })),
	);

	const routerLines = $derived(
		wdttDraft
			? [
					ndmsIface ? `${ndmsIface} · WireGuard` : '',
					rawNdmsIface ? `${rawNdmsIface} · Raw` : '',
					`NAT: ${natModeLabel(wdttDraft.natMode)}`,
					lanLabel ? `LAN: ${lanLabel}` : '',
				].filter(Boolean)
			: [ftDraft?.connect ?? ''].filter(Boolean),
	);

	// ─── Применённое значение exposeToPolicies.
	//
	// Бэкенд его не отдаёт: в `ProcessStatus` такого поля нет. Признак локальный —
	// значение конфига на момент, когда сервер стартовал; бейдж SH-56 держится,
	// пока выбранное не совпало с ним.
	let exposeApplied = $state(untrack(() => wdttServer?.exposeToPolicies ?? false));
	let seenStartedAt = $state(untrack(() => row.startedAt ?? ''));

	$effect(() => {
		const startedAt = row.startedAt ?? '';
		if (startedAt === seenStartedAt) return;
		seenStartedAt = startedAt;
		untrack(() => {
			exposeApplied = wdttServer?.exposeToPolicies ?? false;
		});
	});

	onMount(async () => {
		try {
			const segs = await api.listManagedLANSegments();
			lanOptions = segs.map((s) => ({ value: s.name, label: s.label || s.name }));
		} catch {
			/* сегменты вторичны: без них остаётся выбор без подписей */
		}
		// WS-27: фактическое состояние тумблера — из настроек роутера sing-box.
		if (!wdttDraft) return;
		try {
			const settings = await api.singboxRouterGetSettings();
			ingress = ingressOn(settings.ingressInterfaces, wdttIngressRefs(wdttDraft, wdttStatus));
		} catch {
			/* нет ответа — тумблер остаётся выключенным */
		}
	});

	// ─── Мутации под общим замком.

	async function locked(fn: () => Promise<void>) {
		if (mutating) return;
		mutating = true;
		try {
			await fn();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			mutating = false;
		}
	}

	function setNat(mode: NatMode) {
		if (!wdttDraft || mode === (wdttDraft.natMode ?? 'full')) return;
		void locked(async () => {
			const res = await api.setWdttServerNATMode(row.id, mode);
			if (wdttDraft) wdttDraft.natMode = res.config.natMode ?? mode;
			await onreload();
		});
	}

	function setLan(segments: string[]) {
		if (!wdttDraft) return;
		void locked(async () => {
			const res = await api.setWdttServerLANSegments(row.id, segments);
			if (wdttDraft) wdttDraft.lanSegments = res.config.lanSegments ?? segments;
			await onreload();
		});
	}

	function setPolicy(policy: string) {
		if (!wdttDraft || policy === (wdttDraft.policy || 'none')) return;
		void locked(async () => {
			const res = await api.setWdttServerPolicy(row.id, policy);
			if (wdttDraft) wdttDraft.policy = res.config.policy ?? policy;
			await onreload();
		});
	}

	/** RB-09: тумблер живёт в настройках роутера sing-box, не в конфиге сервера. */
	function toggleIngress(on: boolean) {
		if (!wdttDraft) return;
		void locked(async () => {
			await withIngressLock(async () => {
				const settings = await api.singboxRouterGetSettings();
				const refs = wdttIngressRefs(wdttDraft as WdttServerConfig, wdttStatus);
				await api.singboxRouterPutSettings({
					...settings,
					ingressInterfaces: nextIngressInterfaces(settings.ingressInterfaces, refs, on),
				});
				ingress = on;
			});
		});
	}

	// ─── Сохранение и откат правок.

	async function save() {
		const draft = wdttDraft ?? ftDraft;
		if (!draft) return;
		const sent = cloneConfig(draft);
		// SH-68: режим server.log бэкенд на живом сервере не применяет —
		// перезапуск делает форма.
		const logModeChanged =
			!!wdttDraft && (wdttServer?.statsLog ?? 'ram') !== (wdttDraft.statsLog ?? 'ram');
		await locked(async () => {
			const stored = await onsave(sent);
			if (!stored) return;
			// W-22: ответ ложится в копию, только если пользователь не правил
			// поля во время запроса — иначе его правки были бы затёрты.
			if (JSON.stringify(draft) === JSON.stringify(sent)) {
				if (wdttDraft) wdttDraft = cloneConfig(stored) as WdttServerConfig;
				else ftDraft = cloneConfig(stored) as FreeTurnServerConfig;
			}
			if (logModeChanged && running) await onrestart();
		});
	}

	/** «Отменить» — возврат к тому, что лежит на сервере. */
	function revert() {
		if (wdttServer) wdttDraft = cloneConfig(wdttServer);
		else if (ftServer) ftDraft = cloneConfig(ftServer);
	}
</script>

<Card padding="lg">
	<div class="head">
		<h2>{row.name}</h2>
		<Badge size="sm" variant={row.protocol === 'wdtt' ? 'accent' : 'purple'}>
			{row.protocol === 'wdtt' ? 'WDTT-сервер' : 'FreeTurn-сервер'}
		</Badge>
	</div>

	<RunBar
		state={row.state}
		meta={runMeta}
		{busy}
		{onstart}
		{onstop}
		{onwizard}
		aside={wdttDraft ? ingressToggle : undefined}
	/>

	<DetailSection title="Схема">
		<Topology
			{inbound}
			name={row.name}
			{routerLines}
			policyLine={policyLabel ? `политика: ${policyLabel}` : ''}
			ingressLine={wdttDraft ? (ingress ? 'через sing-box' : 'напрямую') : ''}
		/>
		{#if ndmsNames}
			<div class="line-row">
				<span class="line-label">В роутере:</span>
				<code>{ndmsNames}</code>
				{#if ndmsHint}
					<FieldHint text={ndmsHint} ariaLabel="Подсказка: интерфейсы сервера" />
				{/if}
			</div>
		{/if}
	</DetailSection>

	<!-- Блок «Абоненты» наполняет задача 6; здесь только его место в порядке секций. -->
	<DetailSection title="Абоненты">
		<div class="clients-slot"></div>
	</DetailSection>

	<ShareNetworkSection
		bind:wdttServer={wdttDraft}
		bind:ftServer={ftDraft}
		{lanOptions}
		{exposeApplied}
		{saving}
		busy={mutating}
		onnat={setNat}
		onlan={setLan}
		onpolicy={setPolicy}
		onsave={save}
		onrevert={revert}
	/>

	<ShareAdvancedSection bind:wdttServer={wdttDraft} {ports} />

	<LogSection
		log={status?.log}
		{routerClock}
		hint="Это вывод процесса, а не файл server.log."
	/>
</Card>

{#snippet ingressToggle()}
	<div class="run-toggle">
		<Toggle
			label="Маршрутизация через sing-box"
			checked={ingress}
			disabled={mutating}
			onchange={toggleIngress}
			spinner="before"
		/>
		<FieldHint
			text="Трафик абонентов пойдёт по правилам sing-box — тем же, что у устройств сети. Выключено — абоненты выходят напрямую, минуя правила."
			ariaLabel="Подсказка: маршрутизация через sing-box"
		/>
	</div>
{/snippet}

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

	.line-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.625rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.line-label {
		color: var(--color-text-muted);
	}

	code {
		font-family: var(--font-mono);
		font-size: 0.78em;
		background: var(--color-bg-tertiary);
		padding: 0.05rem 0.3rem;
		border-radius: 4px;
		word-break: break-all;
	}

	.run-toggle {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

</style>
