import { m } from '$lib/i18n';
import { notifications } from '$lib/stores/notifications';
import { waitForBackendRestart } from '$lib/restartRecovery';
import { pageState } from './pageState.svelte';

async function readBackendInstanceId(): Promise<string | null> {
	const res = await fetch('/api/health', {
		method: 'GET',
		cache: 'no-store',
		credentials: 'same-origin'
	});
	if (!res.ok) return null;
	const body = await res.json().catch(() => null);
	const id = body?.data?.instanceId;
	return typeof id === 'string' && id.length > 0 ? id : null;
}

function sleep(ms: number) {
	return new Promise<void>((resolve) => setTimeout(resolve, ms));
}

/**
 * Восстановление данных — из файла и из снимка: запрос, затем ожидание
 * перезапуска демона и перезагрузка страницы. Признак общий для обеих карточек
 * и переживает уход со вкладки: второе восстановление поверх идущего запускать
 * нельзя.
 */
export function restoreOp() {
	const op = pageState('restore', () => ({ restoring: false }));

	async function run(action: () => Promise<unknown>) {
		op.restoring = true;
		const before = await readBackendInstanceId().catch(() => null);
		try {
			await action();
			notifications.success(m.settings_backup_restored());
			const waitResult = await waitForBackendRestart({
				previousInstanceId: before,
				readInstanceId: readBackendInstanceId,
				sleep,
				now: () => Date.now(),
				timeoutMs: 120_000
			});
			if (waitResult === 'timeout') {
				notifications.warning(m.settings_backup_restart_timeout());
				op.restoring = false;
				return;
			}
			location.reload();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_backup_restore_failed());
			op.restoring = false;
		}
	}

	return {
		get restoring() {
			return op.restoring;
		},
		run,
	};
}
