import { writable } from 'svelte/store';
import { notificationCenter } from './notificationCenter';

export type NotificationType = 'success' | 'error' | 'info' | 'warning';

export interface NotificationAction {
	label: string;
	href: string;
}

export interface Notification {
	id: string;
	type: NotificationType;
	message: string;
	duration?: number;
	action?: NotificationAction;
}

interface AddOptions {
	duration?: number;
	action?: NotificationAction;
}

function createNotificationStore() {
	const { subscribe, update } = writable<Notification[]>([]);

	let counter = 0;

	function add(type: NotificationType, message: string, opts: AddOptions = {}) {
		const id = `notification-${++counter}`;
		const notification: Notification = {
			id,
			type,
			message,
			duration: opts.duration,
			action: opts.action,
		};

		update((n) => [...n, notification]);

		const dur = opts.duration ?? 5000;
		if (dur > 0) {
			setTimeout(() => remove(id), dur);
		}

		if (type === 'error' || type === 'warning') {
			notificationCenter.record({ type, message, action: opts.action, ts: Date.now() });
		}

		return id;
	}

	function remove(id: string) {
		update((n) => n.filter((notification) => notification.id !== id));
	}

	function clearAll() {
		update(() => []);
	}

	// Backwards-compatible API: callers may pass either a number (legacy
	// duration-only) or an options object {duration, action}.
	function shorthand(type: NotificationType, defaultDur: number) {
		return (message: string, opts?: number | AddOptions) => {
			if (typeof opts === 'number') {
				return add(type, message, { duration: opts });
			}
			return add(type, message, { duration: defaultDur, ...opts });
		};
	}

	return {
		subscribe,
		success: shorthand('success', 5000),
		error: shorthand('error', 10000),
		info: shorthand('info', 5000),
		warning: shorthand('warning', 8000),
		remove,
		clearAll,
	};
}

export const notifications = createNotificationStore();
