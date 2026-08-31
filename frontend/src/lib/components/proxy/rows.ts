// Модель строки списка/детали страницы «Прокси» и её сборка из статуса и
// конфига. Список слева един для обоих протоколов: протокол и режим — признаки
// строки, а не уровень навигации (ia.md §1).

import type {
  FreeTurnConfig,
  FreeTurnProcessStatus,
  FreeTurnServerConfig,
  FreeTurnStatus,
  WdttConfig,
  WdttProcessStatus,
  WdttServerConfig,
  WdttStatus,
} from "$lib/types";

export type ProxyProtocol = "wdtt" | "freeturn";

export type ProxyRole = "client" | "server";

export type ProxyRunState = "running" | "error" | "stopped";

export interface ProxyInstanceRow {
  /** Ключ строки: id инстанса уникален только внутри протокола. */
  key: string;
  /** id инстанса для ручек API. */
  id: string;
  protocol: ProxyProtocol;
  role: ProxyRole;
  name: string;
  state: ProxyRunState;
  /** UI-имя флага Enabled бэкенда (оговорка LS-08). */
  autostart: boolean;
  pid?: number;
  startedAt?: string;
  /**
   * Наш живой процесс с унаследованным pid-файлом (LS-09). Готовое поле
   * бэкенда — на фронте НЕ вычисляется (спека §2.1).
   */
  orphanedPid: boolean;
  binaryPresent: boolean;
  /** Режим WDTT: connMode клиента / relayMode сервера. У FreeTurn режима нет. */
  mode?: "wg" | "raw";
  /**
   * Имя старого конфига, из которого инстанс перенёс посев. Пусто у
   * заведённых через UI — по нему деталь показывает бейдж «перенесено»,
   * объясняющий происхождение «дефолтных» инстансов.
   */
  seededFrom?: string;
  /**
   * Схема потока для карточки списка: три звена «откуда — через что — куда»,
   * уже словами. Собирается здесь, чтобы список не знал про роли и конфиги.
   */
  flow?: ProxyFlowStep[];
}

/** Звено схемы потока: подпись и техническая деталь под ней. */
export interface ProxyFlowStep {
  label: string;
  detail: string;
}

type ProcessStatus = WdttProcessStatus | FreeTurnProcessStatus;
type InstanceStatus = { id: string; name: string; status: ProcessStatus };

/**
 * «Включён, но не работает» (LS-06, RB-02) — процесс должен работать, но не работает.
 *
 * Судит `enabled` конфига, а не `lastError`: бэкенд снимает Enabled только на
 * явный стоп пользователя (`internal/wdtt/service.go:803`,
 * `internal/freeturn/service.go:702`), а по нему же поднимает клиента
 * супервизор — это и есть «должен работать». `lastError` живёт в памяти
 * процесса и переживает стоп упавшего инстанса (`process.Stop` его не
 * трогает), так что сам по себе состоянием не является.
 */
function runState(s: ProcessStatus, enabled: boolean): ProxyRunState {
  if (s.running) return "running";
  return enabled ? "error" : "stopped";
}

/**
 * Ключ строки списка. Единственный владелец формата: его же собирают
 * `rowKeyFromInstanceKey` и обработчики мастеров на странице прокси, а
 * разъехавшись, они молча перестают находить строку.
 */
export function rowKey(protocol: ProxyProtocol, role: ProxyRole, id: string): string {
  return `${protocol}:${role}:${id}`;
}

function toRow(
  protocol: ProxyProtocol,
  role: ProxyRole,
  inst: InstanceStatus,
  autostart: boolean,
  mode?: "wg" | "raw",
  seededFrom?: string,
  flow?: ProxyFlowStep[],
): ProxyInstanceRow {
  const s = inst.status;
  return {
    key: rowKey(protocol, role, inst.id),
    id: inst.id,
    protocol,
    role,
    name: inst.name,
    state: runState(s, autostart),
    autostart,
    pid: s.pid,
    startedAt: s.startedAt,
    orphanedPid: s.orphanedPid === true,
    binaryPresent: s.binaryPresent,
    mode,
    seededFrom,
    flow,
  };
}

/**
 * Вид записи бэкенда → протокол и роль строки. Форматы ключей разошлись:
 * бэкенд слил протокол с ролью через дефис (`Record.Key()` —
 * `internal/proxyrt/instancestore/record.go:110`), у строки это отдельные
 * звенья. Набор закрыт — `instancestore.AllKinds`.
 */
const INSTANCE_KINDS: Record<string, { protocol: ProxyProtocol; role: ProxyRole }> = {
	'wdtt-client': { protocol: 'wdtt', role: 'client' },
	'wdtt-server': { protocol: 'wdtt', role: 'server' },
	'freeturn-client': { protocol: 'freeturn', role: 'client' },
	'freeturn-server': { protocol: 'freeturn', role: 'server' },
};

/**
 * Ключ строки списка по ключу инстанса бэкенда (`kind:id`) — им адресует
 * глубокая ссылка с карточки туннеля (`ProxyOwnedBadge`). Роль отдаём
 * вместе с ключом: по ней страница выбирает вкладку. null — вид роли
 * неизвестен или id пуст.
 */
