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
	cpuPercent: number;
	memoryRss: number;
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
