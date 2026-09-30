<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import {
		Button,
		Badge,
		Dropdown,
		type DropdownOption,
		Modal,
	} from '$lib/components/ui';
	import {
		Play,
		Square,
		CheckCircle,
		AlertTriangle,
		Zap,
		Settings2,
		Trash2,
		Check,
		ChevronDown,
		ChevronRight,
		Search,
		Shield,
		Network,
		Layers,
		Activity,
		ListFilter,
		RefreshCw,
		Globe,
		Database,
	} from 'lucide-svelte';
	import { lookupIpKnowledge } from '$lib/utils/ipKnowledge';
	import type {
		AdaptiveRoutingSettings,
		OperationalState,
		ResolvedEgress,
		EgressRef,
		LearnedDataResponse,
		SusaninLogEvent,
	} from '$lib/types/adaptiveRouting';
	import type { AccessPolicy, PolicyGlobalInterface, RoutingTunnel } from '$lib/types/routing';

	interface Props {
		policies?: AccessPolicy[];
		policyInterfaces?: PolicyGlobalInterface[];
		tunnels?: RoutingTunnel[];
	}

	let {
		policies = [],
		policyInterfaces = [],
		tunnels = [],
	}: Props = $props();

	function defaultSettings(): AdaptiveRoutingSettings {
		return {
			enabled: false,
			routingTableId: 105,
			fwmarkMask: '0x30000000',
			fwmarkTest: '0x10000000',
			fwmarkOk: '0x20000000',
			rulePriorityTest: 96,
			rulePriorityOk: 95,
			source: {
				type: 'all_lan',
			},
			primaryEgress: {
				kind: 'kernel-tunnel',
				resourceId: '',
				engine: 'system',
			},
			failurePolicy: 'direct',
			detection: {
				fastIntervalSeconds: 1,
				softIntervalSeconds: 1,
				judgeIntervalSeconds: 1,
				healthIntervalSeconds: 5,
				tcpSynRetries: 2,
				lateStallBytes: 1500,
			},
			persistence: {
				okTtlSeconds: 0,
				maxEntries: 4096,
				separateTcpUdp: true,
			},
			dns: {
				enabled: false,
				servers: ['1.1.1.1', '8.8.8.8'],
				routeViaTunnel: true,
				interceptPort53: true,
			},
			alwaysFileEnabled: true,
			neverFileEnabled: true,
			alwaysEntries: [],
			neverEntries: [],
		};
	}

	let loading = $state(true);
	let saving = $state(false);
	let actionBusy = $state(false);
	let testingEgress = $state(false);

	let status = $state<OperationalState | null>(null);
	let settings = $state<AdaptiveRoutingSettings>(defaultSettings());
	let egresses = $state<ResolvedEgress[]>([]);
	let learned = $state<LearnedDataResponse | null>(null);

	let initialSettingsJson = $state('');
	let settingsHydrated = $state(false);

	let testResult = $state<{ available: boolean; interface: string; reason?: string } | null>(null);

	let detectorExpanded = $state(false);
	let activeListTab = $state<'always' | 'never'>('always');
	let showLearnedModal = $state(false);
	let modalTab = $state<'ok' | 'test' | 'radar' | 'always' | 'never'>('ok');
	let ipSearchQuery = $state('');
	let logEvents = $state<SusaninLogEvent[]>([]);
	let radarPollTimer: ReturnType<typeof setInterval> | null = null;
	let radarFilter = $state<'all' | 'confirmed' | 'stall' | 'cooldown'>('all');

	const dnsPresets = [
		{ id: 'cloudflare', name: 'Cloudflare (1.1.1.1, 1.0.0.1)', servers: ['1.1.1.1', '1.0.0.1'] },
		{ id: 'google', name: 'Google DNS (8.8.8.8, 8.8.4.4)', servers: ['8.8.8.8', '8.8.4.4'] },
		{ id: 'adguard', name: 'AdGuard DNS (94.140.14.14, 94.140.15.15)', servers: ['94.140.14.14', '94.140.15.15'] },
		{ id: 'quad9', name: 'Quad9 DNS (9.9.9.9, 149.112.112.112)', servers: ['9.9.9.9', '149.112.112.112'] },
		{ id: 'yandex', name: 'Яндекс DNS (77.88.8.8, 77.88.8.1)', servers: ['77.88.8.8', '77.88.8.1'] },
		{ id: 'geohide', name: 'GeoHide SmartDNS (193.233.112.67, 193.233.112.68)', servers: ['193.233.112.67', '193.233.112.68'] },
		{ id: 'custom', name: 'Пользовательский ввод', servers: [] },
	];
	let selectedDnsPreset = $state('cloudflare');
	let customDnsText = $state('1.1.1.1, 8.8.8.8');

	function getEgressFlag(name: string): string {
		const lower = name.toLowerCase();
		if (lower.includes('us') || lower.includes('usa') || lower.includes('united states') || lower.includes('dallas') || lower.includes('miami') || lower.includes('new york')) return '🇺🇸';
		if (lower.includes('fi') || lower.includes('finland') || lower.includes('helsinki')) return '🇫🇮';
		if (lower.includes('de') || lower.includes('germany') || lower.includes('frankfurt') || lower.includes('falkenstein')) return '🇩🇪';
		if (lower.includes('nl') || lower.includes('netherlands') || lower.includes('amsterdam')) return '🇳🇱';
		if (lower.includes('ru') || lower.includes('russia') || lower.includes('moscow') || lower.includes('dacha')) return '🇷🇺';
		if (lower.includes('fr') || lower.includes('france') || lower.includes('paris')) return '🇫🇷';
		if (lower.includes('gb') || lower.includes('uk') || lower.includes('london')) return '🇬🇧';
		if (lower.includes('se') || lower.includes('sweden') || lower.includes('stockholm')) return '🇸🇪';
		if (lower.includes('sg') || lower.includes('singapore')) return '🇸🇬';
		if (lower.includes('jp') || lower.includes('japan') || lower.includes('tokyo')) return '🇯🇵';
		if (lower.includes('freeturn') || lower.includes('wg') || lower.includes('awg')) return '🛡️';
		return '🌐';
	}

	function getFlagFromCountry(country?: string): string {
		if (!country) return '';
		const c = country.toLowerCase();
		if (c.includes('сша') || c.includes('us')) return '🇺🇸';
		if (c.includes('россия') || c.includes('ru')) return '🇷🇺';
		if (c.includes('нидерланд') || c.includes('nl')) return '🇳🇱';
		if (c.includes('германи') || c.includes('de')) return '🇩🇪';
		if (c.includes('финлянд') || c.includes('fi')) return '🇫🇮';
		if (c.includes('франци') || c.includes('fr')) return '🇫🇷';
		if (c.includes('сингапур') || c.includes('sg')) return '🇸🇬';
		if (c.includes('великобритан') || c.includes('gb') || c.includes('uk')) return '🇬🇧';
		if (c.includes('япони') || c.includes('jp')) return '🇯🇵';
		if (c.includes('швеци') || c.includes('se')) return '🇸🇪';
		if (c.includes('израиль') || c.includes('il')) return '🇮🇱';
		if (c.includes('европ') || c.includes('eu')) return '🇪🇺';
		return '🌐';
	}

	const filteredLogEvents = $derived.by(() => {
		if (radarFilter === 'confirmed') return logEvents.filter((e) => e.action === 'CONFIRMED');
		if (radarFilter === 'stall') return logEvents.filter((e) => e.action === 'STALL' || e.action === 'LATE-STALL' || e.action === 'QUIC' || e.action === 'RESET' || e.action === 'SYN-TIMEOUT');
		if (radarFilter === 'cooldown') return logEvents.filter((e) => e.action === 'COOLDOWN');
		return logEvents;
	});

	function applyDnsPreset(presetId: string) {
		selectedDnsPreset = presetId;
		const p = dnsPresets.find((x) => x.id === presetId);
		if (p && p.servers.length > 0) {
			if (!settings.dns) {
				settings.dns = { enabled: true, servers: [...p.servers], routeViaTunnel: true, interceptPort53: true };
			} else {
				settings.dns.servers = [...p.servers];
			}
			customDnsText = p.servers.join(', ');
		}
	}

	function handleCustomDnsChange(val: string) {
		customDnsText = val;
		const parts = val.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean);
		if (settings.dns) {
			settings.dns.servers = parts;
		}
	}

	let alwaysText = $state('');
	let neverText = $state('');

	let showEgressModal = $state(false);
	let egressFilterTab = $state<'all' | 'proxy' | 'group' | 'tunnel' | 'subscription' | 'singbox'>('all');
	let egressSearchQuery = $state('');

	const selectedEgress = $derived.by(() => {
		if (!selectedEgressValue) return null;
		return egresses.find((e) => egressKey(e.ref) === selectedEgressValue) || null;
	});

	const selectedEgressDescription = $derived.by(() => {
		if (!selectedEgress) return 'Нажмите, чтобы выбрать выход';
		if (selectedEgress.ref.kind === 'mihomo-proxy') return `Прокси-узел Mihomo · ${selectedEgress.interface}`;
		if (selectedEgress.ref.kind === 'mihomo-group') return `Прокси-группа Mihomo · ${selectedEgress.interface}`;
		if (selectedEgress.ref.kind === 'mihomo-subscription') return `Подписка Mihomo · ${selectedEgress.interface}`;
		if (selectedEgress.ref.kind === 'singbox-subscription') return `Подписка Sing-box · ${selectedEgress.interface}`;
		if (selectedEgress.ref.kind === 'singbox-outbound') return `Узел Sing-box · ${selectedEgress.interface}`;
		return `Туннель ядра · ${selectedEgress.interface}`;
	});

	const filteredEgresses = $derived.by(() => {
		let list = egresses;
		if (egressFilterTab === 'proxy') {
			list = list.filter((e) => e.ref.kind === 'mihomo-proxy' || e.ref.kind === 'singbox-outbound');
		} else if (egressFilterTab === 'group') {
			list = list.filter((e) => e.ref.kind === 'mihomo-group');
		} else if (egressFilterTab === 'tunnel') {
			list = list.filter((e) => e.ref.kind === 'kernel-tunnel');
		} else if (egressFilterTab === 'subscription') {
			list = list.filter((e) => e.ref.kind === 'mihomo-subscription' || e.ref.kind === 'singbox-subscription');
		} else if (egressFilterTab === 'singbox') {
			list = list.filter((e) => e.ref.engine === 'sing-box' || e.ref.kind === 'singbox-subscription' || e.ref.kind === 'singbox-outbound');
		}
		const q = egressSearchQuery.trim().toLowerCase();
		if (q) {
			list = list.filter(
				(e) =>
					e.displayName.toLowerCase().includes(q) ||
					e.interface.toLowerCase().includes(q) ||
					e.ref.kind.toLowerCase().includes(q) ||
					e.ref.engine.toLowerCase().includes(q)
			);
		}
		const catOrder: Record<string, number> = {
			'kernel-tunnel': 1,
			'mihomo-proxy': 2,
			'mihomo-group': 3,
			'mihomo-subscription': 4,
			'singbox-subscription': 5,
			'singbox-outbound': 6,
		};
		return [...list].sort((a, b) => {
			const oA = catOrder[a.ref.kind] ?? 99;
			const oB = catOrder[b.ref.kind] ?? 99;
			if (oA !== oB) return oA - oB;
			return a.displayName.localeCompare(b.displayName, undefined, { numeric: true, sensitivity: 'base' });
		});
	});

	let pollTimer: ReturnType<typeof setInterval> | null = null;

	const okEntries = $derived.by(() => {
		if (!learned) return [];
		const list: Array<{ ip: string; proto: string }> = [];
		if (learned.okTcp && learned.okTcp.length > 0) {
			for (const ip of learned.okTcp) {
				list.push({ ip, proto: 'tcp' });
			}
		}
		if (learned.okUdp && learned.okUdp.length > 0) {
			for (const ip of learned.okUdp) {
				list.push({ ip, proto: 'udp' });
			}
		}
		return list;
	});

	const filteredOkEntries = $derived.by(() => {
		const q = ipSearchQuery.trim().toLowerCase();
		if (!q) return okEntries;
		return okEntries.filter((item) => item.ip.toLowerCase().includes(q));
	});

	const testEntries = $derived.by(() => {
		if (!learned) return [];
		const list: Array<{ ip: string; proto: string }> = [];
		if (learned.testTcp && learned.testTcp.length > 0) {
			for (const ip of learned.testTcp) {
				list.push({ ip, proto: 'tcp' });
			}
		}
		if (learned.testUdp && learned.testUdp.length > 0) {
			for (const ip of learned.testUdp) {
				list.push({ ip, proto: 'udp' });
			}
		}
		return list;
	});

	const filteredTestEntries = $derived.by(() => {
		const q = ipSearchQuery.trim().toLowerCase();
		if (!q) return testEntries;
		return testEntries.filter((item) => item.ip.toLowerCase().includes(q));
	});

	async function loadLogs() {
		try {
			const res = await api.getAdaptiveRoutingLogs(50);
			logEvents = res.events || [];
		} catch {
			// ignore silently
		}
	}

	function egressKey(ref: EgressRef): string {
		if (!ref || !ref.resourceId) return '';
		return `${ref.engine}::${ref.kind}::${ref.resourceId}`;
	}

	function parseEgressKey(key: string): EgressRef {
		const parts = key.split('::');
		if (parts.length < 3) {
			return { engine: 'system', kind: 'kernel-tunnel', resourceId: '' };
		}
		return {
			engine: parts[0] as EgressRef['engine'],
			kind: parts[1] as EgressRef['kind'],
			resourceId: parts.slice(2).join('::'),
		};
	}

	let selectedEgressValue = $state('');

	function normalizeSnapshot(
		s: AdaptiveRoutingSettings,
		alwaysLines: string[],
		neverLines: string[]
	): string {
		const cleanAlways = alwaysLines.map((x) => x.trim()).filter(Boolean);
		const cleanNever = neverLines.map((x) => x.trim()).filter(Boolean);
		const obj = {
			enabled: Boolean(s.enabled),
			routingTableId: s.routingTableId ?? 105,
			fwmarkMask: s.fwmarkMask || '0x30000000',
			fwmarkTest: s.fwmarkTest || '0x10000000',
			fwmarkOk: s.fwmarkOk || '0x20000000',
			rulePriorityTest: s.rulePriorityTest ?? 96,
			rulePriorityOk: s.rulePriorityOk ?? 95,
			source: {
				type: s.source?.type || 'all_lan',
				policyId: s.source?.policyId || '',
			},
			primaryEgress: {
				kind: s.primaryEgress?.kind || 'kernel-tunnel',
				resourceId: s.primaryEgress?.resourceId || '',
				engine: s.primaryEgress?.engine || 'system',
			},
			failurePolicy: s.failurePolicy || 'direct',
			detection: {
				fastIntervalSeconds: s.detection?.fastIntervalSeconds ?? 1,
				softIntervalSeconds: s.detection?.softIntervalSeconds ?? 1,
				judgeIntervalSeconds: s.detection?.judgeIntervalSeconds ?? 1,
				healthIntervalSeconds: s.detection?.healthIntervalSeconds ?? 5,
				tcpSynRetries: s.detection?.tcpSynRetries ?? 2,
				lateStallBytes: s.detection?.lateStallBytes ?? 1500,
			},
			persistence: {
				okTtlSeconds: s.persistence?.okTtlSeconds ?? 0,
				maxEntries: s.persistence?.maxEntries ?? 4096,
				separateTcpUdp: Boolean(s.persistence?.separateTcpUdp),
			},
			dns: {
				enabled: Boolean(s.dns?.enabled),
				servers: (s.dns?.servers || ['1.1.1.1', '8.8.8.8']).map((x) => x.trim()).filter(Boolean),
				routeViaTunnel: s.dns?.routeViaTunnel !== false,
				interceptPort53: s.dns?.interceptPort53 !== false,
			},
			alwaysFileEnabled: s.alwaysFileEnabled !== false,
			neverFileEnabled: s.neverFileEnabled !== false,
			alwaysEntries: cleanAlways,
			neverEntries: cleanNever,
		};
		return JSON.stringify(obj);
	}

	function useServerSettings(next: AdaptiveRoutingSettings) {
		settings = structuredClone(next);
		if (!settings.dns) {
			settings.dns = {
				enabled: false,
				servers: ['1.1.1.1', '8.8.8.8'],
				routeViaTunnel: true,
				interceptPort53: true,
			};
		}
		customDnsText = (settings.dns.servers || []).join(', ');
		selectedEgressValue = egressKey(next.primaryEgress);
		const serverAlways = (next.alwaysEntries || []).map((x) => x.trim()).filter(Boolean);
		const serverNever = (next.neverEntries || []).map((x) => x.trim()).filter(Boolean);
		alwaysText = serverAlways.join('\n');
		neverText = serverNever.join('\n');
		initialSettingsJson = normalizeSnapshot(next, serverAlways, serverNever);
		settingsHydrated = true;
	}

	// Accurate Dirty state detection without false positives
	const isDirty = $derived.by(() => {
		if (!initialSettingsJson) return false;
		const curAlways = alwaysText.split('\n');
		const curNever = neverText.split('\n');
		return normalizeSnapshot(settings, curAlways, curNever) !== initialSettingsJson;
	});

	async function loadAll(initial = false) {
		if (initial) loading = true;
		try {
			const fetchLogs = !radarPollTimer;
			const [statusRes, egressRes, learnedRes, logsRes] = await Promise.all([
				api.getAdaptiveRoutingStatus(),
				api.getAdaptiveRoutingEgresses(),
				api.getAdaptiveRoutingLearned().catch(() => null),
				fetchLogs ? api.getAdaptiveRoutingLogs(50).catch(() => ({ events: [] })) : Promise.resolve(null),
			]);

			status = statusRes.state;
			if (!showEgressModal || egresses.length === 0) {
				egresses = egressRes.items || [];
			}
			learned = learnedRes;
			if (logsRes && logsRes.events) {
				logEvents = logsRes.events;
			}

			if (!settingsHydrated) {
				useServerSettings(statusRes.settings);
			}
		} catch (e) {
			if (initial) {
				notifications.error(`Ошибка загрузки Susanin: ${(e as Error).message}`);
			}
		} finally {
			if (initial) loading = false;
		}
	}

	onMount(() => {
		void loadAll(true);
		pollTimer = setInterval(() => {
			void loadAll(false);
		}, 5000);
	});

	onDestroy(() => {
		if (pollTimer) clearInterval(pollTimer);
		if (radarPollTimer) clearInterval(radarPollTimer);
	});

	function handleEgressChange(newVal: string) {
		selectedEgressValue = newVal;
		settings = { ...settings, primaryEgress: parseEgressKey(newVal) };
		testResult = null;
	}

	function selectSource(type: 'all_lan' | 'policy') {
		settings = {
			...settings,
			source: type === 'policy'
				? { type: 'policy', policyId: settings.source.policyId || policies[0]?.name || '' }
				: { type: 'all_lan' },
		};
	}

	function selectFailurePolicy(value: 'direct' | 'block') {
		settings = { ...settings, failurePolicy: value };
	}

	async function handleApply() {
		if (saving) return;
		saving = true;
		try {
			settings.alwaysEntries = alwaysText
				.split('\n')
				.map((s) => s.trim())
				.filter(Boolean);
			settings.neverEntries = neverText
				.split('\n')
				.map((s) => s.trim())
				.filter(Boolean);

			const res = await api.applyAdaptiveRouting(settings);
			status = res.state;
			useServerSettings(res.settings);
			notifications.success('Настройки Susanin успешно применены');
			void loadAll(false);
		} catch (e) {
			notifications.error(`Ошибка применения: ${(e as Error).message}`);
		} finally {
			saving = false;
		}
	}

	async function handleStart() {
		if (actionBusy) return;
		actionBusy = true;
		try {
			const res = await api.startAdaptiveRouting();
			status = res.state;
			notifications.success('Адаптивная маршрутизация Susanin запущена');
			void loadAll(false);
		} catch (e) {
			notifications.error(`Ошибка запуска: ${(e as Error).message}`);
		} finally {
			actionBusy = false;
		}
	}

	async function handleStop() {
		if (actionBusy) return;
		actionBusy = true;
		try {
			const res = await api.stopAdaptiveRouting();
			status = res.state;
			notifications.info('Адаптивная маршрутизация остановлена');
			void loadAll(false);
		} catch (e) {
			notifications.error(`Ошибка остановки: ${(e as Error).message}`);
		} finally {
			actionBusy = false;
		}
	}

	async function handleTestEgress() {
		if (!settings.primaryEgress.resourceId) {
			notifications.warning('Выберите выход для проверки');
			return;
		}
		testingEgress = true;
		testResult = null;
		try {
			const res = await api.testAdaptiveRoutingEgress(settings.primaryEgress);
			testResult = res;
			if (res.available) {
				notifications.success(`Выход доступен (интерфейс: ${res.interface})`);
			} else {
				notifications.warning(`Выход недоступен: ${res.reason || 'неизвестная ошибка'}`);
			}
		} catch (e) {
			notifications.error(`Ошибка проверки: ${(e as Error).message}`);
		} finally {
			testingEgress = false;
		}
	}

	const egressOptions = $derived.by(() => {
		const opts: DropdownOption[] = [
			{ value: '', label: '— Выберите выход для обхода —' },
		];

		const systemTunnels = egresses.filter((e) => e.ref.kind === 'kernel-tunnel');
		const mihomoGroups = egresses.filter((e) => e.ref.kind === 'mihomo-group');
		const mihomoProxies = egresses.filter((e) => e.ref.kind === 'mihomo-proxy');
		const mihomoSubscriptions = egresses.filter((e) => e.ref.kind === 'mihomo-subscription');
		const singboxOutbounds = egresses.filter((e) => e.ref.kind === 'singbox-outbound');

		if (systemTunnels.length > 0) {
			for (const t of systemTunnels) {
				opts.push({
					value: egressKey(t.ref),
					label: t.displayName,
					description: `Туннель · ${t.interface}`,
					group: 'Туннели системы',
				});
			}
		}

		if (mihomoGroups.length > 0) {
			for (const g of mihomoGroups) {
				opts.push({
					value: egressKey(g.ref),
					label: g.displayName,
					description: 'Прокси-группа Mihomo',
					group: 'Прокси-группы',
				});
			}
		}

		if (mihomoProxies.length > 0) {
			for (const p of mihomoProxies) {
				opts.push({
					value: egressKey(p.ref),
					label: p.displayName,
					description: 'Прокси-узел Mihomo',
					group: 'Прокси-узлы',
				});
			}
		}

		if (singboxOutbounds.length > 0) {
			for (const s of singboxOutbounds) {
				opts.push({
					value: egressKey(s.ref),
					label: s.displayName,
					description: 'Выход sing-box',
					group: 'Выходы sing-box',
				});
			}
		}

		for (const subscription of mihomoSubscriptions) {
			opts.push({
				value: egressKey(subscription.ref),
				label: subscription.displayName,
				description: 'Подписка Mihomo',
				group: 'Подписки',
			});
		}

		return opts;
	});

	const activeEgressItem = $derived(
		egresses.find((e) => egressKey(e.ref) === selectedEgressValue)
	);

	function applyDetectorPreset(preset: 'aggressive' | 'balanced' | 'soft') {
		if (preset === 'aggressive') {
			settings.detection.fastIntervalSeconds = 1;
			settings.detection.softIntervalSeconds = 1;
			settings.detection.judgeIntervalSeconds = 1;
			settings.detection.healthIntervalSeconds = 3;
			settings.detection.tcpSynRetries = 1;
			settings.detection.lateStallBytes = 800;
		} else if (preset === 'balanced') {
			settings.detection.fastIntervalSeconds = 2;
			settings.detection.softIntervalSeconds = 2;
			settings.detection.judgeIntervalSeconds = 2;
			settings.detection.healthIntervalSeconds = 5;
			settings.detection.tcpSynRetries = 2;
			settings.detection.lateStallBytes = 1400;
		} else if (preset === 'soft') {
			settings.detection.fastIntervalSeconds = 3;
			settings.detection.softIntervalSeconds = 3;
			settings.detection.judgeIntervalSeconds = 3;
			settings.detection.healthIntervalSeconds = 8;
			settings.detection.tcpSynRetries = 3;
			settings.detection.lateStallBytes = 2500;
		}
		const name = preset === 'aggressive' ? 'Агрессивный (1с / 1 ретрай)' : preset === 'balanced' ? 'Сбалансированный (2с / 2 ретрая)' : 'Мягкий (3с / 3 ретрая)';
		notifications.info(`Применён пресет детектора: ${name}`);
	}

	function addGeoblockedDomains() {
		const geoblockedList = [
			'openai.com',
			'*.openai.com',
			'chatgpt.com',
			'*.chatgpt.com',
			'oaistatic.com',
			'*.oaistatic.com',
			'oaiusercontent.com',
			'*.oaiusercontent.com',
			'anthropic.com',
			'*.anthropic.com',
			'claude.ai',
			'*.claude.ai',
			'cursor.com',
			'*.cursor.com',
			'cursor.sh',
			'*.cursor.sh',
			'cursor-cdn.com',
			'*.cursor-cdn.com',
			'deepl.com',
			'*.deepl.com',
			'midjourney.com',
			'*.midjourney.com',
			'perplexity.ai',
			'*.perplexity.ai',
			'intel.com',
			'*.intel.com',
			'amd.com',
			'*.amd.com',
			'docker.com',
			'*.docker.com',
			'docker.io',
			'*.docker.io',
			'atlassian.com',
			'*.atlassian.com',
			'atlassian.net',
			'*.atlassian.net',
			'autodesk.com',
			'*.autodesk.com',
			'canva.com',
			'*.canva.com',
			'canva-apps.com',
			'*.canva-apps.com',
			'notion.so',
			'*.notion.so',
			'notion.site',
			'*.notion.site',
			'spotify.com',
			'*.spotify.com',
			'spotifycdn.com',
			'*.spotifycdn.com',
			'figma.com',
			'*.figma.com',
			'adobe.io',
			'*.adobe.io',
		];

		const existing = new Set(
			alwaysText
				.split('\n')
				.map((s) => s.trim().toLowerCase())
				.filter(Boolean)
		);
		const toAdd = geoblockedList.filter((d) => !existing.has(d.toLowerCase()));
		if (toAdd.length === 0) {
			notifications.info('Все основные домены геоблокировок уже есть в списке');
			return;
		}
		const sep = alwaysText.trim() ? '\n' : '';
		alwaysText = alwaysText.trim() + sep + toAdd.join('\n');
		notifications.success(`Добавлено ${toAdd.length} доменов для обхода геоблокировок`);
	}
