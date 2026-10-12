import { m } from '$lib/i18n';

// Имя туннеля — описание записи интерфейса в NDMS, а NDMS принимает описание
// не длиннее 256 БАЙТ (internal/tunnel/name.go, MaxNameBytes). maxlength у
// input считает UTF-16-единицы, а не байты, поэтому проверка — здесь.
export const TUNNEL_NAME_MAX_BYTES = 256;

export function tunnelNameBytes(name: string): number {
	return new TextEncoder().encode(name).length;
}

/** Пустая строка — имя влезает; иначе текст ошибки для показа у поля. */
export function tunnelNameError(name: string): string {
	return tunnelNameBytes(name) > TUNNEL_NAME_MAX_BYTES
		? m.validation_tunnel_name_too_long({ max: TUNNEL_NAME_MAX_BYTES })
		: '';
}
