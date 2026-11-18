<script lang="ts">
	import { onDestroy, untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button } from '$lib/components/ui';
	import type {
		WizardKind,
		CapabilitiesResponse,
		EgressOption,
		KeenDNSDiscovery,
		PreflightResponse,
		ServerPlanRecord,
		JobStatusResponse,
		RevealCredentials,
		WizardPlanRequest,
	} from '$lib/types/serverWizard';
	import {
		createInitialWizardDraft,
		buildPlanRequestFromDraft,
		isDraftEgressValid,
		validateKeenDNSLabelClient,
		formatDomainList,
		type ExternalAccessInput,
		type ExternalAccessWizardDraft,
	} from './externalAccessModel';
	import { EXTERNAL_ACCESS_LABELS, cleanTunnelDisplayName } from './connectionLabels';
	import WizardShell from '../wizard/WizardShell.svelte';
	import PreflightList from '../wizard/PreflightList.svelte';
	import PlanSummary from '../wizard/PlanSummary.svelte';
	import ApplyProgress from '../wizard/ApplyProgress.svelte';
	import ConnectionResult from '../wizard/ConnectionResult.svelte';
	import ConnectionChecks from '../wizard/ConnectionChecks.svelte';
	import {
		Shield,
		Send,
		Layers,
		Compass,
		ChevronDown,
		ChevronUp,
		CheckCircle2,
		AlertTriangle,
		Globe,
		HelpCircle,
		Info,
		Sparkles,
	} from 'lucide-svelte';

	interface Props {
		open: boolean;
		initialKind?: WizardKind;
		initialInput?: ExternalAccessInput | null;
		onclose: () => void;
		onapplied?: () => void;
		onsettled?: () => void;
	}

	let {
		open = $bindable(false),
		initialKind,
		initialInput = null,
		onclose,
		onapplied,
		onsettled,
	}: Props = $props();

	const STEPS = [
		{ id: 'services', label: '1. Что подключаем' },
		{ id: 'connection', label: '2. Как подключаться' },
		{ id: 'addresses', label: '3. Адрес подключения' },
		{ id: 'egress', label: '4. Куда направлять интернет' },
		{ id: 'verify', label: '5. Проверить и применить' },
	];

	const TLS_SNI_PRESETS = [
		{ domain: 'www.google.com', label: 'Google (www.google.com)' },
		{ domain: 'www.microsoft.com', label: 'Microsoft (www.microsoft.com)' },
		{ domain: 'www.apple.com', label: 'Apple (www.apple.com)' },
		{ domain: 'dl.google.com', label: 'Google Downloads (dl.google.com)' },
	];

	let stepIndex = $state(0);
	let loading = $state(false);
	let showAdvancedAddresses = $state(false);
	let showTechnicalPlan = $state(false);

	let draft = $state<ExternalAccessWizardDraft>(
		untrack(() => createInitialWizardDraft({}, initialKind))
	);

	// Capabilities, KeenDNS discovery, preflight, plan, job, reveal
	let caps = $state<CapabilitiesResponse | null>(null);
	let keendnsInfo = $state<KeenDNSDiscovery | null>(null);
	let checkingFacts = $state(false);
	let showRouterFacts = $state(false);

	let preflight = $state<PreflightResponse | null>(null);
	let planRecord = $state<ServerPlanRecord | null>(null);
	let jobStatus = $state<JobStatusResponse | null>(null);
	let creds = $state<RevealCredentials | null>(null);
	let revealLoading = $state(false);
	let cancelling = $state(false);
	let applyPhase = $state<'review' | 'applying' | 'done'>('review');
	let pollTimer: ReturnType<typeof setInterval> | null = null;

	let wantXray = $derived(
		draft.selectedKind === 'xray' || draft.selectedKind === 'both'
	);
	let wantTg = $derived(
		draft.selectedKind === 'tgwebproxy' || draft.selectedKind === 'both'
	);
	let wantTgDirect = $derived(
		wantTg && (draft.tgScenario === 'dual' || draft.tgScenario === 'direct_fake_tls')
	);
	let wantTgWeb = $derived(
		wantTg && (draft.tgScenario === 'dual' || draft.tgScenario === 'cdn_http')
	);

	let otherServiceCurrentlyEnabled = $derived(
		(draft.selectedKind === 'xray' && draft.tgInitiallyEnabled) ||
			(draft.selectedKind === 'tgwebproxy' && draft.vlessInitiallyEnabled)
	);

	let otherServiceLabel = $derived(
		draft.selectedKind === 'xray' ? 'Telegram' : 'VLESS'
	);

	let effectiveTlsDomain = $derived(
		draft.tlsPreset === '__custom__' ? draft.customTlsDomain.trim() : draft.tlsPreset
	);

	//KeenDNS helpers
	let keendnsAvailable = $derived(
		Boolean(
			keendnsInfo &&
				keendnsInfo.enabled &&
				keendnsInfo.domain &&
				keendnsInfo.state !== 'unconfigured' &&
				keendnsInfo.state !== 'unsupported'
		)
	);

	let compatibleKeenDNSPublications = $derived(
		(keendnsInfo?.publications ?? []).filter((p) => p.compatible)
	);

	let keendnsValidation = $derived(validateKeenDNSLabelClient(draft.keendnsLabel));

	let previewKeenDNSFqdn = $derived.by(() => {
		const base = (keendnsInfo?.domain || '').trim();
		const lbl = keendnsValidation.normalized || (draft.keendnsLabel || 'connect').trim().toLowerCase();
		if (!base) return lbl;
		return `${lbl}.${base}`;
	});

	let keendnsLabelConflict = $derived.by(() => {
		if (draft.keendnsMode !== 'create' || !keendnsValidation.valid) return '';
		const found = (keendnsInfo?.publications ?? []).find(
			(p) => p.label.toLowerCase() === keendnsValidation.normalized
		);
		if (found && !found.compatible) {
			return (
				found.conflict_reason ||
				`Адрес ${found.fqdn} уже занят другим правилом (${found.upstream})`
			);
		}
		return '';
	});

	// Egress options & top-level categories
	let allEgressOptions = $derived(caps?.egress_options ?? []);

	let xrayTunnelOptions = $derived(
		allEgressOptions.filter(
			(opt) =>
				(opt.kind === 'interface' || !opt.kind) &&
				opt.id !== 'direct' &&
				opt.id !== 'firc:router'
		)
	);

	let tgTunnelOptions = $derived(
		allEgressOptions.filter(
			(opt) =>
				(opt.kind === 'interface' || !opt.kind) &&
				opt.id !== 'direct' &&
				opt.id !== 'firc:router' &&
				(!opt.supported_services || opt.supported_services.includes('tgwebproxy'))
		)
	);

	let xrayEgressCategory = $derived<'direct' | 'router' | 'tunnel'>(
		draft.upstreamDevice === 'direct'
			? 'direct'
			: draft.upstreamDevice === 'firc:router'
				? 'router'
				: 'tunnel'
	);

	let tgEgressCategory = $derived<'direct' | 'tunnel'>(
		!draft.tgUpstreamDevice || draft.tgUpstreamDevice === 'direct'
			? 'direct'
			: 'tunnel'
	);

	let currentXrayEgressOption = $derived(
		allEgressOptions.find(
			(o) => o.id === draft.upstreamDevice || o.iface === draft.upstreamDevice
		)
	);

	let currentTgEgressOption = $derived(
		allEgressOptions.find(
			(o) => o.id === draft.tgUpstreamDevice || o.iface === draft.tgUpstreamDevice
		)
	);

	let egressValidation = $derived(isDraftEgressValid(draft, allEgressOptions));

	function formatTunnelTechBadge(opt: EgressOption): string {
		const shortId = (opt.id || '').replace(/^system:/i, '').trim();
		const iface = (opt.iface || '').trim();
		if (iface && shortId && iface.toLowerCase() !== shortId.toLowerCase()) {
			return `${shortId} · ${iface}`;
		}
		return iface || shortId;
	}

	$effect(() => {
		if (open) {
			void initWizard(initialKind);
		} else {
			stopPolling();
		}
	});

	onDestroy(() => {
		stopPolling();
	});

	function stopPolling() {
		if (pollTimer) {
			clearInterval(pollTimer);
			pollTimer = null;
		}
	}

	async function resolveCurrentInput(): Promise<ExternalAccessInput> {
		if (initialInput) {
			return initialInput;
		}
		const [xCfg, xSt, xTun, tCfg, tSt, dCfg] = await Promise.all([
			api.getXrayServerConfig().catch(() => null),
			api.getXrayServerStatus().catch(() => null),
			api.getXrayTuning().catch(() => null),
			api.getTgWebProxyConfig().catch(() => null),
			api.getTgWebProxyStatus().catch(() => null),
			api.getDispatcherConfig().catch(() => null),
		]);
		return {
			xrayConfig: xCfg,
			xrayStatus: xSt,
			xrayTuning: xTun,
			tgConfig: tCfg,
			tgStatus: tSt,
			dispatcherConfig: dCfg,
		};
	}

	async function initWizard(preferredKind?: WizardKind) {
		stepIndex = 0;
		applyPhase = 'review';
		showAdvancedAddresses = false;
		showTechnicalPlan = false;
		showRouterFacts = false;
		preflight = null;
		planRecord = null;
		jobStatus = null;
		creds = null;

		loading = true;
		try {
			const input = await resolveCurrentInput();
			draft = createInitialWizardDraft(input, preferredKind);
			if (draft.existingClientCount === 0 && !draft.isEditMode) {
				draft.clientRemark = 'Мой телефон';
			}
			await loadCapabilities(draft.selectedKind);
		} finally {
			loading = false;
		}
	}

	async function loadCapabilities(kind: WizardKind) {
		loading = true;
		try {
			const res = await api.getWizardCapabilities(kind);
			caps = res;
			if (res.keendns) {
				keendnsInfo = res.keendns;
			}
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : 'Не удалось загрузить параметры мастера'
			);
		} finally {
			loading = false;
		}
	}

	async function checkRouterFacts() {
		checkingFacts = true;
		showRouterFacts = true;
		try {
			keendnsInfo = await api.getWizardKeenDNS(draft.selectedKind);
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : 'Не удалось проверить статус KeenDNS и WAN'
			);
		} finally {
			checkingFacts = false;
		}
	}

	function selectServiceKind(kind: WizardKind) {
		draft.selectedKind = kind;
		draft.disableOtherService = false;
		void loadCapabilities(kind);
	}

	function setKeenDNSActionMode(mode: 'manual' | 'existing' | 'create') {
		draft.keendnsMode = mode;
		if (mode === 'existing' && compatibleKeenDNSPublications.length > 0) {
			const first = compatibleKeenDNSPublications[0];
			applyExistingPublication(first.label, first.fqdn);
		} else if (mode === 'create') {
			if (!draft.keendnsLabel) {
				draft.keendnsLabel = 'connect';
			}
			syncKeenDNSToAddresses();
		}
	}

	function applyExistingPublication(label: string, fqdn: string) {
		draft.keendnsLabel = label;
		if (draft.connectionWay === 'direct') {
			draft.publicDomain = fqdn;
			draft.originHost = fqdn;
		} else {
			draft.originHost = fqdn;
			draft.originScheme = 'https';
			draft.originPort = 443;
		}
	}

	function syncKeenDNSToAddresses() {
		if (draft.keendnsMode !== 'create' || !keendnsAvailable) return;
		const fqdn = previewKeenDNSFqdn;
		if (!fqdn) return;
		if (draft.connectionWay === 'direct') {
			draft.publicDomain = fqdn;
			draft.originHost = fqdn;
		} else {
			draft.originHost = fqdn;
			draft.originScheme = 'https';
			draft.originPort = 443;
		}
	}

	function selectXrayCategory(cat: 'direct' | 'router' | 'tunnel') {
		draft.legacyXrayEgress = false;
		if (cat === 'direct') {
			draft.upstreamDevice = 'direct';
		} else if (cat === 'router') {
			draft.upstreamDevice = 'firc:router';
		} else {
			const firstAvail =
				xrayTunnelOptions.find((t) => t.available) || xrayTunnelOptions[0];
			if (firstAvail) {
				draft.upstreamDevice = firstAvail.id;
			}
		}
	}

	function selectTgCategory(cat: 'direct' | 'tunnel') {
		draft.legacyTgEgress = false;
		if (cat === 'direct') {
			draft.tgUpstreamDevice = 'direct';
			if (draft.selectedKind === 'tgwebproxy') {
				draft.upstreamDevice = 'direct';
			}
		} else {
			const firstAvail =
				tgTunnelOptions.find((t) => t.available) || tgTunnelOptions[0];
			if (firstAvail) {
				draft.tgUpstreamDevice = firstAvail.id;
				if (draft.selectedKind === 'tgwebproxy') {
					draft.upstreamDevice = firstAvail.id;
				}
			}
		}
	}

	function selectXrayEgress(id: string) {
		draft.upstreamDevice = id;
		draft.legacyXrayEgress = false;
	}

	function selectTgEgress(id: string) {
		draft.tgUpstreamDevice = id;
		draft.legacyTgEgress = false;
		if (draft.selectedKind === 'tgwebproxy') {
			draft.upstreamDevice = id;
		}
	}

	function formatEgressSummaryText(id: string): string {
		if (!id || id === 'direct') return EXTERNAL_ACCESS_LABELS.egressDirect;
		if (id === 'firc:router') return EXTERNAL_ACCESS_LABELS.egressFirc;
		const found = allEgressOptions.find((o) => o.id === id || o.iface === id);
		if (found) return `Через туннель «${cleanTunnelDisplayName(found.name)}»`;
		return `Через интерфейс ${id}`;
	}

	function buildRequest(): WizardPlanRequest {
		if (draft.keendnsMode === 'create' && keendnsAvailable) {
			syncKeenDNSToAddresses();
		}
		return buildPlanRequestFromDraft(draft);
	}

	let canProceedStep = $derived.by(() => {
		if (stepIndex === 0) return true;
		if (stepIndex === 1) return true;
		if (stepIndex === 2) {
			if (draft.keendnsMode === 'create') {
				if (!keendnsAvailable || !keendnsValidation.valid || Boolean(keendnsLabelConflict)) {
					return false;
				}
			}
			const effectivePhoneDomain =
				draft.connectionWay === 'direct' && draft.keendnsMode === 'create' && keendnsAvailable
					? previewKeenDNSFqdn
					: draft.publicDomain.trim();

			if ((wantXray || wantTgWeb) && !effectivePhoneDomain) return false;
			if (
				draft.selectedKind === 'both' &&
				draft.useSeparateTgDomain &&
				wantTgWeb &&
				!draft.tgPublicDomain.trim()
			) {
				return false;
			}
			if (wantTgDirect) {
				const dHost =
					draft.directHost.trim() ||
					draft.originHost.trim() ||
					effectivePhoneDomain;
				if (!dHost || !effectiveTlsDomain) return false;
			}
			return true;
		}
		if (stepIndex === 3) {
			return egressValidation.valid;
		}
		if (stepIndex === 4) {
			if (wantXray && draft.existingClientCount === 0 && !draft.clientRemark.trim()) {
				return false;
			}
			return applyPhase === 'review' && Boolean(preflight?.can_proceed && planRecord);
		}
		return false;
	});

	async function prepareStep5Plan() {
		loading = true;
		try {
			const req = buildRequest();
			const pre = await api.runWizardPreflight(draft.selectedKind, req);
			preflight = pre;
			if (pre.can_proceed) {
				planRecord = await api.createWizardPlan(draft.selectedKind, req);
			} else {
				planRecord = null;
			}
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : 'Ошибка подготовки плана'
			);
		} finally {
			loading = false;
		}
	}

	async function handleNext() {
		if (stepIndex < 3) {
			stepIndex += 1;
			return;
		}
		if (stepIndex === 3) {
			if (!egressValidation.valid) {
				if (egressValidation.reason) {
					notifications.error(egressValidation.reason);
				}
				return;
			}
			await prepareStep5Plan();
			applyPhase = 'review';
			stepIndex = 4;
			return;
		}
		if (stepIndex === 4 && applyPhase === 'review' && planRecord) {
			await startApply();
		}
	}

	function handleBack() {
		if (stepIndex > 0 && applyPhase === 'review') {
			stepIndex -= 1;
		}
	}

	async function startApply() {
		if (!planRecord) return;
		loading = true;
		try {
			// If first client remark was edited in Step 5, refresh plan before apply
			let activePlanId = planRecord.plan_id;
			if (
				wantXray &&
				draft.existingClientCount === 0 &&
				planRecord.request.client_remark !== draft.clientRemark.trim()
			) {
				const updatedPlan = await api.createWizardPlan(draft.selectedKind, buildRequest());
				planRecord = updatedPlan;
				activePlanId = updatedPlan.plan_id;
			}
			const res = await api.applyWizardPlan(draft.selectedKind, activePlanId);
			applyPhase = 'applying';
			await pollJob(res.job_id);
			stopPolling();
			pollTimer = setInterval(() => {
				void pollJob(res.job_id);
			}, 750);
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : 'Не удалось запустить применение'
			);
		} finally {
			loading = false;
		}
	}

	async function pollJob(jobId: string) {
		try {
			const st = await api.getWizardJob(draft.selectedKind, jobId);
			jobStatus = st;
			if (st.phase === 'succeeded') {
				stopPolling();
				applyPhase = 'done';
				onapplied?.();
				onsettled?.();
				await handleReveal();
			} else if (
				st.phase === 'failed' ||
				st.phase === 'cancelled' ||
				st.phase === 'recovery_required'
			) {
				stopPolling();
				onsettled?.();
			}
		} catch {
			// ignore transient poll error
		}
	}

	async function handleCancelJob() {
		if (!jobStatus) return;
		cancelling = true;
		try {
			await api.cancelWizardJob(draft.selectedKind, jobStatus.id);
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : 'Не удалось отменить операцию'
			);
		} finally {
			cancelling = false;
		}
	}

	async function handleReveal() {
		if (!jobStatus) return;
		revealLoading = true;
		try {
			creds = await api.revealWizardCredentials(draft.selectedKind, jobStatus.id);
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : 'Не удалось получить ключи подключения'
			);
		} finally {
			revealLoading = false;
		}
	}

	let nextButtonLabel = $derived.by(() => {
		if (stepIndex === 3) return 'Далее';
		if (stepIndex === 4) return 'Применить';
		return 'Далее';
	});
