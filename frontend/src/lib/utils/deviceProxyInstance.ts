import { m } from '$lib/i18n';
import type { DeviceProxyInstance, DeviceProxyConfig } from '$lib/types';

/** Извлечь редактируемый DeviceProxyConfig из инстанса. */
export function configFromInstance(in_: DeviceProxyInstance): DeviceProxyConfig {
  return {
    enabled: in_.enabled,
    listenAll: in_.listenAll,
    listenInterface: in_.listenInterface,
    port: in_.port,
    auth: { ...in_.auth },
    selectedOutbound: in_.selectedOutbound,
  };
}

/** Слить отредактированный config обратно в инстанс (id/name сохраняются). */
export function mergeInstanceConfig(in_: DeviceProxyInstance, cfg: DeviceProxyConfig): DeviceProxyInstance {
  return {
    ...in_,
    enabled: cfg.enabled,
    listenAll: cfg.listenAll,
    listenInterface: cfg.listenInterface,
    port: cfg.port,
    auth: { ...cfg.auth },
    selectedOutbound: cfg.selectedOutbound,
  };
}

/** Первый свободный порт (>=1099) среди существующих инстансов. */
export function nextFreeDeviceProxyPort(existing: DeviceProxyInstance[]): number {
  const used = new Set(existing.map((in_) => in_.port));
  for (let p = 1099; p <= 65535; p++) {
    if (!used.has(p)) return p;
  }
  return 1100;
}

/** Новый инстанс с дефолтами (имя/порт зависят от существующих). */
export function newDeviceProxyInstance(existing: DeviceProxyInstance[]): DeviceProxyInstance {
  const n = Math.random().toString(36).slice(2, 8);
  return {
    id: `px-${n}`,
    name: m.device_proxy_default_name({ n: existing.length + 1 }),
    enabled: false,
    listenAll: true,
    listenInterface: '',
    port: nextFreeDeviceProxyPort(existing),
    auth: { enabled: false, username: '', password: '' },
    selectedOutbound: 'direct',
  };
}
