// frontend/src/lib/types/singboxConnections.ts

export interface ConnectionMetadata {
	network: 'tcp' | 'udp';
	type: string;
	sourceIP: string;
	sourcePort: string;
	destinationIP: string;
	destinationPort: string;
	host: string;
	processPath?: string;
}

export interface Connection {
	id: string;
	metadata: ConnectionMetadata;
	upload: number;
	download: number;
	start: string; // RFC3339
	chains: string[];
	rule: string;
	rulePayload: string;

	// Frontend-enriched, NOT from Clash payload:
	clientName?: string;
	outboundLabel: string;
}

export interface ConnectionsSnapshot {
	connections: Connection[];
	downloadTotal: number;
	uploadTotal: number;
	connectionsTotal: number;
}

export type NetworkFilter = 'all' | 'tcp' | 'udp';

export interface ConnectionFilters {
	search: string;
	outbound: string; // '' = all
	network: NetworkFilter;
	rule: string; // '' = all
}

export interface ConnectionBucket {
	key: string;
	label?: string;
	upload: number;
	download: number;
	count: number;
	pct: number; // 0..100
}

// Raw shape coming over the WebSocket from Clash (only the fields we use).
export interface ClashConnectionsRaw {
	downloadTotal?: number;
	uploadTotal?: number;
	connections?: Array<{
		id: string;
		metadata: ConnectionMetadata;
		upload: number;
		download: number;
		start: string;
		chains: string[];
		rule: string;
		rulePayload: string;
	}>;
}
