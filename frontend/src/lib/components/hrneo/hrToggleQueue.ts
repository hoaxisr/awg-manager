import type { DnsRoute } from '$lib/types';
import { hrPosKey, hrRevisionOf, parseHrId, withRevision } from './hrIds';

/**
 * Очередь переключений HR-групп. Каждая наша запись меняет ревизию файлов,
 * поэтому второе переключение со старым id получило бы 409. Переключения
 * копятся debounceMs и уходят по одному; id следующего переносится на
 * ревизию из ответа предыдущего — переключение позиций не сдвигает.
 * Переносятся только ревизии, известные очереди (стартовая и полученные в
 * её же ответах): id с чужой ревизией уходит как есть и честно получает 409.
 */
export function createHrToggleQueue(deps: {
	setEnabled: (id: string, enabled: boolean) => Promise<DnsRoute[]>;
	onApplied: (fresh: DnsRoute[]) => void;
	onError: (e: unknown) => void;
	debounceMs: number;
}) {
	let queue = new Map<string, { id: string; enabled: boolean }>();
	let timer: ReturnType<typeof setTimeout> | null = null;
	let running = false;
	/** Ревизии, которые дала наша же запись (и стартовая): с них перенос безопасен. */
	let known = new Set<string>();
	let lastRev: string | null = null;
	/** Вкладку размонтировали: идущий сброс молча останавливается. */
	let disposed = false;

	function schedule() {
		if (timer) clearTimeout(timer);
		timer = setTimeout(() => void flush(), deps.debounceMs);
	}

	async function flush() {
		timer = null;
		// Сброс уже идёт: новые переключения он не видит — их подберёт
		// повторный запуск из finally.
		if (running) return;
		running = true;
		const items = [...queue.entries()];
		try {
			for (const [key, item] of items) {
				const ref = parseHrId(item.id);
				if (ref && known.size === 0) known.add(ref.revision);
				const id = ref && lastRev && known.has(ref.revision) ? withRevision(item.id, lastRev) : item.id;
				const fresh = await deps.setEnabled(id, item.enabled);
				if (disposed) return;
				lastRev = hrRevisionOf(fresh);
				if (lastRev) known.add(lastRev);
				// Группу переключили ещё раз, пока шёл запрос, — новое значение
				// остаётся в очереди.
				if (queue.get(key) === item) queue.delete(key);
				deps.onApplied(fresh);
			}
		} catch (e) {
			if (disposed) return;
			queue = new Map();
			deps.onError(e);
		} finally {
			running = false;
			if (queue.size > 0 && !timer) schedule();
			// Очередь опустела — цепочка наших записей закончилась; следующая
			// начнётся с ревизии, которую покажет свежий список.
			if (queue.size === 0) {
				known = new Set();
				lastRev = null;
			}
		}
	}

	return {
		toggle(id: string, enabled: boolean) {
			queue.set(hrPosKey(id), { id, enabled });
			schedule();
		},
		/** Оптимистичное состояние по позиционному ключу. */
		pending(): Record<string, boolean> {
			return Object.fromEntries([...queue].map(([k, v]) => [k, v.enabled]));
		},
		dispose() {
			disposed = true;
			if (timer) clearTimeout(timer);
			timer = null;
			queue = new Map();
		},
	};
}
