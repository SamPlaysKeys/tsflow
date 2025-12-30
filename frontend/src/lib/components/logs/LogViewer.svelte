<script lang="ts">
	import { rawLogs, uiStore, filteredNodes, filteredEdges, filterStore, devices, services, primaryMatchedNodes } from '$lib/stores';
	import { formatBytes, formatDate, extractIP, getProtocolName } from '$lib/utils';
	import { ArrowRight } from 'lucide-svelte';
	import type { NetworkLog } from '$lib/types';

	// Build IP to device name lookup map
	const ipToDevice = $derived.by(() => {
		const map = new Map<string, string>();
		for (const device of $devices) {
			const displayName = device.hostname || device.name.split('.')[0];
			for (const addr of device.addresses) {
				map.set(addr, displayName);
			}
		}
		return map;
	});

	// Build device ID to device name lookup map (for aggregated flows)
	const deviceIdToName = $derived.by(() => {
		const map = new Map<string, string>();
		for (const device of $devices) {
			const displayName = device.hostname || device.name.split('.')[0];
			map.set(device.id, displayName);
		}
		return map;
	});

	// Build service IP to service name lookup map
	const ipToService = $derived.by(() => {
		const map = new Map<string, string>();
		for (const [, service] of Object.entries($services)) {
			const displayName = service.name.replace(/^svc:/, '');
			for (const addr of service.addrs || []) {
				map.set(addr, displayName);
			}
		}
		return map;
	});

	// Helper to resolve IP or device ID to device/service name
	function resolveIP(address: string): { ip: string; port?: string; deviceName?: string } {
		const ip = extractIP(address);
		// Extract port if present
		const portMatch = address.match(/:(\d+)$/);
		const port = portMatch ? portMatch[1] : undefined;
		// First try device IP lookup
		let deviceName = ipToDevice.get(ip);
		// If no match, try device ID lookup (for aggregated flows)
		if (!deviceName) {
			deviceName = deviceIdToName.get(ip);
		}
		// If still no match, try service lookup
		if (!deviceName) {
			deviceName = ipToService.get(ip);
		}
		return { ip, port, deviceName };
	}

	// Flatten traffic entries for display
	interface FlatTrafficEntry {
		logged: string;
		src: string;
		dst: string;
		protocol: string;
		txBytes: number;
		rxBytes: number;
		trafficType: string;
	}

	// Build a set of IPs from primary matched nodes for log filtering
	const primaryMatchedIPs = $derived.by(() => {
		const ips = new Set<string>();
		for (const node of $primaryMatchedNodes) {
			for (const ip of node.ips) {
				ips.add(ip);
			}
		}
		return ips;
	});

	// Filter logs based on selection, search, and traffic type filters
	const filteredLogs = $derived.by(() => {
		const selectedNodeId = $uiStore.selectedNodeId;
		const selectedEdgeId = $uiStore.selectedEdgeId;
		const searchQuery = $filterStore.search;
		const logs = $rawLogs;

		// No filters active - show recent logs
		if (!selectedNodeId && !selectedEdgeId && !searchQuery) {
			return logs.slice(0, 100);
		}

		return logs.filter((log) => {
			const allTraffic = [
				...(log.virtualTraffic || []),
				...(log.subnetTraffic || []),
				...(log.physicalTraffic || [])
			];

			return allTraffic.some((traffic) => {
				const srcIP = extractIP(traffic.src);
				const dstIP = extractIP(traffic.dst);

				// If a specific node is selected, filter to that node
				if (selectedNodeId) {
					const selectedNode = $filteredNodes.find((n) => n.id === selectedNodeId);
					if (selectedNode) {
						const nodeIPs = selectedNode.ips;
						return nodeIPs.some((ip: string) => ip === srcIP || ip === dstIP);
					}
				}

				// If a specific edge is selected, filter to that edge
				if (selectedEdgeId) {
					const selectedEdge = $filteredEdges.find((e) => e.id === selectedEdgeId);
					if (selectedEdge) {
						return (
							(srcIP === selectedEdge.originalSource && dstIP === selectedEdge.originalTarget) ||
							(srcIP === selectedEdge.originalTarget && dstIP === selectedEdge.originalSource)
						);
					}
				}

				// If searching, filter to logs involving primary matched nodes
				if (searchQuery && primaryMatchedIPs.size > 0) {
					return primaryMatchedIPs.has(srcIP) || primaryMatchedIPs.has(dstIP);
				}

				return false;
			});
		});
	});

	// Flatten logs into individual traffic entries, respecting traffic type filter AND node selection
	const flattenedEntries = $derived.by(() => {
		const entries: FlatTrafficEntry[] = [];
		const activeTrafficTypes = $filterStore.trafficTypes;
		const selectedNodeId = $uiStore.selectedNodeId;
		const selectedEdgeId = $uiStore.selectedEdgeId;

		// Get selected node's IPs for filtering individual traffic entries
		let selectedNodeIPs: string[] = [];
		if (selectedNodeId) {
			const selectedNode = $filteredNodes.find((n) => n.id === selectedNodeId);
			if (selectedNode) {
				selectedNodeIPs = selectedNode.ips;
			}
		}

		// Get selected edge endpoints for filtering
		let selectedEdge: { source: string; target: string } | null = null;
		if (selectedEdgeId) {
			const edge = $filteredEdges.find((e) => e.id === selectedEdgeId);
			if (edge) {
				selectedEdge = { source: edge.originalSource, target: edge.originalTarget };
			}
		}

		filteredLogs.forEach((log: NetworkLog) => {
			const addEntries = (traffic: any[], type: string) => {
				// Skip if traffic type filter is active and this type is not included
				if (activeTrafficTypes.length > 0 && !activeTrafficTypes.includes(type as any)) {
					return;
				}

				traffic.forEach((t) => {
					const srcIP = extractIP(t.src);
					const dstIP = extractIP(t.dst);

					// If a node is selected, only include traffic entries that involve that node
					if (selectedNodeIPs.length > 0) {
						const matchesNode = selectedNodeIPs.some((ip) => ip === srcIP || ip === dstIP);
						if (!matchesNode) return;
					}

					// If an edge is selected, only include traffic entries for that edge
					if (selectedEdge) {
						const matchesEdge =
							(srcIP === selectedEdge.source && dstIP === selectedEdge.target) ||
							(srcIP === selectedEdge.target && dstIP === selectedEdge.source);
						if (!matchesEdge) return;
					}

					entries.push({
						logged: log.logged,
						src: t.src,
						dst: t.dst,
						protocol: getProtocolName(t.proto || 0),
						txBytes: t.txBytes || 0,
						rxBytes: t.rxBytes || 0,
						trafficType: type
					});
				});
			};

			addEntries(log.virtualTraffic || [], 'virtual');
			addEntries(log.subnetTraffic || [], 'subnet');
			addEntries(log.physicalTraffic || [], 'physical');
		});

		return entries.slice(0, 500);
	});

	function getTrafficTypeColor(type: string): string {
		switch (type) {
			case 'virtual':
				return 'text-traffic-virtual';
			case 'subnet':
				return 'text-traffic-subnet';
			case 'physical':
				return 'text-traffic-physical';
			default:
				return 'text-muted-foreground';
		}
	}

	function getTrafficTypeBgColor(type: string): string {
		switch (type) {
			case 'virtual':
				return 'bg-traffic-virtual/20';
			case 'subnet':
				return 'bg-traffic-subnet/20';
			case 'physical':
				return 'bg-traffic-physical/20';
			default:
				return 'bg-muted';
		}
	}

	function capitalizeFirst(str: string): string {
		return str.charAt(0).toUpperCase() + str.slice(1);
	}

	// Handle clicking on a log entry to select the corresponding edge
	function handleLogClick(entry: FlatTrafficEntry) {
		const srcIP = extractIP(entry.src);
		const dstIP = extractIP(entry.dst);

		// Find the edge that matches this src/dst IP pair
		const matchingEdge = $filteredEdges.find((edge) => {
			return (
				(edge.originalSource === srcIP && edge.originalTarget === dstIP) ||
				(edge.originalSource === dstIP && edge.originalTarget === srcIP)
			);
		});

		if (matchingEdge) {
			// Select the edge (this will deselect any selected node)
			uiStore.selectEdge(matchingEdge.id);
		}
	}
