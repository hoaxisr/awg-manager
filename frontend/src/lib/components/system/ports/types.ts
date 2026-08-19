import type { SystemPortBinding } from '$lib/api/client';

export type ProtoFilter = 'all' | 'tcp' | 'udp';

export interface PortAddress {
	proto: string;
	ip: string;
	port: number;
}

export interface GroupedProcessPort {
	key: string;
	port: number;
	pid?: number;
	processName?: string;
	exe?: string;
	cmdline?: string;
	user?: string;
	service?: string;
	isSelf?: boolean;
	isCritical?: boolean;
	protocols: string[];
	addresses: PortAddress[];
	bindings: SystemPortBinding[];
}

export interface PortInspectResult {
	searched: boolean;
	port: number;
	occupied: boolean;
	groups: GroupedProcessPort[];
	totalSockets: number;
}
