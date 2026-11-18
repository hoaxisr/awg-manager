import type { XrayClient } from './servers';

export interface XrayStatus {
	installed: boolean;
	running: boolean;
	configured?: boolean;
	backend?: string;
	binary_path?: string;
	unavailable_reason?: string;
	listen_address?: string;
	pid?: number;
	port?: number;
	version?: string;
	domain?: string;
	path?: string;
	uuid?: string;
	uplinkHTTPMethod?: string;
	cdnHost?: string;
	serverIP?: string;
	error?: string;
	clients_count?: number;
	clients?: XrayClient[];
	recovery_required?: boolean;
	recovery_reason?: string;
	recovery_fingerprint?: string;
	migration_status?: 'migrated' | 'deferred' | 'conflict' | string;
	active_generation?: 'new' | string;
}

export type VlessStatus = XrayStatus;

export interface XrayMigrationStatus {
	discovery?: {
		found: boolean;
		topology?: 'A' | 'B' | 'C';
		listen_address?: string;
		listen_port?: number;
		public_domain?: string;
		path?: string;
		clients?: XrayClient[];
		conflict_reason?: string;
	};
	decision?: {
		active_generation: 'new' | string;
		migration_status: 'migrated' | 'deferred' | 'conflict' | string;
		decided_at?: string;
		generation_counter?: number;
		reason?: string;
	};
}

export type VlessMigrationStatus = XrayMigrationStatus;
