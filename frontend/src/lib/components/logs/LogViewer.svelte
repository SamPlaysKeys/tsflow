<script lang="ts">
	import { networkLogs, uiStore, filteredNodes, filteredEdges, filterStore, devices } from '$lib/stores';
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

	// Helper to resolve IP to device name or return the IP
	function resolveIP(address: string): { ip: string; deviceName?: string } {
		const ip = extractIP(address);
		const deviceName = ipToDevice.get(ip);
		return { ip, deviceName };
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

	// Filter logs based on selection and traffic type filters
	const filteredLogs = $derived.by(() => {
		const selectedNodeId = $uiStore.selectedNodeId;
		const selectedEdgeId = $uiStore.selectedEdgeId;
		const logs = $networkLogs;

		if (!selectedNodeId && !selectedEdgeId) {
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

				if (selectedNodeId) {
					const selectedNode = $filteredNodes.find((n) => n.id === selectedNodeId);
					if (selectedNode) {
						const nodeIPs = selectedNode.ips;
						return nodeIPs.some((ip: string) => ip === srcIP || ip === dstIP);
					}
				}

				if (selectedEdgeId) {
					const selectedEdge = $filteredEdges.find((e) => e.id === selectedEdgeId);
					if (selectedEdge) {
						return (
							(srcIP === selectedEdge.originalSource && dstIP === selectedEdge.originalTarget) ||
							(srcIP === selectedEdge.originalTarget && dstIP === selectedEdge.originalSource)
						);
					}
				}

				return false;
			});
		});
	});

	// Flatten logs into individual traffic entries, respecting traffic type filter
	const flattenedEntries = $derived.by(() => {
		const entries: FlatTrafficEntry[] = [];
		const activeTrafficTypes = $filterStore.trafficTypes;

		filteredLogs.forEach((log: NetworkLog) => {
			const addEntries = (traffic: any[], type: string) => {
				// Skip if traffic type filter is active and this type is not included
				if (activeTrafficTypes.length > 0 && !activeTrafficTypes.includes(type as any)) {
					return;
				}

				traffic.forEach((t) => {
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
					<tr class="border-b border-border/50 hover:bg-secondary/50">
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
								<span class="truncate" title={srcResolved.deviceName ? `${srcResolved.deviceName} (${srcResolved.ip})` : srcResolved.ip}>
									{#if srcResolved.deviceName}
										<span class="text-primary">{srcResolved.deviceName}</span>
									{:else}
										{srcResolved.ip}
									{/if}
								</span>
								<ArrowRight class="h-3 w-3 shrink-0 text-muted-foreground" />
								<span class="truncate" title={dstResolved.deviceName ? `${dstResolved.deviceName} (${dstResolved.ip})` : dstResolved.ip}>
									{#if dstResolved.deviceName}
										<span class="text-primary">{dstResolved.deviceName}</span>
									{:else}
										{dstResolved.ip}
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