</script>

<WizardShell
	bind:open
	title={draft.isEditMode
		? 'Настройка внешнего доступа'
		: 'Мастер настройки внешнего доступа'}
	subtitle="Подключение телефона, ноутбука или Telegram через ваш роутер"
	steps={STEPS}
	currentStepIndex={stepIndex}
	canNext={canProceedStep}
	canBack={applyPhase === 'review'}
	nextLabel={nextButtonLabel}
	loading={loading}
	hideFooter={stepIndex === 4 && applyPhase !== 'review'}
	onnext={handleNext}
	onback={handleBack}
	{onclose}
>
	{#if stepIndex === 0}
		<!-- Step 1: Что подключаем -->
		<div class="step-section">
			<h3 class="step-heading">Что подключаем?</h3>
			<p class="step-desc">
				Выберите, для чего вы настраиваете подключение к вашему роутеру.
			</p>

			<div class="cards-grid">
				<button
					type="button"
					class="choice-card"
					class:selected={draft.selectedKind === 'xray'}
					onclick={() => selectServiceKind('xray')}
				>
					<div class="choice-top">
						<Shield size={20} class="icon-info-soft" />
						{#if draft.vlessInitiallyEnabled && !draft.tgInitiallyEnabled}
							<span class="current-pill">Включено сейчас</span>
						{/if}
					</div>
					<div class="choice-title">Интернет через роутер (VLESS)</div>
					<div class="choice-text">
						Подключение телефона, ноутбука или другого роутера для выхода в интернет и доступа к домашней сети.
					</div>
				</button>

				<button
					type="button"
					class="choice-card"
					class:selected={draft.selectedKind === 'tgwebproxy'}
					onclick={() => selectServiceKind('tgwebproxy')}
				>
					<div class="choice-top">
						<Send size={20} class="icon-success-soft" />
						{#if draft.tgInitiallyEnabled && !draft.vlessInitiallyEnabled}
							<span class="current-pill">Включено сейчас</span>
						{/if}
					</div>
					<div class="choice-title">Только Telegram</div>
					<div class="choice-text">
						Прокси для приложения Telegram на телефоне и компьютере без включения полного VPN.
					</div>
				</button>

				<button
					type="button"
					class="choice-card"
					class:selected={draft.selectedKind === 'both'}
					onclick={() => selectServiceKind('both')}
				>
					<div class="choice-top">
						<Layers size={20} class="icon-accent-soft" />
						{#if draft.vlessInitiallyEnabled && draft.tgInitiallyEnabled}
							<span class="current-pill">Включено сейчас</span>
						{/if}
					</div>
					<div class="choice-title">Оба сервиса</div>
					<div class="choice-text">
						Одновременная работа VLESS для ваших устройств и прокси для приложения Telegram.
					</div>
				</button>
			</div>

			{#if otherServiceCurrentlyEnabled}
				<div class="other-service-warning">
					<AlertTriangle size={16} class="warn-icon" />
					<div class="other-service-body">
						<div class="other-service-title">
							Сервис «{otherServiceLabel}» сейчас включён на роутере
						</div>
						<label class="checkbox-row">
							<input type="checkbox" bind:checked={draft.disableOtherService} />
							<span>
								Отключить «{otherServiceLabel}» при применении (если не отмечено — второй сервис останется работать без изменений)
							</span>
						</label>
					</div>
				</div>
			{/if}

			{#if wantTg}
				<h3 class="step-heading" style="margin-top: 6px;">
					Какой способ подключения нужен для Telegram?
				</h3>
				<div class="cards-grid">
					<button
						type="button"
						class="choice-card compact"
						class:selected={draft.tgScenario === 'direct_fake_tls'}
						onclick={() => (draft.tgScenario = 'direct_fake_tls')}
					>
						<div class="choice-top">
							<span class="choice-title">Прямое подключение для приложения Telegram</span>
							<span class="tech-sub-badge">MTProto / Fake-TLS</span>
						</div>
						<div class="choice-text">
							Стандартный прокси для официального приложения Telegram на телефоне и компьютере.
						</div>
					</button>

					<button
						type="button"
						class="choice-card compact"
						class:selected={draft.tgScenario === 'cdn_http'}
						onclick={() => (draft.tgScenario = 'cdn_http')}
					>
						<div class="choice-top">
							<span class="choice-title">Подключение через домен</span>
							<span class="tech-sub-badge">Веб-мост / Web CDN</span>
						</div>
						<div class="choice-text">
							Работает через доменное имя или веб-мост без открытия прямого порта на роутере.
						</div>
					</button>

					<button
						type="button"
						class="choice-card compact"
						class:selected={draft.tgScenario === 'dual'}
						onclick={() => (draft.tgScenario = 'dual')}
					>
						<div class="choice-top">
							<span class="choice-title">Оба способа</span>
							<span class="tech-sub-badge">MTProto + домен</span>
						</div>
						<div class="choice-text">
							Включает и прямой прокси для приложения Telegram, и веб-ссылку через домен.
						</div>
					</button>
				</div>
			{/if}
		</div>

	{:else if stepIndex === 1}
		<!-- Step 2: Как подключаться -->
		<div class="step-section">
			<h3 class="step-heading">Как подключаться к роутеру?</h3>

			{#if draft.isEditMode && draft.keepExistingConnectionScheme}
				<div class="saved-scheme-card">
					<div class="saved-scheme-top">
						<div class="saved-scheme-badge">
							<CheckCircle2 size={15} />
							<span>Оставить текущее подключение</span>
						</div>
						<Button
							variant="secondary"
							size="sm"
							onclick={() => (draft.keepExistingConnectionScheme = false)}
						>
							Изменить схему подключения
						</Button>
					</div>
					<div class="saved-scheme-summary">
						{draft.connectionSchemeSummary || 'Используется текущая сохранённая схема подключения роутера.'}
					</div>
					{#if draft.connectionSchemeWarning}
						<div class="scheme-warn-note">
							<AlertTriangle size={14} />
							<span>{draft.connectionSchemeWarning}. Существующие параметры транспорта и ключи сохраняются без перезаписи.</span>
						</div>
					{:else}
						<div class="saved-scheme-hint">
							Все текущие настройки транспорта, портов и ключей устройств останутся без изменений. Вы можете сразу перейти к следующему шагу.
						</div>
					{/if}
				</div>
			{:else}
				{#if draft.isEditMode}
					<div class="return-saved-row">
						<button
							type="button"
							class="link-btn"
							onclick={() => (draft.keepExistingConnectionScheme = true)}
						>
							&larr; Оставить текущее подключение без изменения схемы
						</button>
					</div>
				{/if}

				<div class="field-block">
					<span class="field-label">1. Как телефон подключается к роутеру?</span>
					<div class="cards-grid">
						<button
							type="button"
							class="choice-card"
							class:selected={draft.connectionWay === 'direct'}
							onclick={() => {
								draft.connectionWay = 'direct';
							}}
						>
							<div class="choice-title">Напрямую к адресу роутера</div>
							<div class="choice-text">
								Телефон подключается напрямую к адресу KeenDNS, своему DDNS или белому IP вашего роутера.
							</div>
						</button>

						<button
							type="button"
							class="choice-card"
							class:selected={draft.connectionWay === 'external_https'}
							onclick={() => {
								draft.connectionWay = 'external_https';
								draft.publicPort = 443;
							}}
						>
							<div class="choice-title">Через внешний сервис с вашим доменом</div>
							<div class="choice-text">
								Если вы уже настроили промежуточный сервис для подключения (например, собственный домен с пересылкой трафика на роутер).
							</div>
						</button>
					</div>
				</div>

				<div class="field-block" style="margin-top: 4px;">
					<div class="question-header-row">
						<span class="field-label">2. Какой адрес есть у самого роутера?</span>
						<Button
							variant="secondary"
							size="sm"
							onclick={checkRouterFacts}
							disabled={checkingFacts}
						>
							<HelpCircle size={13} class="mr-1" />
							{checkingFacts ? 'Проверка...' : 'Не знаю — проверить'}
						</Button>
					</div>

					<div class="cards-grid">
						<button
							type="button"
							class="choice-card compact"
							class:selected={draft.originType === 'public_address'}
							onclick={() => (draft.originType = 'public_address')}
						>
							<div class="choice-title">
								Публичный адрес роутера (белый IP, прямой KeenDNS или свой DDNS)
							</div>
							<div class="choice-text">
								У роутера есть прямой белый IP-адрес от провайдера, доступный из интернета.
							</div>
						</button>

						<button
							type="button"
							class="choice-card compact"
							class:selected={draft.originType === 'cloud_address'}
							onclick={() => (draft.originType = 'cloud_address')}
						>
							<div class="choice-title">
								Облачный адрес KeenDNS (роутер за серым IP / CGNAT)
							</div>
							<div class="choice-text">
								Провайдер выдаёт серый IP, и доступ к роутеру идёт через облако KeenDNS по HTTPS.
							</div>
						</button>
					</div>

					{#if showRouterFacts && keendnsInfo}
						<div class="router-facts-box">
							<div class="facts-title">Что удалось определить на роутере:</div>
							<ul class="facts-list">
								<li>
									<strong>WAN IP на роутере:</strong>
									{#if keendnsInfo.wan_ip}
										<code>{keendnsInfo.wan_ip}</code>
										— {keendnsInfo.wan_ip_private
											? 'серый адрес (CGNAT / частная подсеть провайдера) — нужен облачный адрес KeenDNS'
											: 'выглядит как публичный IPv4 (прямая доступность портов зависит от провайдера)'}
									{:else}
										не определён
									{/if}
								</li>
								<li>
									<strong>KeenDNS:</strong>
									{#if keendnsInfo.enabled && keendnsInfo.domain}
										домен <code>{keendnsInfo.domain}</code>, режим:
										<strong>
											{keendnsInfo.mode === 'cloud'
												? 'облачный (через облако KeenDNS)'
												: keendnsInfo.mode === 'direct'
													? 'прямой доступ'
													: keendnsInfo.mode}
										</strong>
										({keendnsInfo.cert_ok ? 'сертификат проверен' : 'сертификат поддомена заранее не проверялся'})
									{:else if keendnsInfo.state === 'unsupported'}
										не поддерживается прошивкой роутера
									{:else}
										не настроен на роутере
									{/if}
								</li>
							</ul>
							<div class="facts-scope">
								Примечание: роутер видит только свои локальные настройки. Доступность адреса снаружи проверяется с телефона через мобильный интернет.
							</div>
						</div>
					{/if}
				</div>
			{/if}
		</div>

	{:else if stepIndex === 2}
		<!-- Step 3: Адрес подключения -->
		<div class="step-section">
			<h3 class="step-heading">Адрес подключения</h3>

			{#if draft.connectionWay === 'direct'}
				<!-- Схема: Напрямую к адресу роутера -->
				<p class="step-desc">
					Укажите адрес роутера, к которому будет подключаться телефон, или создайте домен 4-го уровня в KeenDNS.
				</p>

				<div class="origin-box">
					<div class="origin-header-row">
						<span class="origin-title">Адрес для телефона (адрес роутера)</span>
						<div class="pills-row">
							{#if compatibleKeenDNSPublications.length > 0}
								<button
									type="button"
									class="pill-btn"
									class:active={draft.keendnsMode === 'existing'}
									onclick={() => setKeenDNSActionMode('existing')}
								>
									Выбрать существующий ({compatibleKeenDNSPublications.length})
								</button>
							{/if}
							<button
								type="button"
								class="pill-btn"
								class:active={draft.keendnsMode === 'create'}
								onclick={() => setKeenDNSActionMode('create')}
							>
								<Sparkles size={12} class="mr-1" />
								Создать адрес KeenDNS
							</button>
							<button
								type="button"
								class="pill-btn"
								class:active={draft.keendnsMode === 'manual'}
								onclick={() => setKeenDNSActionMode('manual')}
							>
								Ввести вручную
							</button>
						</div>
					</div>

					{#if draft.keendnsMode === 'create'}
						{#if keendnsAvailable && keendnsInfo}
							<div class="keendns-creator">
								<div class="keendns-base-row">
									<span class="field-label">Базовый домен роутера (KeenDNS):</span>
									<code class="base-domain-pill">{keendnsInfo.domain}</code>
								</div>

								<div class="field-block">
									<label class="field-label" for="kd-label-direct">
										Короткое имя нового адреса (4-й уровень):
									</label>
									<div class="keendns-input-group">
										<input
											id="kd-label-direct"
											type="text"
											class="text-input mono"
											bind:value={draft.keendnsLabel}
											oninput={syncKeenDNSToAddresses}
											placeholder="connect"
										/>
										<span class="keendns-suffix mono">.{keendnsInfo.domain}</span>
									</div>
									{#if keendnsValidation.error}
										<span class="field-error">{keendnsValidation.error}</span>
									{:else if keendnsLabelConflict}
										<span class="field-error">{keendnsLabelConflict}</span>
									{/if}
								</div>

								<div class="keendns-preview-box">
									<Globe size={15} class="icon-info-soft" />
									<div>
										<div class="preview-line">
											Итоговый адрес: <strong class="mono">https://{previewKeenDNSFqdn}</strong>
										</div>
										<div class="preview-sub">
											Адрес будет создан в настройках KeenDNS роутера при нажатии «Применить» на последнем шаге. При создании роутер выполнит глобальное сохранение настроек (<code>system.configuration.save</code>), что также зафиксирует любые другие несохранённые изменения на роутере.
										</div>
									</div>
								</div>
							</div>
						{:else}
							<div class="keendns-unavailable-box">
								<AlertTriangle size={16} class="warn-icon" />
								<div>
									<div class="unavail-title">
										{keendnsInfo?.state === 'unsupported'
											? 'KeenDNS не поддерживается прошивкой роутера'
											: 'KeenDNS не настроен на роутере'}
									</div>
									<div class="unavail-desc">
										Сначала включите доменное имя KeenDNS в веб-интерфейсе Keenetic или введите готовый адрес вручную.
									</div>
								</div>
							</div>
						{/if}
					{:else if draft.keendnsMode === 'existing'}
						<div class="existing-pubs-list">
							{#each compatibleKeenDNSPublications as pub (pub.label)}
								<button
									type="button"
									class="choice-card compact"
									class:selected={draft.keendnsLabel === pub.label}
									onclick={() => applyExistingPublication(pub.label, pub.fqdn)}
								>
									<div class="choice-title mono">{pub.fqdn}</div>
									<div class="choice-text">
										Существующая публикация KeenDNS ({pub.upstream})
									</div>
								</button>
							{/each}
						</div>
					{:else}
						<div class="field-block">
							<label class="field-label" for="wiz-direct-pub-domain">
								Адрес для телефона:
							</label>
							<input
								id="wiz-direct-pub-domain"
								type="text"
								class="text-input mono"
								bind:value={draft.publicDomain}
								placeholder="connect.example.keenetic.pro"
							/>
							<span class="field-hint">
								Домен роутера без http:// и без слэшей (можно несколько через запятую)
							</span>
						</div>
					{/if}
				</div>
			{:else}
				<!-- Схема: Через внешний сервис с вашим доменом -->
				<p class="step-desc">
					Укажите внешний домен, к которому подключается телефон, и адрес самого роутера для внешнего сервиса.
				</p>

				{#if wantXray || wantTgWeb}
					<div class="field-block">
						<label class="field-label" for="wiz-pub-domain">
							Адрес для телефона (внешний домен или несколько через запятую):
						</label>
						<input
							id="wiz-pub-domain"
							type="text"
							class="text-input mono"
							bind:value={draft.publicDomain}
							placeholder="access.example.com"
						/>
						<span class="field-hint">
							Домен вашего внешнего сервиса без http:// и без слэшей
						</span>
					</div>
				{/if}

				{#if draft.selectedKind === 'both' && wantTgWeb}
					<label class="checkbox-row">
						<input type="checkbox" bind:checked={draft.useSeparateTgDomain} />
						<span>Использовать отдельный внешний домен для Telegram</span>
					</label>

					{#if draft.useSeparateTgDomain}
						<div class="field-block">
							<label class="field-label" for="wiz-tg-domain">
								Отдельный домен для Telegram:
							</label>
							<input
								id="wiz-tg-domain"
								type="text"
								class="text-input mono"
								bind:value={draft.tgPublicDomain}
								placeholder="tg.example.com"
							/>
						</div>
					{/if}
				{/if}

				<div class="origin-box">
					<div class="origin-header-row">
						<span class="origin-title">Адрес роутера для внешнего сервиса</span>
						<div class="pills-row">
							{#if compatibleKeenDNSPublications.length > 0}
								<button
									type="button"
									class="pill-btn"
									class:active={draft.keendnsMode === 'existing'}
									onclick={() => setKeenDNSActionMode('existing')}
								>
									Выбрать существующий ({compatibleKeenDNSPublications.length})
								</button>
							{/if}
							<button
								type="button"
								class="pill-btn"
								class:active={draft.keendnsMode === 'create'}
								onclick={() => setKeenDNSActionMode('create')}
							>
								<Sparkles size={12} class="mr-1" />
								Создать адрес KeenDNS
							</button>
							<button
								type="button"
								class="pill-btn"
								class:active={draft.keendnsMode === 'manual'}
								onclick={() => setKeenDNSActionMode('manual')}
							>
								Ввести вручную
							</button>
						</div>
					</div>

					{#if draft.keendnsMode === 'create'}
						{#if keendnsAvailable && keendnsInfo}
							<div class="keendns-creator">
								<div class="keendns-base-row">
									<span class="field-label">Базовый домен роутера (KeenDNS):</span>
									<code class="base-domain-pill">{keendnsInfo.domain}</code>
								</div>

								<div class="field-block">
									<label class="field-label" for="kd-label-ext">
										Короткое имя адреса роутера (4-й уровень):
									</label>
									<div class="keendns-input-group">
										<input
											id="kd-label-ext"
											type="text"
											class="text-input mono"
											bind:value={draft.keendnsLabel}
											oninput={syncKeenDNSToAddresses}
											placeholder="connect"
										/>
										<span class="keendns-suffix mono">.{keendnsInfo.domain}</span>
									</div>
									{#if keendnsValidation.error}
										<span class="field-error">{keendnsValidation.error}</span>
									{:else if keendnsLabelConflict}
										<span class="field-error">{keendnsLabelConflict}</span>
									{/if}
								</div>

								<div class="keendns-preview-box">
									<Globe size={15} class="icon-info-soft" />
									<div>
										<div class="preview-line">
											Адрес роутера для внешнего сервиса: <strong class="mono">https://{previewKeenDNSFqdn}</strong>
										</div>
										<div class="preview-sub">
											Адрес будет создан в настройках KeenDNS роутера при нажатии «Применить» на последнем шаге. При создании роутер выполнит глобальное сохранение настроек (<code>system.configuration.save</code>), что также зафиксирует любые другие несохранённые изменения на роутере.
										</div>
									</div>
								</div>
							</div>
						{:else}
							<div class="keendns-unavailable-box">
								<AlertTriangle size={16} class="warn-icon" />
								<div>
									<div class="unavail-title">
										{keendnsInfo?.state === 'unsupported'
											? 'KeenDNS не поддерживается прошивкой роутера'
											: 'KeenDNS не настроен на роутере'}
									</div>
									<div class="unavail-desc">
										Включите доменное имя KeenDNS в настройках Keenetic или укажите адрес роутера вручную.
									</div>
								</div>
							</div>
						{/if}
					{:else if draft.keendnsMode === 'existing'}
						<div class="existing-pubs-list">
							{#each compatibleKeenDNSPublications as pub (pub.label)}
								<button
									type="button"
									class="choice-card compact"
									class:selected={draft.keendnsLabel === pub.label}
									onclick={() => applyExistingPublication(pub.label, pub.fqdn)}
								>
									<div class="choice-title mono">{pub.fqdn}</div>
									<div class="choice-text">
										Существующая публикация KeenDNS ({pub.upstream})
									</div>
								</button>
							{/each}
						</div>
					{:else}
						<div class="form-grid-3">
							<div class="field-block">
								<label class="field-label" for="wiz-origin-host">Адрес роутера:</label>
								<input
									id="wiz-origin-host"
									type="text"
									class="text-input mono"
									bind:value={draft.originHost}
									placeholder="connect.example.keenetic.pro или белый IP"
								/>
							</div>
							<div class="field-block">
								<label class="field-label" for="wiz-origin-scheme">Протокол:</label>
								<select
									id="wiz-origin-scheme"
									class="select-input mono"
									bind:value={draft.originScheme}
								>
									<option value="http">HTTP</option>
									<option value="https">HTTPS</option>
								</select>
							</div>
							<div class="field-block">
								<label class="field-label" for="wiz-origin-port">Порт:</label>
								<input
									id="wiz-origin-port"
									type="number"
									class="text-input mono"
									bind:value={draft.originPort}
								/>
							</div>
						</div>
					{/if}
				</div>
			{/if}

			{#if wantTgDirect}
				<div class="origin-box">
					<div class="origin-title">Прямой адрес для приложения Telegram (MTProto)</div>
					<div class="form-grid-2">
						<div class="field-block">
							<label class="field-label" for="wiz-direct-host">
								Прямой адрес роутера:
							</label>
							<input
								id="wiz-direct-host"
								type="text"
								class="text-input mono"
								bind:value={draft.directHost}
								placeholder={draft.originHost || draft.publicDomain || 'router.example.com'}
							/>
							<span class="field-hint">
								Если оставить пустым, используется основной адрес роутера
							</span>
						</div>

						<div class="field-block">
							<label class="field-label" for="wiz-tls-preset">
								Домен маскировки TLS:
							</label>
							<select id="wiz-tls-preset" class="select-input" bind:value={draft.tlsPreset}>
								{#each TLS_SNI_PRESETS as p (p.domain)}
									<option value={p.domain}>{p.label}</option>
								{/each}
								<option value="__custom__">Указать свой домен...</option>
							</select>
							{#if draft.tlsPreset === '__custom__'}
								<input
									type="text"
									class="text-input mono"
									style="margin-top: 6px;"
									bind:value={draft.customTlsDomain}
									placeholder="example.com"
								/>
							{/if}
						</div>
					</div>
				</div>
			{/if}

			<button
				type="button"
				class="advanced-toggle"
				onclick={() => (showAdvancedAddresses = !showAdvancedAddresses)}
			>
				{#if showAdvancedAddresses}
					<ChevronUp size={15} />
				{:else}
					<ChevronDown size={15} />
				{/if}
				<span>Дополнительные параметры путей и портов</span>
			</button>

			{#if showAdvancedAddresses}
				<div class="advanced-box">
					{#if wantXray}
						<div class="form-grid-2">
							<div class="field-block">
								<label class="field-label" for="wiz-xray-path">Входящий путь VLESS:</label>
								<input
									id="wiz-xray-path"
									type="text"
									class="text-input mono"
									bind:value={draft.path}
								/>
							</div>
							<div class="field-block">
								<label class="field-label" for="wiz-pub-port">Публичный порт для телефона:</label>
								<input
									id="wiz-pub-port"
									type="number"
									class="text-input mono"
									bind:value={draft.publicPort}
								/>
							</div>
						</div>
					{/if}
					{#if wantTgDirect}
						<div class="form-grid-2">
							<div class="field-block">
								<label class="field-label" for="wiz-direct-port">Прямой порт Telegram MTProto:</label>
								<input
									id="wiz-direct-port"
									type="number"
									class="text-input mono"
									bind:value={draft.directPort}
								/>
							</div>
						</div>
					{/if}
				</div>
			{/if}
		</div>

	{:else if stepIndex === 3}
		<!-- Step 4: Куда направлять интернет -->
		<div class="step-section">
			<h3 class="step-heading">Куда направлять интернет?</h3>
			<p class="step-desc">
				Выберите, как подключившиеся к роутеру устройства будут выходить в интернет.
			</p>

			{#if !egressValidation.valid && egressValidation.reason}
				<div class="legacy-egress-banner">
					<AlertTriangle size={16} />
					<span>{egressValidation.reason}</span>
				</div>
			{/if}

			{#if wantXray}
				<div class="egress-service-box">
					<div class="egress-group-title">
						{draft.selectedKind === 'both' ? '1. Интернет для устройств VLESS' : 'Маршрут для устройств VLESS'}
					</div>

					{#if currentXrayEgressOption && !currentXrayEgressOption.available}
						<div class="legacy-egress-banner">
							<AlertTriangle size={15} />
							<span>
								Текущий выход недоступен ({currentXrayEgressOption.name}) — выберите другой или восстановите туннель.
							</span>
						</div>
					{/if}

					<div class="cards-grid">
						<button
							type="button"
							class="choice-card compact"
							class:selected={xrayEgressCategory === 'direct'}
							onclick={() => selectXrayCategory('direct')}
						>
							<div class="choice-title">Напрямую</div>
							<div class="choice-text">
								Через обычного интернет-провайдера вашего роутера.
							</div>
						</button>

						<button
							type="button"
							class="choice-card compact"
							class:selected={xrayEgressCategory === 'router'}
							onclick={() => selectXrayCategory('router')}
						>
							<div class="choice-title">По правилам роутера</div>
							<div class="choice-text">
								По вашим таблицам и спискам маршрутизации на роутере.
							</div>
						</button>

						<button
							type="button"
							class="choice-card compact"
							class:selected={xrayEgressCategory === 'tunnel'}
							onclick={() => selectXrayCategory('tunnel')}
						>
							<div class="choice-title">Через выбранный туннель</div>
							<div class="choice-text">
								Направлять весь интернет-трафик через один выбранный VPN-туннель.
							</div>
						</button>
					</div>

					{#if xrayEgressCategory === 'tunnel'}
						<div class="tunnel-picker-box">
							<span class="field-label">Выберите пользовательский туннель для VLESS:</span>
							{#if xrayTunnelOptions.length === 0}
								<div class="empty-tunnels-note">
									На роутере не найдено подходящих клиентских туннелей. Выберите режим «Напрямую» или «По правилам роутера».
								</div>
							{:else}
								<div class="egress-list">
									{#each xrayTunnelOptions as opt (opt.id)}
										{@const techBadge = formatTunnelTechBadge(opt)}
										<button
											type="button"
											class="egress-card"
											class:selected={draft.upstreamDevice === opt.id || draft.upstreamDevice === opt.iface}
											class:unavailable={!opt.available}
											disabled={!opt.available && !opt.current && draft.upstreamDevice !== opt.id && draft.upstreamDevice !== opt.iface}
											onclick={() => selectXrayEgress(opt.id)}
										>
											<div class="egress-card-left">
												<Compass size={17} class={opt.available ? 'icon-info-soft' : 'icon-warning-soft'} />
												<div class="egress-card-info">
													<div class="egress-name">
														<span>{cleanTunnelDisplayName(opt.name)}</span>
														{#if techBadge}
															<span class="tunnel-iface-pill mono">{techBadge}</span>
														{/if}
														{#if opt.current}
															<span class="current-pill">Текущий</span>
														{/if}
													</div>
													{#if !opt.available}
														<div class="egress-warn">
															{opt.unavailable_reason || opt.degraded_msg || 'Текущий выход недоступен — выберите другой или восстановите туннель'}
														</div>
													{/if}
												</div>
											</div>
											<div class="egress-status">
												{#if opt.available}
													<CheckCircle2 size={16} class="icon-success-soft" />
												{:else}
													<AlertTriangle size={16} class="icon-warning-soft" />
												{/if}
											</div>
										</button>
									{/each}
								</div>
							{/if}
						</div>
					{/if}
				</div>
			{/if}

			{#if wantTg}
				<div class="egress-service-box">
					<div class="egress-group-title">
						{draft.selectedKind === 'both' ? '2. Интернет для Telegram' : 'Маршрут для Telegram'}
					</div>

					{#if currentTgEgressOption && !currentTgEgressOption.available}
						<div class="legacy-egress-banner">
							<AlertTriangle size={15} />
							<span>
								Текущий выход недоступен ({currentTgEgressOption.name}) — выберите другой или восстановите туннель.
							</span>
						</div>
					{/if}

					<div class="cards-grid">
						<button
							type="button"
							class="choice-card compact"
							class:selected={tgEgressCategory === 'direct'}
							onclick={() => selectTgCategory('direct')}
						>
							<div class="choice-title">Напрямую</div>
							<div class="choice-text">
								Через обычного интернет-провайдера вашего роутера.
							</div>
						</button>

						<button
							type="button"
							class="choice-card compact"
							class:selected={tgEgressCategory === 'tunnel'}
							onclick={() => selectTgCategory('tunnel')}
						>
							<div class="choice-title">Через выбранный туннель</div>
							<div class="choice-text">
								Направлять трафик Telegram через выбранный активный туннель.
							</div>
						</button>
					</div>

					{#if tgEgressCategory === 'tunnel'}
						<div class="tunnel-picker-box">
							<span class="field-label">Выберите пользовательский туннель для Telegram:</span>
							{#if tgTunnelOptions.length === 0}
								<div class="empty-tunnels-note">
									На роутере не найдено подходящих туннелей для Telegram. Выберите режим «Напрямую».
								</div>
							{:else}
								<div class="egress-list">
									{#each tgTunnelOptions as opt (opt.id)}
										{@const techBadge = formatTunnelTechBadge(opt)}
										<button
											type="button"
											class="egress-card"
											class:selected={draft.tgUpstreamDevice === opt.id || draft.tgUpstreamDevice === opt.iface}
											class:unavailable={!opt.available}
											disabled={!opt.available && !opt.current && draft.tgUpstreamDevice !== opt.id && draft.tgUpstreamDevice !== opt.iface}
											onclick={() => selectTgEgress(opt.id)}
										>
											<div class="egress-card-left">
												<Compass size={17} class={opt.available ? 'icon-success-soft' : 'icon-warning-soft'} />
												<div class="egress-card-info">
													<div class="egress-name">
														<span>{cleanTunnelDisplayName(opt.name)}</span>
														{#if techBadge}
															<span class="tunnel-iface-pill mono">{techBadge}</span>
														{/if}
														{#if opt.current}
															<span class="current-pill">Текущий</span>
														{/if}
													</div>
													{#if !opt.available}
														<div class="egress-warn">
															{opt.unavailable_reason || opt.degraded_msg || 'Текущий выход недоступен — выберите другой или восстановите туннель'}
														</div>
													{/if}
												</div>
											</div>
											<div class="egress-status">
												{#if opt.available}
													<CheckCircle2 size={16} class="icon-success-soft" />
												{:else}
													<AlertTriangle size={16} class="icon-warning-soft" />
												{/if}
											</div>
										</button>
									{/each}
								</div>
							{/if}
						</div>
					{/if}
				</div>
			{/if}
		</div>

	{:else if stepIndex === 4}
		<!-- Step 5: Проверить и применить -->
		<div class="step-section">
			{#if applyPhase === 'review'}
				<h3 class="step-heading">Проверить и применить</h3>
				<p class="step-desc">
					Проверьте сводку настроек перед сохранением. Все изменения применяются атомарно с автоматическим откатом при ошибке.
				</p>

				{#if draft.keendnsMode === 'create' && keendnsAvailable}
					<div class="keendns-publish-alert">
						<Globe size={18} class="icon-info-soft" />
						<div class="publish-alert-body">
							<div class="publish-alert-title">
								Публикация нового адреса KeenDNS в интернет
							</div>
							<div class="publish-alert-desc">
								На роутере будет создан открытый адрес <strong class="mono">https://{previewKeenDNSFqdn}</strong> с пересылкой входящих подключений на локальный шлюз серверов (<code>127.0.0.1:{draft.dispatcherPort}</code>) без публикации веб-интерфейса роутера. После создания роутер выполнит глобальное сохранение конфигурации (<code>system.configuration.save</code>), которое также сохранит в память все остальные несохранённые настройки роутера.
							</div>
						</div>
					</div>
				{/if}

				<!-- Human-readable summary -->
				<div class="human-summary-card">
					<div class="summary-row-item">
						<span class="sum-label">Подключаемые сервисы:</span>
						<span class="sum-val">
							{#if draft.selectedKind === 'both'}
								Интернет через роутер (VLESS) и Прокси Telegram
							{:else if draft.selectedKind === 'xray'}
								Интернет через роутер (VLESS)
								{#if draft.tgInitiallyEnabled}
									· Telegram {draft.disableOtherService ? 'будет отключён' : 'останется включённым'}
								{/if}
							{:else}
								Прокси Telegram
								{#if draft.vlessInitiallyEnabled}
									· VLESS {draft.disableOtherService ? 'будет отключён' : 'останется включённым'}
								{/if}
							{/if}
						</span>
					</div>

					<div class="summary-row-item">
						<span class="sum-label">Адрес для телефона:</span>
						<span class="sum-val mono">
							{formatDomainList(draft.publicDomain) || (draft.keendnsMode === 'create' ? previewKeenDNSFqdn : formatDomainList(draft.directHost) || '—')}
						</span>
					</div>

					<div class="summary-row-item">
						<span class="sum-label">Адрес роутера:</span>
						<span class="sum-val mono">
							{#if draft.keendnsMode === 'create' && keendnsAvailable}
								{previewKeenDNSFqdn} (новый адрес KeenDNS)
							{:else if draft.keendnsMode === 'existing'}
								{formatDomainList(draft.originHost || draft.publicDomain)} (существующий KeenDNS)
							{:else if draft.connectionWay === 'direct'}
								{formatDomainList(draft.publicDomain) || 'Прямое подключение'}
							{:else}
								{draft.originHost ? `${draft.originScheme}://${formatDomainList(draft.originHost)}:${draft.originPort}` : 'Не указан'}
							{/if}
						</span>
					</div>

					{#if wantXray}
						<div class="summary-row-item">
							<span class="sum-label">Интернет для VLESS:</span>
							<span class="sum-val">{formatEgressSummaryText(draft.upstreamDevice)}</span>
						</div>
					{/if}

					{#if wantTg}
						<div class="summary-row-item">
							<span class="sum-label">Интернет для Telegram:</span>
							<span class="sum-val">{formatEgressSummaryText(draft.tgUpstreamDevice)}</span>
						</div>
					{/if}

					<div class="summary-row-item">
						<span class="sum-label">Перезапуск сервисов:</span>
						<span class="sum-val">
							{planRecord?.plan?.restart_required
								? 'Выбранные службы будут перезапущены автоматически (~1 сек)'
								: 'Без перезапуска'}
						</span>
					</div>
				</div>

				<!-- First device name input ONLY when VLESS has 0 existing clients -->
				{#if wantXray}
					{#if draft.existingClientCount === 0}
						<div class="first-device-box">
							<label class="field-label" for="wiz-first-device">
								Имя первого устройства:
							</label>
							<input
								id="wiz-first-device"
								type="text"
								class="text-input"
								bind:value={draft.clientRemark}
								placeholder="Мой телефон"
							/>
							<span class="field-hint">
								Для этого устройства сразу после применения будут готовы QR-код и ссылка подключения.
							</span>
						</div>
					{:else}
						<div class="preserve-note">
							Устройства VLESS ({draft.existingClientCount} шт.) уже созданы — их имена и ключи доступа останутся прежними.
						</div>
					{/if}
				{/if}

				{#if preflight && (preflight.has_warnings || !preflight.can_proceed)}
					<PreflightList checks={preflight.checks} />
				{/if}

				{#if planRecord?.plan}
					<button
						type="button"
						class="advanced-toggle"
						onclick={() => (showTechnicalPlan = !showTechnicalPlan)}
					>
						{#if showTechnicalPlan}
							<ChevronUp size={14} />
						{:else}
							<ChevronDown size={14} />
						{/if}
						<span>Показать технический план операций ({planRecord.plan?.items?.length || 0})</span>
					</button>
					{#if showTechnicalPlan}
						<PlanSummary plan={planRecord.plan} />
					{/if}
				{/if}
			{:else if applyPhase === 'applying' && jobStatus}
				<ApplyProgress
					job={jobStatus}
					oncancel={handleCancelJob}
					{cancelling}
				/>
			{:else if applyPhase === 'done'}
				<!-- Post-apply status summary distinguishing saved vs cert pending vs external unverified -->
				<div class="post-apply-banners">
					<div class="status-pill-card ok">
						<CheckCircle2 size={16} />
						<div>
							<div class="pill-card-title">Настройки сохранены</div>
							<div class="pill-card-desc">Конфигурация записана на роутер, локальные службы запущены.</div>
						</div>
					</div>

					{#if creds?.keendns_cert_status === 'ready'}
						<div class="status-pill-card ok">
							<CheckCircle2 size={16} />
							<div>
								<div class="pill-card-title">Сертификат проверен</div>
								<div class="pill-card-desc">
									TLS-сертификат для адреса <code class="mono">{creds.keendns_fqdn}</code> проверен и действителен.
								</div>
							</div>
						</div>
					{:else if creds?.keendns_cert_status === 'pending'}
						<div class="status-pill-card warn">
							<AlertTriangle size={16} />
							<div>
								<div class="pill-card-title">Сертификат ещё выпускается</div>
								<div class="pill-card-desc">
									Адрес <code class="mono">{creds.keendns_fqdn}</code> опубликован, выпуск TLS-сертификата в процессе. Подождите 1–2 минуты перед проверкой с телефона.
								</div>
							</div>
						</div>
					{:else if creds?.keendns_cert_status === 'invalid'}
						<div class="status-pill-card warn">
							<AlertTriangle size={16} />
							<div>
								<div class="pill-card-title">Сертификат поддомена ещё не готов или не прошёл проверку</div>
								<div class="pill-card-desc">
									Проверка TLS-сертификата для <code class="mono">{creds.keendns_fqdn}</code> не подтвердила валидный сертификат (возможно, выпуск для нового поддомена ещё не завершён). Подождите 1–2 минуты.
								</div>
							</div>
						</div>
					{:else if creds?.keendns_fqdn}
						<div class="status-pill-card info">
							<Info size={16} />
							<div>
								<div class="pill-card-title">Сертификат поддомена отдельно не проверен с роутера</div>
								<div class="pill-card-desc">
									Публикация <code class="mono">{creds.keendns_fqdn}</code> активна в настройках KeenDNS. Если поддомен создан впервые, выпуск TLS-сертификата облаком KeenDNS может занять 1–2 минуты.
								</div>
							</div>
						</div>
					{/if}

					<div class="status-pill-card info">
						<Info size={16} />
						<div>
							<div class="pill-card-title">Внешнее подключение не проверено</div>
							<div class="pill-card-desc">
								Локальная проверка на роутере не доказывает прохождение трафика из внешней сети. Проверьте подключение на телефоне через мобильный интернет (с выключенным Wi-Fi).
							</div>
						</div>
					</div>
				</div>

				<ConnectionResult
					kind={draft.selectedKind}
					{creds}
					loading={revealLoading}
					onreveal={handleReveal}
				/>
				<div style="margin-top: 14px;">
					<ConnectionChecks kind={draft.selectedKind} autoRun={true} />
				</div>
			{/if}
		</div>
	{/if}
</WizardShell>

<style>
	.step-section {
		display: flex;
		flex-direction: column;
		gap: 14px;
	}
	.step-heading {
		font-size: 15px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}
	.step-desc {
		font-size: 12.5px;
		color: var(--color-text-muted);
		margin: 0;
		line-height: 1.45;
	}
	.cards-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
		gap: 10px;
	}
	.choice-card {
		text-align: left;
		padding: 13px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		cursor: pointer;
		display: flex;
		flex-direction: column;
		gap: 7px;
		transition: border-color 0.15s, background 0.15s;
		min-width: 0;
		max-width: 100%;
	}
	.choice-card.compact {
		padding: 11px 13px;
	}
	:global(.icon-accent-soft) {
		color: var(--color-accent);
	}
	:global(.icon-info-soft) {
		color: var(--color-info);
	}
	:global(.icon-success-soft) {
		color: var(--color-success);
	}
	:global(.icon-warning-soft) {
		color: var(--color-warning);
	}

	.choice-card:hover {
		border-color: var(--color-accent);
	}
	.choice-card.selected {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.choice-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}
	.current-pill {
		font-size: 10px;
		padding: 2px 7px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
		font-weight: 600;
	}
	.tech-sub-badge {
		font-size: 10px;
		padding: 1px 6px;
		border-radius: 4px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}
	.choice-title {
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.choice-text {
		font-size: 12px;
		color: var(--color-text-muted);
		line-height: 1.4;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.other-service-warning {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 11px 13px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
	}
	:global(.warn-icon) {
		color: var(--color-warning);
		flex-shrink: 0;
		margin-top: 2px;
	}
	.other-service-body {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.other-service-title {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.saved-scheme-card {
		padding: 14px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-accent-border);
		background: var(--color-accent-tint);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.saved-scheme-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}
	.saved-scheme-badge {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.saved-scheme-summary {
		font-size: 13px;
		color: var(--color-text-primary);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 9px 11px;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.45;
	}
	.saved-scheme-hint {
		font-size: 12px;
		color: var(--color-text-muted);
	}
	.scheme-warn-note {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--color-warning);
	}
	.return-saved-row {
		margin-bottom: 2px;
	}
	.link-btn {
		background: none;
		border: none;
		padding: 0;
		color: var(--color-accent);
		font-size: 12.5px;
		font-weight: 500;
		cursor: pointer;
	}
	.link-btn:hover {
		text-decoration: underline;
	}

	.question-header-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}
	.router-facts-box {
		padding: 11px 13px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		display: flex;
		flex-direction: column;
		gap: 8px;
		font-size: 12px;
	}
	.facts-title {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.facts-list {
		margin: 0;
		padding-left: 18px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		color: var(--color-text-secondary);
	}
	.facts-scope {
		font-size: 11.5px;
		color: var(--color-text-muted);
		border-top: 1px solid var(--color-border);
		padding-top: 6px;
	}

	.origin-box {
		padding: 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.origin-header-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}
	.origin-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.keendns-creator {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding-top: 4px;
	}
	.keendns-base-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}
	.base-domain-pill {
		font-family: var(--font-mono);
		font-size: 12px;
		padding: 2px 8px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-accent);
	}
	.keendns-input-group {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: nowrap;
		width: 100%;
		min-width: 0;
	}
	.keendns-input-group .text-input {
		flex: 1 1 120px;
		min-width: 96px;
		width: auto;
	}
	.keendns-suffix {
		font-size: 13px;
		color: var(--color-text-secondary);
		flex: 0 1 auto;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 65%;
	}
	.keendns-preview-box {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
	}
	.preview-line {
		font-size: 12.5px;
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.preview-sub {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin-top: 2px;
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.keendns-unavailable-box {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
	}
	.unavail-title {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.unavail-desc {
		font-size: 12px;
		color: var(--color-text-secondary);
		margin-top: 2px;
	}
	.existing-pubs-list {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.field-error {
		font-size: 11.5px;
		color: var(--color-error);
	}

	.egress-service-box {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}
	.egress-group-title {
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.tunnel-picker-box {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding-top: 8px;
		border-top: 1px solid var(--color-border);
	}
	.empty-tunnels-note {
		font-size: 12px;
		color: var(--color-text-muted);
		padding: 8px 10px;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm);
	}

	.legacy-egress-banner {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 9px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		color: var(--color-warning);
		font-size: 12px;
	}
	.preserve-note {
		font-size: 12px;
		color: var(--color-text-secondary);
		padding: 9px 11px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
	}

	/* Step 5 Review & Post-Apply */
	.keendns-publish-alert {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 11px 13px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
	}
	.publish-alert-body {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
	}
	.publish-alert-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.publish-alert-desc {
		font-size: 12px;
		color: var(--color-text-secondary);
		line-height: 1.4;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.human-summary-card {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px 14px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		min-width: 0;
	}
	.summary-row-item {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: 12px;
		padding-bottom: 6px;
		border-bottom: 1px solid var(--color-border);
		font-size: 12.5px;
		flex-wrap: wrap;
		min-width: 0;
	}
	.summary-row-item:last-child {
		padding-bottom: 0;
		border-bottom: none;
	}
	.sum-label {
		color: var(--color-text-muted);
		flex-shrink: 0;
	}
	.sum-val {
		color: var(--color-text-primary);
		font-weight: 600;
		text-align: right;
		min-width: 0;
		max-width: 100%;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.first-device-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 11px 13px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}

	.post-apply-banners {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.status-pill-card {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		font-size: 12px;
	}
	.status-pill-card.ok {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}
	.status-pill-card.warn {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
	}
	.status-pill-card.info {
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
	}
	.pill-card-title {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.pill-card-desc {
		color: var(--color-text-secondary);
		margin-top: 2px;
		line-height: 1.4;
	}

	.field-block {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.field-label {
		font-size: 12.5px;
		font-weight: 500;
		color: var(--color-text-primary);
	}
	.field-hint {
		font-size: 11px;
		color: var(--color-text-muted);
	}
	.text-input,
	.select-input {
		padding: 8px 11px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 13px;
	}
	.mono {
		font-family: var(--font-mono);
	}
	.pills-row {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}
	.pill-btn {
		display: inline-flex;
		align-items: center;
		padding: 5px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 12px;
		cursor: pointer;
	}
	.pill-btn.active {
		border-color: var(--color-accent-border);
		background: var(--color-accent-tint);
		color: var(--color-accent);
		font-weight: 600;
	}
	.checkbox-row {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--color-text-secondary);
		cursor: pointer;
	}
	.advanced-toggle {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		background: none;
		border: none;
		color: var(--color-accent);
		font-size: 12px;
		cursor: pointer;
		padding: 4px 0;
		align-self: flex-start;
	}
	.advanced-box {
		padding: 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.form-grid-2 {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: 10px;
	}
	.form-grid-3 {
		display: grid;
		grid-template-columns: 2fr 1fr 1fr;
		gap: 10px;
	}
	.egress-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.egress-card {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		text-align: left;
		cursor: pointer;
		min-width: 0;
		max-width: 100%;
	}
	.egress-card.selected {
		border-color: var(--color-accent-border);
		background: var(--color-accent-tint);
	}
	.egress-card:disabled,
	.egress-card.unavailable {
		opacity: 0.6;
	}
	.egress-card:disabled {
		cursor: not-allowed;
	}
	.tunnel-iface-pill {
		font-size: 11px;
		font-weight: 500;
		padding: 1px 7px;
		border-radius: 999px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}
	.egress-card-left {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
		flex: 1;
	}
	.egress-card-info {
		min-width: 0;
		flex: 1;
	}
	.egress-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.egress-warn {
		font-size: 11.5px;
		color: var(--color-warning);
		margin-top: 2px;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.step-section :global(.mr-1) {
		margin-right: 4px;
	}

	@media (max-width: 480px) {
		.keendns-input-group {
			flex-wrap: wrap;
		}
		.keendns-suffix {
			max-width: 100%;
		}
	}

	@media (max-width: 640px) {
		.cards-grid,
		.form-grid-2,
		.form-grid-3 {
			grid-template-columns: 1fr;
		}
		.summary-row-item {
			flex-direction: column;
			gap: 2px;
		}
		.sum-val {
			text-align: left;
		}
	}
</style>
