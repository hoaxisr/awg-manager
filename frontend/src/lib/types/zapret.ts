/**
 * Zapret — внешний anti-DPI движок, который пользователь ставит на роутер
 * сам (zapret / zapret2 / z2k / KZM2). AWGM его не устанавливает: только
 * читает состояние и правит то, что принадлежит пользователю.
 *
 * Пути на бэкенде выводятся из найденной установки, поэтому здесь нет
 * захардкоженных `/opt/zapret2` — бэкенд сам отдаёт фактические.
 */

/** Тот же закрытый набор, что у hydraroute, чтобы переиспользовать бейджи. */
export type ZapretProcessState = 'not_installed' | 'stopped' | 'running';

/** Действия POST /api/zapret/control. */
export type ZapretControlAction = 'start' | 'stop' | 'restart' | 'restart-daemons';

/** Stable backend diagnostic code; user-facing copy is localized in the UI. */
export interface ZapretWarning {
	code: string;
	detail?: string;
}

export interface ZapretStatus {
	installed: boolean;
 conflict?: boolean;
	/** zapret2 или zapret — от этого зависит имя переменной с опциями. */
	flavor?: string;
	base?: string;
	config?: string;
	binary?: string;
	/** NFQWS2_OPT (zapret2) либо NFQWS_OPT (zapret). */
	optVar?: string;

	version?: string;
	/** Имя opkg-пакета. В репозитории Entware zapret нет, поэтому это
	 *  всегда локально собранный .ipk — zapret22, zapret2 и т.п. */
	package?: string;
	packageVersion?: string;

	running: boolean;
	pids?: number[];
	processState: ZapretProcessState;
	lastError?: string;

	initScript?: string;

	/**
	 * Прочие найденные установки zapret на диске, кроме `base`. Их бывает
	 * несколько (`/opt/zapret` — zapret v1, `/opt/zapret2` — zapret2, плюс
	 * форки вроде `/opt/zapret22`), и работает та, из которой запущен демон.
	 * Показываем явно: правка не в той копии не даст эффекта.
	 */
	otherInstalls?: string[];
	/** Каталог, из которого реально запущен демон. Пусто, если он не запущен. */
	activeBase?: string;

	strategyCount: number;
	hostlistCount: number;
	hasUserHostlist: boolean;
}

/** Ссылка на список доменов внутри карточки стратегии. */
export interface ZapretStrategyHostlist {
	path: string;
	/**
	 * exists=false означает, что nfqws2 не стартует: он требует
	 * существующий непустой файл и иначе завершает процесс.
	 */
	exists: boolean;
	entries: number;
	hasIps: boolean;
	ipOnly: boolean;

	/**
	 * Заполнено, если список пришёл не из явного `--hostlist=`, а из
	 * маркера <HOSTLIST>: его init-скрипт zapret разворачивает в несколько
	 * реальных списков при старте. Пусто = путь указан явно.
	 */
	fromPlaceholder?: string;
}

/** Один вызов --lua-desync. Их порядок и есть сама стратегия. */
export interface ZapretLuaInstance {
	function: string;
	args?: string;
	/** Значение :strategy=N — слот циклического перебора. */
	slot?: string;
	/** Значение :key= — метка пула, по ней circular помнит, где остановился. */
	key?: string;
}

/**
 * Профиль nfqws2 = одна карточка.
 *
 * index — приоритет, а не идентичность: nfqws2 проверяет фильтры строго
 * слева направо и выигрывает ПЕРВЫЙ подошедший. Перестановка карточек
 * обязана менять файл, иначе интерфейс будет врать о порядке.
 */
export interface ZapretStrategy {
	index: number;
	/** Исходная строка опций без завершающего --new. Сохраняется как есть. */
	raw: string;
	name?: string;

	filterTcp?: string;
	filterUdp?: string;
	filterL7?: string;
	filterL3?: string;
	filterIcmp?: string;
	filterIpp?: string;
	inRange?: string;
	outRange?: string;
	payload?: string;

	hostlists?: ZapretStrategyHostlist[];
	hostlistExcludes?: ZapretStrategyHostlist[];
	hostlistAuto?: ZapretStrategyHostlist[];
	ipsets?: ZapretStrategyHostlist[];

	/**
	 * Маркеры <HOSTLIST> / <HOSTLIST_NOAUTO> в этом профиле. Zapret
	 * разворачивает их в набор списков при старте, поэтому профиль с
	 * <HOSTLIST> работает сразу с несколькими файлами.
	 */
	placeholders?: string[];

	luaDesync?: ZapretLuaInstance[];
	/** Грабли конкретной стратегии — показываются прямо на карточке. */
	warnings?: ZapretWarning[];
}

export interface ZapretStrategies {
 revision: string;
	optVar: string;
	config: string;
	items: ZapretStrategy[];
	/** Значение переменной как есть — для «показать как есть» и отладки. */
	raw: string;
	parseWarnings?: ZapretWarning[];
}

export type ZapretHostlistId = 'user' | 'exclude' | 'auto' | 'main';

export interface ZapretHostlistInfo {
	id: ZapretHostlistId;
	path: string;
	exists: boolean;
	entries: number;
	domains: number;
	ips: number;
	/**
	 * editable=false у генерируемых списков: zapret пересобирает их
	 * своими задачами, и правка была бы потеряна.
	 */
	editable: boolean;
	gzipped: boolean;
	generated: boolean;
	warnings?: ZapretWarning[];
}

export interface ZapretHostlistContent {
	restartError?: string;
 revision: string;
	id: ZapretHostlistId;
	path: string;
	content?: string;
}
