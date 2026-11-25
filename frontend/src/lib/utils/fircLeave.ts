import { api } from '$lib/api/client';
import { m } from '$lib/i18n';
import { reloadFircAll } from '$lib/stores/firc';
import { notifications } from '$lib/stores/notifications';
import { singboxRouter } from '$lib/stores/singboxRouter';

/** Уйти с движка AWGM Route: на sing-box, а без него — выключить маршрутизацию панели. */
export async function leaveFircEngine(): Promise<void> {
	try {
		await api.fircUpdateSettings({ routingEngine: 'sing-box' });
		await Promise.all([reloadFircAll(), singboxRouter.reloadSettings()]);
		notifications.success(m.firc_foreign_left());
	} catch (e) {
		notifications.error(m.firc_foreign_leave_failed({ error: (e as Error).message }));
	}
}
