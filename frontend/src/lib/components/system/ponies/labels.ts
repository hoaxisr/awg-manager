import { m } from '$lib/i18n';
import type { PonyClass, PonyDestination, PonyOption } from './types';

/** Направления считаются при каждом вызове — подписи следуют за языком. */
export function ponyDestinations(): PonyDestination[] {
	return [
		{ id: 'ponyland', name: m.system_ponies_dest_ponyland_name(), desc: m.system_ponies_dest_ponyland_desc() },
		{ id: 'friday', name: m.system_ponies_dest_friday_name(), desc: m.system_ponies_dest_friday_desc() },
		{ id: 'marshmallow', name: m.system_ponies_dest_marshmallow_name(), desc: m.system_ponies_dest_marshmallow_desc() },
		{ id: 'nobugs', name: m.system_ponies_dest_nobugs_name(), desc: m.system_ponies_dest_nobugs_desc() },
	];
}

export function ponyClassLabel(serviceClass: PonyClass): string {
	switch (serviceClass) {
		case 'vip':
			return m.system_ponies_ticket_class_vip();
		case 'business':
			return m.system_ponies_ticket_class_business();
		case 'eco':
			return m.system_ponies_ticket_class_eco();
	}
}

export function ponyOptionLabel(option: PonyOption): string {
	switch (option) {
		case 'vpn':
			return m.system_ponies_ticket_opt_vpn();
		case 'marshmallow':
			return m.system_ponies_ticket_opt_marshmallow();
		case 'rainbow':
			return m.system_ponies_ticket_opt_rainbow();
	}
}
