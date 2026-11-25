<script lang="ts">
	import { onDestroy, untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { startVisiblePoll } from '$lib/utils/visiblePoll';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal, WizardSteps } from '$lib/components/ui';
	import { m, type UiText } from '$lib/i18n';
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
		doesStep2RequireNATConfirm,
		isCloudKeenDNSHost,
		isDraftEgressValid,
		isGrayIPNetwork,
		isTgDirectOnlyDraft,
		isValidMtprotoDirectPort,
		pickDefaultKeenDNSActionMode,
		pickDefaultMtprotoDirectHost,
		resolveEffectiveMtprotoDirectHost,
		shouldKeepExistingSchemeAfterStep1Change,
		validateKeenDNSLabelClient,
		type ExternalAccessInput,
		type ExternalAccessWizardDraft,
	} from './externalAccessModel';
	import ExtWizardStepServices from './ExtWizardStepServices.svelte';
	import ExtWizardStepConnection from './ExtWizardStepConnection.svelte';
	import ExtWizardStepAddresses from './ExtWizardStepAddresses.svelte';
	import ExtWizardStepEgress from './ExtWizardStepEgress.svelte';
	import ExtWizardStepVerify from './ExtWizardStepVerify.svelte';
	import type { XrayRealityState, XrayRealityTransport } from '$lib/types/xray';

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
		m.servers_ext_wiz_step_services,
		m.servers_ext_wiz_step_connection,
		m.servers_ext_wiz_step_addresses,
		m.servers_ext_wiz_step_egress,
		m.servers_ext_wiz_step_verify,
	];

	let stepIndex = $state(0);
	let initializing = $state(false);
	let capsLoading = $state(false);
	let planLoading = $state(false);
	let loading = $derived(initializing || capsLoading || planLoading);
	let initSeq = 0;
	let capsSeq = 0;
	let userTouchedTgScenario = false;
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
	let stopJobPoll: (() => void) | null = null;
	let initialEditWasTgDirectOnly = $state(false);
	let initialEditWasRealityOnly = $state(false);

	// Direct VLESS + REALITY entry (white-IP preset)
	type WizardRealityMode = 'off' | 'also' | 'only';
	let realityState = $state<XrayRealityState | null>(null);
	let realityMode = $state<WizardRealityMode>('off');
	let realityInitiallyEnabled = $state(false);
	let realityPort = $state(2053);
	let realityTransport = $state<XrayRealityTransport>('tcp');
	let realityDest = $state('www.microsoft.com:443');
	let realityServerName = $state('');
	let realityPublicHost = $state('');
	let realityResultLink = $state('');
	let realityResultError = $state<UiText | null>(null);
	let realityNetChecking = $state(false);

	let wantXray = $derived(
		draft.selectedKind === 'xray' || draft.selectedKind === 'both'
	);
	let isRealityOnly = $derived(draft.selectedKind === 'xray' && realityMode === 'only');
	let usesReality = $derived(wantXray && realityMode !== 'off');
	let realityWhiteIP = $derived(Boolean(realityState?.network?.white_ip));
	let wantTg = $derived(
		draft.selectedKind === 'tgwebproxy' || draft.selectedKind === 'both'
	);
	let wantTgDirect = $derived(
		wantTg && (draft.tgScenario === 'dual' || draft.tgScenario === 'direct_fake_tls')
	);
	let wantTgWeb = $derived(
		wantTg && (draft.tgScenario === 'dual' || draft.tgScenario === 'cdn_http')
	);
	let isTgDirectOnly = $derived(isTgDirectOnlyDraft(draft));
	let detectedMtprotoWhiteIP = $derived(
		(realityState?.network?.white_ip
			? realityState.network.external_ip || realityState.network.wan_address || ''
			: '') ||
			(keendnsInfo?.wan_ip && !keendnsInfo.wan_ip_private ? keendnsInfo.wan_ip : '')
	);
	let keenDNSBaseDomain = $derived(
		(
			realityState?.network?.keendns_domain ||
			(keendnsInfo?.enabled ? keendnsInfo.domain : '') ||
			''
		).trim()
	);
	let keenDNSBaseIsDirect = $derived(
		Boolean(
			realityState?.network?.keendns_domain ||
				(keendnsInfo?.enabled && keendnsInfo.mode === 'direct')
		)
	);
	let detectedMtprotoDirectDomain = $derived(keenDNSBaseIsDirect ? keenDNSBaseDomain : '');
	let grayIPDetected = $derived(
		isGrayIPNetwork({
			hasRealityNetwork: Boolean(realityState?.network),
			realityWhiteIP,
			wanIPPrivate: keendnsInfo?.wan_ip_private,
			keendnsEnabled: keendnsInfo?.enabled,
			keendnsMode: keendnsInfo?.mode,
		})
	);
	let effectiveDirectHost = $derived(resolveEffectiveMtprotoDirectHost(draft));
	let isDirectHostCloudKeenDNS = $derived(
		isCloudKeenDNSHost(effectiveDirectHost, keenDNSBaseDomain, keenDNSBaseIsDirect)
	);
	let isDirectPortInvalid = $derived(wantTgDirect && !isValidMtprotoDirectPort(draft.directPort));
	let isRealityPublicHostCloudKeenDNS = $derived(
		usesReality &&
			Boolean(realityPublicHost.trim()) &&
			isCloudKeenDNSHost(realityPublicHost, keenDNSBaseDomain, keenDNSBaseIsDirect)
	);
	let realityPublicHostRequired = $derived(
		usesReality && grayIPDetected && !realityWhiteIP && !realityPublicHost.trim()
	);
	let canReturnToSavedScheme = $derived(
		draft.isEditMode &&
			!(initialEditWasTgDirectOnly && !isTgDirectOnly) &&
			!(initialEditWasRealityOnly && !isRealityOnly)
	);
	let step2NeedsNATConfirm = $derived(
		doesStep2RequireNATConfirm({
			isEditMode: draft.isEditMode,
			keepExistingConnectionScheme: draft.keepExistingConnectionScheme,
			isTgDirectOnly,
			isRealityOnly,
			wantXray,
			realityMode,
			originType: draft.originType,
			grayIPDetected,
			realityWhiteIP,
		})
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
				m.servers_ext_wiz_kd_taken({ fqdn: found.fqdn, upstream: found.upstream })
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

	let xrayRouterOption = $derived(allEgressOptions.find((o) => o.id === 'firc:router'));

	let currentTgEgressOption = $derived(
		allEgressOptions.find(
			(o) => o.id === draft.tgUpstreamDevice || o.iface === draft.tgUpstreamDevice
		)
	);

	let egressValidation = $derived(isDraftEgressValid(draft, allEgressOptions));

	$effect(() => {
		if (open) {
			void initWizard(initialKind);
		} else {
			initSeq++;
			capsSeq++;
			initializing = false;
			capsLoading = false;
			planLoading = false;
			realityNetChecking = false;
			stopPolling();
		}
	});

	onDestroy(() => {
		stopPolling();
	});

	function stopPolling() {
		stopJobPoll?.();
		stopJobPoll = null;
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
		const seq = ++initSeq;
		userTouchedTgScenario = false;
		stepIndex = 0;
		applyPhase = 'review';
		showAdvancedAddresses = false;
		showTechnicalPlan = false;
		showRouterFacts = false;
		caps = null;
		keendnsInfo = null;
		realityState = null;
		preflight = null;
		planRecord = null;
		jobStatus = null;
		creds = null;
		realityResultLink = '';
		realityResultError = null;
		realityNetChecking = false;

		initializing = true;
		try {
			const [input, rSt] = await Promise.all([
				resolveCurrentInput(),
				api.getXrayReality(false).catch(() => null),
			]);
			if (seq !== initSeq || !open) return;

			const draftInput: ExternalAccessInput = {
				...input,
				realityEnabled: Boolean(rSt?.settings?.enabled),
				realityPort: rSt?.settings?.port,
				realityPublicHost: rSt?.settings?.public_host,
			};
			const initial = createInitialWizardDraft(draftInput, preferredKind);
			// Имя первого устройства — данные (уходят на сервер как remark и
			// правятся в поле ввода), а не подпись: язык момента открытия верен.
			draft =
				initial.existingClientCount === 0 && !initial.isEditMode
					? { ...initial, clientRemark: m.servers_ext_wiz_my_phone() }
					: initial;
			initialEditWasTgDirectOnly = draft.isEditMode && isTgDirectOnlyDraft(draft);
			realityState = rSt;
			const rEnabled = Boolean(rSt?.settings?.enabled);
			realityInitiallyEnabled = rEnabled;
			if (rSt) {
				const s = rSt.settings;
				realityPort = s.public_key ? s.port : rSt.suggested_port || s.port || 2053;
				realityTransport = (s.transport as XrayRealityTransport) || 'tcp';
				realityDest = s.dest || 'www.microsoft.com:443';
				realityServerName = (s.server_names && s.server_names[0]) || '';
				realityPublicHost = s.public_host || '';
			} else {
				realityPort = 2053;
				realityTransport = 'tcp';
				realityDest = 'www.microsoft.com:443';
				realityServerName = '';
				realityPublicHost = '';
			}
			const hasCdnDomain = Boolean((input.xrayConfig?.public_domain || '').trim());
			initialEditWasRealityOnly = Boolean(
				draft.isEditMode && draft.selectedKind === 'xray' && rEnabled && !hasCdnDomain
			);
			if (draft.selectedKind === 'xray' && rEnabled && !hasCdnDomain) {
				realityMode = 'only';
			} else if ((draft.selectedKind === 'xray' || draft.selectedKind === 'both') && rEnabled) {
				realityMode = 'also';
			} else {
				realityMode = 'off';
			}
			const loadedCaps = await loadCapabilities(draft.selectedKind, seq);
			if (seq !== initSeq || !open) return;

			if (!draft.isEditMode && !userTouchedTgScenario) {
				const kd = loadedCaps?.keendns ?? null;
				const grayNow = isGrayIPNetwork({
					hasRealityNetwork: Boolean(rSt?.network),
					realityWhiteIP: Boolean(rSt?.network?.white_ip),
					wanIPPrivate: kd?.wan_ip_private,
					keendnsEnabled: kd?.enabled,
					keendnsMode: kd?.mode,
				});
				const kdAvailable = Boolean(
					kd && kd.enabled && kd.domain && kd.state !== 'unconfigured' && kd.state !== 'unsupported'
				);
				const kdHasCompatPubs = Boolean((kd?.publications ?? []).some((p) => p.compatible));
				if (grayNow && (draft.selectedKind === 'tgwebproxy' || draft.selectedKind === 'both')) {
					draft.tgScenario = 'cdn_http';
					draft.originType = 'cloud_address';
					if (kdAvailable) {
						draft.connectionWay = 'direct';
						setKeenDNSActionMode(
							pickDefaultKeenDNSActionMode({
								keendnsAvailable: kdAvailable,
								hasCompatiblePublications: kdHasCompatPubs,
							})
						);
					}
				}
			}
		} finally {
			if (seq === initSeq) {
				initializing = false;
			}
		}
		if (seq === initSeq && open && !realityState?.network) {
			void refreshRealityNetworkInBackground(seq);
		}
	}

	async function refreshRealityNetworkInBackground(seq: number) {
		realityNetChecking = true;
		try {
			const fresh = await api.getXrayReality(true).catch(() => null);
			if (seq !== initSeq || !open || !fresh) return;
			realityState = fresh;
			if (wantTgDirect) {
				prefillMtprotoDirectHostIfEmpty();
			}
			if (
				!draft.isEditMode &&
				!userTouchedTgScenario &&
				stepIndex === 0 &&
				(draft.selectedKind === 'tgwebproxy' || draft.selectedKind === 'both') &&
				draft.tgScenario === 'dual' &&
				fresh.network &&
				!fresh.network.white_ip
			) {
				draft.tgScenario = 'cdn_http';
				draft.originType = 'cloud_address';
				if (keendnsAvailable) {
					draft.connectionWay = 'direct';
					setKeenDNSActionMode(
						pickDefaultKeenDNSActionMode({
							keendnsAvailable,
							hasCompatiblePublications: compatibleKeenDNSPublications.length > 0,
						})
					);
				}
			}
		} finally {
			if (seq === initSeq) {
				realityNetChecking = false;
			}
		}
	}

	async function checkRealityNetwork() {
		realityNetChecking = true;
		try {
			realityState = await api.getXrayReality(true);
			if (wantTgDirect) {
				prefillMtprotoDirectHostIfEmpty();
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_ext_wiz_err_wan_check());
		} finally {
			realityNetChecking = false;
		}
	}

	async function loadCapabilities(
		kind: WizardKind,
		expectedInitSeq?: number
	): Promise<CapabilitiesResponse | null> {
		const reqSeq = ++capsSeq;
		capsLoading = true;
		try {
			const res = await api.getWizardCapabilities(kind);
			if (reqSeq !== capsSeq) return null;
			if (expectedInitSeq !== undefined && (expectedInitSeq !== initSeq || !open)) return null;
			caps = res;
			if (res.keendns) {
				keendnsInfo = res.keendns;
			}
			if (wantTgDirect) {
				prefillMtprotoDirectHostIfEmpty();
			}
			return res;
		} catch (e) {
			if (reqSeq === capsSeq) {
				notifications.error(
					e instanceof Error ? e.message : m.servers_ext_wiz_err_caps()
				);
			}
			return null;
		} finally {
			if (reqSeq === capsSeq) {
				capsLoading = false;
			}
		}
	}

	async function checkRouterFacts() {
		checkingFacts = true;
		showRouterFacts = true;
		try {
			keendnsInfo = await api.getWizardKeenDNS(draft.selectedKind);
			if (!draft.isEditMode && keendnsInfo?.wan_ip_private) {
				draft.originType = 'cloud_address';
			}
			if (wantTgDirect) {
				prefillMtprotoDirectHostIfEmpty();
			}
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : m.servers_ext_wiz_err_keendns()
			);
		} finally {
			checkingFacts = false;
		}
	}

	function prefillMtprotoDirectHostIfEmpty() {
		if (draft.directHost.trim()) return;
		const suggested = pickDefaultMtprotoDirectHost({
			directDomain: detectedMtprotoDirectDomain,
			whiteIP: detectedMtprotoWhiteIP,
		});
		if (suggested) {
			draft.directHost = suggested;
		}
	}

	function selectServiceKind(kind: WizardKind) {
		if (initializing) return;
		draft.selectedKind = kind;
		draft.disableOtherService = false;
		draft.confirmDirectBehindNAT = false;
		if (kind === 'tgwebproxy') {
			realityMode = 'off';
			if (draft.tgScenario === 'direct_fake_tls') {
				draft.keendnsMode = 'manual';
				draft.connectionWay = 'direct';
				draft.originType = 'public_address';
			}
		} else if (kind === 'both' && realityMode === 'only') {
			realityMode = 'also';
		}
		draft.keepExistingConnectionScheme = shouldKeepExistingSchemeAfterStep1Change(
			initialEditWasTgDirectOnly,
			draft
		);
		if (
			(kind === 'tgwebproxy' || kind === 'both') &&
			(draft.tgScenario === 'dual' || draft.tgScenario === 'direct_fake_tls')
		) {
			prefillMtprotoDirectHostIfEmpty();
		}
		void loadCapabilities(kind);
	}

	function selectTgScenario(scenario: string) {
		if (initializing) return;
		userTouchedTgScenario = true;
		draft.tgScenario = scenario;
		draft.confirmDirectBehindNAT = false;
		if (draft.selectedKind === 'tgwebproxy' && scenario === 'direct_fake_tls') {
			draft.keendnsMode = 'manual';
			draft.connectionWay = 'direct';
			draft.originType = 'public_address';
		}
		draft.keepExistingConnectionScheme = shouldKeepExistingSchemeAfterStep1Change(
			initialEditWasTgDirectOnly,
			draft
		);
		if (scenario === 'dual' || scenario === 'direct_fake_tls') {
			prefillMtprotoDirectHostIfEmpty();
		}
	}

	function switchToWebBridgeFromCGNAT() {
		userTouchedTgScenario = true;
		draft.tgScenario = 'cdn_http';
		draft.keepExistingConnectionScheme = false;
		draft.confirmDirectBehindNAT = false;
		draft.originType = 'cloud_address';
		draft.connectionWay = 'direct';
		draft.directHost = '';
		setKeenDNSActionMode(
			pickDefaultKeenDNSActionMode({
				keendnsAvailable,
				hasCompatiblePublications: compatibleKeenDNSPublications.length > 0,
			})
		);
	}

	function switchToWebBridgeOnly() {
		userTouchedTgScenario = true;
		draft.tgScenario = 'cdn_http';
		draft.confirmDirectBehindNAT = false;
		draft.directHost = '';
	}

	function switchToVlessDomain() {
		realityMode = 'off';
		draft.confirmDirectBehindNAT = false;
		draft.keepExistingConnectionScheme = false;
		if (keendnsAvailable) {
			draft.connectionWay = 'direct';
			draft.originType = 'cloud_address';
			setKeenDNSActionMode(
				pickDefaultKeenDNSActionMode({
					keendnsAvailable,
					hasCompatiblePublications: compatibleKeenDNSPublications.length > 0,
				})
			);
		} else {
			draft.connectionWay = 'external_https';
		}
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

	function buildRequest(): WizardPlanRequest {
		if (!isTgDirectOnly && draft.keendnsMode === 'create' && keendnsAvailable) {
			syncKeenDNSToAddresses();
		}
		return buildPlanRequestFromDraft(draft);
	}

	let realityStep2Valid = $derived(
		!usesReality ||
			(Number.isInteger(Number(realityPort)) &&
				Number(realityPort) >= 1024 &&
				Number(realityPort) <= 65535 &&
				Boolean(realityDest.trim()) &&
				!isRealityPublicHostCloudKeenDNS &&
				!realityPublicHostRequired)
	);

	let canProceedStep = $derived.by(() => {
		if (stepIndex === 0) return true;
		if (stepIndex === 1) {
			if (step2NeedsNATConfirm && !draft.confirmDirectBehindNAT) {
				return false;
			}
			return true;
		}
		if (stepIndex === 2) {
			if (isRealityOnly) {
				return realityStep2Valid;
			}
			if (isTgDirectOnly) {
				if (isDirectHostCloudKeenDNS) return false;
				return Boolean(
					draft.directHost.trim() &&
						effectiveTlsDomain &&
						isValidMtprotoDirectPort(draft.directPort)
				);
			}
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
				const dHost = resolveEffectiveMtprotoDirectHost({
					...draft,
					publicDomain: effectivePhoneDomain,
				});
				if (isCloudKeenDNSHost(dHost, keenDNSBaseDomain, keenDNSBaseIsDirect)) {
					return false;
				}
				if (
					!dHost ||
					!effectiveTlsDomain ||
					!isValidMtprotoDirectPort(draft.directPort)
				) {
					return false;
				}
			}
			return realityStep2Valid;
		}
		if (stepIndex === 3) {
			return egressValidation.valid;
		}
		if (stepIndex === 4) {
			if (wantXray && draft.existingClientCount === 0 && !draft.clientRemark.trim()) {
				return false;
			}
			if (isRealityOnly) {
				return applyPhase === 'review';
			}
			return applyPhase === 'review' && Boolean(preflight?.can_proceed && planRecord);
		}
		return false;
	});

	async function prepareStep5Plan() {
		planLoading = true;
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
				e instanceof Error ? e.message : m.servers_ext_wiz_err_plan()
			);
		} finally {
			planLoading = false;
		}
	}

	async function handleNext() {
		if (stepIndex === 1 && !isTgDirectOnly && !isRealityOnly) {
			if (
				draft.connectionWay === 'direct' &&
				draft.keendnsMode === 'manual' &&
				!draft.publicDomain.trim() &&
				keendnsAvailable
			) {
				setKeenDNSActionMode(
					pickDefaultKeenDNSActionMode({
						keendnsAvailable,
						hasCompatiblePublications: compatibleKeenDNSPublications.length > 0,
					})
				);
			}
			stepIndex += 1;
			return;
		}
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
			if (!isRealityOnly) {
				await prepareStep5Plan();
			}
			applyPhase = 'review';
			stepIndex = 4;
			return;
		}
		if (stepIndex === 4 && applyPhase === 'review' && (isRealityOnly || planRecord)) {
			await startApply();
		}
	}

	function realityHostPart(d: string): string {
		const v = (d || '').trim().replace(/^https?:\/\//, '').replace(/\/.*$/, '');
		const idx = v.lastIndexOf(':');
		return idx > 0 ? v.slice(0, idx) : v;
	}

	function buildRealityPayload() {
		const sn = realityServerName.trim();
		const destTrim = realityDest.trim();
		return {
			enabled: true,
			port: Number(realityPort),
			transport: realityTransport,
			dest: destTrim,
			server_names: sn ? [sn] : [realityHostPart(destTrim)],
			public_host: realityPublicHost.trim(),
		};
	}

	function resolveRealityEgress(): { outbound_mode: string; outbound_interface?: string } {
		const u = (draft.upstreamDevice || '').trim();
		if (!u || u === 'direct') return { outbound_mode: 'direct' };
		if (u === 'firc:router' || u === 'router') return { outbound_mode: 'router' };
		return { outbound_mode: 'interface', outbound_interface: u };
	}

	async function startApply() {
		if (isRealityOnly) {
			planLoading = true;
			realityResultLink = '';
			realityResultError = null;
			try {
				const egress = resolveRealityEgress();
				const res = await api.xrayRealityQuickSetup(
					draft.clientRemark.trim() || m.servers_ext_wiz_my_phone(),
					buildRealityPayload(),
					{
						preserve_existing_clients: draft.existingClientCount > 0,
						disable_other_service: draft.tgInitiallyEnabled
							? draft.disableOtherService
							: false,
						...egress,
					}
				);
				realityState = res.state;
				realityResultLink = res.links?.vless_url || '';
				realityResultError = res.links_error || null;
				applyPhase = 'done';
				onapplied?.();
				onsettled?.();
			} catch (e) {
				notifications.error(e instanceof Error ? e.message : m.servers_ext_wiz_err_reality_setup());
			} finally {
				planLoading = false;
			}
			return;
		}
		if (!planRecord) return;
		planLoading = true;
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
			// Ход задания: короткий опрос до конечной фазы, первый тик сразу.
			// Раньше таймер ставился и после уже конечной первой фазы —
			// onapplied/onsettled звучали дважды; visiblePoll к тому же не
			// накладывает запросы.
			stopPolling();
			stopJobPoll = startVisiblePoll(() => pollJob(res.job_id), 750);
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : m.servers_ext_wiz_err_apply_start()
			);
		} finally {
			planLoading = false;
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
				e instanceof Error ? e.message : m.servers_ext_wiz_err_cancel()
			);
		} finally {
			cancelling = false;
		}
	}

	async function handleReveal() {
		if (!jobStatus) return;
		revealLoading = true;
		realityResultLink = '';
		realityResultError = null;
		try {
			creds = await api.revealWizardCredentials(draft.selectedKind, jobStatus.id);
			if (usesReality) {
				try {
					realityState = await api.updateXrayReality(buildRealityPayload());
					let targetUuid = creds?.uuid || '';
					if (!targetUuid) {
						const xCfg = await api.getXrayServerConfig().catch(() => null);
						targetUuid = xCfg?.clients?.find((c) => c.enabled)?.id || xCfg?.clients?.[0]?.id || '';
					}
					if (targetUuid) {
						const l = await api.getXrayClientLinks(targetUuid, undefined, 'reality');
						realityResultLink = l.vless_url || '';
					} else {
						realityResultError = () => m.servers_ext_wiz_err_no_device();
					}
				} catch (re) {
					realityResultError = re instanceof Error ? re.message : () => m.servers_ext_wiz_err_reality_apply();
				}
			} else if (wantXray && realityInitiallyEnabled) {
				try {
					realityState = await api.updateXrayReality({ enabled: false });
				} catch {
					// non-fatal
				}
			}
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : m.servers_ext_wiz_err_reveal()
			);
		} finally {
			revealLoading = false;
		}
	}

	// Переход вперёд идёт через handleNext (проверки, план); назад — только
	// до применения, как и кнопка «Назад».
	function go(i: number) {
		if (i > stepIndex) {
			void handleNext();
		} else if (applyPhase === 'review') {
			stepIndex = i;
		}
	}
