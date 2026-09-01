// Итог удаления инстанса словами пользователя (TS-02, TS-03).

import { notifications } from '$lib/stores/notifications';

/**
 * TS-03 просит имена туннелей, а бэкенд отдаёт строку «Имя (id): ошибка»
 * (`fmt.Sprintf("%s (%s): %v", …)`, `internal/api/wdtt_linked.go:95`) —
 * отрезаем хвост с id и текстом ошибки. Строка без этого хвоста (сбой чтения
 * хранилища) остаётся как есть.
 *
 * Имя туннеля пользователь правит сам и может вписать в него что угодно,
 * поэтому хвост ищется с конца и только по тому, что похоже на id: id
 * менеджера — `awg10`, `wdttraw-default` — без пробелов и скобок
 * (`internal/wdtt/raw_tunnel_meta.go:18`). Так имя вида «Клиент (v2): тест»
 * не обрезается раньше времени. Ошибка, у которой внутри есть своё
 * ` (токен): `, разделима только на глаз — там отрежется по ней.
 */
export function tunnelErrorNames(errors: string[]): string[] {
	return errors
		.map((e) => e.replace(/^([\s\S]*) \([A-Za-z0-9_-]+\): [\s\S]*$/, '$1').trim())
		.filter(Boolean);
}

export function reportDeletedTunnels(deleted?: string[], errors?: string[]): void {
	if (deleted?.length) {
		notifications.success(`AWG-туннелей удалено: ${deleted.length} — перезапустите клиент`);
	}
	if (errors?.length) {
		notifications.error(`Не удалось удалить туннели: ${tunnelErrorNames(errors).join(', ')}`);
	}
}

/**
 * Отчёт об уборке из ОТКАЗА удаления (PF23). Бэкенд кладёт его в тот же
 * конверт, в поле `data`, а клиент отдаёт всё тело ошибки в `err.body` —
 * поэтому читать его можно без своей формы ответа.
 *
 * Зачем вообще: удаление многошаговое, и падение второго шага не отменяет
 * первый. Туннели уже сняты, инстанс остался — сказать только «не удалось»
 * значит оставить человека с исчезнувшей карточкой туннеля и без объяснения.
 */
export function reportDeletedTunnelsFromError(e: unknown): void {
	const body = (e as { body?: { data?: { deletedTunnels?: string[]; tunnelErrors?: string[] } } })
		?.body;
	reportDeletedTunnels(body?.data?.deletedTunnels, body?.data?.tunnelErrors);
}
