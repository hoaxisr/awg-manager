import type { SingboxRouterSettings } from '$lib/types';
import { api } from '$lib/api/client';
import { singboxRouter } from '$lib/stores/singboxRouter';

export interface BypassPresetMeta {
  id: string;
  label: string;
  desc: string;
}

export const BYPASS_PRESETS: readonly BypassPresetMeta[] = [
  { id: 'l2tp', label: 'L2TP / IPsec VPN', desc: 'UDP 500, 1701, 4500' },
  { id: 'ntp', label: 'NTP (синхронизация времени)', desc: 'UDP 123' },
  { id: 'netbios-smb', label: 'NetBIOS / SMB', desc: 'UDP 137/138, TCP 139/445' },
  // Не порты/CIDR: имена KeenDNS/CrazeDNS уходят резолверу самого роутера,
  // а адреса, которые он на них отдаёт, исключаются из перехвата.
  { id: 'keendns', label: 'KeenDNS / CrazeDNS', desc: 'имена роутера резолвит сам роутер, его адреса — мимо sing-box' },
];

export async function mergeAndSaveSettings(
  patch: Partial<SingboxRouterSettings>,
): Promise<void> {
  // База для merge — СВЕЖИЙ GET с сервера, а не значение стора: настройки
  // меняются и вне settings-форм (fakeipRealServer пишет бэкенд при правке
  // адреса DNS-сервера «real» — #487), а PUT уносит полный объект, поэтому
  // эхо устаревшего стора молча откатывало такие изменения.
  const current = await api.singboxRouterGetSettings();
  const merged: SingboxRouterSettings = { ...current, ...patch };
  await api.singboxRouterPutSettings(merged);
  await singboxRouter.loadAll();
}
