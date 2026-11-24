import type { FircInterfaceOption } from '$lib/types/firc';

/** Подпись интерфейса в списках настроек FIRC: «id — описание», WAN — как Direct. */
export function formatIfaceLabel(opt: FircInterfaceOption): string {
  const desc = (opt.name || opt.ndmsName || '').split(' (')[0].trim();
  const base = desc && desc !== opt.id ? `${opt.id} — ${desc}` : opt.id;
  return opt.role === 'wan' ? `Direct (${base})` : base;
}
