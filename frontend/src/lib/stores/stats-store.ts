import { writable, derived } from 'svelte/store';
import { tailscaleService } from '$lib/services/tailscale-service';
import { queryTimeWindow } from './data-source-store';
import type { TrafficStatsSummary, TrafficStatsBucket, TopTalker, TopPair, PortStat } from '$lib/types';

interface StatsState {
	summary: TrafficStatsSummary | null;
	buckets: TrafficStatsBucket[];
	topTalkers: TopTalker[];
	topPairs: TopPair[];
	isLoading: boolean;
	error: string | null;
}

const defaultState: StatsState = {
	summary: null,
	buckets: [],
	topTalkers: [],
	topPairs: [],
	isLoading: false,
	error: null
};

const statsState = writable<StatsState>(defaultState);

let refreshTimer: ReturnType<typeof setInterval> | null = null;

export async function loadStats() {
	statsState.update((s) => ({ ...s, isLoading: true, error: null }));

	try {
		let start: Date;
		let end: Date;

		const unsubscribe = queryTimeWindow.subscribe((tw) => {
			start = tw.start;
			end = tw.end;
		});
		unsubscribe();

		const [overviewRes, talkersRes, pairsRes] = await Promise.all([
			tailscaleService.getStatsOverview(start!, end!),
			tailscaleService.getTopTalkers(start!, end!, 15),
			tailscaleService.getTopPairs(start!, end!, 15)
		]);

		statsState.set({
			summary: overviewRes.summary,
			buckets: overviewRes.buckets || [],
			topTalkers: talkersRes.talkers || [],
			topPairs: pairsRes.pairs || [],
			isLoading: false,
			error: null
		});
	} catch (err) {
		statsState.update((s) => ({
			...s,
			isLoading: false,
			error: err instanceof Error ? err.message : 'Failed to load stats'
		}));
	}
}

export function startStatsRefresh(intervalMs = 60_000) {
	stopStatsRefresh();
	loadStats();
	refreshTimer = setInterval(loadStats, intervalMs);
}

export function stopStatsRefresh() {
	if (refreshTimer) {
		clearInterval(refreshTimer);
		refreshTimer = null;
	}
}

export const statsSummary = derived(statsState, ($s) => $s.summary);
export const statsBuckets = derived(statsState, ($s) => $s.buckets);
export const topTalkers = derived(statsState, ($s) => $s.topTalkers);
export const topPairs = derived(statsState, ($s) => $s.topPairs);
export const statsLoading = derived(statsState, ($s) => $s.isLoading);
export const statsError = derived(statsState, ($s) => $s.error);

export const topPorts = derived(statsState, ($s): PortStat[] => {
	if (!$s.buckets || $s.buckets.length === 0) return [];
	const portMap = new Map<string, PortStat>();
	for (const bucket of $s.buckets) {
		try {
			const ports: PortStat[] = JSON.parse(bucket.topPorts || '[]');
			for (const p of ports) {
				const key = `${p.proto}:${p.port}`;
				const existing = portMap.get(key);
				if (existing) {
					existing.bytes += p.bytes;
				} else {
					portMap.set(key, { ...p });
				}
			}
		} catch {
			// skip malformed JSON
		}
	}
	return Array.from(portMap.values())
		.sort((a, b) => b.bytes - a.bytes)
		.slice(0, 15);
});
