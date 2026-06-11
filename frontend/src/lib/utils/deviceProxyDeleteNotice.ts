import { api } from '$lib/api/client';
import { notifications } from '$lib/stores/notifications';

const DEFAULT_SUCCESS = 'Прокси удалён';
const DEFAULT_PENDING_APPLY =
	'Прокси удалён из конфига, но sing-box ещё не обновлён — изменение применится, когда сервис снова будет доступен.';

export type DeviceProxyDeleteNoticeOptions = {
	successMessage?: string;
	pendingApplyMessage?: string;
};

/** DELETE /proxy/instance with consistent success/warning when apply is deferred. */
export async function deleteDeviceProxyInstanceWithNotice(
	id: string,
	options: DeviceProxyDeleteNoticeOptions = {},
): Promise<{ deleted: boolean; applied: boolean }> {
	const result = await api.deleteDeviceProxyInstance(id);
	if (result.applied) {
		notifications.success(options.successMessage ?? DEFAULT_SUCCESS);
	} else {
		notifications.warning(options.pendingApplyMessage ?? DEFAULT_PENDING_APPLY);
	}
	return result;
}