</script>

<div class="susanin-page">
	<!-- 1. Компактная Status Bar (Screen 3 Redesign) -->
	<div class="susanin-status-bar bg-[var(--color-bg-secondary)] rounded-xl border border-[var(--color-border)] flex flex-wrap items-center justify-between gap-3 px-4 py-2.5 shadow-xs">
		<div class="flex items-center gap-2.5 flex-wrap min-w-0">
			<!-- Pulsing LED & Status -->
			<div class="flex items-center gap-2 pr-3 border-r border-[var(--color-border)]">
				<span class="relative flex h-2.5 w-2.5">
					{#if status?.status === 'running'}
						<span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-success)] opacity-75"></span>
						<span class="relative inline-flex rounded-full h-2.5 w-2.5 bg-[var(--color-success)]"></span>
					{:else if status?.status === 'degraded'}
						<span class="relative inline-flex rounded-full h-2.5 w-2.5 bg-[var(--color-warning)]"></span>
					{:else}
						<span class="relative inline-flex rounded-full h-2.5 w-2.5 bg-gray-400"></span>
					{/if}
				</span>
				<span class="font-bold text-xs uppercase tracking-wider text-[var(--color-text-primary)]">
					{status?.status === 'running' ? 'Susanin активен' : status?.status === 'degraded' ? 'Susanin деградирован' : 'Susanin остановлен'}
				</span>
			</div>

			<!-- Routing Owner Pill -->
			<div class="flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-medium border {status?.routingOwner === 'susanin' ? 'bg-[var(--color-success-tint)] text-[var(--color-success)] border-[var(--color-success-border)]' : 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)] border-[var(--color-border)]'}">
				<span>Владелец:</span>
				<span class="font-semibold text-[var(--color-text-primary)]">{status?.routingOwner === 'susanin' ? 'Susanin' : status?.routingOwner || 'Нет'}</span>
			</div>

			<!-- Active Egress Pill -->
			{#if activeEgressItem}
				<button
					type="button"
					onclick={() => (showEgressModal = true)}
					class="flex items-center gap-1.5 text-xs bg-[var(--color-bg-tertiary)] hover:bg-[var(--color-bg-hover)] px-2.5 py-1 rounded-md border border-[var(--color-border)] transition-colors cursor-pointer group"
					title="Нажмите, чтобы сменить выход"
				>
					<span class="text-[var(--color-text-muted)]">Выход:</span>
					<span class="font-semibold text-[var(--color-text-primary)] truncate max-w-[200px] flex items-center gap-1">
						<span>{getEgressFlag(activeEgressItem.displayName || activeEgressItem.interface)}</span>
						<span>{activeEgressItem.displayName}</span>
					</span>
					<span class="text-[10px] font-mono text-[var(--color-text-muted)] ml-0.5">({activeEgressItem.interface})</span>
				</button>
			{:else}
				<button
					type="button"
					onclick={() => (showEgressModal = true)}
					class="text-xs text-[var(--color-warning)] hover:underline flex items-center gap-1 cursor-pointer bg-[var(--color-warning-tint)] px-2.5 py-1 rounded-md border border-[var(--color-warning)]/30"
				>
					<AlertTriangle class="w-3.5 h-3.5" />
					<span>Выбрать выход</span>
				</button>
			{/if}

			<!-- Policy Mode Pill -->
			<span class="px-2 py-0.5 rounded text-[11px] font-medium border {settings.failurePolicy === 'direct' ? 'bg-sky-500/10 text-sky-600 dark:text-sky-400 border-sky-500/20' : 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/20'}">
				{settings.failurePolicy === 'direct' ? 'Fail-Open (Direct)' : 'Kill-Switch'}
			</span>

			<!-- Live Stats Chips -->
			{#if status?.status === 'running'}
				<div class="hidden sm:flex items-center gap-1 text-[11px] text-[var(--color-text-muted)] bg-[var(--color-bg-tertiary)] px-2 py-0.5 rounded border border-[var(--color-border)]">
					<Database class="w-3 h-3 text-[var(--color-accent)]" />
					<span class="font-mono font-semibold text-[var(--color-text-primary)]">{status?.learnedTcpCount ?? 0}</span>
					<span>IP в VPN</span>
				</div>
			{/if}

			{#if isDirty}
				<span class="text-xs text-[var(--color-warning)] flex items-center gap-1 font-medium bg-[var(--color-warning-tint)] px-2.5 py-1 rounded-md border border-[var(--color-warning)]/30">
					<AlertTriangle class="w-3.5 h-3.5" />
					Есть несохранённые изменения
				</span>
			{/if}
		</div>

		<!-- Action Buttons: Right-aligned -->
		<div class="flex items-center gap-2 shrink-0 ml-auto">
			{#if isDirty}
				<Button
					variant="primary"
					size="sm"
					loading={saving}
					onclick={handleApply}
				>
					<Check class="w-3.5 h-3.5 mr-1" />
					Применить
				</Button>
			{/if}

			{#if status?.status === 'running'}
				<Button
					variant="secondary"
					size="sm"
					disabled={actionBusy}
					onclick={handleStop}
				>
					<Square class="w-3.5 h-3.5 mr-1 text-[var(--color-error)]" />
					Остановить
				</Button>
			{:else}
				<Button
					variant="primary"
					size="sm"
					disabled={actionBusy || !settings.primaryEgress.resourceId}
					title={!settings.primaryEgress.resourceId ? 'Сначала выберите выход' : 'Запустить адаптивную маршрутизацию'}
					onclick={handleStart}
				>
					<Play class="w-3.5 h-3.5 mr-1" />
					Запустить
				</Button>
			{/if}
		</div>
	</div>

	<!-- 2. Основная рабочая сетка из 2 сбалансированных карточек -->
	<div class="susanin-workspace grid grid-cols-1 lg:grid-cols-2 gap-3.5">
		<!-- Карточка 1: Выход и источник трафика -->
		<div class="susanin-card bg-[var(--color-bg-secondary)] rounded-xl border border-[var(--color-border)] flex flex-col justify-between shadow-xs">
			<div class="susanin-card-body">
				<div class="susanin-card-header flex items-center justify-between border-b border-[var(--color-border)]">
					<div class="flex items-center gap-2">
						<Network class="w-4 h-4 text-[var(--color-accent)]" />
						<h3 class="font-semibold text-sm text-[var(--color-text-primary)]">Маршрутизация и выход</h3>
					</div>
					<Badge variant="muted" size="sm">Шаг 1</Badge>
				</div>

				<!-- Выбор выхода -->
				<div class="susanin-form-section">
					<div class="flex items-center justify-between mb-1">
						<label class="block text-xs font-medium text-[var(--color-text-primary)]">
							Выход для обхода блокировок
						</label>
						<span class="text-[11px] text-[var(--color-text-muted)]">
							{egresses.length} доступно
						</span>
					</div>
					<div class="flex items-center gap-2">
						<button
							type="button"
							class="flex-1 min-w-0 p-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-primary)] hover:border-[var(--color-accent)] transition-all flex items-center justify-between text-left group shadow-2xs cursor-pointer"
							onclick={() => (showEgressModal = true)}
							title="Нажмите, чтобы выбрать выход в модальном окне"
						>
							<div class="flex items-center gap-2.5 min-w-0">
								<div class="w-8 h-8 rounded-lg bg-[var(--color-accent-tint)] flex items-center justify-center shrink-0 border border-[var(--color-accent-border)]">
									{#if selectedEgress?.ref.kind === 'mihomo-group'}
										<Layers class="w-4 h-4 text-[var(--color-accent)]" />
									{:else if selectedEgress?.ref.kind === 'kernel-tunnel'}
										<Shield class="w-4 h-4 text-[var(--color-accent)]" />
									{:else if selectedEgress?.ref.kind === 'mihomo-subscription'}
										<Globe class="w-4 h-4 text-[var(--color-accent)]" />
									{:else}
										<Zap class="w-4 h-4 text-[var(--color-accent)]" />
									{/if}
								</div>
								<div class="min-w-0">
									<div class="text-xs font-semibold text-[var(--color-text-primary)] group-hover:text-[var(--color-accent)] transition-colors truncate">
										{selectedEgress?.displayName || '— Выберите туннель или прокси —'}
									</div>
									<div class="text-[10px] text-[var(--color-text-muted)] truncate">
										{selectedEgressDescription}
									</div>
								</div>
							</div>
							<div class="flex items-center gap-1.5 shrink-0 ml-2">
								<span class="text-[11px] px-2 py-0.5 rounded-md bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] text-[var(--color-text-secondary)] group-hover:border-[var(--color-accent)] group-hover:text-[var(--color-accent)] transition-colors">
									Выбрать
								</span>
							</div>
						</button>
						<Button
							variant="secondary"
							size="sm"
							disabled={testingEgress || !selectedEgressValue}
							loading={testingEgress}
							onclick={handleTestEgress}
							title="Проверить доступность выхода"
						>
							<Activity class="w-3.5 h-3.5 mr-1" />
							Тест
						</Button>
					</div>

					{#if testResult}
						<div class="mt-2 text-xs flex items-center gap-1.5 p-2 rounded-lg border {testResult.available ? 'bg-[var(--color-success-tint)] border-[var(--color-success)] text-[var(--color-success)]' : 'bg-[var(--color-error-tint)] border-[var(--color-error)] text-[var(--color-error)]'}">
							{#if testResult.available}
								<CheckCircle class="w-3.5 h-3.5 shrink-0" />
								<span>Выход доступен (интерфейс {testResult.interface})</span>
							{:else}
								<AlertTriangle class="w-3.5 h-3.5 shrink-0" />
								<span>Недоступен: {testResult.reason || 'Ошибка проверки'}</span>
							{/if}
						</div>
					{/if}
				</div>

				<!-- Источник трафика -->
				<div class="susanin-form-section">
					<span class="block text-xs font-medium text-[var(--color-text-primary)] mb-1.5">
						Источник трафика
					</span>
					<div class="grid grid-cols-2 gap-2">
						<button
							type="button"
							class="px-3 py-2 text-xs rounded-lg border text-left transition-colors flex flex-col gap-0.5 {settings.source.type === 'all_lan' ? 'bg-[var(--color-accent-tint)] border-[var(--color-accent)] text-[var(--color-accent)] font-medium' : 'bg-[var(--color-bg-tertiary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-hover)]'}"
							onclick={() => selectSource('all_lan')}
						>
							<span class="font-semibold text-xs">Вся сеть LAN</span>
							<span class="text-[10px] opacity-80">Все домашние клиенты</span>
						</button>

						<button
							type="button"
							class="px-3 py-2 text-xs rounded-lg border text-left transition-colors flex flex-col gap-0.5 {settings.source.type === 'policy' ? 'bg-[var(--color-accent-tint)] border-[var(--color-accent)] text-[var(--color-accent)] font-medium' : 'bg-[var(--color-bg-tertiary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-hover)]'}"
							onclick={() => selectSource('policy')}
						>
							<span class="font-semibold text-xs">Политика Keenetic</span>
							<span class="text-[10px] opacity-80">Выборочные устройства</span>
						</button>
					</div>

					{#if settings.source.type === 'policy'}
						<div class="mt-2 p-2.5 bg-[var(--color-bg-tertiary)] rounded-lg border border-[var(--color-border)]">
							<label for="susanin-pol-select" class="block text-[11px] text-[var(--color-text-muted)] mb-1">
								Выберите политику Keenetic
							</label>
							<select
								id="susanin-pol-select"
								value={settings.source.policyId || ''}
								onchange={(e) => {
									settings = { ...settings, source: { ...settings.source, policyId: e.currentTarget.value } };
								}}
								class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)] focus:outline-none focus:border-[var(--color-accent)]"
							>
								{#each policies as pol}
									<option value={pol.name}>{pol.name} ({pol.description || 'Политика'})</option>
								{/each}
							</select>
						</div>
					{/if}
				</div>

				<!-- Политика при сбое -->
				<div class="susanin-form-section">
					<span class="block text-xs font-medium text-[var(--color-text-primary)] mb-1.5">
						Поведение при сбое выхода
					</span>
					<div class="grid grid-cols-2 gap-2">
						<button
							type="button"
							class="px-3 py-2 text-xs rounded-lg border text-left transition-colors flex flex-col gap-0.5 {settings.failurePolicy === 'direct' ? 'bg-[var(--color-accent-tint)] border-[var(--color-accent)] text-[var(--color-accent)] font-medium' : 'bg-[var(--color-bg-tertiary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-hover)]'}"
							onclick={() => selectFailurePolicy('direct')}
						>
							<span class="font-semibold text-xs">Fail-Open (Прямой)</span>
							<span class="text-[10px] opacity-80">Связь не прерывается</span>
						</button>

						<button
							type="button"
							class="px-3 py-2 text-xs rounded-lg border text-left transition-colors flex flex-col gap-0.5 {settings.failurePolicy === 'block' ? 'bg-[var(--color-accent-tint)] border-[var(--color-accent)] text-[var(--color-accent)] font-medium' : 'bg-[var(--color-bg-tertiary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-hover)]'}"
							onclick={() => selectFailurePolicy('block')}
						>
							<span class="font-semibold text-xs">Kill-Switch (Блок)</span>
							<span class="text-[10px] opacity-80">Защита от утечек</span>
						</button>
					</div>
				</div>

				<!-- Настройки DNS для Susanin (Generic / Engine-Agnostic) -->
				<div class="susanin-form-section pt-1 border-t border-[var(--color-border)]/50">
					<div class="flex items-center justify-between mb-1.5">
						<div class="flex items-center gap-1.5">
							<Globe class="w-3.5 h-3.5 text-[var(--color-accent)]" />
							<span class="text-xs font-semibold text-[var(--color-text-primary)]">
								DNS для клиентов Susanin
							</span>
						</div>
						<label class="relative inline-flex items-center cursor-pointer">
							<input
								type="checkbox"
								checked={Boolean(settings.dns?.enabled)}
								onchange={(e) => {
									if (!settings.dns) {
										settings.dns = { enabled: false, servers: ['1.1.1.1', '8.8.8.8'], routeViaTunnel: true, interceptPort53: true };
									}
									settings = { ...settings, dns: { ...settings.dns, enabled: e.currentTarget.checked } };
								}}
								class="sr-only peer"
							/>
							<div class="w-8 h-4.5 bg-gray-600 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-3.5 after:w-3.5 after:transition-all peer-checked:bg-[var(--color-accent)]"></div>
						</label>
					</div>

					{#if settings.dns?.enabled}
						<div class="p-3 bg-[var(--color-bg-tertiary)] rounded-lg border border-[var(--color-border)] space-y-2.5 transition-all">
							<!-- Preset selector -->
							<div>
								<label for="susanin-dns-preset" class="block text-[11px] text-[var(--color-text-muted)] mb-1">
									Предустановка или провайдер DNS
								</label>
								<select
									id="susanin-dns-preset"
									value={selectedDnsPreset}
									onchange={(e) => applyDnsPreset(e.currentTarget.value)}
									class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)] focus:outline-none focus:border-[var(--color-accent)]"
								>
									{#each dnsPresets as preset}
										<option value={preset.id}>{preset.name}</option>
									{/each}
								</select>
							</div>

							<!-- Custom DNS servers input -->
							<div>
								<label for="susanin-dns-servers" class="block text-[11px] text-[var(--color-text-muted)] mb-1">
									Серверы DNS (через запятую или пробел)
								</label>
								<input
									id="susanin-dns-servers"
									type="text"
									value={customDnsText}
									oninput={(e) => handleCustomDnsChange(e.currentTarget.value)}
									placeholder="1.1.1.1, 8.8.8.8"
									class="w-full px-2.5 py-1.5 text-xs font-mono bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)] focus:outline-none focus:border-[var(--color-accent)]"
								/>
							</div>

							<!-- Toggles: Route via tunnel and Intercept port 53 -->
							<div class="space-y-2 pt-1 border-t border-[var(--color-border)]/40 text-xs">
								<label class="flex items-start gap-2 cursor-pointer">
									<input
										type="checkbox"
										checked={settings.dns.routeViaTunnel !== false}
										onchange={(e) => {
											if (settings.dns) {
												settings = { ...settings, dns: { ...settings.dns, routeViaTunnel: e.currentTarget.checked } };
											}
										}}
										class="mt-0.5 rounded border-[var(--color-border)] text-[var(--color-accent)] focus:ring-[var(--color-accent)]"
									/>
									<div>
										<span class="font-medium text-[var(--color-text-primary)]">Маршрутизировать DNS через туннель вывода</span>
										<p class="text-[10px] text-[var(--color-text-muted)]">Запросы на порт 53 отправляются в таблицу 105 через выбранный туннель/прокси, обходя цензуру DNS провайдером</p>
									</div>
								</label>

								<label class="flex items-start gap-2 cursor-pointer">
									<input
										type="checkbox"
										checked={settings.dns.interceptPort53 !== false}
										onchange={(e) => {
											if (settings.dns) {
												settings = { ...settings, dns: { ...settings.dns, interceptPort53: e.currentTarget.checked } };
											}
										}}
										class="mt-0.5 rounded border-[var(--color-border)] text-[var(--color-accent)] focus:ring-[var(--color-accent)]"
									/>
									<div>
										<span class="font-medium text-[var(--color-text-primary)]">Перехватывать DNS-запросы устройств (порт 53)</span>
										<p class="text-[10px] text-[var(--color-text-muted)]">DNAT перенаправляет все DNS-запросы клиентов на указанный DNS, предотвращая утечки (DNS leak protection)</p>
									</div>
								</label>
							</div>
						</div>
					{:else}
						<div class="text-[11px] text-[var(--color-text-muted)] italic">
							Используется системный DNS роутера по умолчанию (независимо от движков Mihomo / sing-box)
						</div>
					{/if}
				</div>
			</div>
		</div>

		<!-- Карточка 2: Телеметрия обучения и списки доменов -->
		<div class="susanin-card bg-[var(--color-bg-secondary)] rounded-xl border border-[var(--color-border)] flex flex-col justify-between shadow-xs">
			<div class="susanin-card-body">
				<!-- Шапка и счетчики -->
				<div class="susanin-card-header flex items-center justify-between border-b border-[var(--color-border)]">
					<div class="flex items-center gap-2">
						<Zap class="w-4 h-4 text-[var(--color-warning)]" />
						<h3 class="font-semibold text-sm text-[var(--color-text-primary)]">Обучение и списки</h3>
					</div>
					<div class="flex items-center gap-1.5 flex-wrap">
						<button
							type="button"
							class="px-2.5 py-1 text-xs font-medium rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-primary)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] flex items-center gap-1.5 transition-colors shadow-2xs cursor-pointer"
							onclick={() => { modalTab = 'ok'; showLearnedModal = true; }}
							title="Открыть базу изученных IP"
						>
							<Database class="w-3.5 h-3.5 text-[var(--color-accent)]" />
							<span>База IP</span>
							<Badge variant="success" size="sm">{status?.learnedTcpCount ?? 0}</Badge>
						</button>

						<button
							type="button"
							class="px-2.5 py-1 text-xs font-medium rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-primary)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] flex items-center gap-1.5 transition-colors shadow-2xs cursor-pointer"
							onclick={() => {
								modalTab = 'radar';
								showLearnedModal = true;
								void loadLogs();
								if (!radarPollTimer) {
									radarPollTimer = setInterval(() => { void loadLogs(); }, 2000);
								}
							}}
							title="Открыть радар активности соединений"
						>
							<Activity class="w-3.5 h-3.5 text-[var(--color-accent)]" />
							<span>Радар активности</span>
						</button>

						<button
							type="button"
							class="p-1 text-xs text-[var(--color-text-muted)] hover:text-[var(--color-error)] hover:bg-[var(--color-error-tint)] rounded-lg transition-colors border border-transparent hover:border-[var(--color-error-border)] cursor-pointer"
							title="Очистить кэш обучения"
							onclick={async () => {
								try {
									await api.clearAdaptiveRoutingCache();
									notifications.success('Кэш обучения очищен');
									void loadAll(false);
								} catch (e) {
									notifications.error(`Ошибка очистки: ${(e as Error).message}`);
								}
							}}
						>
							<Trash2 class="w-3.5 h-3.5" />
						</button>
					</div>
				</div>

				<!-- Компактная полоса статистики -->
				<div class="susanin-stats-grid grid grid-cols-4 gap-2 text-center">
					<div class="p-2 rounded-lg bg-[var(--color-bg-tertiary)] border border-[var(--color-border)]">
						<div class="text-[10px] uppercase text-[var(--color-text-muted)] font-medium">TCP ОК</div>
						<div class="text-base font-bold text-[var(--color-success)] font-mono">
							{status?.learnedTcpCount ?? 0}
						</div>
					</div>
					<div class="p-2 rounded-lg bg-[var(--color-bg-tertiary)] border border-[var(--color-border)]">
						<div class="text-[10px] uppercase text-[var(--color-text-muted)] font-medium">UDP ОК</div>
						<div class="text-base font-bold text-[var(--color-success)] font-mono">
							{status?.learnedUdpCount ?? 0}
						</div>
					</div>
					<div class="p-2 rounded-lg bg-[var(--color-bg-tertiary)] border border-[var(--color-border)]">
						<div class="text-[10px] uppercase text-[var(--color-text-muted)] font-medium">В тесте</div>
						<div class="text-base font-bold text-[var(--color-warning)] font-mono">
							{(status?.testingTcpCount ?? 0) + (status?.testingUdpCount ?? 0)}
						</div>
					</div>
					<div class="p-2 rounded-lg bg-[var(--color-bg-tertiary)] border border-[var(--color-border)]">
						<div class="text-[10px] uppercase text-[var(--color-text-muted)] font-medium">Прямо</div>
						<div class="text-base font-bold text-[var(--color-text-secondary)] font-mono">
							{status?.neverCount ?? 0}
						</div>
					</div>
				</div>

				<!-- Компактная мини-лента активности детектора в реальном времени -->
				<div class="p-2.5 rounded-lg bg-[var(--color-bg-primary)] border border-[var(--color-border)] flex flex-col gap-1.5 my-2">
					<div class="flex items-center justify-between text-[11px]">
						<div class="flex items-center gap-1.5 text-[var(--color-text-secondary)] font-medium">
							<span class="w-1.5 h-1.5 rounded-full bg-[var(--color-accent)] opacity-80"></span>
							<span>Анализ соединений в реальном времени</span>
						</div>
						<button
							type="button"
							class="text-[var(--color-accent)] hover:underline flex items-center gap-0.5"
							onclick={() => {
								modalTab = 'radar';
								showLearnedModal = true;
								void loadLogs();
								if (!radarPollTimer) {
									radarPollTimer = setInterval(() => { void loadLogs(); }, 2000);
								}
							}}
						>
							Радар подробно →
						</button>
					</div>
					{#if logEvents.length > 0}
						<div class="space-y-1">
							{#each logEvents.slice(0, 3) as ev}
								<div class="flex items-center justify-between text-[11px] font-mono text-[var(--color-text-secondary)] truncate">
									<div class="flex items-center gap-1.5 truncate">
										<span class="text-[9px] px-1.5 py-0.2 rounded font-mono font-medium border border-[var(--color-border)] bg-[var(--color-bg-secondary)] {ev.action === 'CONFIRMED' ? 'text-[var(--color-success)]' : ev.action === 'STALL' || ev.action === 'LATE-STALL' ? 'text-[var(--color-warning)]' : ev.action === 'RESET' ? 'text-[var(--color-error)]' : 'text-[var(--color-text-secondary)]'}">
											{ev.action}
										</span>
										<span class="text-[var(--color-text-primary)] font-semibold truncate">{ev.target || ev.message}</span>
									</div>
									<span class="text-[10px] text-[var(--color-text-muted)] ml-2 shrink-0">{ev.timestamp}</span>
								</div>
							{/each}
						</div>
					{:else}
						<div class="text-[11px] text-[var(--color-text-muted)] italic">
							Ожидание сетевой активности детектора...
						</div>
					{/if}
				</div>

				<!-- Вкладки Always / Never списков -->
				<div class="susanin-lists">
					<div class="flex items-center gap-2 mb-2">
						<button
							type="button"
							class="text-xs px-2.5 py-1 rounded-md transition-colors {activeListTab === 'always' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-medium border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
							onclick={() => (activeListTab = 'always')}
						>
							Всегда через VPN (Always)
							<span class="ml-1 text-[10px] px-1 py-0.2 rounded bg-[var(--color-accent-tint)] text-[var(--color-accent)]">
								{alwaysText.split('\n').filter((x) => x.trim()).length}
							</span>
						</button>

						<button
							type="button"
							class="text-xs px-2.5 py-1 rounded-md transition-colors {activeListTab === 'never' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-medium border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
							onclick={() => (activeListTab = 'never')}
						>
							Всегда напрямую (Never)
							<span class="ml-1 text-[10px] px-1 py-0.2 rounded bg-gray-500/20 text-[var(--color-text-secondary)]">
								{neverText.split('\n').filter((x) => x.trim()).length}
							</span>
						</button>
					</div>

					{#if activeListTab === 'always'}
						<textarea
							bind:value={alwaysText}
							rows={6}
							placeholder="Домены и IP, по одному на строку:&#10;instagram.com&#10;*.rutracker.org&#10;149.154.160.0/20"
							class="w-full p-2.5 text-xs font-mono bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-lg text-[var(--color-text-primary)] placeholder:text-[var(--color-text-muted)] focus:outline-none focus:border-[var(--color-accent)] resize-y"
						></textarea>
						<div class="flex items-center justify-between gap-2 mt-1.5 flex-wrap">
							<p class="text-[11px] text-[var(--color-text-muted)]">
								Направления, которые всегда принудительно направляются в туннель.
							</p>
							<button
								type="button"
								class="text-[11px] text-[var(--color-accent)] hover:underline flex items-center gap-1 shrink-0 font-medium cursor-pointer"
								onclick={addGeoblockedDomains}
								title="Добавить популярные зарубежные сервисы с геоблокировкой РФ (ChatGPT, Claude, Intel, Canva, Notion и др.)"
							>
								<Globe class="w-3.5 h-3.5 text-sky-500" />
								<span>+ Список геоблокировок (AI, Intel, Canva...)</span>
							</button>
						</div>
					{:else}
						<textarea
							bind:value={neverText}
							rows={6}
							placeholder="Домены и IP, по одному на строку:&#10;gosuslugi.ru&#10;sberbank.ru&#10;192.168.0.0/16"
							class="w-full p-2.5 text-xs font-mono bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-lg text-[var(--color-text-primary)] placeholder:text-[var(--color-text-muted)] focus:outline-none focus:border-[var(--color-accent)] resize-y"
						></textarea>
						<p class="text-[11px] text-[var(--color-text-muted)] mt-1">
							Ресурсы банков, госуслуг и локальные сети, которые никогда не направляются в VPN.
						</p>
					{/if}
				</div>
			</div>
		</div>
	</div>

	<!-- 3. Сворачиваемая тонкая настройка детектора -->
	<div class="susanin-detector bg-[var(--color-bg-secondary)] rounded-xl border border-[var(--color-border)] overflow-hidden shadow-xs">
		<button
			type="button"
			class="w-full px-4 py-2.5 flex items-center justify-between hover:bg-[var(--color-bg-tertiary)] transition-colors text-left"
			onclick={() => (detectorExpanded = !detectorExpanded)}
		>
			<div class="flex items-center gap-2">
				<Settings2 class="w-4 h-4 text-[var(--color-text-muted)]" />
				<span class="font-medium text-xs text-[var(--color-text-primary)]">Параметры детектора блокировок</span>
				<Badge variant="muted" size="sm">Экспертные настройки</Badge>
			</div>
			<div class="flex items-center gap-2">
				<span class="text-xs text-[var(--color-text-muted)]">
					{detectorExpanded ? 'Свернуть' : 'Настроить'}
				</span>
				<ChevronDown class="w-3.5 h-3.5 text-[var(--color-text-muted)] transition-transform {detectorExpanded ? 'rotate-180' : ''}" />
			</div>
		</button>

		{#if detectorExpanded}
			<div class="p-4 pt-2 border-t border-[var(--color-border)] space-y-3.5 bg-[var(--color-bg-primary)]">
				<!-- Пресеты -->
				<div class="flex items-center gap-2 flex-wrap">
					<span class="text-xs text-[var(--color-text-muted)] mr-1">Быстрые пресеты:</span>
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] transition-colors"
						onclick={() => applyDetectorPreset('aggressive')}
					>
						Агрессивный (1с / 1 ретрай)
					</button>
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] transition-colors"
						onclick={() => applyDetectorPreset('balanced')}
					>
						Сбалансированный (2с / 2 ретрая)
					</button>
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] transition-colors"
						onclick={() => applyDetectorPreset('soft')}
					>
						Мягкий (3с / 3 ретрая)
					</button>
				</div>

				<!-- Сетка параметров -->
				<div class="grid grid-cols-2 sm:grid-cols-4 md:grid-cols-4 gap-3">
					<div>
						<label for="susanin-fast-interval" class="block text-[11px] text-[var(--color-text-muted)] mb-1">Fast интервал (с)</label>
						<input
							id="susanin-fast-interval"
							type="number"
							min="1"
							max="10"
							bind:value={settings.detection.fastIntervalSeconds}
							class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)]"
						/>
					</div>

					<div>
						<label for="susanin-soft-interval" class="block text-[11px] text-[var(--color-text-muted)] mb-1">Soft интервал (с)</label>
						<input
							id="susanin-soft-interval"
							type="number"
							min="1"
							max="10"
							bind:value={settings.detection.softIntervalSeconds}
							class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)]"
						/>
					</div>

					<div>
						<label for="susanin-judge-interval" class="block text-[11px] text-[var(--color-text-muted)] mb-1">Judge интервал (с)</label>
						<input
							id="susanin-judge-interval"
							type="number"
							min="1"
							max="10"
							bind:value={settings.detection.judgeIntervalSeconds}
							class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)]"
						/>
					</div>

					<div>
						<label for="susanin-health-interval" class="block text-[11px] text-[var(--color-text-muted)] mb-1">Health интервал (с)</label>
						<input
							id="susanin-health-interval"
							type="number"
							min="2"
							max="30"
							bind:value={settings.detection.healthIntervalSeconds}
							class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)]"
						/>
					</div>

					<div>
						<label for="susanin-syn-retries" class="block text-[11px] text-[var(--color-text-muted)] mb-1">SYN ретраев</label>
						<input
							id="susanin-syn-retries"
							type="number"
							min="1"
							max="5"
							bind:value={settings.detection.tcpSynRetries}
							class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)]"
						/>
					</div>

					<div>
						<label for="susanin-late-stall" class="block text-[11px] text-[var(--color-text-muted)] mb-1">Late stall (байт)</label>
						<input
							id="susanin-late-stall"
							type="number"
							min="500"
							max="10000"
							step="100"
							bind:value={settings.detection.lateStallBytes}
							class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)]"
						/>
					</div>

					<div>
						<label for="susanin-ok-ttl" class="block text-[11px] text-[var(--color-text-muted)] mb-1">Время жизни IP (TTL)</label>
						<select
							id="susanin-ok-ttl"
							bind:value={settings.persistence.okTtlSeconds}
							class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)] focus:outline-none focus:border-[var(--color-accent)]"
						>
							<option value={0}>Бессрочно (до сбоя)</option>
							<option value={3600}>1 час</option>
							<option value={43200}>12 часов</option>
							<option value={86400}>24 часа (1 день)</option>
							<option value={259200}>3 дня</option>
							<option value={604800}>7 дней</option>
							<option value={2592000}>30 дней</option>
						</select>
					</div>

					<div>
						<label for="susanin-max-entries" class="block text-[11px] text-[var(--color-text-muted)] mb-1">Макс. записей</label>
						<input
							id="susanin-max-entries"
							type="number"
							min="512"
							max="65536"
							step="512"
							bind:value={settings.persistence.maxEntries}
							class="w-full p-1.5 text-xs bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded text-[var(--color-text-primary)]"
						/>
					</div>
				</div>
			</div>
		{/if}
	</div>
