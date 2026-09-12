// Миграция ключа подписки Amnezia Premium из localStorage (F272).
//
// Прежние версии клали ключ подписки в localStorage браузера ОТКРЫТЫМ
// ТЕКСТОМ. Код, который его читал и писал, удалён (коммит 325294061), но у
// всех, кто хоть раз нажал «Запомнить ключ», запись осталась лежать и уже
// никем не используется. Этот файл — весь код миграции: мастер подставляет
// ключ в пустое поле один раз и стирает запись после ЛЮБОГО успешного входа.
//
// ФАЙЛ ВРЕМЕННЫЙ. Снимается целиком вместе с обоими вызовами в мастере через
// несколько релизов после того, в котором уехал мастер, — задача F276 в
// docs/TRACKER.md. Раньше нельзя: у не открывавших панель секрет так и
// останется в браузере.

const LEGACY_PREMIUM_KEY_STORAGES = [
	'awgm.tunnels.premiumVpnKey',
	'awgm.tunnels.new.premiumVpnKey',
	'awgm.tunnels.replace.premiumVpnKey',
] as const;

/** Ключ, оставленный прежней версией; пустая строка — мигрировать нечего. */
export function readLegacyPremiumKey(): string {
	if (typeof localStorage === 'undefined') return '';
	for (const name of LEGACY_PREMIUM_KEY_STORAGES) {
		try {
			const value = localStorage.getItem(name)?.trim();
			if (value) return value;
		} catch {
			// Приватный режим или запрет на хранилище — миграции просто не будет.
		}
	}
	return '';
}

/**
 * Стирает ключ по всем трём именам.
 *
 * Вызывается после ЛЮБОГО успешного входа, а не только когда пользователь
 * выбрал «запомнить»: запись в localStorage — открытый секрет на диске
 * браузера, и уйти она обязана независимо от того, что пользователь решил
 * про хранение ключа на роутере.
 */
export function clearLegacyPremiumKeys(): void {
	if (typeof localStorage === 'undefined') return;
	for (const name of LEGACY_PREMIUM_KEY_STORAGES) {
		try {
			localStorage.removeItem(name);
		} catch {
			// Стереть не дали — показать это пользователю нечем и незачем.
		}
	}
}
