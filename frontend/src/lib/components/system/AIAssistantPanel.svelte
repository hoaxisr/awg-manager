<script lang="ts">
	import { onDestroy, onMount, tick } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import {
		AlertTriangle,
		BrainCircuit,
		Check,
		CheckCircle2,
		ChevronDown,
		Cpu,
		ExternalLink,
		Eye,
		EyeOff,
		KeyRound,
		LockKeyhole,
		Power,
		RefreshCw,
		RotateCcw,
		Search,
		Settings2,
		ShieldAlert,
		Sparkles,
		Terminal,
		Wrench,
		Zap,
		Trash2,
		PanelLeftClose,
		PanelLeftOpen,
		PanelRightClose,
		PanelRightOpen,
		GripVertical,
		GripHorizontal,
		Maximize2,
		Minimize2,
	} from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import type { AIAssistantState, AIEmbeddedStatus, AIModelConfig, DownloadOutbound } from '$lib/types';
	import { Button } from '$lib/components/ui';
	import AIMemoryDrawer from './AIMemoryDrawer.svelte';

	interface FetchedModel {
		id: string;
		name: string;
		description?: string;
		contextLength?: number;
	}

	let memoryDrawerOpen = $state(false);
	let dialogStreamElem = $state<HTMLElement | null>(null);
	let question = $state('');

	// Resizable layout state (expandable left/right and up/down)
	let sidebarWidth = $state(320);
	let panelHeight = $state(750);
	let isFullscreen = $state(false);
	let isResizingH = $state(false);
	let isResizingV = $state(false);
	let startX = 0, startW = 0;
	let startY = 0, startH = 0;

	async function scrollToBottom() {
		await tick();
		if (dialogStreamElem) {
			dialogStreamElem.scrollTop = dialogStreamElem.scrollHeight;
		}
	}
	let assistantState = $state<AIAssistantState | null>(null);
	let loading = $state(false);
	let loadError = $state('');
	let pollTimer: ReturnType<typeof setInterval> | null = null;
	let modelConfig = $state<AIModelConfig | null>(null);
	let embeddedStatus = $state<AIEmbeddedStatus | null>(null);
	let configOpen = $state(false);
	let configEnabled = $state(false);
	let configAutoFix = $state(false);
	let configProvider = $state('google');
	let configBaseURL = $state('');
	let configModel = $state('gemini-2.0-flash');
	let isCustomModel = $state(false);
	let customModelInput = $state('');
	let configAPIKey = $state('');
	let configRouteTag = $state('direct');
	let configRouteKind = $state<DownloadOutbound['kind']>('direct');
	let routeOutbounds = $state<DownloadOutbound[]>([]);
	let showAPIKey = $state(false);
	let savingConfig = $state(false);
	let configMessage = $state('');
	let configMessageType = $state<'success' | 'error' | ''>('');
	let stoppingEmbedded = $state(false);
	let applyingAction = $state(false);
	let confirmingProposalID = $state('');
	let localBinaryPath = $state('');
	let localModelPath = $state('');
	let localContextSize = $state(1536);
	let localThreads = $state(2);
	let localPort = $state(11435);
	let localAutoStopMinutes = $state(10);
	let activeTimelineExpanded = $state<Record<number, boolean>>({});

	// Dynamic model fetching state
	let fetchingModels = $state(false);
	let fetchModelsError = $state('');
	let fetchedModels = $state<FetchedModel[]>([]);
	let apiKeyDebounceTimer: ReturnType<typeof setTimeout> | null = null;

	// Collapsible sections and panel state
	let sidebarOpen = $state(true);
	let providersSectionOpen = $state(true);
	let configSectionOpen = $state(true);
	let instructionsOpen = $state(true);

	// Chat font size state (persisted in localStorage)
	const fontSizes = [
		{ label: '9px', size: '9px' },
		{ label: '10px', size: '10px' },
		{ label: '11px', size: '11px' },
		{ label: '12px', size: '12px' },
		{ label: '13px', size: '13px' },
		{ label: '14px', size: '14px' },
		{ label: '15px', size: '15px' }, // default
		{ label: '16.5px', size: '16.5px' },
		{ label: '18px', size: '18px' },
		{ label: '20px', size: '20px' }
	];
	let fontSizeIdx = $state(6); // default: 15px

	function changeFontSize(delta: number) {
		const next = Math.max(0, Math.min(fontSizes.length - 1, fontSizeIdx + delta));
		if (next !== fontSizeIdx) {
			fontSizeIdx = next;
			try {
				localStorage.setItem('awgm_ai_chat_font_size', String(fontSizeIdx));
				localStorage.setItem('awgm_ai_chat_font_size_label', fontSizes[fontSizeIdx].label);
			} catch {}
		}
	}

	interface ProviderConfigState {
		baseUrl?: string;
		model?: string;
		apiKey?: string;
		apiKeySet?: boolean;
		routeTag?: string;
		routeKind?: DownloadOutbound['kind'];
	}
	let providerStates = $state<Record<string, ProviderConfigState>>({});

	function isLocalURL(u: string): boolean {
		if (!u) return false;
		try {
			const parsed = new URL(u.startsWith('http://') || u.startsWith('https://') ? u : `http://${u}`);
			const h = parsed.hostname;
			if (h === 'localhost' || h === '127.0.0.1' || h === '::1') return true;
			if (h.startsWith('192.168.') || h.startsWith('10.') || h.startsWith('172.16.') || h.startsWith('172.17.') || h.startsWith('172.18.') || h.startsWith('172.19.') || h.startsWith('172.2') || h.startsWith('172.30.') || h.startsWith('172.31.')) {
				return true;
			}
			return false;
		} catch {
			return false;
		}
	}

	const running = $derived(assistantState?.status === 'running' || loading);
	const runningChecks = $derived((assistantState?.toolSteps?.length ?? 0) > 0);
	const activeProviderKeySet = $derived(
		(providerStates[configProvider]?.apiKeySet ?? false) ||
		(modelConfig?.provider === configProvider && modelConfig.apiKeySet) ||
		(modelConfig?.providers?.[configProvider]?.apiKeySet ?? false)
	);

	interface ProviderInfo {
		label: string;
		shortLabel: string;
		badgeText?: string;
		badgeVariant?: 'accent' | 'success' | 'warning' | 'neutral';
		defaultModel: string;
		defaultBaseUrl: string;
		apiKeyUrl?: string;
		apiKeyUrlLabel?: string;
		apiKeyPlaceholder: string;
		summary: string;
		instructions: string[];
		fallbackModels: FetchedModel[];
	}

	const providers: Record<string, ProviderInfo> = {
		google: {
			label: 'Google Gemini',
			shortLabel: 'Gemini',
			badgeText: 'Бесплатно',
			badgeVariant: 'success',
			defaultModel: 'gemini-2.5-flash',
			defaultBaseUrl: '',
			apiKeyUrl: 'https://aistudio.google.com/apikey',
			apiKeyUrlLabel: 'Получить ключ в Google AI Studio ↗',
			apiKeyPlaceholder: 'AIzaSy...',
			summary: 'Официальный API Google Gemini. Быстрый отклик, высокое качество анализа и бесплатная квота без привязки банковской карты.',
			instructions: [
				'Перейдите в Google AI Studio по ссылке выше.',
				'Войдите со своим Google-аккаунтом и нажмите кнопку «Create API Key».',
				'Скопируйте полученный ключ и вставьте в поле ниже — список доступных моделей подгрузится автоматически!',
			],
			fallbackModels: [],
		},
		openrouter: {
			label: 'OpenRouter',
			shortLabel: 'OpenRouter',
			badgeText: 'Все модели мира',
			badgeVariant: 'accent',
			defaultModel: 'openai/gpt-4o-mini',
			defaultBaseUrl: 'https://openrouter.ai/api/v1',
			apiKeyUrl: 'https://openrouter.ai/keys',
			apiKeyUrlLabel: 'Получить ключ в OpenRouter ↗',
			apiKeyPlaceholder: 'sk-or-v1-...',
			summary: 'Универсальный шлюз: доступ к GPT-4o, Claude 3.7, DeepSeek R1/V3, Qwen, Gemini через единый баланс и ключ.',
			instructions: [
				'Зарегистрируйтесь на openrouter.ai и откройте раздел «Keys».',
				'Нажмите «Create Key», задайте имя и скопируйте созданный ключ.',
				'Вставьте ключ ниже — список моделей загрузится автоматически.',
			],
			fallbackModels: [],
		},
		deepseek: {
			label: 'DeepSeek',
			shortLabel: 'DeepSeek',
			badgeText: 'Выгодно и умно',
			badgeVariant: 'accent',
			defaultModel: 'deepseek-chat',
			defaultBaseUrl: 'https://api.deepseek.com/v1',
			apiKeyUrl: 'https://platform.deepseek.com/api_keys',
			apiKeyUrlLabel: 'Получить ключ в DeepSeek Platform ↗',
			apiKeyPlaceholder: 'sk-...',
			summary: 'Официальный API DeepSeek. Модели V3 и R1 с выдающимся пониманием сетевых протоколов, iptables и скриптов по минимальной цене.',
			instructions: [
				'Перейдите на platform.deepseek.com и войдите в аккаунт.',
				'В боковом меню выберите «API Keys» и нажмите «Create API Key».',
				'Вставьте ключ в поле ниже для автоматического получения моделей.',
			],
			fallbackModels: [],
		},
		openai: {
			label: 'OpenAI (ChatGPT)',
			shortLabel: 'OpenAI',
			badgeText: 'Официальный GPT',
			badgeVariant: 'neutral',
			defaultModel: 'gpt-4o-mini',
			defaultBaseUrl: '',
			apiKeyUrl: 'https://platform.openai.com/api-keys',
			apiKeyUrlLabel: 'Получить ключ в OpenAI Platform ↗',
			apiKeyPlaceholder: 'sk-proj-...',
			summary: 'Официальный API OpenAI. Использует endpoint Responses / ChatCompletions с отключенным сохранением данных.',
			instructions: [
				'Откройте platform.openai.com/api-keys.',
				'Нажмите «Create new secret key» и скопируйте его.',
				'Вставьте ключ ниже — список всех доступных в аккаунте моделей подгрузится автоматически.',
			],
			fallbackModels: [],
		},
		anthropic: {
			label: 'Anthropic (Claude)',
			shortLabel: 'Claude',
			badgeText: 'Официальный Claude',
			badgeVariant: 'neutral',
			defaultModel: 'claude-3-5-sonnet-20241022',
			defaultBaseUrl: '',
			apiKeyUrl: 'https://console.anthropic.com/settings/keys',
			apiKeyUrlLabel: 'Получить ключ в Anthropic Console ↗',
			apiKeyPlaceholder: 'sk-ant-api03-...',
			summary: 'Официальный API Anthropic Claude. Модели Sonnet и Haiku с эталонным качеством анализа сетевых конфигураций и логов.',
			instructions: [
				'Откройте console.anthropic.com/settings/keys.',
				'Создайте ключ в разделе «API Keys» и скопируйте его.',
				'Вставьте ключ в поле ниже — список моделей подгрузится автоматически.',
			],
			fallbackModels: [
				{ id: 'claude-3-5-sonnet-20241022', name: 'Claude 3.5 Sonnet' },
				{ id: 'claude-3-5-haiku-20241022', name: 'Claude 3.5 Haiku' },
				{ id: 'claude-3-7-sonnet-20250219', name: 'Claude 3.7 Sonnet' },
				{ id: 'claude-3-opus-20240229', name: 'Claude 3 Opus' },
			],
		},
		ollama: {
			label: 'Ollama (ПК / Сервер в LAN)',
			shortLabel: 'Ollama',
			badgeText: 'Локальная сеть',
			badgeVariant: 'warning',
			defaultModel: '',
			defaultBaseUrl: 'http://192.168.1.50:11434/v1',
			apiKeyPlaceholder: 'Не требуется (оставьте пустым)',
			summary: 'Локальная нейросеть на домашнем компьютере или сервере/NAS в локальной сети. Полная автономность и конфиденциальность.',
			instructions: [
				'Установите Ollama (ollama.com) на домашний ПК или сервер.',
				'Запустите службу с переменной OLLAMA_HOST=0.0.0.0 для доступа из локальной сети.',
				'Укажите IP-адрес вашего ПК в поле «Базовый URL» — список скачанных локальных моделей подгрузится автоматически.',
			],
			fallbackModels: [],
		},
		local_embedded: {
			label: 'Встроенный на роутере',
			shortLabel: 'GGUF на роутере',
			badgeText: 'ARM64 Keenetic',
			badgeVariant: 'warning',
			defaultModel: 'qwen-0.5b',
			defaultBaseUrl: 'http://127.0.0.1:11435/v1',
			apiKeyPlaceholder: 'Не требуется',
			summary: 'Прямой запуск квантованной GGUF-модели на процессоре роутера через встроенный llama-server (требуется Entware и USB-накопитель).',
			instructions: [
				'Поместите бинарник llama-server в /opt/bin/llama-server.',
				'Поместите модель Qwen 0.5B Q4_K_M в /opt/storage/ai/model.gguf.',
				'ИИ будет работать прямо на роутере без расхода внешнего трафика.',
			],
			fallbackModels: [
				{ id: 'qwen-0.5b', name: 'Qwen 2.5 0.5B Q4_K_M (~350 МБ RAM)', description: 'Встроенный легковесный движок llama-server' },
				{ id: 'qwen-1.5b', name: 'Qwen 2.5 1.5B Q4_K_M (~900 МБ RAM)', description: 'Встроенный движок llama-server' },
			],
		},
		custom: {
			label: 'Свой прокси / URL',
			shortLabel: 'Свой URL',
			badgeText: 'OpenAI-совместимый',
			badgeVariant: 'neutral',
			defaultModel: '',
			defaultBaseUrl: 'http://192.168.90.50:8045/v1',
			apiKeyPlaceholder: 'API ключ сервиса (если требуется)',
			summary: 'Локальный или сторонний прокси с OpenAI-совместимым API (Antigravity Tools, LiteLLM, ProxyAPI, vLLM).',
			instructions: [
				'Укажите базовый URL эндпоинта (например, http://192.168.90.50:8045/v1 для локального прокси).',
				'Нажмите кнопку «Обновить список» для загрузки доступных моделей с сервера.',
			],
			fallbackModels: [],
		},
	};

	const currentProviderInfo = $derived(providers[configProvider] || providers.google);

	// Display list: strictly the models returned by the API from the active provider / proxy
	const availableModelList = $derived.by(() => {
		if (fetchedModels.length > 0) return fetchedModels;
		if (configProvider === 'local_embedded') {
			return currentProviderInfo.fallbackModels;
		}
		// If models haven't been fetched yet, only include the currently saved model if it exists
		if (configModel) {
			return [{ id: configModel, name: configModel }];
		}
		return [];
	});

	const activeModelLabel = $derived.by(() => {
		const found = availableModelList.find((m) => m.id === configModel);
		if (found) return found.name || found.id;
		if (configModel) return configModel;
		return 'Выберите модель';
	});

	function stopPolling() {
		if (pollTimer) clearInterval(pollTimer);
		pollTimer = null;
	}

	async function refresh() {
		try {
			assistantState = await api.systemAIStatus();
			if (confirmingProposalID && assistantState?.proposal?.id !== confirmingProposalID) confirmingProposalID = '';
			loadError = '';
			if (assistantState && assistantState.status !== 'running') stopPolling();
			void scrollToBottom();
		} catch (error) {
			loadError = error instanceof Error ? error.message : 'Не удалось получить состояние помощника';
			stopPolling();
		}
	}

	function startPolling() {
		stopPolling();
		pollTimer = setInterval(() => void refresh(), 1200);
	}

	let clearingChat = $state(false);

	async function clearChat() {
		if (running || clearingChat) return;
		clearingChat = true;
		loadError = '';
		try {
			assistantState = await api.systemAIClearChat();
			question = '';
			if (typeof window !== 'undefined') {
				try { sessionStorage.removeItem('awgm_ai_assistant_state'); } catch {}
			}
		} catch (error) {
			loadError = error instanceof Error ? error.message : 'Не удалось очистить диалог';
		} finally {
			clearingChat = false;
		}
	}

	async function sendMessage() {
		const submitted = question.trim();
		if (!submitted || running) return;
		loading = true;
		confirmingProposalID = '';
		loadError = '';
		question = '';
		try {
			assistantState = await api.systemAIDiagnose(submitted);
			startPolling();
			void scrollToBottom();
		} catch (error) {
			loadError = error instanceof Error ? error.message : 'Не удалось отправить сообщение';
			question = submitted;
		} finally {
			loading = false;
		}
	}

	function sendQuickPrompt(promptText: string) {
		if (running) return;
		question = promptText;
		void sendMessage();
	}

	async function applyProposal() {
		if (!assistantState?.proposal || assistantState.proposal.status !== 'pending') return;
		if (confirmingProposalID !== assistantState.proposal.id) return;
		applyingAction = true;
		loadError = '';
		try {
			assistantState = await api.systemAIApplyAction(assistantState.proposal.id);
		} catch (error) {
			loadError = error instanceof Error ? error.message : 'Не удалось применить исправление';
			await refresh();
		} finally {
			applyingAction = false;
			confirmingProposalID = '';
		}
	}

	function requestProposalConfirmation() {
		if (!assistantState?.proposal || assistantState.proposal.status !== 'pending') return;
		confirmingProposalID = assistantState.proposal.id;
	}

	function onComposerKeydown(event: KeyboardEvent) {
		if (event.key === 'Enter' && !event.shiftKey) {
			event.preventDefault();
			void sendMessage();
		}
	}

	function chooseQuestion(value: string) {
		question = value;
	}

	async function fetchModelsFromAPI() {
		const hasKey = Boolean(configAPIKey.trim() || activeProviderKeySet);
		if (!hasKey && configProvider !== 'ollama' && configProvider !== 'local_embedded' && configProvider !== 'custom') {
			return;
		}
		fetchingModels = true;
		fetchModelsError = '';
		try {
			const routeTagToUse = isLocalURL(configBaseURL) ? 'direct' : configRouteTag;
			const routeKindToUse = isLocalURL(configBaseURL) ? 'direct' : configRouteKind;
			const models = await api.systemAIFetchModels({
				provider: configProvider,
				apiKey: configAPIKey.trim(),
				baseUrl: configBaseURL.trim(),
				routeTag: routeTagToUse,
				routeKind: routeKindToUse,
			});
			if (models && models.length > 0) {
				fetchedModels = models;
				// If current model is not in the list, auto-select the first one
				const exists = models.some((m) => m.id === configModel);
				if (!exists && models[0]) {
					configModel = models[0].id;
					isCustomModel = false;
				}
			} else {
				fetchedModels = [];
			}
		} catch (error) {
			fetchModelsError = error instanceof Error ? error.message : 'Не удалось получить список моделей';
			fetchedModels = [];
		} finally {
			fetchingModels = false;
		}
	}

	function onAPIKeyInput() {
		if (apiKeyDebounceTimer) clearTimeout(apiKeyDebounceTimer);
		apiKeyDebounceTimer = setTimeout(() => {
			if (configAPIKey.trim().length >= 10) {
				void fetchModelsFromAPI();
			}
		}, 800);
	}

	function onProviderChange(p: string) {
		// 1. Save current provider's inputs into providerStates before switching
		if (configProvider) {
			providerStates[configProvider] = {
				...providerStates[configProvider],
				baseUrl: configBaseURL,
				model: isCustomModel ? customModelInput : configModel,
				apiKey: configAPIKey,
				apiKeySet: activeProviderKeySet,
				routeTag: configRouteTag,
				routeKind: configRouteKind,
			};
		}

		// 2. Switch to new provider
		configProvider = p;
		configSectionOpen = true;
		fetchedModels = [];
		fetchModelsError = '';
		isCustomModel = false;
		customModelInput = '';
		showAPIKey = false;
		configMessage = '';

		const def = providers[p];
		const saved = providerStates[p];

		if (saved && (saved.model || saved.baseUrl || saved.apiKeySet)) {
			configBaseURL = saved.baseUrl ?? (def?.defaultBaseUrl || '');
			configModel = saved.model ?? (def?.defaultModel || '');
			configAPIKey = saved.apiKey || '';
			configRouteTag = saved.routeTag || (isLocalURL(configBaseURL) ? 'direct' : 'direct');
			configRouteKind = saved.routeKind || 'direct';
		} else if (def) {
			configBaseURL = def.defaultBaseUrl;
			configModel = def.defaultModel;
			configAPIKey = '';
			configRouteTag = isLocalURL(configBaseURL) ? 'direct' : 'direct';
			configRouteKind = 'direct';
		}

		// Auto-fetch if key is available or optional (ollama, local_embedded, custom)
		if (p === 'ollama' || p === 'local_embedded' || p === 'custom' || activeProviderKeySet) {
			void fetchModelsFromAPI();
		}
	}

	function onModelSelectChange(event: Event) {
		const val = (event.currentTarget as HTMLSelectElement).value;
		if (val === '__custom__') {
			isCustomModel = true;
			customModelInput = configModel;
		} else {
			isCustomModel = false;
			configModel = val;
		}
	}

	async function loadConfig() {
		try {
			modelConfig = await api.systemAIConfig();
			if (modelConfig) {
				configEnabled = modelConfig.enabled;
				configAutoFix = modelConfig.autoFix ?? false;
				configProvider = modelConfig.provider || 'google';
				configBaseURL = modelConfig.baseUrl || '';
				configModel = modelConfig.model || '';
				configRouteTag = modelConfig.routeTag || 'direct';
				configRouteKind = (modelConfig.routeKind || 'direct') as DownloadOutbound['kind'];
				localBinaryPath = modelConfig.localEngine?.binaryPath || '';
				localModelPath = modelConfig.localEngine?.modelPath || '';
				localContextSize = modelConfig.localEngine?.contextSize || 1536;
				localThreads = modelConfig.localEngine?.threads || 2;
				localPort = modelConfig.localEngine?.port || 11435;
				localAutoStopMinutes = modelConfig.localEngine?.autoStopMinutes || 10;

				// Load all saved provider profiles into client-side state
				if (modelConfig.providers) {
					for (const [pKey, prof] of Object.entries(modelConfig.providers)) {
						providerStates[pKey] = {
							baseUrl: prof.baseUrl,
							model: prof.model,
							apiKeySet: prof.apiKeySet,
							routeTag: prof.routeTag,
							routeKind: (prof.routeKind || 'direct') as DownloadOutbound['kind'],
						};
					}
				}
				providerStates[configProvider] = {
					baseUrl: configBaseURL,
					model: configModel,
					apiKeySet: modelConfig.apiKeySet,
					routeTag: configRouteTag,
					routeKind: configRouteKind,
				};

				if (modelConfig.apiKeySet || configProvider === 'ollama' || configProvider === 'local_embedded' || configProvider === 'custom') {
					void fetchModelsFromAPI();
				}
			}
			await loadEmbeddedStatus();
		} catch (error) {
			configMessage = error instanceof Error ? error.message : 'Не удалось загрузить настройки модели';
		}
	}

	async function loadRouteOutbounds() {
		try {
			routeOutbounds = await api.listDownloadOutbounds();
		} catch {
			routeOutbounds = [{ tag: 'direct', kind: 'direct', label: 'Direct (WAN)', available: true }];
		}
	}

	function routeKey(ob: Pick<DownloadOutbound, 'tag' | 'kind'>): string {
		return `${ob.kind}\u0000${ob.tag}`;
	}

	function selectRoute(value: string) {
		const split = value.indexOf('\u0000');
		configRouteKind = (split >= 0 ? value.slice(0, split) : 'direct') as DownloadOutbound['kind'];
		configRouteTag = split >= 0 ? value.slice(split + 1) : 'direct';
		// If key exists, re-fetch models via new route
		if (configAPIKey.trim() || activeProviderKeySet || configProvider === 'custom') {
			void fetchModelsFromAPI();
		}
	}

	async function loadEmbeddedStatus() {
		try {
			embeddedStatus = await api.systemAIEmbedded();
		} catch {
			// ignore if not available
		}
	}

	async function stopEmbedded() {
		stoppingEmbedded = true;
		try {
			embeddedStatus = await api.systemAIEmbeddedStop();
			configMessage = '';
		} catch (error) {
			configMessage = error instanceof Error ? error.message : 'Не удалось остановить локальный движок';
		} finally {
			stoppingEmbedded = false;
		}
	}

	async function saveConfig(): Promise<boolean> {
		savingConfig = true;
		configMessage = '';
		configMessageType = '';
		const finalModel = isCustomModel ? customModelInput.trim() : configModel.trim();
		if (configProvider !== 'ollama' && configProvider !== 'local_embedded' && configProvider !== 'custom' && !configAPIKey.trim() && !activeProviderKeySet) {
			configMessage = 'Для выбранного провайдера необходимо ввести API-ключ.';
			configMessageType = 'error';
			savingConfig = false;
			return false;
		}
		const routeTagToSave = isLocalURL(configBaseURL) ? 'direct' : configRouteTag;
		const routeKindToSave = isLocalURL(configBaseURL) ? 'direct' : configRouteKind;
		try {
			modelConfig = await api.systemAISaveConfig({
				enabled: configEnabled,
				autoFix: configAutoFix,
				provider: configProvider,
				baseUrl: configBaseURL.trim(),
				model: finalModel,
				routeTag: routeTagToSave,
				routeKind: routeKindToSave,
				...(configAPIKey.trim() ? { apiKey: configAPIKey.trim() } : {}),
				...(configProvider === 'local_embedded' ? {
					localEngine: {
						enabled: true,
						binaryPath: localBinaryPath.trim(),
						modelPath: localModelPath.trim(),
						contextSize: localContextSize,
						threads: localThreads,
						port: localPort,
						autoStopMinutes: localAutoStopMinutes,
					},
				} : {}),
			});
			configAPIKey = '';
			configMessage = 'Настройки успешно сохранены.';
			configMessageType = 'success';

			// Update client-side provider state
			providerStates[configProvider] = {
				baseUrl: modelConfig.baseUrl,
				model: modelConfig.model,
				apiKeySet: modelConfig.apiKeySet,
				routeTag: modelConfig.routeTag,
				routeKind: (modelConfig.routeKind || 'direct') as DownloadOutbound['kind'],
			};
			if (modelConfig.providers) {
				for (const [pKey, prof] of Object.entries(modelConfig.providers)) {
					providerStates[pKey] = {
						baseUrl: prof.baseUrl,
						model: prof.model,
						apiKeySet: prof.apiKeySet,
						routeTag: prof.routeTag,
						routeKind: (prof.routeKind || 'direct') as DownloadOutbound['kind'],
					};
				}
			}
			await loadEmbeddedStatus();
			await refresh();
			return true;
		} catch (error) {
			let msg = error instanceof Error ? error.message : 'Не удалось сохранить настройки';
			if (msg.toLowerCase().includes('api key is required') || msg.toLowerCase().includes('api key')) {
				msg = 'Для облачной модели необходимо указать API-ключ.';
			} else if (msg.toLowerCase().includes('model is required') || msg.toLowerCase().includes('model name is required')) {
				msg = 'Пожалуйста, выберите или укажите модель.';
			}
			configMessage = msg;
			configMessageType = 'error';
			return false;
		} finally {
			savingConfig = false;
		}
	}

	async function toggleAIEnabled() {
		if (savingConfig) return;
		const previous = configEnabled;
		configEnabled = !configEnabled;
		if (!(await saveConfig())) {
			configEnabled = previous;
			configOpen = true;
		}
	}

	function selectAgent(event: Event) {
		onProviderChange((event.currentTarget as HTMLSelectElement).value);
		configOpen = true;
	}

	function toggleTimelineStep(index: number) {
		activeTimelineExpanded[index] = !activeTimelineExpanded[index];
	}

	$effect(() => {
		if (typeof window !== 'undefined' && assistantState) {
			try {
				if ((assistantState.messages && assistantState.messages.length > 0) || assistantState.proposal) {
					sessionStorage.setItem('awgm_ai_assistant_state', JSON.stringify(assistantState));
				}
			} catch {}
		}
	});

	onMount(() => {
		if (typeof window !== 'undefined') {
			try {
				const cached = sessionStorage.getItem('awgm_ai_assistant_state');
				if (cached) {
					const parsed = JSON.parse(cached);
					if (parsed && typeof parsed === 'object') {
						assistantState = parsed;
					}
				}
			} catch {}
			if (window.innerWidth <= 768) {
				sidebarOpen = false;
			}
			try {
				const savedLabel = localStorage.getItem('awgm_ai_chat_font_size_label');
				if (savedLabel) {
					const idx = fontSizes.findIndex((f) => f.label === savedLabel);
					if (idx !== -1) fontSizeIdx = idx;
				} else {
					const saved = localStorage.getItem('awgm_ai_chat_font_size');
					if (saved !== null) {
						const parsed = parseInt(saved, 10);
						if (!isNaN(parsed) && parsed >= 0 && parsed < fontSizes.length) {
							fontSizeIdx = parsed;
						}
					}
				}
				const savedW = localStorage.getItem('awgm_ai_sidebar_width');
				if (savedW) {
					const parsedW = parseInt(savedW, 10);
					if (!isNaN(parsedW) && parsedW >= 180 && parsedW <= 800) {
						sidebarWidth = parsedW;
					}
				}
				const savedH = localStorage.getItem('awgm_ai_panel_height');
				if (savedH) {
					const parsedH = parseInt(savedH, 10);
					if (!isNaN(parsedH) && parsedH >= 400 && parsedH <= 1600) {
						panelHeight = parsedH;
					}
				}
			} catch {}
		}
		void loadConfig();
		void loadRouteOutbounds();
		void refresh().then(() => {
			if (assistantState?.status === 'running') startPolling();
		});
	});

	function startHResize(e: PointerEvent) {
		if (isFullscreen) return;
		isResizingH = true;
		startX = e.clientX;
		startW = sidebarWidth;
		window.addEventListener('pointermove', onHResizeMove);
		window.addEventListener('pointerup', onHResizeEnd);
		e.preventDefault();
	}

	function onHResizeMove(e: PointerEvent) {
		if (!isResizingH) return;
		const dx = e.clientX - startX;
		const maxW = Math.max(260, (window.innerWidth || 1200) - 420);
		// Since sidebar is on the right, dragging to the left (dx < 0) expands sidebar
		sidebarWidth = Math.max(240, Math.min(maxW, startW - dx));
	}

	function onHResizeEnd() {
		if (!isResizingH) return;
		isResizingH = false;
		window.removeEventListener('pointermove', onHResizeMove);
		window.removeEventListener('pointerup', onHResizeEnd);
		try { localStorage.setItem('awgm_ai_sidebar_width', String(sidebarWidth)); } catch {}
	}

	function resetSidebarWidth() {
		sidebarWidth = 320;
		try { localStorage.setItem('awgm_ai_sidebar_width', '320'); } catch {}
	}

	function startVResize(e: PointerEvent) {
		if (isFullscreen) return;
		isResizingV = true;
		startY = e.clientY;
		startH = panelHeight;
		window.addEventListener('pointermove', onVResizeMove);
		window.addEventListener('pointerup', onVResizeEnd);
		e.preventDefault();
	}

	function onVResizeMove(e: PointerEvent) {
		if (!isResizingV) return;
		const dy = e.clientY - startY;
		const maxH = Math.max(600, (window.innerHeight || 900) + 500);
		panelHeight = Math.max(450, Math.min(maxH, startH + dy));
	}

	function onVResizeEnd() {
		if (!isResizingV) return;
		isResizingV = false;
		window.removeEventListener('pointermove', onVResizeMove);
		window.removeEventListener('pointerup', onVResizeEnd);
		try { localStorage.setItem('awgm_ai_panel_height', String(panelHeight)); } catch {}
	}

	function resetPanelHeight() {
		panelHeight = 750;
		try { localStorage.setItem('awgm_ai_panel_height', '750'); } catch {}
	}

	function toggleFullscreen() {
		isFullscreen = !isFullscreen;
	}

	onDestroy(() => {
		stopPolling();
		if (apiKeyDebounceTimer) clearTimeout(apiKeyDebounceTimer);
		if (typeof window !== 'undefined') {
			window.removeEventListener('pointermove', onHResizeMove);
			window.removeEventListener('pointerup', onHResizeEnd);
			window.removeEventListener('pointermove', onVResizeMove);
			window.removeEventListener('pointerup', onVResizeEnd);
		}
	});

	let lastHandledAsk = '';

	$effect(() => {
		const ask = $page.url.searchParams.get('ask');
		if (ask && ask !== lastHandledAsk && !running && !loading) {
			lastHandledAsk = ask;
			if (typeof window !== 'undefined') {
				const cleanUrl = new URL(window.location.href);
				cleanUrl.searchParams.delete('ask');
				window.history.replaceState({}, '', cleanUrl.pathname + cleanUrl.search);
				void goto(cleanUrl.pathname + cleanUrl.search, { replaceState: true, noScroll: true, keepFocus: true });
			}
			sendQuickPrompt(ask);
		}
	});

	$effect(() => {
		if (typeof window === 'undefined') return;
		const onKeyDown = (e: KeyboardEvent) => {
			if (e.key === 'Escape' && isFullscreen) {
				isFullscreen = false;
			}
		};
		window.addEventListener('keydown', onKeyDown);
		return () => window.removeEventListener('keydown', onKeyDown);
	});

	function renderMarkdown(md: string): string {
		if (!md) return '';
		let s = md
			.replace(/&/g, '&amp;')
			.replace(/</g, '&lt;')
			.replace(/>/g, '&gt;');

		// Code blocks
		s = s.replace(/```([a-zA-Z0-9_-]*)\n([\s\S]*?)```/g, (_, lang, code) => {
			return `<pre class="ai-code-block"><div class="code-lang">${lang || 'shell'}</div><code>${code.trim()}</code></pre>`;
		});

		// Inline code
		s = s.replace(/`([^`]+)`/g, '<code class="ai-inline-code">$1</code>');

		// Markdown tables
		s = s.replace(/((?:^\|[^\n]+\|\n?)+)/gm, (tableMatch) => {
			const lines = tableMatch.trim().split('\n').filter((l: string) => l.trim().startsWith('|'));
			if (lines.length < 2) return tableMatch;
			let html = '<div class="ai-table-wrap"><table class="ai-table">';
			let inBody = false;
			for (let i = 0; i < lines.length; i++) {
				const line = lines[i].trim();
				if (line.match(/^\|(?:\s*:?-+:?\s*\|)+$/)) {
					inBody = true;
					continue;
				}
				const cells = line.split('|').slice(1, -1).map((c: string) => c.trim());
				if (!inBody && i === 0) {
					html += '<thead><tr>' + cells.map((c: string) => `<th>${c}</th>`).join('') + '</tr></thead><tbody>';
				} else {
					html += '<tr>' + cells.map((c: string) => `<td>${c}</td>`).join('') + '</tr>';
				}
			}
			html += '</tbody></table></div>';
			return html;
		});

		// Blockquotes / Alerts
		s = s.replace(/^>\s*(.+)$/gm, '<blockquote class="ai-quote">$1</blockquote>');

		// Headings
		s = s.replace(/^### (.*$)/gm, '<h5 class="ai-h3">$1</h5>');
		s = s.replace(/^## (.*$)/gm, '<h4 class="ai-h2">$1</h4>');
		s = s.replace(/^# (.*$)/gm, '<h3 class="ai-h1">$1</h3>');

		// Horizontal rule
		s = s.replace(/^---$/gm, '<hr class="ai-hr" />');

		// Bullet lists (processed before single-star italics)
		s = s.replace(/^[\*\-]\s+(.*$)/gm, '<li class="ai-li">$1</li>');
		s = s.replace(/((?:<li class="ai-li">.*<\/li>\n?)+)/g, '<ul class="ai-ul">$1</ul>');

		// Numbered lists
		s = s.replace(/^\d+\.\s+(.*$)/gm, '<li class="ai-num-li">$1</li>');
		s = s.replace(/((?:<li class="ai-num-li">.*<\/li>\n?)+)/g, '<ol class="ai-ol">$1</ol>');

		// Bold and italic (single line to avoid eating multiline asterisks)
		s = s.replace(/\*\*([^\*\n]+)\*\*/g, '<strong>$1</strong>');
		s = s.replace(/\*([^\*\n]+)\*/g, '<em>$1</em>');

		// Paragraph breaks (consecutive newlines)
		s = s.replace(/\n\n+/g, '<div class="ai-paragraph-break"></div>');
		s = s.replace(/\n/g, '<br/>');

		return s;
	}

</script>

<div class="ai-wrapper" class:is-fullscreen={isFullscreen}>
	<div
		class="ai-layout"
		class:sidebar-collapsed={!sidebarOpen}
		class:is-resizing={isResizingH || isResizingV}
		style="--sidebar-width: {sidebarWidth}px; --panel-height: {panelHeight}px;"
	>
		<!-- LEFT MAIN AREA: FULL-WIDTH ANCHORED CHAT WORKSPACE -->
	<main class="ai-main">
		<div class="chat-workspace card">
			<!-- CHAT TOP BAR -->
			<div class="chat-top-header">
				<div class="chat-top-left">
					<div class="chat-top-title">
						<BrainCircuit size={15} class="accent-icon" />
						<span class="chat-title-text">Диалог</span>
					</div>
					<button
						type="button"
						class="btn-memory-drawer"
						onclick={() => (memoryDrawerOpen = true)}
						title="База знаний, выученные сценарии (Playbooks) и фоновый Sentinel"
					>
						<Sparkles size={13} class="accent-icon" />
						<span class="btn-text">Память и Обучение</span>
					</button>
				</div>
				<div class="chat-top-right">
					<div class="font-size-widget" title="Размер шрифта сообщений">
						<button
							type="button"
							class="font-btn"
							onclick={() => changeFontSize(-1)}
							disabled={fontSizeIdx <= 0}
							title="Уменьшить шрифт"
							aria-label="Уменьшить шрифт"
						>
							A<span class="font-sign">−</span>
						</button>
						<span class="font-value" title="Текущий размер шрифта">{fontSizes[fontSizeIdx].label}</span>
						<button
							type="button"
							class="font-btn"
							onclick={() => changeFontSize(1)}
							disabled={fontSizeIdx >= fontSizes.length - 1}
							title="Увеличить шрифт"
							aria-label="Увеличить шрифт"
						>
							A<span class="font-sign">+</span>
						</button>
					</div>
					<button
						type="button"
						class="btn-icon-ghost"
						onclick={toggleFullscreen}
						title={isFullscreen ? 'Свернуть диалог (Esc)' : 'Развернуть диалог на весь экран'}
						aria-label={isFullscreen ? 'Свернуть' : 'Развернуть'}
					>
						{#if isFullscreen}
							<Minimize2 size={14} />
						{:else}
							<Maximize2 size={14} />
						{/if}
					</button>
					{#if assistantState?.messages?.length}
						<button
							type="button"
							class="btn-clear-chat"
							onclick={clearChat}
							disabled={running || clearingChat}
							title="Очистить диалог"
						>
							<Trash2 size={13} />
							<span class="clear-btn-text">{clearingChat ? 'Очищаю…' : 'Очистить'}</span>
						</button>
					{/if}
					<button
						type="button"
						class="btn-toggle-sidebar"
						onclick={() => (sidebarOpen = !sidebarOpen)}
						title={sidebarOpen ? 'Скрыть панель настроек' : 'Настройки ИИ'}
					>
						{#if sidebarOpen}
							<PanelRightClose size={14} />
							<span class="btn-text">Скрыть</span>
						{:else}
							<Settings2 size={14} />
							<span class="btn-text">Настройки</span>
							<span class="active-pill-badge">{providers[configProvider]?.shortLabel || configProvider}</span>
						{/if}
					</button>
				</div>
			</div>

			<!-- MESSAGES STREAM -->
			<section class="dialog-stream" bind:this={dialogStreamElem} style="--chat-font-size: {fontSizes[fontSizeIdx].size};" aria-live="polite">
				{#if !assistantState?.messages?.length && !running}
					<div class="welcome-empty-state">
						<div class="welcome-icon-wrap">
							<BrainCircuit size={40} class="welcome-icon" />
						</div>
						<h3>ИИ-помощник AWGM готов</h3>
						<p>Задайте любой вопрос о состоянии роутера, сети, туннелей или нагрузке. Модель проведёт диагностику и предложит решение.</p>
						<div class="privacy-note">
							<LockKeyhole size={13} class="lock-icon" />
							<span>Конфиденциальные ключи, пароли и токены автоматически маскируются перед отправкой.</span>
						</div>
					</div>
				{:else}
					{#each (assistantState?.messages || []) as message, msgIdx}
						<div class="message-bubble-row" class:user={message.role === 'user'}>
							<div class="message-avatar">
								{#if message.role === 'user'}
									<span>Вы</span>
								{:else}
									<BrainCircuit size={15} />
								{/if}
							</div>
							<div class="message-content-bubble">
								<div class="message-sender">
									{message.role === 'user' ? 'Вы' : (providers[modelConfig?.provider || '']?.shortLabel || 'ИИ-помощник')}
								</div>
								<div class="message-text">
									{#if message.role === 'user'}
										{message.content}
									{:else}
										{@html renderMarkdown(message.content)}
									{/if}
								</div>

								<!-- Collapsible tools -->
								{#if message.role !== 'user' && msgIdx === (assistantState?.messages?.length || 0) - 1 && assistantState?.toolSteps?.length}
									<details class="clean-tool-dropdown">
										<summary class="clean-tool-summary">
											<Terminal size={13} class="accent-icon" />
											<span>Выполненные проверки ({assistantState.toolSteps.length})</span>
											<ChevronDown size={13} class="chevron" />
										</summary>
										<div class="clean-tool-list">
											{#each assistantState.toolSteps as step}
												<div class="clean-tool-item {step.status}">
													{#if step.status === 'passed'}
														<CheckCircle2 size={13} class="status-success" />
													{:else if step.status === 'warning'}
														<AlertTriangle size={13} class="status-warning" />
													{:else}
														<ShieldAlert size={13} class="status-error" />
													{/if}
													<span class="tool-name">{step.title}</span>
													<span class="tool-dur">{step.durationMs} мс</span>
													{#if step.summary}
														<span class="tool-summary-inline">— {step.summary}</span>
													{/if}
												</div>
											{/each}
										</div>
									</details>
								{/if}

								<!-- Proposal Banner -->
								{#if message.role !== 'user' && msgIdx === (assistantState?.messages?.length || 0) - 1 && assistantState?.proposal}
									<div class="clean-proposal-box" class:applied={assistantState.proposal.status === 'applied'} class:failed={assistantState.proposal.status === 'failed'}>
										<div class="proposal-text-row">
											<Wrench size={15} class="proposal-icon" />
											<div class="proposal-desc-block">
												<strong>{assistantState.proposal.title}</strong>
												<span class="proposal-desc-text">{assistantState.proposal.description}</span>
											</div>
										</div>
										<div class="proposal-actions-row">
											{#if assistantState.proposal.status === 'pending'}
												{#if confirmingProposalID === assistantState.proposal.id}
													<div class="confirm-group">
														<Button size="sm" variant="ghost" onclick={() => (confirmingProposalID = '')}>Отмена</Button>
														<Button size="sm" variant="danger" loading={applyingAction} onclick={applyProposal}>
															Подтвердить
														</Button>
													</div>
												{:else}
													<Button size="sm" variant="primary" onclick={requestProposalConfirmation}>
														<Wrench size={13} /> Применить исправление
													</Button>
												{/if}
											{:else if assistantState.proposal.status === 'applied'}
												<span class="status-pill active"><CheckCircle2 size={13} /> Исправление применено</span>
											{:else}
												<span class="status-pill disabled"><AlertTriangle size={13} /> {assistantState.proposal.error || assistantState.proposal.status}</span>
											{/if}
										</div>
									</div>
								{/if}
							</div>
						</div>
					{/each}

					{#if running}
						<div class="running-indicator-card">
							<div class="indicator-spinner"></div>
							<div class="indicator-info">
								<strong>{runningChecks ? 'ИИ выполняет проверки роутера…' : 'ИИ формирует ответ…'}</strong>
								<span>{assistantState?.progress || 'Обработка запроса'}</span>
							</div>
						</div>
					{/if}
				{/if}
			</section>

			<!-- COMPOSER FOOTER -->
			<footer class="composer-container">
				<!-- QUICK ACTION CHIPS (NO EMOJIS) -->
				<div class="quick-chips-bar">
					{#each [
						'Проверить туннели',
						'Проверить DNS',
						'Статус маршрутизации',
						'Показать отклонения',
						'Что ты умеешь'
					] as chip}
						<button
							type="button"
							class="quick-chip-item"
							disabled={running}
							onclick={() => sendQuickPrompt(chip)}
						>
							{chip}
						</button>
					{/each}
				</div>

				<div class="composer-box">
					<textarea
						id="ai-question"
						bind:value={question}
						maxlength="1000"
						rows="1"
						disabled={running}
						onkeydown={onComposerKeydown}
						placeholder="Задайте вопрос о работе сети, DNS, VPN или нагрузке..."
						class="composer-textarea"
					></textarea>
					<div class="composer-actions">
						<Button
							variant="primary"
							size="sm"
							loading={running}
							onclick={sendMessage}
							disabled={!question.trim()}
							title="Отправить сообщение (Enter)"
						>
							<Sparkles size={14} />
							<span>{running ? '...' : 'Отправить'}</span>
						</Button>
					</div>
				</div>
				<div class="composer-subhint">
					<span>Enter — отправить, Shift+Enter — перенос строки</span>
				</div>
			</footer>
		</div>
	</main>

	{#if sidebarOpen && !isFullscreen}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="layout-resizer-h"
			class:resizing={isResizingH}
			onpointerdown={startHResize}
			ondblclick={resetSidebarWidth}
			title="Потяните влево/вправо для изменения ширины блоков (двойной клик — сброс 320px)"
			role="separator"
			aria-orientation="vertical"
			tabindex="0"
		>
			<div class="resizer-handle-h">
				<GripVertical size={12} />
			</div>
		</div>
	{/if}

	<!-- RIGHT SIDEBAR: Provider Selection & Setup -->
		{#if sidebarOpen}
			<aside class="ai-sidebar" style="width: {sidebarWidth}px;">
			<!-- MODEL STATUS & POWER CARD -->
			<div class="sidebar-card status-card">
				<div class="status-card-header">
					<div class="ai-avatar-badge" class:pulse={running}>
						<BrainCircuit size={20} class="avatar-icon" />
					</div>
					<div class="ai-header-info">
						<div class="title-line">
							<strong class="ai-title">ИИ-помощник AWGM</strong>
							{#if modelConfig?.enabled}
								<span class="status-pill active"><span class="dot"></span> Готов</span>
							{:else}
								<span class="status-pill disabled"><span class="dot"></span> Выкл</span>
							{/if}
						</div>
						<div class="model-line">
							<span class="provider-tag">{providers[modelConfig?.provider || '']?.shortLabel || modelConfig?.provider || 'Google'}</span>
							<span class="model-name-tag" title={modelConfig?.model}>{modelConfig?.model || '—'}</span>
						</div>
					</div>
					<button
						type="button"
						class="btn-return-chat"
						onclick={() => (sidebarOpen = false)}
						title="Перейти к диалогу с ИИ"
					>
						<PanelRightClose size={14} class="desktop-only-icon" />
						<BrainCircuit size={14} class="mobile-only-icon" />
						<span class="btn-return-text">Скрыть</span>
					</button>
				</div>

			<div class="status-card-actions">
				<button
					type="button"
					class="switch-pill-btn"
					class:active={configEnabled}
					onclick={toggleAIEnabled}
					disabled={savingConfig}
					title={configEnabled ? 'Нажмите, чтобы выключить ИИ' : 'Нажмите, чтобы включить ИИ'}
				>
					<span class="switch-toggle"></span>
					<span>{configEnabled ? 'Включен' : 'Выключен'}</span>
				</button>
				<button
					type="button"
					class="btn-return-chat"
					style="margin-left: 0;"
					onclick={() => (memoryDrawerOpen = true)}
					title="База знаний роутера, Playbooks и Sentinel"
				>
					<Sparkles size={13} class="accent-icon" />
					<span>Память</span>
				</button>
			</div>
		</div>

		<!-- PROVIDER SELECTION CARDS -->
		<div class="sidebar-card providers-card">
			<button
				type="button"
				class="section-collapse-header"
				onclick={() => (providersSectionOpen = !providersSectionOpen)}
				aria-expanded={providersSectionOpen}
			>
				<div class="collapse-header-title">
					<Cpu size={15} class="accent-icon" />
					<span>Провайдеры искусственного интеллекта</span>
				</div>
				<div class="collapse-header-right">
					<span class="badge-count">{Object.keys(providers).length}</span>
					<span class="chevron" class:collapsed={!providersSectionOpen}><ChevronDown size={16} /></span>
				</div>
			</button>

			{#if providersSectionOpen}
				<div class="provider-cards-list">
					{#each Object.entries(providers) as [key, p]}
						<button
							type="button"
							class="provider-card-btn"
							class:selected={configProvider === key}
							onclick={() => onProviderChange(key)}
						>
							<div class="p-card-top">
								<span class="p-name">{p.label}</span>
								{#if p.badgeText}
									<span class="mini-tag {p.badgeVariant || 'accent'}">{p.badgeText}</span>
								{/if}
							</div>
							<p class="p-summary">{p.summary}</p>
						</button>
					{/each}
				</div>
			{/if}
		</div>

		<!-- CURRENT PROVIDER CONFIGURATION & INSTRUCTIONS -->
		<div class="sidebar-card config-details-card">
			<button
				type="button"
				class="section-collapse-header"
				onclick={() => (configSectionOpen = !configSectionOpen)}
				aria-expanded={configSectionOpen}
			>
				<div class="collapse-header-title">
					<KeyRound size={15} class="accent-icon" />
					<span>Настройка: {providers[configProvider]?.label || configProvider}</span>
				</div>
				<div class="collapse-header-right">
					{#if activeProviderKeySet}
						<span class="badge-configured">Ключ сохранён</span>
					{/if}
					<span class="chevron" class:collapsed={!configSectionOpen}><ChevronDown size={16} /></span>
				</div>
			</button>

			{#if configSectionOpen}
				<div class="config-section-body">
					<!-- API Key instructions block -->
					{#if providers[configProvider]?.instructions?.length}
						<div class="instructions-box">
							<button
								type="button"
								class="inst-toggle-btn"
								onclick={() => (instructionsOpen = !instructionsOpen)}
								aria-expanded={instructionsOpen}
							>
								<span class="inst-title">Где взять API ключ:</span>
								{#if providers[configProvider]?.apiKeyUrl}
									<a
										href={providers[configProvider].apiKeyUrl}
										target="_blank"
										rel="noreferrer"
										class="inst-link"
										onclick={(e) => e.stopPropagation()}
									>
										{providers[configProvider].apiKeyUrlLabel || 'Получить ключ ↗'}
									</a>
								{/if}
								<span class="chevron-sm" class:collapsed={!instructionsOpen}><ChevronDown size={13} /></span>
							</button>
							{#if instructionsOpen}
								<ol class="inst-steps">
									{#each providers[configProvider].instructions as step}
										<li>{step}</li>
									{/each}
								</ol>
							{/if}
						</div>
					{/if}

					<!-- API Key Input -->
					{#if configProvider !== 'ollama' && configProvider !== 'local_embedded'}
						<div class="sidebar-field">
							<label for="ai-sidebar-key">API Ключ:</label>
							<div class="password-wrapper">
								<input
									id="ai-sidebar-key"
									type={showAPIKey ? 'text' : 'password'}
									bind:value={configAPIKey}
									oninput={onAPIKeyInput}
									placeholder={activeProviderKeySet ? '••••••••  (сохранён в памяти)' : (providers[configProvider]?.apiKeyPlaceholder || 'Вставьте ключ')}
									class="text-input"
								/>
								<button type="button" class="eye-btn" onclick={() => (showAPIKey = !showAPIKey)} title={showAPIKey ? 'Скрыть' : 'Показать'}>
									{#if showAPIKey}<EyeOff size={13} />{:else}<Eye size={13} />{/if}
								</button>
							</div>
						</div>
					{/if}

					<!-- Base URL Input -->
					{#if configProvider === 'custom' || configProvider === 'ollama' || configProvider === 'openrouter' || configProvider === 'deepseek'}
						<div class="sidebar-field">
							<label for="ai-sidebar-url">Базовый URL эндпоинта:</label>
							<input
								id="ai-sidebar-url"
								type="text"
								bind:value={configBaseURL}
								oninput={() => {
									if (isLocalURL(configBaseURL)) {
										configRouteTag = 'direct';
										configRouteKind = 'direct';
									}
								}}
								placeholder={providers[configProvider]?.defaultBaseUrl || 'http://192.168.90.50:8045/v1'}
								class="text-input"
							/>
						</div>
					{/if}

					<!-- Model Selector -->
					<div class="sidebar-field">
						<div class="label-with-link">
							<label for="ai-sidebar-model">Модель:</label>
							<button type="button" class="fetch-link-btn" onclick={fetchModelsFromAPI} disabled={fetchingModels}>
								{fetchingModels ? 'Загрузка…' : 'Обновить список'}
							</button>
						</div>

						{#if isCustomModel}
							<div class="custom-model-row">
								<input type="text" bind:value={configModel} placeholder="ID модели (из поддерживаемых API)" class="text-input" />
								<button type="button" class="btn-xs" onclick={() => (isCustomModel = false)}>Выбрать из списка</button>
							</div>
						{:else}
							<select id="ai-sidebar-model" value={configModel} onchange={onModelSelectChange} class="select-input" disabled={fetchingModels}>
								{#if availableModelList.length === 0}
									<option value="" disabled selected>
										{fetchingModels ? 'Загрузка моделей по API…' : 'Нажмите «Обновить список» для загрузки моделей'}
									</option>
								{:else}
									{#each availableModelList as m}
										<option value={m.id}>{m.name && m.name !== m.id ? `${m.name} (${m.id})` : m.id}</option>
									{/each}
								{/if}
								<option value="__custom__">+ Указать вручную...</option>
							</select>
						{/if}
					</div>

					<!-- Route outbound -->
					<div class="sidebar-field">
						<label for="ai-sidebar-route">Маршрут запросов к ИИ:</label>
						<select id="ai-sidebar-route" value={routeKey({ kind: configRouteKind, tag: configRouteTag })} onchange={(e) => selectRoute(e.currentTarget.value)} class="select-input">
							{#each routeOutbounds as ob}
								<option value={routeKey(ob)}>{ob.label}</option>
							{/each}
						</select>
						{#if isLocalURL(configBaseURL)}
							<small class="field-help" style="color: var(--color-success); font-weight: 500;">
								Локальный адрес сети LAN: запросы к этому провайдеру отправляются напрямую без VPN.
							</small>
						{:else}
							<small class="field-help">Позволяет направить запросы к ИИ через рабочий VPN-туннель для обхода блокировок.</small>
						{/if}
					</div>

					<!-- Auto-Fix -->
					<label class="toggle-row">
						<input type="checkbox" bind:checked={configAutoFix} />
						<div class="toggle-texts">
							<strong>Автоматическое исправление сбоев</strong>
							<small>ИИ сразу перезапустит упавшие службы без ожидания подтверждения.</small>
						</div>
					</label>

					{#if configMessage}
						<div class="sidebar-msg {configMessageType}">
							{#if configMessageType === 'error'}
								<AlertTriangle size={14} class="status-error" />
							{:else if configMessageType === 'success'}
								<CheckCircle2 size={14} class="status-success" />
							{/if}
							<span>{configMessage}</span>
						</div>
					{/if}

					<div class="sidebar-save-row">
						<Button size="sm" variant="primary" loading={savingConfig} onclick={saveConfig}>
							Сохранить настройки
						</Button>
					</div>
				</div>
			{/if}
		</div>
	</aside>
	{/if}
	</div>

	{#if !isFullscreen}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="layout-resizer-v"
			class:resizing={isResizingV}
			onpointerdown={startVResize}
			ondblclick={resetPanelHeight}
			title="Потяните вниз/вверх для изменения высоты блоков (двойной клик — сброс 750px)"
			role="separator"
			aria-orientation="horizontal"
			tabindex="0"
		>
			<div class="resizer-handle-v">
				<GripHorizontal size={12} />
				<span class="resizer-hint">{panelHeight}px</span>
			</div>
		</div>
	{/if}
</div>

<AIMemoryDrawer open={memoryDrawerOpen} onClose={() => (memoryDrawerOpen = false)} />

<style>

	.ai-wrapper {
		display: flex;
		flex-direction: column;
		width: 100%;
		max-width: 100%;
		position: relative;
	}

	.ai-wrapper.is-fullscreen {
		position: fixed;
		inset: 10px;
		z-index: 1000;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg, 12px);
		box-shadow: 0 25px 70px rgba(0, 0, 0, 0.55);
		padding: 10px;
		box-sizing: border-box;
		height: calc(100vh - 20px) !important;
	}

	.ai-layout {
		display: flex;
		flex-direction: row;
		align-items: stretch;
		gap: 0;
		width: 100%;
		max-width: 100%;
		color: var(--color-text-primary);
		position: relative;
	}

	.ai-layout.is-resizing {
		user-select: none;
	}

	.ai-sidebar {
		display: flex;
		flex-direction: column;
		gap: 12px;
		height: var(--panel-height, 750px);
		max-height: calc(100vh - 80px);
		min-height: 450px;
		overflow-y: auto;
		padding-left: 6px; padding-right: 2px;
		flex-shrink: 0;
		box-sizing: border-box;
	}

	.ai-layout.sidebar-collapsed .ai-sidebar {
		display: none;
	}

	.ai-wrapper.is-fullscreen .ai-sidebar {
		height: 100% !important;
		max-height: 100% !important;
	}

	/* HORIZONTAL RESIZER */
	.layout-resizer-h {
		width: 12px;
		margin: 0 2px;
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: col-resize;
		user-select: none;
		touch-action: none;
		position: relative;
		z-index: 5;
		flex-shrink: 0;
		border-radius: 6px;
		transition: background 0.15s ease;
	}

	.layout-resizer-h:hover,
	.layout-resizer-h.resizing {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.15));
	}

	.resizer-handle-h {
		width: 4px;
		height: 36px;
		border-radius: 2px;
		background: var(--color-border);
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--color-text-secondary);
		opacity: 0.6;
		transition: all 0.15s ease;
	}

	.layout-resizer-h:hover .resizer-handle-h,
	.layout-resizer-h.resizing .resizer-handle-h {
		width: 6px;
		height: 52px;
		background: var(--color-accent);
		color: #fff;
		opacity: 1;
		box-shadow: 0 0 8px var(--color-accent);
	}

	/* VERTICAL RESIZER */
	.layout-resizer-v {
		width: 100%;
		height: 14px;
		margin-top: 6px;
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: row-resize;
		user-select: none;
		touch-action: none;
		border-radius: 6px;
		transition: background 0.15s ease;
		background: transparent;
	}

	.layout-resizer-v:hover,
	.layout-resizer-v.resizing {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.15));
	}

	.resizer-handle-v {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 2px 14px;
		border-radius: 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		opacity: 0.7;
		font-size: 10px;
		font-weight: 500;
		transition: all 0.15s ease;
	}

	.layout-resizer-v:hover .resizer-handle-v,
	.layout-resizer-v.resizing .resizer-handle-v {
		border-color: var(--color-accent);
		color: var(--color-accent);
		opacity: 1;
		box-shadow: 0 0 6px rgba(122, 162, 247, 0.3);
	}

	.resizer-hint {
		font-family: var(--font-mono, monospace);
		font-size: 9px;
	}

	@media (max-width: 768px) {
		.ai-layout {
			flex-direction: column;
		}

		.layout-resizer-h,
		.layout-resizer-v {
			display: none !important;
		}

		.ai-sidebar {
			width: 100% !important;
			height: auto !important;
			max-height: 480px;
		}

		/* On mobile: when settings are open, show only settings; when closed, show only chat */
		.ai-layout:not(.sidebar-collapsed) .ai-main {
			display: none;
		}
	}

	.ai-sidebar::-webkit-scrollbar { width: 5px; }
	.ai-sidebar::-webkit-scrollbar-track { background: transparent; }
	.ai-sidebar::-webkit-scrollbar-thumb { background: var(--color-border); border-radius: 3px; }
	.ai-sidebar::-webkit-scrollbar-thumb:hover { background: var(--color-text-secondary); }

	.sidebar-card {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 10px);
		padding: 14px;
		box-shadow: var(--shadow-sm);
	}

	/* COLLAPSIBLE HEADERS */
	.section-collapse-header {
		width: 100%;
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0;
		background: transparent;
		border: none;
		color: var(--color-text-primary);
		cursor: pointer;
		font-size: 0.8rem;
		font-weight: 700;
		text-align: left;
		user-select: none;
		gap: 8px;
	}
	.section-collapse-header:hover .collapse-header-title {
		color: var(--color-accent);
	}
	.collapse-header-title {
		display: inline-flex;
		align-items: center;
		gap: 7px;
		font-size: 0.78rem;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-secondary);
		transition: color 0.15s ease;
	}
	.collapse-header-right {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	.chevron {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--color-text-secondary);
		transition: transform 0.2s ease;
		flex-shrink: 0;
	}
	.chevron.collapsed {
		transform: rotate(-90deg);
	}
	.badge-count {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 1px 6px;
		border-radius: 9999px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
		border: 1px solid var(--color-border);
	}
	.badge-configured {
		font-size: 0.65rem;
		font-weight: 600;
		padding: 1px 6px;
		border-radius: 9999px;
		background: var(--color-success-tint);
		color: var(--color-success);
		border: 1px solid var(--color-success-border);
	}

	/* STATUS CARD */
	.status-card-header {
		display: flex;
		align-items: center;
		gap: 10px;
		margin-bottom: 10px;
	}

	.ai-avatar-badge {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 36px;
		height: 36px;
		border-radius: 9px;
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.15));
		border: 1px solid var(--color-accent-border, rgba(122, 162, 247, 0.4));
		color: var(--color-accent);
		flex-shrink: 0;
	}

	.ai-avatar-badge.pulse {
		animation: pulse-glow 1.8s infinite;
	}

	@keyframes pulse-glow {
		0%, 100% { box-shadow: 0 0 0 0 var(--color-accent-border); }
		50% { box-shadow: 0 0 0 6px transparent; }
	}

	.ai-header-info {
		display: flex;
		flex-direction: column;
		gap: 2px;
		overflow: hidden;
	}

	.title-line {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.ai-title {
		font-size: 0.95rem;
		font-weight: 700;
	}

	.model-line {
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: 0.74rem;
	}

	.provider-tag {
		color: var(--color-text-secondary);
		font-weight: 600;
	}

	.model-name-tag {
		color: var(--color-accent);
		font-family: var(--font-mono, monospace);
		font-size: 0.7rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 180px;
	}

	.status-pill {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 1px 6px;
		border-radius: 9999px;
		font-size: 0.68rem;
		font-weight: 700;
	}

	.status-pill.active {
		background: var(--color-success-tint);
		color: var(--color-success);
		border: 1px solid var(--color-success-border);
	}

	.status-pill.disabled {
		background: var(--color-muted-tint);
		color: var(--color-text-secondary);
		border: 1px solid var(--color-border);
	}

	.status-pill .dot {
		width: 5px;
		height: 5px;
		border-radius: 50%;
		background: currentColor;
	}

	.status-card-actions {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.switch-pill-btn {
		width: 100%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 6px;
		padding: 6px 12px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all 0.15s;
	}

	.switch-pill-btn.active {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
	}

	.switch-toggle {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: currentColor;
	}

	/* PROVIDERS LIST (2-COLUMN GRID) */
	.providers-card {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.provider-cards-list {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 8px;
	}

	.provider-card-btn {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 9px 10px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		text-align: left;
		cursor: pointer;
		transition: all 0.15s ease;
		min-width: 0;
	}

	.provider-card-btn:hover {
		border-color: var(--color-accent);
		background: color-mix(in srgb, var(--color-accent) 6%, var(--color-bg-tertiary));
		transform: translateY(-1px);
	}

	.provider-card-btn.selected {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.15));
		border-color: var(--color-accent);
		box-shadow: 0 0 0 1px var(--color-accent);
	}

	.p-card-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 4px;
		min-width: 0;
	}

	.p-name {
		font-size: 0.78rem;
		font-weight: 700;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.mini-tag {
		font-size: 0.62rem;
		font-weight: 600;
		padding: 1px 5px;
		border-radius: 4px;
		white-space: nowrap;
		flex-shrink: 0;
	}
	.mini-tag.success { background: var(--color-success-tint); color: var(--color-success); }
	.mini-tag.accent { background: var(--color-accent-tint); color: var(--color-accent); }
	.mini-tag.warning { background: var(--color-warning-tint); color: var(--color-warning); }
	.mini-tag.neutral { background: var(--color-muted-tint); color: var(--color-text-secondary); }

	.p-summary {
		margin: 0;
		font-size: 0.7rem;
		color: var(--color-text-secondary);
		line-height: 1.35;
		display: -webkit-box;
		-webkit-line-clamp: 3;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	/* CONFIG DETAILS CARD */
	.config-details-card {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.config-section-body {
		display: flex;
		flex-direction: column;
		gap: 12px;
		margin-top: 6px;
	}

	.instructions-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 9px 11px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
	}

	.inst-toggle-btn {
		display: flex;
		align-items: center;
		width: 100%;
		background: transparent;
		border: none;
		padding: 0;
		cursor: pointer;
		color: var(--color-text-primary);
		font-size: 0.76rem;
		font-weight: 700;
		gap: 6px;
		text-align: left;
	}

	.inst-title {
		color: var(--color-text-primary);
	}

	.inst-link {
		color: var(--color-accent);
		text-decoration: none;
		font-size: 0.72rem;
		margin-left: auto;
		margin-right: 4px;
	}
	.inst-link:hover { text-decoration: underline; }

	.chevron-sm {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--color-text-secondary);
		transition: transform 0.2s ease;
		flex-shrink: 0;
	}
	.chevron-sm.collapsed {
		transform: rotate(-90deg);
	}

	.inst-steps {
		margin: 0;
		padding-left: 16px;
		font-size: 0.72rem;
		color: var(--color-text-secondary);
		line-height: 1.35;
	}

	.inst-steps li { margin: 2px 0; }

	.sidebar-field {
		display: flex;
		flex-direction: column;
		gap: 3px;
	}

	.sidebar-field label {
		font-size: 0.74rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.field-help {
		font-size: 0.68rem;
		color: var(--color-text-secondary);
		line-height: 1.25;
	}

	.label-with-link {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.fetch-link-btn {
		font-size: 0.7rem;
		color: var(--color-accent);
		background: transparent;
		border: none;
		cursor: pointer;
		padding: 0;
	}
	.fetch-link-btn:hover { text-decoration: underline; }

	.text-input, .select-input {
		width: 100%;
		padding: 6px 8px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 5px;
		color: var(--color-text-primary);
		font-size: 0.8rem;
		font-weight: 500;
		box-sizing: border-box;
		outline: none;
	}
	.text-input:focus, .select-input:focus { border-color: var(--color-accent); }

	.password-wrapper {
		position: relative;
		display: flex;
		align-items: center;
	}
	.password-wrapper .text-input { padding-right: 28px; }
	.eye-btn {
		position: absolute;
		right: 4px;
		background: transparent;
		border: none;
		color: var(--color-text-secondary);
		cursor: pointer;
		padding: 3px;
	}

	.custom-model-row {
		display: flex;
		gap: 4px;
	}

	.btn-xs {
		padding: 4px 8px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 0.72rem;
		color: var(--color-text-primary);
		cursor: pointer;
	}

	.toggle-row {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		cursor: pointer;
		margin-top: 2px;
	}
	.toggle-row input[type="checkbox"] { margin-top: 2px; }
	.toggle-texts { display: flex; flex-direction: column; gap: 1px; }
	.toggle-texts strong { font-size: 0.76rem; font-weight: 600; }
	.toggle-texts small { font-size: 0.68rem; color: var(--color-text-secondary); line-height: 1.25; }

	.sidebar-msg {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 7px 10px;
		border-radius: 6px;
		font-size: 0.74rem;
		font-weight: 600;
		line-height: 1.35;
	}
	.sidebar-msg.success {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}
	.sidebar-msg.error {
		background: var(--color-error-tint, rgba(247, 118, 142, 0.15));
		border: 1px solid var(--color-error-border, rgba(247, 118, 142, 0.4));
		color: var(--color-error, #f7768e);
	}
	.sidebar-save-row { display: flex; justify-content: flex-end; margin-top: 2px; }

	/* RIGHT MAIN: EXPANDED CHAT */
	.ai-main {
		display: flex;
		flex-direction: column;
		flex: 1 1 0%;
		width: 100%;
		min-width: 0;
	}

	.chat-workspace {
		display: flex;
		flex-direction: column;
		flex: 1 1 auto;
		width: 100%;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 10px);
		height: var(--panel-height, 750px);
		max-height: calc(100vh - 80px);
		min-height: 450px;
		overflow: hidden;
		box-shadow: var(--shadow-sm);
	}

	.ai-wrapper.is-fullscreen .chat-workspace {
		height: 100% !important;
		max-height: 100% !important;
	}

	.chat-top-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 10px 18px;
		border-bottom: 1px solid var(--color-border);
		background: rgba(255, 255, 255, 0.02);
	}
	.chat-top-left {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}

	.btn-toggle-sidebar {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 4px 8px;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-size: 11px;
		font-weight: 500;
		cursor: pointer;
		transition: all 0.15s ease;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.btn-toggle-sidebar:hover {
		background: var(--color-hover);
		color: var(--color-text-primary);
		border-color: var(--color-accent);
	}

	.active-pill-badge {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.15));
		color: var(--color-accent);
		font-size: 10px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 4px;
	}

	.btn-return-chat {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 4px 9px;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-size: 11px;
		font-weight: 600;
		cursor: pointer;
		transition: all 0.15s ease;
		margin-left: auto;
		white-space: nowrap;
	}

	.btn-return-chat:hover {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.15));
		color: var(--color-accent);
		border-color: var(--color-accent);
	}

	:global(.desktop-only-icon) { display: inline-block; }
	:global(.mobile-only-icon) { display: none; }

	.btn-icon-ghost {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 4px;
		background: transparent;
		border: 1px solid transparent;
		border-radius: var(--radius-sm, 6px);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all 0.15s ease;
		margin-left: auto;
	}

	.btn-icon-ghost:hover {
		background: var(--color-hover);
		color: var(--color-text-primary);
		border-color: var(--color-border);
	}

	.chat-top-title {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 11px;
		font-weight: 600;
		color: var(--color-text-secondary);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.chat-top-right {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-left: auto;
	}

	.font-size-widget {
		display: inline-flex;
		align-items: center;
		padding: 2px;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		background: var(--color-bg-primary);
		opacity: 0.7;
		transition: opacity 0.15s ease, border-color 0.15s ease;
	}
	.font-size-widget:hover {
		opacity: 1;
		border-color: var(--color-border-hover, var(--color-border));
	}

	.font-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 1px 4px;
		height: 20px;
		background: transparent;
		border: none;
		border-radius: 4px;
		color: var(--color-text-secondary);
		cursor: pointer;
		font-size: 11px;
		font-weight: 600;
		line-height: 1;
		transition: all 0.12s ease;
	}
	.font-btn:hover:not(:disabled) {
		background: var(--color-hover);
		color: var(--color-text-primary);
	}
	.font-btn:disabled {
		opacity: 0.3;
		cursor: not-allowed;
	}
	.font-sign {
		font-size: 9px;
		vertical-align: super;
		margin-left: 1px;
	}

	.font-value {
		font-size: 10px;
		font-weight: 500;
		color: var(--color-text-secondary);
		padding: 0 4px;
		min-width: 22px;
		text-align: center;
		user-select: none;
	}

	.btn-clear-chat {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 4px 8px;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
		font-size: 11px;
		white-space: nowrap;
		flex-shrink: 0;
		transition: all 0.15s ease;
	}
	.btn-clear-chat:hover:not(:disabled) {
		color: var(--color-error);
		border-color: color-mix(in srgb, var(--color-error) 40%, var(--color-border));
		background: rgba(239, 68, 68, 0.06);
	}

	.btn-memory-drawer {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 4px 9px;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
		font-size: 11px;
		font-weight: 500;
		white-space: nowrap;
		flex-shrink: 0;
		transition: all 0.15s ease;
	}
	.btn-memory-drawer:hover {
		color: var(--color-accent);
		border-color: var(--color-accent);
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.1));
	}

	.dialog-stream {
		display: flex;
		flex-direction: column;
		gap: 16px;
		padding: 20px 24px;
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		scroll-behavior: smooth;
	}

	.dialog-stream::-webkit-scrollbar { width: 6px; }
	.dialog-stream::-webkit-scrollbar-track { background: transparent; }
	.dialog-stream::-webkit-scrollbar-thumb { background: var(--color-border); border-radius: 3px; }
	.dialog-stream::-webkit-scrollbar-thumb:hover { background: var(--color-text-secondary); }

	.welcome-empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		text-align: center;
		padding: 60px 20px;
		margin: auto;
		max-width: 480px;
	}

	.welcome-icon-wrap {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 64px;
		height: 64px;
		border-radius: 16px;
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
		margin-bottom: 14px;
	}

	.welcome-empty-state h3 {
		margin: 0 0 8px;
		font-size: 1.15rem;
		font-weight: 700;
	}

	.welcome-empty-state p {
		margin: 0 0 16px;
		font-size: 0.86rem;
		color: var(--color-text-secondary);
		line-height: 1.45;
	}

	.privacy-note {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 0.74rem;
		color: var(--color-text-secondary);
		padding: 5px 10px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
	}

	/* MESSAGE BUBBLES */
	.message-bubble-row {
		display: flex;
		align-items: flex-start;
		gap: 12px;
		width: 100%;
	}

	.message-bubble-row.user {
		justify-content: flex-end;
	}

	.message-avatar {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		border-radius: 7px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		font-size: 0.72rem;
		font-weight: 700;
		color: var(--color-accent);
		flex-shrink: 0;
	}

	.message-bubble-row.user .message-avatar {
		display: none;
	}

	.message-content-bubble {
		display: flex;
		flex-direction: column;
		gap: 4px;
		max-width: 90%;
	}

	.message-bubble-row.user .message-content-bubble {
		max-width: 80%;
		align-items: flex-end;
	}

	.message-sender {
		font-size: 0.72rem;
		font-weight: 700;
		color: var(--color-text-secondary);
	}

	.message-bubble-row.user .message-sender {
		display: none;
	}

	.message-text {
		font-size: var(--chat-font-size, 15px);
		line-height: 1.58;
		color: var(--color-text-primary);
	}

	.message-bubble-row.user .message-text {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.18));
		border: 1px solid var(--color-accent-border, rgba(122, 162, 247, 0.35));
		padding: 8px 14px;
		border-radius: 12px 12px 2px 12px;
		font-weight: 500;
		font-size: var(--chat-font-size, 15px);
	}

	/* MARKDOWN FORMATTING */
	:global(.ai-h1) { margin: 12px 0 6px; font-size: calc(var(--chat-font-size, 15px) * 1.25); font-weight: 700; color: var(--color-text-primary); }
	:global(.ai-h2) { margin: 10px 0 5px; font-size: calc(var(--chat-font-size, 15px) * 1.15); font-weight: 700; color: var(--color-text-primary); }
	:global(.ai-h3) { margin: 8px 0 4px; font-size: calc(var(--chat-font-size, 15px) * 1.05); font-weight: 700; color: var(--color-text-primary); }
	:global(.ai-quote) {
		margin: 8px 0;
		padding: 6px 12px;
		background: var(--color-bg-tertiary);
		border-left: 3px solid var(--color-accent);
		border-radius: 0 5px 5px 0;
		font-size: calc(var(--chat-font-size, 15px) * 0.94);
		color: var(--color-text-secondary);
	}
	:global(.ai-hr) { border: none; border-top: 1px solid var(--color-border); margin: 12px 0; }
	:global(.ai-ul), :global(.ai-ol) { margin: 6px 0; padding-left: 20px; font-size: var(--chat-font-size, 15px); }
	:global(.ai-li), :global(.ai-num-li) { margin: 3px 0; }
	:global(.ai-paragraph-break) { height: 8px; }
	:global(.ai-inline-code) {
		font-family: var(--font-mono, monospace);
		font-size: calc(var(--chat-font-size, 15px) * 0.9);
		padding: 1px 5px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		color: var(--color-accent);
	}
	:global(.ai-code-block) {
		margin: 8px 0;
		padding: 8px 12px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		font-family: var(--font-mono, monospace);
		font-size: calc(var(--chat-font-size, 15px) * 0.9);
		overflow-x: auto;
	}
	:global(.ai-code-block .code-lang) {
		font-size: calc(var(--chat-font-size, 15px) * 0.75);
		font-weight: 700;
		text-transform: uppercase;
		color: var(--color-text-secondary);
		margin-bottom: 4px;
	}
	:global(.ai-table-wrap) {
		margin: 10px 0;
		overflow-x: auto;
		border: 1px solid var(--color-border);
		border-radius: 6px;
	}
	:global(.ai-table) {
		width: 100%;
		border-collapse: collapse;
		font-size: calc(var(--chat-font-size, 15px) * 0.92);
		text-align: left;
	}
	:global(.ai-table th) {
		padding: 6px 10px;
		background: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
		font-weight: 700;
		color: var(--color-text-secondary);
	}
	:global(.ai-table td) {
		padding: 5px 10px;
		border-bottom: 1px solid var(--color-border);
	}
	:global(.ai-table tr:last-child td) {
		border-bottom: none;
	}

	/* TOOLS COLLAPSIBLE */
	.clean-tool-dropdown {
		margin-top: 6px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 5px;
		font-size: 0.76rem;
	}
	.clean-tool-summary {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 6px 10px;
		cursor: pointer;
		font-weight: 600;
		color: var(--color-text-secondary);
		user-select: none;
	}
	.clean-tool-summary:hover { color: var(--color-text-primary); }
	.clean-tool-summary .chevron { margin-left: auto; transition: transform 0.2s; }
	details[open] .clean-tool-summary .chevron { transform: rotate(180deg); }
	.clean-tool-list {
		display: flex;
		flex-direction: column;
		gap: 3px;
		padding: 5px 10px 8px;
		border-top: 1px solid var(--color-border);
	}
	.clean-tool-item {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 0.74rem;
		color: var(--color-text-secondary);
	}
	.tool-name { font-weight: 600; color: var(--color-text-primary); font-family: var(--font-mono, monospace); }
	.tool-dur { font-size: 0.68rem; }

	/* PROPOSAL */
	.clean-proposal-box {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		margin-top: 8px;
		padding: 8px 12px;
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.1));
		border: 1px solid var(--color-accent-border, rgba(122, 162, 247, 0.4));
		border-radius: 6px;
	}
	.clean-proposal-box.applied { background: var(--color-success-tint); border-color: var(--color-success-border); }
	.clean-proposal-box.failed { background: var(--color-error-tint); border-color: var(--color-error-border); }
	.proposal-text-row { display: flex; align-items: center; gap: 8px; }
	.proposal-icon { color: var(--color-accent); flex-shrink: 0; }
	.proposal-desc-block { display: flex; flex-direction: column; gap: 1px; }
	.proposal-desc-block strong { font-size: 0.82rem; color: var(--color-text-primary); }
	.proposal-desc-text { font-size: 0.74rem; color: var(--color-text-secondary); }
	.proposal-actions-row { display: flex; align-items: center; gap: 4px; }
	.confirm-group { display: flex; align-items: center; gap: 4px; }

	/* RUNNING INDICATOR */
	.running-indicator-card {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 12px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		max-width: 320px;
	}
	.indicator-spinner {
		width: 14px;
		height: 14px;
		border: 2px solid var(--color-accent-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
		flex-shrink: 0;
	}
	@keyframes spin { to { transform: rotate(360deg); } }
	.indicator-info { display: flex; flex-direction: column; gap: 1px; }
	.indicator-info strong { font-size: 0.78rem; color: var(--color-text-primary); }
	.indicator-info span { font-size: 0.7rem; color: var(--color-text-secondary); }

	/* COMPOSER */
	.composer-container {
		display: flex;
		flex-direction: column;
		gap: 3px;
		padding: 10px 14px;
		background: var(--color-bg-tertiary);
		border-top: 1px solid var(--color-border);
		flex-shrink: 0;
	}

	.quick-chips-bar {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-bottom: 6px;
	}

	.quick-chip-item {
		background: rgba(125, 135, 155, 0.1);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		color: var(--color-text-secondary);
		font-size: 11px;
		font-weight: 500;
		padding: 4px 9px;
		cursor: pointer;
		transition: all 0.15s ease;
		user-select: none;
		white-space: nowrap;
	}

	.quick-chip-item:hover:not(:disabled) {
		background: rgba(99, 102, 241, 0.12);
		border-color: var(--color-primary);
		color: var(--color-text-primary);
	}

	.quick-chip-item:disabled {
		opacity: 0.45;
		cursor: not-allowed;
	}

	.composer-box {
		display: flex;
		align-items: center;
		gap: 6px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		padding: 4px 6px 4px 12px;
		transition: border-color 0.15s;
	}

	.composer-box:focus-within {
		border-color: var(--color-accent);
		box-shadow: 0 0 0 1px var(--color-accent);
	}

	.composer-textarea {
		flex: 1;
		background: transparent;
		border: none;
		outline: none;
		font-size: 0.86rem;
		font-family: inherit;
		color: var(--color-text-primary);
		resize: none;
		min-height: 24px;
		max-height: 120px;
		padding: 4px 0;
		line-height: 1.4;
	}

	.composer-textarea::placeholder {
		color: var(--color-text-secondary);
		opacity: 0.7;
	}

	.composer-actions {
		display: flex;
		align-items: center;
		flex-shrink: 0;
	}

	.composer-subhint {
		display: flex;
		justify-content: flex-end;
		font-size: 0.65rem;
		color: var(--color-text-secondary);
		opacity: 0.6;
		padding-right: 2px;
	}

	/* RESPONSIVE MOBILE TWEAKS */
	@media (max-width: 768px) {
		.chat-workspace,
		.ai-sidebar {
			height: calc(100dvh - 180px);
			min-height: 420px;
			max-height: calc(100dvh - 140px);
			border-radius: var(--radius-sm, 8px);
		}

		:global(.desktop-only-icon) {
			display: none;
		}
		:global(.mobile-only-icon) {
			display: inline-block;
		}

		.dialog-stream {
			padding: 10px 10px;
			gap: 10px;
		}

		.message-bubble-row {
			gap: 8px;
		}

		.message-content-bubble {
			max-width: 96%;
		}

		.message-bubble-row.user .message-content-bubble {
			max-width: 90%;
		}

		.welcome-empty-state {
			padding: 24px 10px;
		}

		.welcome-icon-wrap {
			width: 48px;
			height: 48px;
			border-radius: 12px;
			margin-bottom: 8px;
		}

		.composer-container {
			padding: 6px 8px;
			gap: 0;
		}

		.composer-subhint {
			display: none;
		}

		.composer-box {
			padding: 3px 6px 3px 10px;
		}

		.composer-textarea {
			font-size: 0.84rem;
			min-height: 22px;
			max-height: 90px;
		}
	}

	@media (max-width: 640px) {
		.chat-top-header {
			padding: 6px 10px;
		}

		.btn-toggle-sidebar .btn-text {
			display: none;
		}

		.btn-clear-chat .clear-btn-text {
			display: none;
		}

		.btn-clear-chat {
			padding: 4px 7px;
		}

		.provider-cards-list {
			grid-template-columns: 1fr;
		}
	}
</style>
