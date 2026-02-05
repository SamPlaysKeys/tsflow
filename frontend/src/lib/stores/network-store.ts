import { writable, derived, get } from 'svelte/store';
import type { Device, NetworkLog, NetworkNode, NetworkLink } from '$lib/types';
import { tailscaleService, type AggregatedFlow } from '$lib/services';
import { processNetworkLogs } from '$lib/utils/network-processor';
import { filterStore, timeRangeStore, TIME_RANGES } from './filter-store';
import { uiStore } from './ui-store';
import { dataSourceStore } from './data-source-store';

// Raw data stores
export const devices = writable<Device[]>([]);
export const networkLogs = writable<NetworkLog[]>([]); // Used for graph (aggregated in historical mode)
export const rawLogs = writable<NetworkLog[]>([]); // Used for LogViewer (always full detail with ports)
export const services = writable<Record<string, { name: string; addrs: string[]; tags?: string[] }>>({});
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

// Helper to check if a node matches the search query
function nodeMatchesSearch(node: NetworkNode, query: string): boolean {
	if (!query) return true;

	const q = query.toLowerCase().trim();

	if (q.startsWith('tag:')) {
		const tagSearch = q.substring(4);
		const nodeTagsLower = node.tags.map((t) => t.toLowerCase().replace('tag:', ''));
		return nodeTagsLower.some((tag) => tag.includes(tagSearch));
	} else if (q.startsWith('ip:')) {
		const ipSearch = q.substring(3);
		return node.ips.some((ip) => ip.toLowerCase().includes(ipSearch));
	} else if (q.includes('@')) {
		return node.user?.toLowerCase().includes(q.replace('user@', '')) || false;
	} else {
		const matchesIP = node.ips.some((ip) => ip.toLowerCase().includes(q));
		const matchesName = node.displayName.toLowerCase().includes(q);
		const matchesUser = node.user?.toLowerCase().includes(q) || false;
		const matchesTags = node.tags.some((tag) =>
			tag.toLowerCase().replace('tag:', '').includes(q)
		);
		return matchesIP || matchesName || matchesUser || matchesTags;
	}
}

// Primary matched nodes (nodes directly matching the search query)
export const primaryMatchedNodes = derived(
	[processedNetwork, filterStore, nodesWithTrafficConnections],
	([$network, $filters, $connectedNodeIds]) => {
		return $network.nodes.filter((node) => {
			if (!$connectedNodeIds.has(node.id)) return false;
			return nodeMatchesSearch(node, $filters.search);
		});
	}
);

// Get IDs of nodes connected to primary matches
const connectedToMatchedNodes = derived(
	[primaryMatchedNodes, trafficFilteredEdges],
	([$primaryNodes, $edges]) => {
		const primaryIds = new Set($primaryNodes.map((n) => n.id));
		const connectedIds = new Set<string>();

		// If no search filter, no need to expand - primaryIds contains all valid nodes
		if (primaryIds.size === 0) return connectedIds;

		// Find all nodes connected to primary matches
		$edges.forEach((edge) => {
			if (primaryIds.has(edge.source)) {
				connectedIds.add(edge.target);
			}
			if (primaryIds.has(edge.target)) {
				connectedIds.add(edge.source);
			}
		});

		return connectedIds;
	}
);

// Filtered nodes: primary matches + their connected nodes
export const filteredNodes = derived(
	[processedNetwork, primaryMatchedNodes, connectedToMatchedNodes, nodesWithTrafficConnections, filterStore],
	([$network, $primaryNodes, $connectedIds, $connectedNodeIds, $filters]) => {
		const primaryIds = new Set($primaryNodes.map((n) => n.id));

		// If no search, return all nodes with connections
		if (!$filters.search) {
			return $network.nodes.filter((node) => $connectedNodeIds.has(node.id));
		}

		// Return primary matches + connected nodes
		return $network.nodes.filter((node) => {
			if (!$connectedNodeIds.has(node.id)) return false;
			return primaryIds.has(node.id) || $connectedIds.has(node.id);
		});
	}
);

