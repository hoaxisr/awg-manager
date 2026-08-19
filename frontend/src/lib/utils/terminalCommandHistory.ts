const HISTORY_KEY = 'awgm-terminal-cmd-history';
const ENABLED_KEY = 'awgm-terminal-cmd-history-enabled';
const HISTORY_WIDTH_KEY = 'awgm-terminal-history-width';
const MAX_COMMANDS = 100;

export const HISTORY_MIN_WIDTH = 100;
export const TERMINAL_MIN_WIDTH = 160;
export const HISTORY_DEFAULT_WIDTH = 200;
export const SPLITTER_WIDTH = 6;

export function loadTerminalHistoryEnabled(): boolean {
	try {
		const raw = localStorage.getItem(ENABLED_KEY);
		if (raw === null) return true;
		return raw === '1';
	} catch {
		return true;
	}
}

export function saveTerminalHistoryEnabled(enabled: boolean): void {
	try {
		localStorage.setItem(ENABLED_KEY, enabled ? '1' : '0');
	} catch {
		/* ignore quota errors */
	}
}

export function loadTerminalHistoryWidth(fallback = HISTORY_DEFAULT_WIDTH): number {
	try {
		const raw = localStorage.getItem(HISTORY_WIDTH_KEY);
		if (!raw) return fallback;
		const parsed = Number.parseInt(raw, 10);
		if (!Number.isFinite(parsed)) return fallback;
		return Math.max(HISTORY_MIN_WIDTH, parsed);
	} catch {
		return fallback;
	}
}

export function saveTerminalHistoryWidth(width: number): void {
	try {
		localStorage.setItem(HISTORY_WIDTH_KEY, String(Math.max(HISTORY_MIN_WIDTH, Math.round(width))));
	} catch {
		/* ignore quota errors */
	}
}

export function loadTerminalCommands(): string[] {
	try {
		const raw = sessionStorage.getItem(HISTORY_KEY);
		if (!raw) return [];
		const parsed = JSON.parse(raw);
		if (!Array.isArray(parsed)) return [];
		return parsed.filter((item): item is string => typeof item === 'string' && item.trim().length > 0);
	} catch {
		return [];
	}
}

export function saveTerminalCommands(commands: string[]): void {
	try {
		sessionStorage.setItem(HISTORY_KEY, JSON.stringify(commands.slice(0, MAX_COMMANDS)));
	} catch {
		/* ignore quota errors */
	}
}

/** Move command to top; skip consecutive duplicates. */
export function pushTerminalCommand(commands: string[], command: string): string[] {
	const trimmed = command.trim();
	if (!trimmed) return commands;
	const next = commands.filter((c) => c !== trimmed);
	next.unshift(trimmed);
	return next.slice(0, MAX_COMMANDS);
}

/** Tracks typed line in xterm and submits on Enter once shell is ready. */
export function createCommandLineTracker(onSubmit: (command: string) => void) {
	let line = '';
	let shellReady = false;

	function markShellReady() {
		shellReady = true;
	}

	function reset() {
		line = '';
		shellReady = false;
	}

	function feed(data: string) {
		if (!shellReady) return;

		if (data.includes('\x1b')) {
			if (data === '\x03' || data === '\x1a') line = '';
			if (data === '\x15') line = '';
			return;
		}

		for (const ch of data) {
			if (ch === '\r' || ch === '\n') {
				const cmd = line.trim();
				line = '';
				if (cmd) onSubmit(cmd);
			} else if (ch === '\x7f' || ch === '\b') {
				line = line.slice(0, -1);
			} else if (ch === '\x03' || ch === '\x1a') {
				line = '';
			} else if (ch === '\x15') {
				line = '';
			} else if (ch >= ' ' || ch === '\t') {
				line += ch;
			}
		}
	}

	return { feed, reset, markShellReady };
}

/** Detect BusyBox / ash shell prompt in tty output. */
export function looksLikeShellPrompt(text: string): boolean {
	const tail = text.replace(/\r/g, '').slice(-120);
	return /(?:^|\n)[^\n]*(?:#\s*|[$>]\s*)$/.test(tail) || /~\s*#\s*$/.test(tail);
}
