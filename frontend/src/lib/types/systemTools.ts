// Типы вкладки «Система» (файлы, службы, opkg, порты, процессы).
// Живут здесь, а не в клиенте: остальные типы клиент импортирует из $lib/types.

export type SystemFileRoot = {
	path: string;
	label: string;
	readOnly: boolean;
};

export type SystemFileEntry = {
	name: string;
	path: string;
	isDir: boolean;
	size: number;
	mode: string;
	modTime: string;
};

export type FileSystemScriptStatus = {
	path: string;
	isScript: boolean;
	running: boolean;
	pids?: number[];
	isService: boolean;
	serviceName?: string;
	statusText?: string;
	canExecute: boolean;
};

export type SystemServiceItem = {
	name: string;
	script: string;
	enabled: boolean;
	running: boolean;
	statusText: string;
	logPath?: string;
	managed: boolean;
	managedHint?: string;
};

export type SystemOpkgPackage = {
	name: string;
	version: string;
	upgradeVersion?: string;
	description?: string;
	installedAt?: string;
};

export type SystemPortBinding = {
	proto: string;
	port: number;
	ip: string;
	state: string;
	inode: number;
	pid?: number;
	processName?: string;
	exe?: string;
	cmdline?: string;
	user?: string;
	service?: string;
	isSelf?: boolean;
	isCritical?: boolean;
};

export type SystemCpuCore = {
	id: string; // "total", "cpu0", "cpu1"
	user: number;
	system: number;
	nice: number;
	idle: number;
	iowait: number;
	usage: number; // 0..100
};

export type SystemMemoryInfo = {
	total: number;
	free: number;
	available: number;
	used: number;
	buffers: number;
	cached: number;
	swapTotal: number;
	swapFree: number;
	swapUsed: number;
	usagePercent: number;
};

export type SystemProcessItem = {
	pid: number;
	ppid: number;
	user: string;
	priority: number;
	nice: number;
	threads: number;
	state: string; // "R", "S", "D", "Z", "T"
	cpuPercent: number; // доля всего процессора (все ядра), 0..100
	cpuTimeSec: number; // utime+stime с запуска процесса
	memoryRss: number; // VmRSS = memoryOwn + memoryFile
	memoryOwn: number; // RssAnon+RssShmem — ядро без swap не отдаст
	memoryFile: number; // RssFile — страницы файлов, ядро отдаёт при нехватке
	memoryVsize: number;
	memoryPercent: number;
	name: string;
	cmdline: string;
	exe?: string;
	service?: string;
	isSelf: boolean;
	isCritical: boolean;
	isKernel?: boolean;
};

export type SystemProcSummary = {
	total: number;
	running: number;
	sleeping: number;
	stopped: number;
	zombie: number;
	threads: number;
};

export type SystemProcSnapshot = {
	timestamp: string;
	uptimeSeconds: number;
	loadAvg: [number, number, number];
	cpuModel?: string;
	cpuArchitecture?: string;
	cpuCount?: number;
	cores: SystemCpuCore[];
	memory: SystemMemoryInfo;
	processSummary: SystemProcSummary;
	processes: SystemProcessItem[];
};

export type AIAssistantFinding = {
	severity: 'critical' | 'warning' | 'info';
	title: string;
	detail: string;
	recommendation: string;
	source: string;
};

export type AIAssistantState = {
	status: 'idle' | 'running' | 'done' | 'error';
	progress?: string;
	question?: string;
	summary?: string;
	engine: 'local-diagnostics' | string;
	readOnly: boolean;
	startedAt?: string;
	completedAt?: string;
	stats: { passed: number; failed: number; skipped: number };
	findings: AIAssistantFinding[];
	intent: { kind: string; entity?: Record<string, string> };
	toolSteps: Array<{
		name: string;
		title: string;
		status: 'passed' | 'warning' | 'error';
		summary: string;
		evidence?: string[];
		durationMs: number;
		readOnly: boolean;
		startedAt: string;
	}>;
	modelAnswer?: string;
	modelError?: string;
	error?: string;
	messages: Array<{
		role: 'user' | 'assistant';
		content: string;
		createdAt: string;
	}>;
	proposal?: {
		id: string;
		action: string;
		target?: string;
		title: string;
		description: string;
		risk: 'low' | 'medium' | 'high';
		status: 'pending' | 'applying' | 'applied' | 'failed' | 'expired';
		autoApplied?: boolean;
		verification?: {
			status: 'passed' | 'warning' | 'failed';
			summary: string;
			detail?: string;
		};
		error?: string;
		createdAt: string;
	};
};

export type AIEmbeddedConfig = {
	enabled: boolean;
	binaryPath?: string;
	modelPath?: string;
	contextSize?: number;
	threads?: number;
	port?: number;
	autoStopMinutes?: number;
};

export type AIEmbeddedStatus = {
	available: boolean;
	running: boolean;
	managed: boolean;
	pid?: number;
	port: number;
	binaryExists: boolean;
	binaryPath?: string;
	modelExists: boolean;
	modelPath?: string;
	memAvailableMB: number;
	lastActive?: string;
	autoStopMinutes: number;
	error?: string;
};

export type AIProviderProfile = {
	baseUrl?: string;
	model?: string;
	apiKeySet?: boolean;
	routeTag?: string;
	routeKind?: string;
};

