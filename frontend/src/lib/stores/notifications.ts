import { writable } from 'svelte/store';

export type NotificationType = 'success' | 'error' | 'info' | 'warning';

export interface Notification {
	id: string;
	type: NotificationType;
	message: string;
	duration?: number;
}

function createNotificationStore() {
	const { subscribe, update } = writable<Notification[]>([]);

	let counter = 0;

	function add(type: NotificationType, message: string, duration = 5000) {
		const id = `notification-${++counter}`;
		const notification: Notification = { id, type, message, duration };

		update((n) => [...n, notification]);

		if (duration > 0) {
			setTimeout(() => remove(id), duration);
		}

		return id;
	}

	function remove(id: string) {
		update((n) => n.filter((notification) => notification.id !== id));
	}

	function clearAll() {
		update(() => []);
	}

	return {
		subscribe,
		success: (message: string, duration?: number) => add('success', message, duration ?? 5000),
		error: (message: string, duration?: number) => add('error', message, duration ?? 10000),
		info: (message: string, duration?: number) => add('info', message, duration ?? 5000),
		warning: (message: string, duration?: number) => add('warning', message, duration ?? 8000),
		remove,
		clearAll
	};
}

export const notifications = createNotificationStore();
