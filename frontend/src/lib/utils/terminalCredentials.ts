const STORAGE_KEY = 'awgm-terminal-auto-login';

export type TerminalAutoLogin = {
	login: string;
	password: string;
	enabled: boolean;
};

export function loadTerminalAutoLogin(): TerminalAutoLogin | null {
	try {
		const raw = sessionStorage.getItem(STORAGE_KEY);
		if (!raw) return null;
		const parsed = JSON.parse(raw) as TerminalAutoLogin;
		if (!parsed?.enabled || !parsed.login?.trim()) return null;
		return {
			login: parsed.login.trim(),
			password: parsed.password ?? '',
			enabled: true,
		};
	} catch {
		return null;
	}
}

export function saveTerminalAutoLogin(value: TerminalAutoLogin): void {
	const payload: TerminalAutoLogin = {
		login: value.login.trim(),
		password: value.password,
		enabled: value.enabled && !!value.login.trim(),
	};
	if (!payload.enabled) {
		sessionStorage.removeItem(STORAGE_KEY);
		return;
	}
	sessionStorage.setItem(STORAGE_KEY, JSON.stringify(payload));
}

export function clearTerminalAutoLogin(): void {
	sessionStorage.removeItem(STORAGE_KEY);
}
