import { writable, derived, get } from 'svelte/store';
import { tailscaleService, type DataRange, type PollerStatus } from '$lib/services';

export type DataSourceMode = 'live' | 'historical';

interface DataSourceState {
	mode: DataSourceMode;
	dataRange: DataRange | null;
	pollerStatus: PollerStatus | null;
	selectedStart: Date | null; // Start of selected range
	selectedEnd: Date | null; // End of selected range
	isLoading: boolean;
	error: string | null;
}

const defaultState: DataSourceState = {
	mode: 'live',
	dataRange: null,
	pollerStatus: null,
	selectedStart: null,
	selectedEnd: null,
	isLoading: false,
	error: null
};

function createDataSourceStore() {
	const { subscribe, set, update } = writable<DataSourceState>(defaultState);

	return {
		subscribe,

		setMode: (mode: DataSourceMode) =>
			update((s) => ({
				...s,
				mode,
				selectedStart: mode === 'historical' ? s.selectedStart : null,
				selectedEnd: mode === 'historical' ? s.selectedEnd : null
			})),

		setSelectedRange: (start: Date, end: Date) =>
			update((s) => ({ ...s, selectedStart: start, selectedEnd: end })),

		setSelectedStart: (start: Date) => update((s) => ({ ...s, selectedStart: start })),

		setSelectedEnd: (end: Date) => update((s) => ({ ...s, selectedEnd: end })),

		async fetchDataRange() {
			update((s) => ({ ...s, isLoading: true, error: null }));
			try {
				const dataRange = await tailscaleService.getDataRange();
				update((s) => ({ ...s, dataRange, isLoading: false }));
				return dataRange;
			} catch (err) {
				const error = err instanceof Error ? err.message : 'Failed to fetch data range';
				update((s) => ({ ...s, error, isLoading: false }));
				return null;
			}
		},

		async fetchPollerStatus() {
			try {
				const pollerStatus = await tailscaleService.getPollerStatus();
				update((s) => ({ ...s, pollerStatus }));
				return pollerStatus;
			} catch (err) {
				console.error('Failed to fetch poller status:', err);
				return null;
			}
		},

		reset: () => set(defaultState)
	};
}

export const dataSourceStore = createDataSourceStore();

// Derived store for whether historical data is available
export const hasHistoricalData = derived(dataSourceStore, ($store) => {
	return $store.dataRange !== null && $store.dataRange.count > 0;
});

// Derived store for the time window to query (based on mode and selected range)
export const queryTimeWindow = derived(dataSourceStore, ($store) => {
	if ($store.mode === 'historical' && $store.selectedStart && $store.selectedEnd) {
		return {
			start: $store.selectedStart,
			end: $store.selectedEnd
		};
	}
	// For live mode, use current time minus 1 hour (analytics uses this window)
	const now = new Date();
	return {
		start: new Date(now.getTime() - 60 * 60 * 1000),
		end: now
	};
});
