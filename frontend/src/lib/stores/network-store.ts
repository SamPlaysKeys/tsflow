import { writable, derived, get } from 'svelte/store';
import type { Device, NetworkLog, NetworkNode, NetworkLink } from '$lib/types';
import { tailscaleService } from '$lib/services';
import { processNetworkLogs } from '$lib/utils/network-processor';
import { filterStore, timeRangeStore } from './filter-store';
import { uiStore } from './ui-store';
import { dataSourceStore } from './data-source-store';

// Raw data stores
export const devices = writable<Device[]>([]);
export const networkLogs = writable<NetworkLog[]>([]);
export const services = writable<Record<string, { name: string; addrs: string[] }>>({});
export const records = writable<Record<string, { addrs: string[]; comment?: string }>>({});

// Processed network data
export const processedNetwork = derived(
	[networkLogs, devices, services, records],
	([$logs, $devices, $services, $records]) => {
		return processNetworkLogs($logs, $devices, $services, $records);
	}
);

// First, filter edges by traffic type to determine which nodes should be visible
const trafficFilteredEdges = derived([processedNetwork, filterStore], ([$network, $filters]) => {
	return $network.links.filter((link) => {
		// Traffic type filter - if types are selected, only show those types
		if ($filters.trafficTypes.length > 0) {
			if (!$filters.trafficTypes.includes(link.trafficType)) return false;
		}
		return true;
	});
});

// Get node IDs that have at least one connection after traffic type filtering
const nodesWithTrafficConnections = derived(trafficFilteredEdges, ($edges) => {
	const nodeIds = new Set<string>();
	$edges.forEach((edge) => {
		nodeIds.add(edge.source);
		nodeIds.add(edge.target);
	});
	return nodeIds;
});

// Filtered nodes based on filter settings - only show nodes with traffic connections
export const filteredNodes = derived(
	[processedNetwork, filterStore, nodesWithTrafficConnections],
	([$network, $filters, $connectedNodeIds]) => {
		return $network.nodes.filter((node) => {
			// First check if node has any connections after traffic type filtering
			if (!$connectedNodeIds.has(node.id)) return false;

			// Search filter
			if ($filters.search) {
				const query = $filters.search.toLowerCase().trim();

				if (query.startsWith('tag:')) {
					const tagSearch = query.substring(4);
					const nodeTagsLower = node.tags.map((t) => t.toLowerCase().replace('tag:', ''));
					if (!nodeTagsLower.some((tag) => tag.includes(tagSearch))) return false;
				} else if (query.startsWith('ip:')) {
					const ipSearch = query.substring(3);
					if (!node.ips.some((ip) => ip.toLowerCase().includes(ipSearch))) return false;
				} else if (query.includes('@')) {
					if (!node.user?.toLowerCase().includes(query.replace('user@', ''))) return false;
				} else {
					const matchesIP = node.ips.some((ip) => ip.toLowerCase().includes(query));
					const matchesName = node.displayName.toLowerCase().includes(query);
					const matchesUser = node.user?.toLowerCase().includes(query) || false;
					const matchesTags = node.tags.some((tag) =>
						tag.toLowerCase().replace('tag:', '').includes(query)
					);
					if (!matchesIP && !matchesName && !matchesUser && !matchesTags) return false;
				}
			}

			return true;
		});
	}
);

// Filtered edges - show edges where both nodes are visible
export const filteredEdges = derived([trafficFilteredEdges, filteredNodes], ([$trafficEdges, $nodes]) => {
	const nodeIds = new Set($nodes.map((n) => n.id));

	return $trafficEdges.filter((link) => {
		// Only show edges where both nodes are visible after all filtering
		return nodeIds.has(link.source) && nodeIds.has(link.target);
	});
});

// Network stats
export const networkStats = derived([filteredNodes, filteredEdges], ([$nodes, $edges]) => {
	const totalBytes = $nodes.reduce((sum, n) => sum + n.totalBytes, 0);
	const totalConnections = $edges.length;
	const tailscaleNodes = $nodes.filter((n) => n.isTailscale).length;
	const externalNodes = $nodes.length - tailscaleNodes;

	return {
		totalNodes: $nodes.length,
		totalConnections,
		totalBytes,
		tailscaleNodes,
		externalNodes
	};
});

// Combined store for export
export const networkStore = {
	subscribe: derived(
		[devices, networkLogs, processedNetwork, filteredNodes, filteredEdges, networkStats],
		([$devices, $logs, $processed, $filteredNodes, $filteredEdges, $stats]) => ({
			devices: $devices,
			logs: $logs,
			nodes: $processed.nodes,
			links: $processed.links,
			filteredNodes: $filteredNodes,
			filteredEdges: $filteredEdges,
			stats: $stats
		})
	).subscribe
};

