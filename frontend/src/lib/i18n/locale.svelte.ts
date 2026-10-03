import {
	baseLocale,
	localStorageKey,
	locales,
	overwriteGetLocale,
	toLocale,
	type Locale,
} from '$lib/paraglide/runtime';

export type { Locale };

/** Все поддерживаемые языки в порядке показа в переключателе. */
export const LOCALES: readonly Locale[] = locales;

/** Самоназвания языков — не переводятся, чтобы свой язык находился в любой локали. */
export const LOCALE_LABELS: Record<Locale, string> = {
	ru: 'Русский',
	en: 'English',
};

function readStoredLocale(): Locale | undefined {
	try {
		return toLocale(localStorage.getItem(localStorageKey)) ?? undefined;
	} catch {
		return undefined; // private mode / запрет хранилища
	}
}

/**
 * Стартовая локаль: явный выбор пользователя → ru (как strategy в
 * scripts/paraglide.mjs). Определяем сами, а не штатным getLocale() рантайма:
 * тот записывает найденную локаль в localStorage, а храним только явный выбор.
 */
function detectLocale(): Locale {
	if (typeof window === 'undefined') return baseLocale;
	return readStoredLocale() ?? baseLocale;
}

let current = $state<Locale>(detectLocale());

// Сообщения Paraglide (m.foo()) читают локаль через getLocale(). Отдаём им
// $state — тогда любой шаблон или $derived, вызвавший m.foo(), подписывается
// на смену языка и перерисовывается без перезагрузки страницы.
overwriteGetLocale(() => current);

/** Формат дат и чисел: «авто» или явная локаль Intl. */
export type DateFormat = 'auto' | 'ru-RU' | 'en-GB' | 'en-US';
export const DATE_FORMATS: readonly DateFormat[] = ['auto', 'ru-RU', 'en-GB', 'en-US'];
const DATE_FORMAT_KEY = 'awg-manager-date-format';

function readDateFormat(): DateFormat {
	if (typeof window === 'undefined') return 'auto';
	try {
		const raw = localStorage.getItem(DATE_FORMAT_KEY);
		return DATE_FORMATS.find((f) => f === raw) ?? 'auto';
	} catch {
		return 'auto';
	}
}

let dateFormatPref = $state<DateFormat>(readDateFormat());

/**
 * «Авто»: русский интерфейс — ru-RU; английский — en-US, если первый
 * английский язык браузера американский, иначе en-GB (24 часа, день перед
 * месяцем, как в русском).
 */
export function autoFormatLocale(): string {
	if (current !== 'en') return 'ru-RU';
	try {
		const langs = navigator.languages?.length ? navigator.languages : [navigator.language];
		const english = langs.find((l) => l.toLowerCase().startsWith('en'));
		return english?.toLowerCase() === 'en-us' ? 'en-US' : 'en-GB';
	} catch {
		return 'en-GB';
	}
}

/**
 * Локаль Intl для форматов дат и чисел: выбор пользователя или «авто».
 * Вызов внутри шаблона или $derived подписывается на смену языка и формата.
 */
export function formatLocale(): string {
	return dateFormatPref === 'auto' ? autoFormatLocale() : dateFormatPref;
}

export const dateFormat = {
	get current(): DateFormat {
		return dateFormatPref;
	},
	/** Ручной выбор формата: применяется сразу и запоминается в этом браузере. */
	set(next: DateFormat): void {
		if (!DATE_FORMATS.includes(next)) return;
		try {
			localStorage.setItem(DATE_FORMAT_KEY, next);
		} catch {
			/* quota / private mode: формат сменится до перезагрузки */
		}
		dateFormatPref = next;
	},
};

export const locale = {
	get current(): Locale {
		return current;
	},
	/** Ручной выбор языка: применяется сразу и запоминается в этом браузере. */
	set(next: Locale): void {
		if (!LOCALES.includes(next)) return;
		try {
			localStorage.setItem(localStorageKey, next);
		} catch {
			/* quota / private mode: язык сменится до перезагрузки */
		}
		current = next;
	},
};
