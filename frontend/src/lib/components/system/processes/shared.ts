export type SortField = 'cpu' | 'mem' | 'pid' | 'name' | 'user' | 'threads' | 'state';

export type CpuLevel = 'low' | 'med' | 'high';

export function getCpuClass(pct: number): CpuLevel {
	if (pct >= 80) return 'high';
	if (pct >= 45) return 'med';
	return 'low';
}
