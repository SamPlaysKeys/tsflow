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

// LocalStorage key for persisted filter preferences
const FILTER_STORAGE_KEY = 'tsflow-filter-prefs';

function loadPersistedFilters(): Partial<FilterState> {
	if (typeof window === 'undefined') return {};
	try {
		const stored = localStorage.getItem(FILTER_STORAGE_KEY);
		if (!stored) return {};
		const parsed = JSON.parse(stored);
		// Only restore preferences that make sense to persist
		return {
			trafficTypes: Array.isArray(parsed.trafficTypes) ? parsed.trafficTypes : [],
			showIpv4: parsed.showIpv4 ?? true,
			showIpv6: parsed.showIpv6 ?? true
		};
	} catch {
		return {};
	}
}

function persistFilters(state: FilterState) {
	if (typeof window === 'undefined') return;
	try {
		localStorage.setItem(FILTER_STORAGE_KEY, JSON.stringify({
			trafficTypes: state.trafficTypes,
			showIpv4: state.showIpv4,
			showIpv6: state.showIpv6
		}));
	} catch { /* quota exceeded or private browsing */ }
}

// Default filter state — virtual + subnet shown by default
const defaultFilterState: FilterState = {
	search: '',
	protocols: [],
	trafficTypes: ['virtual', 'subnet'],
	minBandwidth: 0,
	maxBandwidth: 1000000000, // 1GB
	minConnections: 0,
	showIpv4: true,
	showIpv6: true,
	selectedTags: [],
	...loadPersistedFilters()
};

// Debounce delay for search filtering (ms)
const SEARCH_DEBOUNCE_MS = 300;

// Debounced search value - used by derived stores for filtering
const debouncedSearchStore = writable('');
let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null;

// Create filter store with debounced search
function createFilterStore() {
	const { subscribe, set, update } = writable<FilterState>(defaultFilterState);

	return {
		subscribe,
		set,
		update,
		setSearch: (search: string) => {
			// Update immediate value for UI responsiveness
			update((s) => ({ ...s, search }));

			// Debounce the value used for expensive filtering operations
			if (searchDebounceTimer) clearTimeout(searchDebounceTimer);
			searchDebounceTimer = setTimeout(() => {
				debouncedSearchStore.set(search);
				searchDebounceTimer = null;
			}, SEARCH_DEBOUNCE_MS);
		},
		setProtocols: (protocols: Protocol[]) => update((s) => ({ ...s, protocols })),
		toggleProtocol: (protocol: Protocol) =>
			update((s) => ({
				...s,
				protocols: s.protocols.includes(protocol)
					? s.protocols.filter((p) => p !== protocol)
					: [...s.protocols, protocol]
			})),
		setTrafficTypes: (trafficTypes: TrafficType[]) => update((s) => {
			const next = { ...s, trafficTypes };
			persistFilters(next);
			return next;
		}),
		toggleTrafficType: (type: TrafficType) =>
			update((s) => {
				const next = {
					...s,
					trafficTypes: s.trafficTypes.includes(type)
						? s.trafficTypes.filter((t) => t !== type)
						: [...s.trafficTypes, type]
				};
				persistFilters(next);
				return next;
			}),
		setBandwidthRange: (min: number, max: number) =>
			update((s) => ({ ...s, minBandwidth: min, maxBandwidth: max })),
		setMinConnections: (min: number) => update((s) => ({ ...s, minConnections: min })),
		toggleIpv4: () => update((s) => {
			const next = { ...s, showIpv4: !s.showIpv4 };
			persistFilters(next);
			return next;
		}),
		toggleIpv6: () => update((s) => {
			const next = { ...s, showIpv6: !s.showIpv6 };
			persistFilters(next);
			return next;
		}),
		setSelectedTags: (tags: string[]) => update((s) => ({ ...s, selectedTags: tags })),
		reset: () => {
			set(defaultFilterState);
			// Also reset debounced search immediately
			if (searchDebounceTimer) {
				clearTimeout(searchDebounceTimer);
				searchDebounceTimer = null;
			}
			debouncedSearchStore.set('');
		},
		// Cleanup function to clear any pending timers (call on unmount)
		cleanup: () => {
			if (searchDebounceTimer) {
				clearTimeout(searchDebounceTimer);
				searchDebounceTimer = null;
			}
		}
	};
}

export const filterStore = createFilterStore();

// Derived store that combines filter state with debounced search
// Use this for expensive filtering operations (network-store, LogViewer)
export const debouncedFilterStore = derived(
	[filterStore, debouncedSearchStore],
	([$filters, $debouncedSearch]) => ({
		...$filters,
		search: $debouncedSearch
	})
);

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

			// Fallback to 5 minutes - log warning for debugging
			if ($store.selected !== 'custom') {
				console.warn(`Unknown time range preset: "${$store.selected}", falling back to 5 minutes`);
			}
			const end = new Date();
			const start = new Date(end.getTime() - 5 * 60 * 1000);
			return { start, end };
		})
	};
}

export const timeRangeStore = createTimeRangeStore();
