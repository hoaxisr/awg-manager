import { api } from '$lib/api/client';
import { m } from '$lib/i18n';
import { notifications } from '$lib/stores/notifications';
import { zapretStatus } from '$lib/stores/zapret';
import type { ZapretControlAction, ZapretStatus } from '$lib/types';

/** Run a Zapret control action and keep the shared status store in sync. */
export async function runZapretControl(
	action: ZapretControlAction,
	base: string,
): Promise<ZapretStatus | null> {
	try {
		const fresh = await api.controlZapret(action, base);
		zapretStatus.applyMutationResponse(fresh);
		switch (action) {
			case 'start':
				notifications.success(m.zapret_action_started());
				break;
			case 'stop':
				notifications.success(m.zapret_action_stopped());
				break;
			case 'restart':
			case 'restart-daemons':
				notifications.success(m.zapret_action_restarted());
				break;
		}
		return fresh;
	} catch (error) {
		zapretStatus.invalidate();
		notifications.error(error instanceof Error ? error.message : m.zapret_error_control());
		return null;
	}
}
