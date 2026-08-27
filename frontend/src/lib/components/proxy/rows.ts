// Модель строки списка/детали страницы «Прокси» и её сборка из статуса и
// конфига. Список слева един для обоих протоколов: протокол и режим — признаки
// строки, а не уровень навигации (ia.md §1).

import type {
  FreeTurnConfig,
  FreeTurnProcessStatus,
  FreeTurnStatus,
  WdttConfig,
  WdttProcessStatus,
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
}

type ProcessStatus = WdttProcessStatus | FreeTurnProcessStatus;
type InstanceStatus = { id: string; name: string; status: ProcessStatus };

/**
 * «Не запускается» (LS-06, RB-02) — процесс должен работать, но не работает.
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

function toRow(
  protocol: ProxyProtocol,
  role: ProxyRole,
  inst: InstanceStatus,
  autostart: boolean,
  mode?: "wg" | "raw",
  seededFrom?: string,
): ProxyInstanceRow {
  const s = inst.status;
  return {
    key: `${protocol}:${role}:${inst.id}`,
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
  };
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
        c?.relayMode === "raw" ? "raw" : "wg",
        inst?.seededFrom,
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
      );
    }),
  ];
}