// Filtered edges - show edges where at least one endpoint is a primary match
export const filteredEdges = derived(
	[trafficFilteredEdges, filteredNodes, primaryMatchedNodes, filterStore],
	([$trafficEdges, $nodes, $primaryNodes, $filters]) => {
		const nodeIds = new Set($nodes.map((n) => n.id));
		const primaryIds = new Set($primaryNodes.map((n) => n.id));

		return $trafficEdges.filter((link) => {
			// Both nodes must be visible
			if (!nodeIds.has(link.source) || !nodeIds.has(link.target)) return false;

			// If searching, at least one endpoint must be a primary match
			if ($filters.search) {
				return primaryIds.has(link.source) || primaryIds.has(link.target);
			}

			return true;
		});
	}
);

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
				const preset = TIME_RANGES.find((p) => p.value === timeRange.selected);

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
		let graphLogs;
		let viewerLogs;
		if (dataSource.mode === 'historical') {
			// Fetch aggregated flows for graph (fast, node-level)
			const aggregatedData = await tailscaleService.getAggregatedFlows(start, end);
			graphLogs = convertAggregatedFlowsToNetworkLogs(aggregatedData.flows || [], start, end);
			// Fetch raw logs for LogViewer (full detail with ports)
			const storedLogs = await tailscaleService.getStoredFlowLogs(start, end);
			viewerLogs = convertStoredLogsToNetworkLogs(storedLogs.logs || []);
		} else {
			// Fetch live from Tailscale API - same data for both
			const logsData = await tailscaleService.getNetworkLogs(start, end);
			graphLogs = logsData.logs || [];
			viewerLogs = graphLogs;
		}

		devices.set(devicesData);
		networkLogs.set(graphLogs); // For graph visualization
		rawLogs.set(viewerLogs); // For LogViewer with full detail
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
		const group = grouped.get(key);
		if (group) {
			group.push(log);
		}
	}

	// Convert to NetworkLog format
	const networkLogs: NetworkLog[] = [];

	// Helper to format IP:port correctly for IPv4 and IPv6
	const formatAddress = (ip: string, port: number): string => {
		if (ip.includes(':')) {
			// IPv6: use [ip]:port format
			return `[${ip}]:${port}`;
		}
		// IPv4: use ip:port format
		return `${ip}:${port}`;
	};

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
				src: formatAddress(log.srcIp, log.srcPort),
				dst: formatAddress(log.dstIp, log.dstPort),
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
				default:
					// Unknown traffic type - default to virtual to avoid data loss
					console.warn(`Unknown traffic type "${log.trafficType}" in stored log, treating as virtual`);
					networkLog.virtualTraffic.push(traffic);
					break;
			}
		}

		networkLogs.push(networkLog);
	}

	return networkLogs;
}

// Convert pre-aggregated node-pair flows to NetworkLog format for the graph
// The backend now returns srcNodeId/dstNodeId (device IDs or IPs for external nodes)
function convertAggregatedFlowsToNetworkLogs(flows: AggregatedFlow[], rangeStart: Date, rangeEnd: Date): NetworkLog[] {
	// Group by srcNodeId to create NetworkLog entries
	const grouped = new Map<string, AggregatedFlow[]>();

	for (const flow of flows) {
		// Use srcNodeId as the primary grouping key
		const key = flow.srcNodeId;
		if (!grouped.has(key)) {
			grouped.set(key, []);
		}
		const group = grouped.get(key);
		if (group) {
			group.push(flow);
		}
	}

	const networkLogs: NetworkLog[] = [];
	const startISO = rangeStart.toISOString();
	const endISO = rangeEnd.toISOString();

	for (const [nodeId, nodeFlows] of grouped) {
		if (nodeFlows.length === 0) continue;

		const networkLog: NetworkLog = {
			logged: endISO,
			nodeId: nodeId,
			start: startISO,
			end: endISO,
			virtualTraffic: [],
			subnetTraffic: [],
			physicalTraffic: []
		};

		for (const flow of nodeFlows) {
			// The src/dst are now node IDs (device IDs or IPs)
			// Format them as addresses for backwards compatibility with graph processing
			const traffic = {
				proto: flow.protocol || 0,
				src: flow.srcNodeId,
				dst: flow.dstNodeId,
				txBytes: flow.totalTxBytes,
				rxBytes: flow.totalRxBytes,
				txPkts: flow.totalTxPkts || 0,
				rxPkts: flow.totalRxPkts || 0
			};

			switch (flow.trafficType) {
				case 'virtual':
					networkLog.virtualTraffic.push(traffic);
					break;
				case 'subnet':
					networkLog.subnetTraffic.push(traffic);
					break;
				case 'physical':
					networkLog.physicalTraffic.push(traffic);
					break;
				default:
					// Unknown traffic type - default to virtual to avoid data loss
					console.warn(`Unknown traffic type "${flow.trafficType}" for flow ${flow.srcNodeId} -> ${flow.dstNodeId}, treating as virtual`);
					networkLog.virtualTraffic.push(traffic);
					break;
			}
		}

		networkLogs.push(networkLog);
	}

	return networkLogs;
}

// Refresh data periodically
let refreshInterval: ReturnType<typeof setInterval> | null = null;

// Clean up on page unload to prevent memory leaks
if (typeof window !== 'undefined') {
	window.addEventListener('beforeunload', () => {
		stopAutoRefresh();
	});
}

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
