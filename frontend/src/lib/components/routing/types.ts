export type SourceSummary =
	| { kind: 'lists-manual'; lists: number; manual: number }
	| { kind: 'lists'; lists: number }
	| { kind: 'manual' }
	| { kind: 'subnets'; count: number };

export interface MatchedRule {
	id: string;
	name: string;
	type: 'dns' | 'ip';
	matches: string[];
	totalMatches: number;
	enabled: boolean;
	tunnelName: string;
	domainCount: number;
	sourceSummary: SourceSummary | null;
	iconUrl?: string;
}

export interface ResolveMatch {
	domain: string;
	ips: string[];
	rules: MatchedRule[];
}