</script>

<Modal
	{open}
	title={draft.isEditMode ? m.servers_ext_wiz_title_edit() : m.servers_ext_wiz_title_new()}
	{onclose}
	width="820px"
>
	<div class="ext-wizard">
		<p class="wizard-subtitle">{m.servers_ext_wiz_subtitle()}</p>
		<WizardSteps
			steps={STEPS.map((label) => label())}
			current={stepIndex}
			canNext={canProceedStep}
			canBack={applyPhase === 'review'}
			busy={loading}
			ongo={go}
			finish={applyPhase === 'review' ? applyAction : applyPhase === 'done' ? doneAction : undefined}
		>
			{#if stepIndex === 0}
				<ExtWizardStepServices
					bind:draft
					{wantTg}
					{initializing}
					{grayIPDetected}
					{otherServiceCurrentlyEnabled}
					{otherServiceLabel}
					onselectkind={selectServiceKind}
					onselectscenario={selectTgScenario}
				/>
			{:else if stepIndex === 1}
				<ExtWizardStepConnection
					bind:draft
					bind:realityMode
					{isRealityOnly}
					{isTgDirectOnly}
					{wantXray}
					{wantTgDirect}
					{grayIPDetected}
					{realityPort}
					{realityState}
					{realityWhiteIP}
					{realityNetChecking}
					{checkingFacts}
					{showRouterFacts}
					{keendnsInfo}
					{canReturnToSavedScheme}
					oncheckreality={() => void checkRealityNetwork()}
					oncheckfacts={() => void checkRouterFacts()}
					onswitchtowebbridge={switchToWebBridgeFromCGNAT}
					onswitchtowebonly={switchToWebBridgeOnly}
					onswitchtovlessdomain={switchToVlessDomain}
				/>
			{:else if stepIndex === 2}
				<ExtWizardStepAddresses
					bind:draft
					{isRealityOnly}
					{isTgDirectOnly}
					{usesReality}
					{wantXray}
					{wantTgWeb}
					{wantTgDirect}
					{grayIPDetected}
					{effectiveDirectHost}
					{isDirectHostCloudKeenDNS}
					{isDirectPortInvalid}
					{isRealityPublicHostCloudKeenDNS}
					{realityPublicHostRequired}
					{detectedMtprotoWhiteIP}
					{keenDNSBaseDomain}
					{keenDNSBaseIsDirect}
					{detectedMtprotoDirectDomain}
					{keendnsInfo}
					{keendnsAvailable}
					{keendnsValidation}
					{keendnsLabelConflict}
					{previewKeenDNSFqdn}
					{compatibleKeenDNSPublications}
					bind:realityState
					bind:realityPort
					bind:realityTransport
					bind:realityDest
					bind:realityServerName
					bind:realityPublicHost
					bind:showAdvancedAddresses
					onsetkeendnsmode={setKeenDNSActionMode}
					onapplypublication={applyExistingPublication}
					onsynckeendns={syncKeenDNSToAddresses}
					onswitchtowebbridge={switchToWebBridgeFromCGNAT}
					onswitchtowebonly={switchToWebBridgeOnly}
					onswitchtovlessdomain={switchToVlessDomain}
				/>
			{:else if stepIndex === 3}
				<ExtWizardStepEgress
					{draft}
					{wantXray}
					{wantTg}
					{egressValidation}
					{currentXrayEgressOption}
					{currentTgEgressOption}
					{xrayRouterOption}
					{xrayEgressCategory}
					{tgEgressCategory}
					{xrayTunnelOptions}
					{tgTunnelOptions}
					onselectxraycategory={selectXrayCategory}
					onselecttgcategory={selectTgCategory}
					onselectxrayegress={selectXrayEgress}
					onselecttgegress={selectTgEgress}
				/>
			{:else if stepIndex === 4}
				<ExtWizardStepVerify
					bind:draft
					{applyPhase}
					{isRealityOnly}
					{isTgDirectOnly}
					{grayIPDetected}
					{usesReality}
					{wantXray}
					{wantTg}
					{wantTgDirect}
					{effectiveDirectHost}
					{effectiveTlsDomain}
					{keendnsAvailable}
					{previewKeenDNSFqdn}
					{realityState}
					{realityInitiallyEnabled}
					{realityPort}
					{realityTransport}
					{realityDest}
					{realityServerName}
					{realityPublicHost}
					{realityResultLink}
					{realityResultError}
					{preflight}
					{planRecord}
					{jobStatus}
					{cancelling}
					{creds}
					{revealLoading}
					egressOptions={allEgressOptions}
					bind:showTechnicalPlan
					oncanceljob={handleCancelJob}
					onreveal={handleReveal}
				/>
			{/if}
		</WizardSteps>
	</div>
</Modal>

{#snippet applyAction()}
	<Button variant="primary" disabled={!canProceedStep} {loading} onclick={() => void handleNext()}>
		{m.common_apply()}
	</Button>
{/snippet}

{#snippet doneAction()}
	<Button variant="primary" onclick={onclose}>
		{m.common_close()}
	</Button>
{/snippet}

<style>
	.wizard-subtitle {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin: 0 0 0.75rem;
		line-height: 1.4;
	}

	:where(.ext-wizard) :global(.step-section) {
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	:where(.ext-wizard) :global(.step-heading) {
		font-size: 15px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	:where(.ext-wizard) :global(.step-desc) {
		font-size: 12.5px;
		color: var(--color-text-muted);
		margin: 0;
		line-height: 1.45;
	}

	:where(.ext-wizard) :global(.cards-grid) {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(195px, 1fr));
		gap: 10px;
	}

	:where(.ext-wizard) :global(.cards-grid.cols-2) {
		grid-template-columns: repeat(2, minmax(0, 1fr));
	}

	:where(.ext-wizard) :global(.choice-card) {
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

	:where(.ext-wizard) :global(.choice-card.compact) {
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

	:where(.ext-wizard) :global(.choice-card:hover:not(:disabled)) {
		border-color: var(--color-accent);
	}

	:where(.ext-wizard) :global(.choice-card:disabled) {
		cursor: not-allowed;
		/* Глобальное :disabled гасит карточку целиком вместе с причиной. */
		opacity: 1;
	}

	/* Гасим только заголовок: текст карточки называет причину недоступности. */
	:where(.ext-wizard) :global(.choice-card:disabled .choice-title) {
		color: var(--color-text-muted);
	}

	:where(.ext-wizard) :global(.choice-card:disabled .choice-text) {
		color: var(--color-text-secondary);
	}

	:where(.ext-wizard) :global(.choice-card.selected) {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	:where(.ext-wizard) :global(.choice-top) {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}

	:where(.ext-wizard) :global(.current-pill) {
		font-size: 10px;
		padding: 2px 7px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
		font-weight: 600;
	}

	:where(.ext-wizard) :global(.tech-sub-badge) {
		font-size: 10px;
		padding: 1px 6px;
		border-radius: 4px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}

	:where(.ext-wizard) :global(.choice-title) {
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	:where(.ext-wizard) :global(.choice-text) {
		font-size: 12px;
		color: var(--color-text-muted);
		line-height: 1.4;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	:global(.warn-icon) {
		color: var(--color-warning);
		flex-shrink: 0;
		margin-top: 2px;
	}

	:where(.ext-wizard) :global(.origin-box) {
		padding: 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	:where(.ext-wizard) :global(.field-block) {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}

	:where(.ext-wizard) :global(.field-label) {
		font-size: 12.5px;
		font-weight: 500;
		color: var(--color-text-primary);
		margin-bottom: 0;
		line-height: 1.35;
	}

	:where(.ext-wizard) :global(.field-hint) {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.4;
	}

	:where(.ext-wizard) :global(.text-input),
	:where(.ext-wizard) :global(.select-input) {
		padding: 8px 11px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 13px;
	}

	:where(.ext-wizard) :global(.mono) {
		font-family: var(--font-mono);
	}

	:where(.ext-wizard) :global(.checkbox-row) {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		font-size: 12px;
		line-height: 1.4;
		margin-bottom: 0;
		color: var(--color-text-secondary);
		cursor: pointer;
	}

	:where(.ext-wizard) :global(.checkbox-row input[type='checkbox']) {
		margin-top: 2px;
		flex-shrink: 0;
		accent-color: var(--color-accent);
		cursor: pointer;
	}

	:where(.ext-wizard) :global(.advanced-toggle) {
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

	:where(.ext-wizard) :global(.form-grid-2) {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: 10px;
	}

	:where(.ext-wizard) :global(.form-grid-3) {
		display: grid;
		grid-template-columns: 2fr 1fr 1fr;
		gap: 10px;
	}

	:where(.ext-wizard) :global(.badge-ok) {
		background: var(--color-success-tint);
		color: var(--color-success);
		border: 1px solid var(--color-success-border);
		font-weight: 500;
	}

	:where(.ext-wizard) :global(.badge-warn) {
		background: var(--color-warning-tint);
		color: var(--color-warning);
		border: 1px solid var(--color-warning-border);
		font-weight: 500;
	}

	@media (max-width: 640px) {
		:where(.ext-wizard) :global(.cards-grid),
		:where(.ext-wizard) :global(.cards-grid.cols-2),
		:where(.ext-wizard) :global(.form-grid-2),
		:where(.ext-wizard) :global(.form-grid-3) {
			grid-template-columns: 1fr;
		}
	}

	:where(.ext-wizard) :global(.origin-title) {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
</style>
