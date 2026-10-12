import { m } from '$lib/i18n';
import { api } from '$lib/api/client';
import { notifications } from '$lib/stores/notifications';

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
		notifications.success(options.successMessage ?? m.device_proxy_deleted());
	} else {
		notifications.warning(options.pendingApplyMessage ?? m.device_proxy_deleted_pending_apply());
	}
	return result;
}