// Load network data
export async function loadNetworkData() {
	uiStore.setLoading(true);
	uiStore.setError(null);

	try {
		// Check data source mode
		const dataSource = get(dataSourceStore);
		let start: Date, end: Date;

		if (dataSource.mode === 'historical' && dataSource.selectedStart && dataSource.selectedEnd) {
			// Use the selected time range for historical mode
			start = dataSource.selectedStart;
			end = dataSource.selectedEnd;
		} else {
			// Live mode - use time range store
			const timeRange = get(timeRangeStore);

			if (timeRange.selected === 'custom' && timeRange.customStart && timeRange.customEnd) {
				start = timeRange.customStart;
				end = timeRange.customEnd;
			} else {
				const preset = [
					{ value: '1m', minutes: 1 },
					{ value: '5m', minutes: 5 },
					{ value: '15m', minutes: 15 },
					{ value: '30m', minutes: 30 },
					{ value: '1h', minutes: 60 },
					{ value: '6h', minutes: 360 },
					{ value: '24h', minutes: 1440 },
					{ value: '7d', minutes: 10080 },
					{ value: '30d', minutes: 43200 }
				].find((p) => p.value === timeRange.selected);

				end = new Date();
				start = new Date(end.getTime() - (preset?.minutes || 5) * 60 * 1000);
			}
		}

		// Fetch devices and services (always from live API)
		const [devicesData, servicesData] = await Promise.all([
			tailscaleService.getDevices(),
			tailscaleService.getServicesRecords()
		]);

		// Fetch logs based on mode
		let logs;
		if (dataSource.mode === 'historical') {
			// Fetch from stored database
			const storedLogs = await tailscaleService.getStoredFlowLogs(start, end);
			// Convert stored logs to NetworkLog format
			logs = convertStoredLogsToNetworkLogs(storedLogs.logs || []);
		} else {
			// Fetch live from Tailscale API
			const logsData = await tailscaleService.getNetworkLogs(start, end);
			logs = logsData.logs || [];
		}

		devices.set(devicesData);
		networkLogs.set(logs);
		services.set(servicesData.services || {});
		records.set(servicesData.records || {});
	} catch (err) {
		console.error('Failed to load network data:', err);
		uiStore.setError(err instanceof Error ? err.message : 'Failed to load network data');
	} finally {
		uiStore.setLoading(false);
	}
}

// Convert stored flow logs to NetworkLog format for processing
function convertStoredLogsToNetworkLogs(storedLogs: any[]): NetworkLog[] {
	// Group logs by nodeId and time period
	const grouped = new Map<string, any[]>();

	for (const log of storedLogs) {
		const key = `${log.nodeId}-${log.periodStart}-${log.periodEnd}`;
		if (!grouped.has(key)) {
			grouped.set(key, []);
		}
		grouped.get(key)!.push(log);
	}

	// Convert to NetworkLog format
	const networkLogs: NetworkLog[] = [];

	for (const [, logs] of grouped) {
		if (logs.length === 0) continue;

		const first = logs[0];
		const networkLog: NetworkLog = {
			logged: first.loggedAt,
			nodeId: first.nodeId,
			start: first.periodStart,
			end: first.periodEnd,
			virtualTraffic: [],
			subnetTraffic: [],
			physicalTraffic: []
		};

		for (const log of logs) {
			const traffic = {
				proto: log.protocol,
				src: `${log.srcIp}:${log.srcPort}`,
				dst: `${log.dstIp}:${log.dstPort}`,
				txBytes: log.txBytes,
				rxBytes: log.rxBytes,
				txPkts: log.txPkts,
				rxPkts: log.rxPkts
			};

			switch (log.trafficType) {
				case 'virtual':
					networkLog.virtualTraffic.push(traffic);
					break;
				case 'subnet':
					networkLog.subnetTraffic.push(traffic);
					break;
				case 'physical':
					networkLog.physicalTraffic.push(traffic);
					break;
			}
		}

		networkLogs.push(networkLog);
	}

	return networkLogs;
}

// Refresh data periodically
let refreshInterval: ReturnType<typeof setInterval> | null = null;

export function startAutoRefresh(intervalMs = 300000) {
	stopAutoRefresh();
	refreshInterval = setInterval(loadNetworkData, intervalMs);
}

export function stopAutoRefresh() {
	if (refreshInterval) {
		clearInterval(refreshInterval);
		refreshInterval = null;
	}
}