export function rowKeyFromInstanceKey(
	instanceKey: string,
): { key: string; role: ProxyRole } | null {
	const sep = instanceKey.indexOf(':');
	if (sep < 0) return null;
	const kind = INSTANCE_KINDS[instanceKey.slice(0, sep)];
	const id = instanceKey.slice(sep + 1);
	if (!kind || !id) return null;
	return { key: rowKey(kind.protocol, kind.role, id), role: kind.role };
}

/** Порт из адреса `host:port`; пусто — адреса нет или он без порта. */
function portOf(addr?: string): string {
	const p = (addr ?? '').trim().split(':').pop() ?? '';
	return /^\d+$/.test(p) ? `:${p}` : '';
}

/** Хост из адреса, без порта: в узкой карточке порт сервера роли не играет. */
function hostOf(addr?: string): string {
	const a = (addr ?? '').trim();
	if (!a) return '—';
	const idx = a.lastIndexOf(':');
	return idx > 0 ? a.slice(0, idx) : a;
}

/** Поток клиента: роутер → сервер → интернет. */
function clientFlow(listen?: string, peer?: string): ProxyFlowStep[] {
	return [
		{ label: 'Этот роутер', detail: portOf(listen) || 'локальный порт' },
		{ label: hostOf(peer), detail: portOf(peer) || 'адрес сервера' },
		{ label: 'Интернет', detail: 'через выход' },
	];
}

/** Поток WDTT-сервера: абоненты → роутер → интернет. */
function wdttServerFlow(c?: WdttServerConfig, st?: WdttProcessStatus): ProxyFlowStep[] {
	// NDMS-имена берём из СТАТУСА: конфиг несёт только WG-половину, а raw-имя
	// (`rawNdmsIface`) живёт в наблюдении процесса.
	const ifaces = [st?.ndmsIface ?? c?.ndmsIface, st?.rawNdmsIface].filter(Boolean).join(' · ');
	const nat = c?.natMode === 'none' ? 'без NAT' : c?.natMode === 'internet-only' ? 'NAT: интернет' : 'NAT: полный';
	return [
		{ label: 'Абоненты', detail: portOf(c?.listen) || 'порт раздачи' },
		{ label: 'Этот роутер', detail: ifaces || 'интерфейсы не выделены' },
		{ label: 'Интернет', detail: nat },
	];
}

/** Поток FreeTurn-сервера: клиенты → роутер → бэкенд. */
function ftServerFlow(c?: FreeTurnServerConfig): ProxyFlowStep[] {
	return [
		{ label: 'Клиенты', detail: portOf(c?.listen) || 'порт раздачи' },
		{ label: 'Этот роутер', detail: c?.mode === 'tcp' ? 'TCP' : 'UDP' },
		{ label: c?.connect ? hostOf(c.connect) : 'Бэкенд', detail: c?.connect ? 'WG-сервер роутера' : 'не задан' },
	];
}

export interface ProxySources {
  wdttStatus: WdttStatus | null;
  wdttConfig: WdttConfig | null;
  ftStatus: FreeTurnStatus | null;
  ftConfig: FreeTurnConfig | null;
}

/** Вкладка «Выход»: клиенты обоих протоколов. */
export function exitRows(src: ProxySources): ProxyInstanceRow[] {
  return [
    ...(src.wdttStatus?.clients ?? []).map((i) => {
      const inst = src.wdttConfig?.clients.find((x) => x.id === i.id);
      const c = inst?.config;
      return toRow(
        "wdtt",
        "client",
        i,
        c?.enabled === true,
        c?.connMode === "raw" ? "raw" : "wg",
        inst?.seededFrom,
        clientFlow(c?.listen, c?.peer),
      );
    }),
    ...(src.ftStatus?.clients ?? []).map((i) => {
      const inst = src.ftConfig?.clients.find((x) => x.id === i.id);
      return toRow(
        "freeturn",
        "client",
        i,
        inst?.config.enabled === true,
        undefined,
        inst?.seededFrom,
        clientFlow(inst?.config.listen, inst?.config.peer),
      );
    }),
  ];
}

/** Вкладка «Раздача»: серверы обоих протоколов. */
export function shareRows(src: ProxySources): ProxyInstanceRow[] {
  return [
    ...(src.wdttStatus?.servers ?? []).map((i) => {
      const inst = src.wdttConfig?.servers.find((x) => x.id === i.id);
      const c = inst?.config;
      return toRow(
        "wdtt",
        "server",
        i,
        c?.enabled === true,
        // Режима у сервера нет: обе половины работают всегда, а выбор WG/Raw
        // относится к выдаваемой ссылке (панель ссылки абоненту).
        undefined,
        inst?.seededFrom,
        wdttServerFlow(c, i.status),
      );
    }),
    ...(src.ftStatus?.servers ?? []).map((i) => {
      const inst = src.ftConfig?.servers.find((x) => x.id === i.id);
      return toRow(
        "freeturn",
        "server",
        i,
        inst?.config.enabled === true,
        undefined,
        inst?.seededFrom,
        ftServerFlow(inst?.config),
      );
    }),
  ];
}
