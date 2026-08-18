import { stripAnsi } from '$lib/utils/ansi';

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

type AutoLoginPhase = 'wait_login' | 'wait_password' | 'done';

/** Watches tty output and injects login/password when login(1) prompts appear. */
export function createTerminalAutoLogin(
	send: (data: string) => void,
	creds: Pick<TerminalAutoLogin, 'login' | 'password'> | null,
) {
	let phase: AutoLoginPhase = 'wait_login';
	let buf = '';

	function reset() {
		phase = 'wait_login';
		buf = '';
	}

	function feed(chunk: string) {
		if (!creds?.login || phase === 'done') return;
		buf = (buf + stripAnsi(chunk)).slice(-600);
		const tail = buf.replace(/\r/g, '').trimEnd();

		if (phase === 'wait_login' && loginPromptRe.test(tail)) {
			send(creds.login + '\r');
			phase = 'wait_password';
			buf = '';
			return;
		}
		if (phase === 'wait_password' && passwordPromptRe.test(tail)) {
			send(creds.password + '\r');
			phase = 'done';
			buf = '';
		}
	}

	return { feed, reset };
}

// Busybox/Entware login, Keenetic, generic getty prompts.
const loginPromptRe = /(?:^|\n)[^\n]*\b(?:login|username|логин)\s*:\s*$/i;
const passwordPromptRe = /(?:^|\n)[^\n]*\b(?:password|пароль)\s*:\s*$/i;
