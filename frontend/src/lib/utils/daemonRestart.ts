import { m } from '$lib/i18n';
import { waitForBackendRestart } from '$lib/restartRecovery';

/** Запрос перезапуска демона. Обрыв соединения — тоже успех: демон уходит. */
export async function requestDaemonRestart(): Promise<'accepted' | 'network-drop'> {
	try {
		const response = await fetch('/api/system/restart', {
			method: 'POST',
			credentials: 'same-origin',
			cache: 'no-store',
			headers: { 'Content-Type': 'application/json' },
		});

		if (response.status === 401) {
			throw new Error(m.settings_page_session_expired());
		}

		if (!response.ok) {
			const text = await response.text().catch(() => '');
			throw new Error(m.settings_page_restart_http_failed({ status: response.status, text: text.substring(0, 120) }));
		}

		return 'accepted';
	} catch (e) {
		if (e instanceof TypeError) {
			return 'network-drop';
		}
		throw e;
	}
}

/** Идентификатор процесса демона из /api/health; новый — значит перезапустился. */
export async function readBackendInstanceId(): Promise<string | null> {
	const res = await fetch('/api/health', {
		method: 'GET',
		cache: 'no-store',
		credentials: 'same-origin',
	});
	if (!res.ok) {
		return null;
	}
	const body = await res.json().catch(() => null);
	const id = body?.data?.instanceId;
	return typeof id === 'string' && id.length > 0 ? id : null;
}

function sleep(ms: number) {
	return new Promise<void>((resolve) => setTimeout(resolve, ms));
}

export function waitForDaemonRestart(previousInstanceId: string | null) {
	return waitForBackendRestart({
		previousInstanceId,
		readInstanceId: readBackendInstanceId,
		sleep,
		now: () => Date.now(),
		timeoutMs: 45_000,
		pollMs: 750,
		stableOnlineMs: 3_000,
	});
}