</script>

<div class="flex h-full flex-col">
	<div class="border-b border-border p-3">
		<h2 class="text-sm font-semibold">Network Logs</h2>
		<p class="text-xs text-muted-foreground">
			{#if $uiStore.selectedNodeId}
				Showing logs for selected node
			{:else if $uiStore.selectedEdgeId}
				Showing logs for selected connection
			{:else}
				Showing recent logs (select a node or edge to filter)
			{/if}
			{#if $filterStore.trafficTypes.length > 0}
				<span class="text-primary"> • Filtered: {$filterStore.trafficTypes.join(', ')}</span>
			{/if}
		</p>
	</div>

	<div class="flex-1 overflow-auto">
		<table class="w-full text-xs">
			<thead class="sticky top-0 bg-card">
				<tr class="border-b border-border text-left">
					<th class="px-2 py-1.5 font-medium">Time</th>
					<th class="px-2 py-1.5 font-medium">Type</th>
					<th class="px-2 py-1.5 font-medium">Flow</th>
					<th class="px-2 py-1.5 font-medium">Proto</th>
					<th class="px-2 py-1.5 font-medium text-right">TX</th>
					<th class="px-2 py-1.5 font-medium text-right">RX</th>
				</tr>
			</thead>
			<tbody>
				{#each flattenedEntries as entry}
					{@const srcResolved = resolveIP(entry.src)}
					{@const dstResolved = resolveIP(entry.dst)}
					<tr
						class="border-b border-border/50 hover:bg-secondary/50 cursor-pointer"
						onclick={() => handleLogClick(entry)}
						role="button"
						tabindex="0"
						onkeydown={(e) => e.key === 'Enter' && handleLogClick(entry)}
					>
						<td class="whitespace-nowrap px-2 py-1 text-muted-foreground">
							{formatDate(entry.logged).split(',')[1]?.trim() || formatDate(entry.logged)}
						</td>
						<td class="px-2 py-1">
							<span class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 {getTrafficTypeBgColor(entry.trafficType)} {getTrafficTypeColor(entry.trafficType)}">
								<span class="h-1.5 w-1.5 rounded-full bg-current"></span>
								{capitalizeFirst(entry.trafficType)}
							</span>
						</td>
						<td class="px-2 py-1">
							<div class="flex items-center gap-1">
								<span class="truncate" title={srcResolved.deviceName ? `${srcResolved.deviceName} (${srcResolved.ip}${srcResolved.port ? ':' + srcResolved.port : ''})` : entry.src}>
									{#if srcResolved.deviceName}
										<span class="text-primary">{srcResolved.deviceName}</span>{#if srcResolved.port}<span class="text-muted-foreground">:{srcResolved.port}</span>{/if}
									{:else}
										{entry.src}
									{/if}
								</span>
								<ArrowRight class="h-3 w-3 shrink-0 text-muted-foreground" />
								<span class="truncate" title={dstResolved.deviceName ? `${dstResolved.deviceName} (${dstResolved.ip}${dstResolved.port ? ':' + dstResolved.port : ''})` : entry.dst}>
									{#if dstResolved.deviceName}
										<span class="text-primary">{dstResolved.deviceName}</span>{#if dstResolved.port}<span class="text-muted-foreground">:{dstResolved.port}</span>{/if}
									{:else}
										{entry.dst}
									{/if}
								</span>
							</div>
						</td>
						<td class="px-2 py-1">
							<span class="font-mono">{entry.protocol}</span>
						</td>
						<td class="whitespace-nowrap px-2 py-1 text-right">{formatBytes(entry.txBytes)}</td>
						<td class="whitespace-nowrap px-2 py-1 text-right">{formatBytes(entry.rxBytes)}</td>
					</tr>
				{/each}
			</tbody>
		</table>

		{#if flattenedEntries.length === 0}
			<div class="flex h-32 items-center justify-center text-sm text-muted-foreground">
				No logs to display
			</div>
		{/if}
	</div>
</div>
