// Итог удаления инстанса словами пользователя (TS-02, TS-03).

import { notifications } from '$lib/stores/notifications';

/**
 * TS-03 просит имена туннелей, а бэкенд отдаёт строку «Имя (id): ошибка»
 * (`internal/api/wdtt_linked.go:95`) — отрезаем хвост с id и текстом ошибки.
 * Строка без этого хвоста (сбой чтения хранилища) остаётся как есть.
 */
export function tunnelErrorNames(errors: string[]): string[] {
	return errors.map((e) => e.replace(/ \([^)]*\): [\s\S]*$/, '').trim()).filter(Boolean);
}

export function reportDeletedTunnels(deleted?: string[], errors?: string[]): void {
	if (deleted?.length) {
		notifications.success(`AWG-туннелей удалено: ${deleted.length} — перезапустите клиент`);
	}
	if (errors?.length) {
		notifications.error(`Не удалось удалить туннели: ${tunnelErrorNames(errors).join(', ')}`);
	}
}
