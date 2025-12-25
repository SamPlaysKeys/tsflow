import { writable, derived } from 'svelte/store';
import type { FilterState, TimeRange, Protocol, TrafficType } from '$lib/types';

// Time range presets
export const TIME_RANGES: TimeRange[] = [
	{ label: '1 minute', value: '1m', minutes: 1 },
	{ label: '5 minutes', value: '5m', minutes: 5 },
	{ label: '15 minutes', value: '15m', minutes: 15 },
	{ label: '30 minutes', value: '30m', minutes: 30 },
	{ label: '1 hour', value: '1h', minutes: 60 },
	{ label: '6 hours', value: '6h', minutes: 360 },
	{ label: '24 hours', value: '24h', minutes: 1440 },
	{ label: '7 days', value: '7d', minutes: 10080 },
	{ label: '30 days', value: '30d', minutes: 43200 },
	{ label: 'Custom', value: 'custom' }
];

// Default filter state
const defaultFilterState: FilterState = {
	search: '',
	protocols: [],
	trafficTypes: [],
	minBandwidth: 0,
	maxBandwidth: 1000000000, // 1GB
	minConnections: 0,
	showIpv4: true,
	showIpv6: true,
	selectedTags: []
};

// Create filter store
function createFilterStore() {
	const { subscribe, set, update } = writable<FilterState>(defaultFilterState);

	return {
		subscribe,
		set,
		update,
		setSearch: (search: string) => update((s) => ({ ...s, search })),
		setProtocols: (protocols: Protocol[]) => update((s) => ({ ...s, protocols })),
		toggleProtocol: (protocol: Protocol) =>
			update((s) => ({
				...s,
				protocols: s.protocols.includes(protocol)
					? s.protocols.filter((p) => p !== protocol)
					: [...s.protocols, protocol]
			})),
		setTrafficTypes: (trafficTypes: TrafficType[]) => update((s) => ({ ...s, trafficTypes })),
		toggleTrafficType: (type: TrafficType) =>
			update((s) => ({
				...s,
				trafficTypes: s.trafficTypes.includes(type)
					? s.trafficTypes.filter((t) => t !== type)
					: [...s.trafficTypes, type]
			})),
		setBandwidthRange: (min: number, max: number) =>
			update((s) => ({ ...s, minBandwidth: min, maxBandwidth: max })),
		setMinConnections: (min: number) => update((s) => ({ ...s, minConnections: min })),
		toggleIpv4: () => update((s) => ({ ...s, showIpv4: !s.showIpv4 })),
		toggleIpv6: () => update((s) => ({ ...s, showIpv6: !s.showIpv6 })),
		setSelectedTags: (tags: string[]) => update((s) => ({ ...s, selectedTags: tags })),
		reset: () => set(defaultFilterState)
	};
}

export const filterStore = createFilterStore();

// Time range store
function createTimeRangeStore() {
	const { subscribe, set, update } = writable({
		selected: '5m' as string,
		customStart: null as Date | null,
		customEnd: null as Date | null
	});

	return {
		subscribe,
		setPreset: (value: string) =>
			update((s) => ({
				...s,
				selected: value,
				customStart: null,
				customEnd: null
			})),
		setCustomRange: (start: Date, end: Date) =>
			update((s) => ({
				...s,
				selected: 'custom',
				customStart: start,
				customEnd: end
			})),
		getDateRange: derived({ subscribe }, ($store) => {
			if ($store.selected === 'custom' && $store.customStart && $store.customEnd) {
				return { start: $store.customStart, end: $store.customEnd };
			}

			const preset = TIME_RANGES.find((r) => r.value === $store.selected);
			if (preset?.minutes) {
				const end = new Date();
				const start = new Date(end.getTime() - preset.minutes * 60 * 1000);
				return { start, end };
			}

			// Default to 5 minutes
			const end = new Date();
			const start = new Date(end.getTime() - 5 * 60 * 1000);
			return { start, end };
		})
	};
}

export const timeRangeStore = createTimeRangeStore();
