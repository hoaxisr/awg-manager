// Проверка полей режима FakeIP (пулы и MTU) перед сохранением.
//
// Проверка ЛЁГКАЯ и клиентская: авторитетно валидирует бэкенд, здесь только то,
// что позволяет не отправлять заведомо мусорный PUT и вернуть поле к прежнему
// значению. Вынесено из разметки, потому что диапазон MTU и допустимость пустого
// v6-пула — правила, а не оформление: их нужно проверять без рендера.
//
// Пустое значение допустимо у ОБОИХ пулов, но по разным причинам: пустой v6
// выключает IPv6 в туннеле, пустой v4 бэкенд заменит умолчанием при
// нормализации настроек.

const CIDR4 = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/;
const CIDR6 = /^[0-9a-fA-F:]+\/\d{1,3}$/;

export const MTU_MIN = 576;
export const MTU_MAX = 9000;

/** Ошибка поля пула или null, если значение можно отправлять. */
export function poolError(value: string, family: 4 | 6): string | null {
	const v = value.trim();
	if (v === '') return null;
	if (family === 4) {
		return CIDR4.test(v) ? null : 'Некорректный IPv4-CIDR пула (пример: 10.128.0.0/10)';
	}
	return CIDR6.test(v) ? null : 'Некорректный IPv6-CIDR пула (пусто — выключить; пример: fc00::/18)';
}

/** Ошибка поля MTU или null. Пусто — ошибка: MTU туннеля обязателен. */
export function mtuError(value: string): string | null {
	const raw = value.trim();
	const n = Number(raw);
	if (raw === '' || !Number.isInteger(n) || n < MTU_MIN || n > MTU_MAX) {
		return `MTU должен быть целым числом в диапазоне ${MTU_MIN}–${MTU_MAX}`;
	}
	return null;
}