</div>

<!-- Модальное окно базы изученных адресов и радара -->
{#if showLearnedModal}
	<Modal
		open={showLearnedModal}
		title="База маршрутов и радар Susanin"
		size="wide"
		bodyLayout="fill"
		onclose={() => {
			showLearnedModal = false;
			if (radarPollTimer) {
				clearInterval(radarPollTimer);
				radarPollTimer = null;
			}
		}}
	>
		<div class="flex flex-col flex-1 min-h-0 h-full p-4 gap-3.5">
			<!-- Tab navigation & actions toolbar -->
			<div class="shrink-0 flex items-center justify-between gap-2 flex-wrap border-b border-[var(--color-border)] pb-2.5">
				<div class="flex items-center gap-1.5 flex-wrap">
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors flex items-center gap-1.5 {modalTab === 'ok' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => {
							modalTab = 'ok';
							if (radarPollTimer) { clearInterval(radarPollTimer); radarPollTimer = null; }
						}}
					>
						<span>Изученные адреса</span>
						<Badge variant="success" size="sm">{okEntries.length}</Badge>
					</button>

					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors flex items-center gap-1.5 {modalTab === 'radar' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => {
							modalTab = 'radar';
							void loadLogs();
							if (!radarPollTimer) {
								radarPollTimer = setInterval(() => { void loadLogs(); }, 2000);
							}
						}}
					>
						<Activity class="w-3.5 h-3.5 text-[var(--color-text-muted)]" />
						<span>Радар активности</span>
						<span class="w-1.5 h-1.5 rounded-full bg-[var(--color-accent)] opacity-75"></span>
					</button>

					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors flex items-center gap-1.5 {modalTab === 'test' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => {
							modalTab = 'test';
							if (radarPollTimer) { clearInterval(radarPollTimer); radarPollTimer = null; }
						}}
					>
						<span>В процессе теста</span>
						<Badge variant="warning" size="sm">{testEntries.length}</Badge>
					</button>

					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors flex items-center gap-1.5 {modalTab === 'always' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => {
							modalTab = 'always';
							if (radarPollTimer) { clearInterval(radarPollTimer); radarPollTimer = null; }
						}}
					>
						<span>Всегда в VPN</span>
						<Badge variant="muted" size="sm">{(learned?.always || []).length}</Badge>
					</button>

					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors flex items-center gap-1.5 {modalTab === 'never' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => {
							modalTab = 'never';
							if (radarPollTimer) { clearInterval(radarPollTimer); radarPollTimer = null; }
						}}
					>
						<span>Напрямую</span>
						<Badge variant="muted" size="sm">{(learned?.never || []).length}</Badge>
					</button>
				</div>

				<div class="flex items-center gap-1.5">
					<Button
						variant="secondary"
						size="sm"
						onclick={() => {
							void loadAll(false);
							void loadLogs();
						}}
					>
						<RefreshCw class="w-3.5 h-3.5 mr-1" />
						Обновить
					</Button>
					<Button
						variant="danger"
						size="sm"
						onclick={async () => {
							try {
								await api.clearAdaptiveRoutingCache();
								notifications.success('Кэш обучения очищен');
								void loadAll(false);
							} catch (e) {
								notifications.error(`Ошибка очистки: ${(e as Error).message}`);
							}
						}}
					>
						<Trash2 class="w-3.5 h-3.5 mr-1" />
						Очистить кэш
					</Button>
				</div>
			</div>

			<!-- Search bar for lists -->
			{#if modalTab === 'ok' || modalTab === 'test'}
				<div class="shrink-0 relative">
					<Search class="w-4 h-4 text-[var(--color-text-muted)] absolute left-2.5 top-1/2 -translate-y-1/2" />
					<input
						type="text"
						bind:value={ipSearchQuery}
						placeholder="Фильтр по IP адресу (например, 157.240)..."
						class="w-full pl-8 pr-3 py-1.5 text-xs bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-md text-[var(--color-text-primary)] placeholder:text-[var(--color-text-muted)] focus:outline-none focus:border-[var(--color-accent)]"
					/>
				</div>
			{/if}

			<!-- Tab 1: Изученные адреса (ОК) -->
			{#if modalTab === 'ok'}
				<div class="flex-1 min-h-0 flex flex-col bg-[var(--color-bg-secondary)] p-3 rounded-lg border border-[var(--color-border)]">
					<div class="shrink-0 text-xs text-[var(--color-text-muted)] mb-2.5 flex items-center justify-between">
						<span>Адреса, подтверждённые Susanin и направляемые в туннель:</span>
						<span>{filteredOkEntries.length} из {okEntries.length}</span>
					</div>
					<div class="flex-1 min-h-0 grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 gap-2.5 overflow-y-auto content-start pr-1">
						{#each filteredOkEntries as item}
							{@const info = lookupIpKnowledge(item.ip)}
							<div class="p-2.5 rounded-lg bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] hover:border-[var(--color-accent)] flex flex-col justify-between gap-1.5 transition-all shadow-2xs">
								<div class="flex items-center justify-between gap-2">
									<div class="flex items-center gap-1.5 font-mono min-w-0">
										<span class="w-2 h-2 rounded-full bg-[var(--color-success)] shrink-0"></span>
										<span class="text-xs font-semibold text-[var(--color-text-primary)] truncate">{item.ip}</span>
										<span class="text-[9px] uppercase px-1 py-0.2 rounded bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] font-mono border border-[var(--color-border)] shrink-0">{item.proto}</span>
									</div>
									<button
										type="button"
										class="text-[var(--color-text-muted)] hover:text-[var(--color-error)] text-[11px] px-1.5 py-0.5 rounded hover:bg-[var(--color-error-tint)] transition-colors shrink-0"
										title="Забыть этот адрес и вернуть на прямой доступ"
										onclick={async () => {
											try {
												await api.forgetAdaptiveRoute(item.ip, item.proto);
												notifications.success(`Адрес ${item.ip} удалён из базы`);
												void loadAll(false);
											} catch (e) {
												notifications.error(`Ошибка: ${(e as Error).message}`);
											}
										}}
									>
										Забыть
									</button>
								</div>
								<div class="flex items-center gap-1.5 flex-wrap pt-0.5 border-t border-[var(--color-border)]/40">
									{#if info}
										<span class="text-[10px] font-medium px-1.5 py-0.2 rounded bg-[var(--color-accent-tint)] text-[var(--color-accent)] border border-[var(--color-accent-border)] truncate max-w-[190px]">
											{info.title}
										</span>
										{#if info.country}
											<span class="text-[10px] text-[var(--color-text-muted)]">
												{info.country}
											</span>
										{/if}
									{:else}
										<span class="text-[10px] text-[var(--color-text-muted)] italic">
											Внешний узел
										</span>
									{/if}
								</div>
							</div>
						{:else}
							<div class="col-span-full text-[var(--color-text-muted)] italic text-center py-8">
								{okEntries.length === 0 ? 'Сусанин пока не зафиксировал блокировок. База наполняется автоматически при появлении сетевых сбоев.' : 'Ничего не найдено по вашему фильтру.'}
							</div>
						{/each}
					</div>
				</div>
			{/if}

			<!-- Tab 2: Радар активности -->
			{#if modalTab === 'radar'}
				<div class="flex-1 min-h-0 flex flex-col gap-3">
					<!-- Сводные метрики радара -->
					<div class="shrink-0 grid grid-cols-2 sm:grid-cols-5 gap-2">
						<div class="p-2.5 rounded-lg bg-[var(--color-bg-secondary)] border border-[var(--color-border)] flex flex-col">
							<span class="text-[10px] text-[var(--color-text-muted)] uppercase tracking-wider">Всего событий</span>
							<span class="text-lg font-bold font-mono text-[var(--color-text-primary)] mt-0.5">{logEvents.length}</span>
						</div>
						<div class="p-2.5 rounded-lg bg-[var(--color-bg-secondary)] border border-[var(--color-border)] flex flex-col">
							<span class="text-[10px] text-[var(--color-success)] uppercase tracking-wider">В туннель</span>
							<span class="text-lg font-bold font-mono text-[var(--color-success)] mt-0.5">{logEvents.filter((e) => e.action === 'CONFIRMED').length}</span>
						</div>
						<div class="p-2.5 rounded-lg bg-[var(--color-bg-secondary)] border border-[var(--color-border)] flex flex-col">
							<span class="text-[10px] text-[var(--color-warning)] uppercase tracking-wider">Задержки DPI</span>
							<span class="text-lg font-bold font-mono text-[var(--color-warning)] mt-0.5">{logEvents.filter((e) => e.action === 'STALL' || e.action === 'LATE-STALL').length}</span>
						</div>
						<div class="p-2.5 rounded-lg bg-[var(--color-bg-secondary)] border border-[var(--color-border)] flex flex-col">
							<span class="text-[10px] text-[var(--color-error)] uppercase tracking-wider">Сбросы RST</span>
							<span class="text-lg font-bold font-mono text-[var(--color-error)] mt-0.5">{logEvents.filter((e) => e.action === 'RESET').length}</span>
						</div>
						<div class="p-2.5 rounded-lg bg-[var(--color-bg-secondary)] border border-[var(--color-border)] flex flex-col">
							<span class="text-[10px] text-[var(--color-accent)] uppercase tracking-wider">QUIC / UDP</span>
							<span class="text-lg font-bold font-mono text-[var(--color-accent)] mt-0.5">{logEvents.filter((e) => e.action === 'QUIC').length}</span>
						</div>
					</div>

					<div class="flex-1 min-h-0 flex flex-col bg-[var(--color-bg-secondary)] p-3 rounded-lg border border-[var(--color-border)]">
						<!-- Тулбар фильтрации радара -->
						<div class="shrink-0 flex flex-col sm:flex-row sm:items-center justify-between gap-2 mb-3 pb-2.5 border-b border-[var(--color-border)]/50">
							<div class="flex items-center gap-1.5 flex-wrap">
								<button
									type="button"
									class="text-xs px-2.5 py-1 rounded-md transition-colors {radarFilter === 'all' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
									onclick={() => (radarFilter = 'all')}
								>
									Все ({logEvents.length})
								</button>
								<button
									type="button"
									class="text-xs px-2.5 py-1 rounded-md transition-colors {radarFilter === 'confirmed' ? 'bg-[var(--color-success)]/15 text-[var(--color-success)] font-semibold border border-[var(--color-success)]/30' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
									onclick={() => (radarFilter = 'confirmed')}
								>
									В туннеле ({logEvents.filter((e) => e.action === 'CONFIRMED').length})
								</button>
								<button
									type="button"
									class="text-xs px-2.5 py-1 rounded-md transition-colors {radarFilter === 'stall' ? 'bg-[var(--color-warning)]/15 text-[var(--color-warning)] font-semibold border border-[var(--color-warning)]/30' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
									onclick={() => (radarFilter = 'stall')}
								>
									Сбои и задержки ({logEvents.filter((e) => e.action === 'STALL' || e.action === 'LATE-STALL' || e.action === 'RESET' || e.action === 'QUIC').length})
								</button>
								<button
									type="button"
									class="text-xs px-2.5 py-1 rounded-md transition-colors {radarFilter === 'cooldown' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
									onclick={() => (radarFilter = 'cooldown')}
								>
									Охлаждение ({logEvents.filter((e) => e.action === 'COOLDOWN').length})
								</button>
							</div>

							<div class="flex items-center gap-2 text-[11px] font-mono text-[var(--color-text-secondary)]">
								<span class="w-2 h-2 rounded-full bg-[var(--color-accent)] animate-pulse"></span>
								<span>2с интервал · {filteredLogEvents.length} в выборке</span>
							</div>
						</div>

						<!-- Лента событий -->
						<div class="flex-1 min-h-0 space-y-1.5 overflow-y-auto pr-1">
							{#each filteredLogEvents as ev}
								{@const targetKnowledge = ev.target ? lookupIpKnowledge(ev.target) : null}
								{@const title = ev.resourceTitle || targetKnowledge?.title || (ev.target ? 'Внешний узел' : '')}
								{@const country = ev.resourceCountry || targetKnowledge?.country || ''}
								{@const cc = ev.resourceCc || targetKnowledge?.countryCode || ''}
								{@const icon = ev.resourceIcon || (cc ? getFlagFromCountry(cc) : '🌐')}
								<div class="p-2.5 rounded-lg bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] hover:border-[var(--color-border-hover)] flex flex-col md:flex-row md:items-center justify-between gap-2.5 text-xs transition-colors">
									<!-- Слева: время, тип действия, IP, ресурс -->
									<div class="flex items-center gap-2 flex-wrap min-w-0">
										<span class="text-[11px] font-mono text-[var(--color-text-muted)] shrink-0">{ev.timestamp}</span>

										<span class="text-[10px] font-semibold px-2 py-0.5 rounded font-mono border shrink-0 flex items-center gap-1.5 {
											ev.action === 'CONFIRMED' ? 'bg-[var(--color-success)]/10 text-[var(--color-success)] border-[var(--color-success)]/30' :
											ev.action === 'STALL' || ev.action === 'LATE-STALL' ? 'bg-[var(--color-warning)]/10 text-[var(--color-warning)] border-[var(--color-warning)]/30' :
											ev.action === 'RESET' ? 'bg-[var(--color-error)]/10 text-[var(--color-error)] border-[var(--color-error)]/30' :
											ev.action === 'QUIC' ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)] border-[var(--color-accent)]/30' :
											'bg-[var(--color-bg-secondary)] text-[var(--color-text-secondary)] border-[var(--color-border)]'
										}">
											<span class="w-1.5 h-1.5 rounded-full {
												ev.action === 'CONFIRMED' ? 'bg-[var(--color-success)]' :
												ev.action === 'STALL' || ev.action === 'LATE-STALL' ? 'bg-[var(--color-warning)]' :
												ev.action === 'RESET' ? 'bg-[var(--color-error)]' :
												ev.action === 'QUIC' ? 'bg-[var(--color-accent)]' :
												'bg-[var(--color-text-muted)]'
											}"></span>
											{ev.action}
										</span>

										{#if ev.target}
											<span class="font-mono font-medium text-[var(--color-text-primary)] shrink-0 px-1.5 py-0.5 bg-[var(--color-bg-secondary)] rounded border border-[var(--color-border)]/60 text-[11px]">
												{ev.target}
											</span>
										{/if}

										{#if title}
											<div class="flex items-center gap-1.5 px-2 py-0.5 rounded bg-[var(--color-accent-tint)]/25 text-[var(--color-accent)] border border-[var(--color-accent-border)] font-medium shrink-0 shadow-2xs">
												<span class="text-xs">{icon}</span>
												<span class="text-[11px] font-semibold truncate max-w-[200px]">{title}</span>
												{#if country}
													<span class="text-[10px] text-[var(--color-text-muted)] opacity-80 shrink-0">· {country}</span>
												{/if}
											</div>
										{/if}
									</div>

									<!-- Справа: пояснение и направление -->
									<div class="flex items-center gap-2 shrink-0 md:max-w-[45%] text-[11px] justify-between md:justify-end">
										<span class="text-[var(--color-text-secondary)] truncate" title={ev.message}>{ev.message}</span>
										{#if ev.action === 'CONFIRMED'}
											<span class="shrink-0 text-[10px] px-2 py-0.5 rounded bg-[var(--color-success)]/15 text-[var(--color-success)] border border-[var(--color-success)]/30 font-medium">
												→ Туннель
											</span>
										{:else if ev.action === 'COOLDOWN'}
											<span class="shrink-0 text-[10px] px-2 py-0.5 rounded bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] border border-[var(--color-border)]">
												→ Прямой
											</span>
										{/if}
									</div>
								</div>
							{:else}
								<div class="text-[var(--color-text-muted)] italic text-center py-8">
									{radarFilter === 'all'
										? 'Журнал событий пока пуст. События появляются в реальном времени при открытии заблокированных видео или сайтов.'
										: 'Нет событий, соответствующих выбранному фильтру.'}
								</div>
							{/each}
						</div>
					</div>
				</div>
			{/if}

			<!-- Tab 3: В процессе теста -->
			{#if modalTab === 'test'}
				<div class="flex-1 min-h-0 flex flex-col bg-[var(--color-bg-secondary)] p-3 rounded-lg border border-[var(--color-border)]">
					<div class="shrink-0 text-xs text-[var(--color-text-muted)] mb-2 flex items-center justify-between">
						<span>Адреса, проходящие проверку на блокировку прямо сейчас:</span>
						<span>{filteredTestEntries.length} из {testEntries.length}</span>
					</div>
					<div class="flex-1 min-h-0 grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 gap-2.5 overflow-y-auto content-start pr-1">
						{#each filteredTestEntries as item}
							{@const info = lookupIpKnowledge(item.ip)}
							<div class="p-2.5 rounded-lg bg-[var(--color-bg-tertiary)] flex flex-col justify-between gap-1.5 text-xs font-mono border border-[var(--color-border)] shadow-2xs">
								<div class="flex items-center justify-between gap-2 min-w-0">
									<div class="flex items-center gap-1.5 min-w-0">
										<span class="w-2 h-2 rounded-full bg-[var(--color-warning)] shrink-0"></span>
										<span class="text-[var(--color-text-primary)] font-semibold truncate">{item.ip}</span>
										<span class="text-[9px] uppercase px-1 py-0.2 rounded bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] font-mono border border-[var(--color-border)] shrink-0">{item.proto}</span>
									</div>
									<span class="text-[11px] text-[var(--color-warning)] font-sans shrink-0 ml-1">Тест</span>
								</div>
								{#if info}
									<div class="pt-0.5 border-t border-[var(--color-border)]/40 font-sans">
										<span class="text-[10px] font-medium px-1.5 py-0.2 rounded bg-[var(--color-accent-tint)] text-[var(--color-accent)] border border-[var(--color-accent-border)] truncate max-w-[190px]">
											{info.title}
										</span>
									</div>
								{/if}
							</div>
						{:else}
							<div class="col-span-full text-[var(--color-text-muted)] italic text-center py-8">
								Сейчас нет адресов на стадии тестирования.
							</div>
						{/each}
					</div>
				</div>
			{/if}

			<!-- Tab 4: Always -->
			{#if modalTab === 'always'}
				<div class="flex-1 min-h-0 flex flex-col bg-[var(--color-bg-secondary)] p-3 rounded-lg border border-[var(--color-border)]">
					<div class="shrink-0 font-semibold text-xs text-[var(--color-text-primary)] mb-2 flex items-center justify-between">
						<span>Фиксированные подсети и домены (Always)</span>
						<Badge variant="accent" size="sm">{(learned?.always || []).length} записей</Badge>
					</div>
					<div class="flex-1 min-h-0 grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-2 overflow-y-auto content-start text-xs font-mono text-[var(--color-text-secondary)] pr-1">
						{#each learned?.always || [] as entry}
							<div class="p-1.5 rounded bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] truncate">
								{entry}
							</div>
						{:else}
							<div class="col-span-full text-[var(--color-text-muted)] italic text-center py-4">
								Список пуст
							</div>
						{/each}
					</div>
				</div>
			{/if}

			<!-- Tab 5: Never -->
			{#if modalTab === 'never'}
				<div class="flex-1 min-h-0 flex flex-col bg-[var(--color-bg-secondary)] p-3 rounded-lg border border-[var(--color-border)]">
					<div class="shrink-0 font-semibold text-xs text-[var(--color-text-primary)] mb-2 flex items-center justify-between">
						<span>Прямой доступ (Never)</span>
						<Badge variant="muted" size="sm">{(learned?.never || []).length} записей</Badge>
					</div>
					<div class="flex-1 min-h-0 grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-2 overflow-y-auto content-start text-xs font-mono text-[var(--color-text-secondary)] pr-1">
						{#each learned?.never || [] as entry}
							<div class="p-1.5 rounded bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] truncate">
								{entry}
							</div>
						{:else}
							<div class="col-span-full text-[var(--color-text-muted)] italic text-center py-4">
								Список пуст
							</div>
						{/each}
					</div>
				</div>
			{/if}

			<div class="shrink-0 flex justify-end pt-2 border-t border-[var(--color-border)]">
				<Button variant="secondary" onclick={() => {
					showLearnedModal = false;
					if (radarPollTimer) {
						clearInterval(radarPollTimer);
						radarPollTimer = null;
					}
				}}>
					Закрыть
				</Button>
			</div>
		</div>
	</Modal>
{/if}

<!-- Модальное окно выбора выхода (карточки туннелей и прокси) -->
{#if showEgressModal}
	<Modal
		open={showEgressModal}
		title="Выбор выхода для обхода блокировок"
		size="wide"
		bodyLayout="fill"
		onclose={() => (showEgressModal = false)}
	>
		<div class="flex flex-col flex-1 min-h-0 h-full p-4 gap-3.5">
			<!-- Toolbar: категории и поиск -->
			<div class="shrink-0 flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-2.5 pb-2.5 border-b border-[var(--color-border)]">
				<div class="flex items-center gap-1.5 flex-wrap">
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors {egressFilterTab === 'all' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => (egressFilterTab = 'all')}
					>
						Все ({egresses.length})
					</button>
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors {egressFilterTab === 'proxy' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => (egressFilterTab = 'proxy')}
					>
						Прокси ({egresses.filter((e) => e.ref.kind === 'mihomo-proxy' || e.ref.kind === 'singbox-outbound').length})
					</button>
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors {egressFilterTab === 'group' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => (egressFilterTab = 'group')}
					>
						Группы ({egresses.filter((e) => e.ref.kind === 'mihomo-group').length})
					</button>
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors {egressFilterTab === 'tunnel' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => (egressFilterTab = 'tunnel')}
					>
						Туннели ({egresses.filter((e) => e.ref.kind === 'kernel-tunnel').length})
					</button>
					<button
						type="button"
						class="text-xs px-2.5 py-1 rounded-md transition-colors {egressFilterTab === 'subscription' ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] font-semibold border border-[var(--color-border)]' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
						onclick={() => (egressFilterTab = 'subscription')}
					>
						Подписки ({egresses.filter((e) => e.ref.kind === 'mihomo-subscription' || e.ref.kind === 'singbox-subscription').length})
					</button>
					{#if egresses.some((e) => e.ref.engine === 'sing-box' || e.ref.kind === 'singbox-subscription' || e.ref.kind === 'singbox-outbound')}
						<button
							type="button"
							class="text-xs px-2.5 py-1 rounded-md transition-colors {egressFilterTab === 'singbox' ? 'bg-amber-500/15 text-amber-600 dark:text-amber-400 font-semibold border border-amber-500/30' : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'}"
							onclick={() => (egressFilterTab = 'singbox')}
						>
							Sing-box ({egresses.filter((e) => e.ref.engine === 'sing-box' || e.ref.kind === 'singbox-subscription' || e.ref.kind === 'singbox-outbound').length})
						</button>
					{/if}
				</div>

				<div class="relative min-w-[200px] sm:w-64">
					<Search class="w-3.5 h-3.5 text-[var(--color-text-muted)] absolute left-2.5 top-1/2 -translate-y-1/2" />
					<input
						type="text"
						bind:value={egressSearchQuery}
						placeholder="Поиск по названию или интерфейсу..."
						class="w-full pl-8 pr-3 py-1.5 text-xs bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-md text-[var(--color-text-primary)] placeholder:text-[var(--color-text-muted)] focus:outline-none focus:border-[var(--color-accent)]"
					/>
				</div>
			</div>

			<!-- Карточки выходов: адаптивная сетка на всю высоту без пустого пространства -->
			<div class="flex-1 min-h-0 overflow-y-auto pr-1 grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5 gap-3 content-start">
				{#each filteredEgresses as eg (egressKey(eg.ref))}
					{@const key = egressKey(eg.ref)}
					{@const isSelected = selectedEgressValue === key}
					<button
						type="button"
						class="p-3.5 rounded-xl border text-left transition-all flex flex-col justify-between gap-3 cursor-pointer group {isSelected ? 'border-[var(--color-accent)] ring-2 ring-[var(--color-accent)]/20 bg-[var(--color-accent-tint)]/15 shadow-sm' : 'border-[var(--color-border)] bg-[var(--color-bg-primary)] hover:border-[var(--color-border-hover)] hover:bg-[var(--color-bg-secondary)]'}"
						onclick={() => {
							handleEgressChange(key);
							showEgressModal = false;
						}}
					>
						<div class="flex items-start justify-between gap-2.5">
							<div class="flex items-center gap-3 min-w-0">
								<div class="w-10 h-10 rounded-xl flex items-center justify-center shrink-0 border text-xl shadow-2xs {isSelected ? 'bg-[var(--color-accent-tint)] border-[var(--color-accent-border)]' : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] group-hover:border-[var(--color-border-hover)]'}">
									{#if eg.ref.engine === 'sing-box'}
										<span class="text-xs font-bold font-mono text-amber-500">SB</span>
									{:else}
										{getEgressFlag(eg.displayName || eg.interface)}
									{/if}
								</div>
								<div class="min-w-0">
									<div class="text-sm font-semibold text-[var(--color-text-primary)] truncate" title={eg.displayName}>
										{eg.displayName}
									</div>
									<div class="text-[11px] text-[var(--color-text-muted)] flex items-center gap-1.5 truncate mt-0.5">
										<span class="font-mono text-[var(--color-text-secondary)]">{eg.interface}</span>
										<span>·</span>
										<span>
											{#if eg.ref.kind === 'mihomo-proxy'}
												Mihomo Proxy
											{:else if eg.ref.kind === 'mihomo-group'}
												Proxy Group
											{:else if eg.ref.kind === 'mihomo-subscription'}
												Подписка Mihomo
											{:else if eg.ref.kind === 'singbox-subscription'}
												Подписка Sing-box
											{:else if eg.ref.kind === 'singbox-outbound'}
												Узел Sing-box
											{:else}
												Kernel Tunnel
											{/if}
										</span>
									</div>
								</div>
							</div>

							{#if isSelected}
								<span class="w-6 h-6 rounded-full bg-[var(--color-accent)] text-white flex items-center justify-center shrink-0 shadow-xs">
									<Check class="w-4 h-4" />
								</span>
							{:else}
								<span class="w-6 h-6 rounded-full border border-[var(--color-border)] group-hover:border-[var(--color-accent)] shrink-0 transition-colors"></span>
							{/if}
						</div>

						<div class="flex items-center justify-between gap-2 pt-2.5 border-t border-[var(--color-border)]/40 text-[11px]">
							<div class="flex items-center gap-1.5 flex-wrap">
								<span class="text-[10px] font-mono px-1.5 py-0.2 rounded border {eg.ref.engine === 'sing-box' ? 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/20' : eg.ref.engine === 'mihomo' ? 'bg-blue-500/10 text-blue-600 dark:text-blue-400 border-blue-500/20' : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)]'}">
									{eg.ref.engine}
								</span>
								{#if eg.capabilities?.tcp}
									<span class="text-[9px] font-mono px-1.5 py-0.2 rounded bg-[var(--color-accent)]/10 text-[var(--color-accent)] border border-[var(--color-accent)]/20">TCP</span>
								{/if}
								{#if eg.capabilities?.udp}
									<span class="text-[9px] font-mono px-1.5 py-0.2 rounded bg-[var(--color-success)]/10 text-[var(--color-success)] border border-[var(--color-success)]/20">UDP</span>
								{/if}
								{#if eg.capabilities?.ipv4}
									<span class="text-[9px] font-mono px-1.5 py-0.2 rounded bg-[var(--color-bg-secondary)] text-[var(--color-text-muted)] border border-[var(--color-border)]">IPv4</span>
								{/if}
							</div>

							<div class="flex items-center gap-1.5 shrink-0">
								<span class="w-2 h-2 rounded-full {eg.available ? 'bg-[var(--color-success)] shadow-[0_0_6px_var(--color-success)]' : 'bg-[var(--color-warning)]'}"></span>
								<span class="text-[10px] font-medium {eg.available ? 'text-[var(--color-success)]' : 'text-[var(--color-text-muted)]'}">
									{eg.available ? 'Доступен' : 'Отключен'}
								</span>
							</div>
						</div>
					</button>
				{:else}
					<div class="col-span-full text-[var(--color-text-muted)] italic text-center py-8">
						Выходы не найдены по заданным критериям
					</div>
				{/each}
			</div>

			<div class="shrink-0 flex justify-end pt-2 border-t border-[var(--color-border)]">
				<Button variant="secondary" onclick={() => (showEgressModal = false)}>
					Закрыть
				</Button>
			</div>
		</div>
	</Modal>
{/if}

<style>
	.susanin-page {
		width: 100%;
		max-width: 1120px;
		margin: 0 auto;
	}

	.susanin-page > * + * { margin-top: 1rem; }

	.susanin-status-bar { padding: 0.85rem 1rem; }

	.susanin-card { padding: 1.1rem; min-height: 100%; }
	.susanin-card-body > * + * { margin-top: 1.15rem; }
	.susanin-card-header { padding-bottom: 0.7rem; }
	.susanin-form-section > * + * { margin-top: 0.45rem; }
	.susanin-stats-grid { margin-top: 0.25rem; }
	.susanin-lists { padding-top: 0.25rem; }
	.susanin-detector > button { padding: 0.75rem 1rem; }

	@media (max-width: 720px) {
		.susanin-status-bar,
		.susanin-card { padding: 0.85rem; }
		.susanin-stats-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
	}
</style>
