<script lang="ts">
	import { uiStore, filteredNodes, rawLogs } from '$lib/stores';
	import { formatBytes, extractIP, getProtocolName } from '$lib/utils';
	import type { NetworkLog } from '$lib/types';

	// Well-known port names
	const PORT_NAMES: Record<number, string> = {
		20: 'FTP-DATA',
		21: 'FTP',
		22: 'SSH',
		23: 'Telnet',
		25: 'SMTP',
		53: 'DNS',
		80: 'HTTP',
		110: 'POP3',
		143: 'IMAP',
		443: 'HTTPS',
		465: 'SMTPS',
		587: 'SMTP',
		993: 'IMAPS',
		995: 'POP3S',
		3306: 'MySQL',
		5432: 'PostgreSQL',
		6379: 'Redis',
		8080: 'HTTP-Alt',
		8443: 'HTTPS-Alt',
		9090: 'Prometheus',
		27017: 'MongoDB'
	};

	function getPortName(port: number): string {
		return PORT_NAMES[port] || (port > 0 ? `Port ${port}` : 'Unknown');
	}

	// Get selected node info
	const selectedNode = $derived.by(() => {
		const selectedId = $uiStore.selectedNodeId;
		if (!selectedId) return null;
		return $filteredNodes.find((n) => n.id === selectedId) || null;
	});

	// Aggregate port usage from logs
	interface PortStat {
		port: number;
		name: string;
		protocol: string;
		txBytes: number;
		rxBytes: number;
		connections: number;
	}

	// Store the full port count separately from the display-limited list
	const portData = $derived.by(() => {
		if (!selectedNode) return { stats: [], totalCount: 0 };

		const nodeIPs = new Set(selectedNode.ips);
		const portMap = new Map<string, PortStat>();

		$rawLogs.forEach((log: NetworkLog) => {
			const allTraffic = [
				...(log.virtualTraffic || []),
				...(log.subnetTraffic || [])
			];

			allTraffic.forEach((t) => {
				const srcIP = extractIP(t.src);
				const dstIP = extractIP(t.dst);
				const dstPort = parseInt(t.dst.split(':')[1]) || 0;
				const proto = getProtocolName(t.proto || 0);

				// Check if this node is involved
				if (nodeIPs.has(srcIP) || nodeIPs.has(dstIP)) {
					// Always use destination port - it represents the service being accessed
					// Source ports are typically ephemeral and not meaningful
					if (dstPort === 0) return;

					const key = `${dstPort}-${proto}`;
					const existing = portMap.get(key);

					if (existing) {
						existing.txBytes += t.txBytes || 0;
						existing.rxBytes += t.rxBytes || 0;
						existing.connections += 1;
					} else {
						portMap.set(key, {
							port: dstPort,
							name: getPortName(dstPort),
							protocol: proto,
							txBytes: t.txBytes || 0,
							rxBytes: t.rxBytes || 0,
							connections: 1
						});
					}
				}
			});
		});

		const allPorts = Array.from(portMap.values())
			.sort((a, b) => (b.txBytes + b.rxBytes) - (a.txBytes + a.rxBytes));

		return {
			stats: allPorts.slice(0, 20), // Top 20 ports for display
			totalCount: allPorts.length    // True total count
		};
	});

	const portStats = $derived(portData.stats);
	const totalPortCount = $derived(portData.totalCount);

	const totalTraffic = $derived.by(() => {
		const tx = portStats.reduce((sum, p) => sum + p.txBytes, 0);
		const rx = portStats.reduce((sum, p) => sum + p.rxBytes, 0);
		return { tx, rx, total: tx + rx };
	});
</script>

{#if selectedNode && portStats.length > 0}
	<div class="border-t border-border bg-card">
		<div class="border-b border-border p-3">
			<h2 class="text-sm font-semibold">Port Usage</h2>
			<p class="text-xs text-muted-foreground">
				{selectedNode.displayName} - {totalPortCount} active ports{totalPortCount > 20 ? ' (showing top 20)' : ''}
				<span class="ml-2">
					TX: {formatBytes(totalTraffic.tx)} | RX: {formatBytes(totalTraffic.rx)}
				</span>
			</p>
		</div>
		<div class="max-h-48 overflow-auto">
			<table class="w-full text-xs">
				<thead class="sticky top-0 bg-card">
					<tr class="border-b border-border text-left">
						<th class="px-3 py-1.5 font-medium">Port</th>
						<th class="px-3 py-1.5 font-medium">Service</th>
						<th class="px-3 py-1.5 font-medium">Proto</th>
						<th class="px-3 py-1.5 font-medium text-right">Connections</th>
						<th class="px-3 py-1.5 font-medium text-right">TX</th>
						<th class="px-3 py-1.5 font-medium text-right">RX</th>
						<th class="px-3 py-1.5 font-medium text-right">Total</th>
					</tr>
				</thead>
				<tbody>
					{#each portStats as stat}
						<tr class="border-b border-border/50 hover:bg-secondary/50">
							<td class="px-3 py-1 font-mono text-primary">{stat.port}</td>
							<td class="px-3 py-1">{stat.name}</td>
							<td class="px-3 py-1 uppercase text-muted-foreground">{stat.protocol}</td>
							<td class="px-3 py-1 text-right">{stat.connections}</td>
							<td class="px-3 py-1 text-right text-blue-400">{formatBytes(stat.txBytes)}</td>
							<td class="px-3 py-1 text-right text-emerald-400">{formatBytes(stat.rxBytes)}</td>
							<td class="px-3 py-1 text-right font-medium">{formatBytes(stat.txBytes + stat.rxBytes)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</div>
{/if}
