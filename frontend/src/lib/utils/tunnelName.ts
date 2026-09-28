// Имя туннеля — описание записи интерфейса в NDMS, а NDMS принимает описание
// не длиннее 256 БАЙТ (internal/tunnel/name.go, MaxNameBytes). maxlength у
// input считает UTF-16-единицы, а не байты, поэтому проверка — здесь.
export const TUNNEL_NAME_MAX_BYTES = 256;

/** Текст совпадает с ErrNameTooLong сервера. */
export const TUNNEL_NAME_TOO_LONG = `имя туннеля длиннее ${TUNNEL_NAME_MAX_BYTES} байт (ограничение роутера)`;

export function tunnelNameBytes(name: string): number {
	return new TextEncoder().encode(name).length;
}

/** Пустая строка — имя влезает; иначе текст ошибки для показа у поля. */
export function tunnelNameError(name: string): string {
	return tunnelNameBytes(name) > TUNNEL_NAME_MAX_BYTES ? TUNNEL_NAME_TOO_LONG : '';
}
