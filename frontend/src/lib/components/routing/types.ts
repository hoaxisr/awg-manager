export interface MatchedRule {
	id: string;
	name: string;
	type: 'dns' | 'ip';
	matches: string[];
	totalMatches: number;
	enabled: boolean;
	tunnelName: string;
	domainCount: number;
	sourceSummary: string;
}

export interface ResolveMatch {
	domain: string;
	ips: string[];
	rules: MatchedRule[];
}