export type AIModelConfig = {
	enabled: boolean;
	autoFix?: boolean;
	provider: 'openai' | 'google' | 'deepseek' | 'openrouter' | 'ollama' | 'local_embedded' | 'custom' | string;
	baseUrl?: string;
	model: string;
	apiKeySet: boolean;
	routeTag?: string;
	routeKind?: string;
	localEngine?: AIEmbeddedConfig;
	providers?: Record<string, AIProviderProfile>;
	updatedAt?: string;
};

export type AIModelConfigUpdate = {
	enabled: boolean;
	autoFix?: boolean;
	provider: 'openai' | 'google' | 'deepseek' | 'openrouter' | 'ollama' | 'local_embedded' | 'custom' | string;
	baseUrl?: string;
	model: string;
	apiKey?: string;
	clearApiKey?: boolean;
	routeTag?: string;
	routeKind?: string;
	localEngine?: AIEmbeddedConfig;
};

export type DomainKnowledge = {
	title?: string;
	description?: string;
	org?: string;
	country?: string;
	countryCode?: string;
	icon?: string;
	category?: string;
};

export type ItemRouteStatus = {
	target: 'mihomo' | 'singbox' | 'catalog' | 'ndms' | 'hydraroute' | 'static_route';
	targetLabel: string;
	ruleName: string;
	ruleId?: string;
	matchedPattern?: string;
	isDirect?: boolean;
};

export type TrafficDevice = {
	ip: string;
	mac: string;
	name: string;
	hostname: string;
	active: boolean;
	activeSessions: number;
	policy?: string;
};

export type TrafficSession = {
	id: string;
	protocol: string;
	srcIp: string;
	srcPort: number;
	dstIp: string;
	dstPort: number;
	domain?: string;
	state: string;
	packets: number;
	bytesIn: number;
	bytesOut: number;
	totalBytes: number;
	ttl: number;
	serviceName?: string;
	serviceCategory?: string;
	knowledge?: DomainKnowledge;
	domainRoutes?: ItemRouteStatus[];
	ipRoutes?: ItemRouteStatus[];
	isConfigured?: boolean;
};

export type TrafficDomainGroup = {
	groupKey: string;
	title?: string;
	domain: string;
	domains?: string[];
	serviceName?: string;
	serviceCategory?: string;
	knowledge?: DomainKnowledge;
	sessionCount: number;
	totalBytes: number;
	bytesIn: number;
	bytesOut: number;
	ips: string[];
	ports: number[];
	sessions?: TrafficSession[];
	domainStatuses?: Record<string, ItemRouteStatus[]>;
	ipStatuses?: Record<string, ItemRouteStatus[]>;
	existingRules?: string[];
	overallStatus: 'routed' | 'partial' | 'new';
	newDomainsCount: number;
	newIpsCount: number;
};

export type ActiveEngineInfo = {
	id: 'catalog' | 'mihomo' | 'singbox' | 'hydraroute' | 'static_route';
	label: string;
	description: string;
	active: boolean;
};

export type TrafficSnapshot = {
	device: TrafficDevice;
	totalSessions: number;
	activeCount: number;
	totalBytesIn: number;
	totalBytesOut: number;
	domainGroups: TrafficDomainGroup[];
	sessions: TrafficSession[];
	activeEngines?: ActiveEngineInfo[];
	timestamp: string;
};

export type TrafficExportRequest = {
	target: 'catalog' | 'mihomo' | 'singbox' | 'hydraroute' | 'static_route';
	mode?: 'append' | 'create';
	targetRuleId?: string;
	targetPresetId?: string;
	serviceName?: string;
	domains?: string[];
	ips?: string[];
	outbound?: string;
};

export type TrafficExportResponse = {
	success: boolean;
	message: string;
	count: number;
	addedDomains?: string[];
	addedIps?: string[];
	skippedDomains?: string[];
	skippedIps?: string[];
};

export type AIMemoryFact = {
	id: string;
	category: string;
	content: string;
	source: string;
	createdAt: string;
	updatedAt: string;
};

export type AILearnedPlaybook = {
	id: string;
	category: string;
	title: string;
	trigger: string;
	diagnosis: string;
	action: string;
	target?: string;
	successCount: number;
	learnedFrom: string;
	createdAt: string;
	lastUsedAt?: string;
};

export type AILearningJournalEntry = {
	id: string;
	timestamp: string;
	trigger: string;
	query: string;
	cloudAdvice: string;
	actionTaken: string;
	outcome: string;
};

export type AISentinelSettings = {
	enabled: boolean;
	intervalSeconds: number;
	autonomyLevel: 'notify_only' | 'safe_auto' | 'disabled' | string;
};

export type AISentinelStatus = {
	running: boolean;
	enabled: boolean;
	autonomyLevel: string;
	intervalSeconds: number;
	lastCheck?: string;
	lastSymptom?: string;
	lastAction?: string;
	lastError?: string;
};

export type AIMemoryData = {
	facts: AIMemoryFact[];
	playbooks: AILearnedPlaybook[];
	journal: AILearningJournalEntry[];
	settings: AISentinelSettings;
	sentinel: AISentinelStatus;
};
