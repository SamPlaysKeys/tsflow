<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Activity, Network, Link, ArrowUpDown, Loader2 } from 'lucide-svelte';
	import Header from '$lib/components/layout/Header.svelte';
	import DonutChart from '$lib/components/charts/DonutChart.svelte';
	import BarChart from '$lib/components/charts/BarChart.svelte';
	import StatCard from '$lib/components/charts/StatCard.svelte';
	import {
		startStatsRefresh,
		stopStatsRefresh,
		statsSummary,
		topTalkers,
		topPairs,
		topPorts,
		statsLoading,
		statsError,
		queryTimeWindow
	} from '$lib/stores';
	import { formatBytes } from '$lib/utils';
	import { getPortLabel } from '$lib/utils/protocol';

	onMount(() => {
		startStatsRefresh(60_000);
	});

	onDestroy(() => {
		stopStatsRefresh();
	});

	// Sorting state for tables
	type TalkerField = 'totalBytes' | 'txBytes' | 'rxBytes';
	type PairField = 'totalBytes' | 'flowCount';
	let talkerSort: TalkerField = $state('totalBytes');
	let talkerSortDir: 'asc' | 'desc' = $state('desc');
	let pairSort: PairField = $state('totalBytes');
	let pairSortDir: 'asc' | 'desc' = $state('desc');

	function toggleTalkerSort(field: TalkerField) {
		if (talkerSort === field) {
			talkerSortDir = talkerSortDir === 'desc' ? 'asc' : 'desc';
		} else {
			talkerSort = field;
			talkerSortDir = 'desc';
		}
	}

	function togglePairSort(field: PairField) {
		if (pairSort === field) {
			pairSortDir = pairSortDir === 'desc' ? 'asc' : 'desc';
		} else {
			pairSort = field;
			pairSortDir = 'desc';
		}
	}

	function sortArrow(active: boolean, dir: 'asc' | 'desc'): string {
		if (!active) return '';
		return dir === 'desc' ? ' \u25BE' : ' \u25B4';
	}

	const sortedTalkers = $derived.by(() => {
		const list = [...$topTalkers];
		const mul = talkerSortDir === 'desc' ? -1 : 1;
		return list.sort((a, b) => mul * (a[talkerSort] - b[talkerSort]));
	});

	const sortedPairs = $derived.by(() => {
		const list = [...$topPairs];
		const mul = pairSortDir === 'desc' ? -1 : 1;
		return list.sort((a, b) => mul * (a[pairSort] - b[pairSort]));
	});

	const protoSegments = $derived.by(() => {
		const s = $statsSummary;
		if (!s) return [];
		return [
			{ label: 'TCP', value: s.tcpBytes, color: 'var(--color-primary)' },
			{ label: 'UDP', value: s.udpBytes, color: 'var(--color-traffic-subnet)' },
			{ label: 'Other', value: s.otherProtoBytes, color: 'var(--color-traffic-physical)' }
		];
	});

	const trafficTypeSegments = $derived.by(() => {
		const s = $statsSummary;
		if (!s) return [];
		return [
			{ label: 'Virtual', value: s.virtualBytes, color: 'var(--color-traffic-virtual)' },
			{ label: 'Subnet', value: s.subnetBytes, color: 'var(--color-traffic-subnet)' },
			{ label: 'Physical', value: s.physicalBytes, color: 'var(--color-traffic-physical)' }
		];
	});

	const portBars = $derived.by(() => {
		return $topPorts.map((p) => ({
			label: getPortLabel(p.port, p.proto),
			value: p.bytes,
			color:
				p.proto === 6
					? 'var(--color-primary)'
					: p.proto === 17
						? 'var(--color-traffic-subnet)'
						: 'var(--color-traffic-physical)'
		}));
	});

	const totalBytes = $derived(
		$statsSummary
			? $statsSummary.tcpBytes + $statsSummary.udpBytes + $statsSummary.otherProtoBytes
			: 0
	);

	const timeWindowLabel = $derived.by(() => {
		const tw = $queryTimeWindow;
		const diffMs = tw.end.getTime() - tw.start.getTime();
		const mins = Math.round(diffMs / 60_000);
		if (mins < 60) return `Last ${mins} min`;
		const hrs = Math.round(mins / 60);
		if (hrs < 24) return `Last ${hrs}h`;
		const days = Math.round(hrs / 24);
		return `Last ${days}d`;
	});

	function nodeLabel(id: string, displayName?: string): string {
		if (displayName) return displayName;
		if (/^\d{10,}$/.test(id)) return id.slice(0, 8) + '\u2026';
		return id;
	}
</script>

<div class="flex h-screen flex-col bg-background">
	<Header />

	<main class="flex-1 overflow-y-auto p-6">
		{#if $statsLoading && !$statsSummary}
			<div class="flex h-full items-center justify-center">
				<Loader2 class="h-8 w-8 animate-spin text-primary" />
			</div>
		{:else if $statsError && !$statsSummary}
			<div class="flex h-full items-center justify-center text-destructive">
				{$statsError}
			</div>
		{:else}
			<!-- Overview Cards -->
			<div class="mb-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
				<StatCard label="Total Traffic" value={formatBytes(totalBytes)} subtitle={timeWindowLabel}>
					{#snippet icon()}<Activity class="h-4 w-4" />{/snippet}
				</StatCard>
				<StatCard
					label="Total Flows"
					value={($statsSummary?.totalFlows ?? 0).toLocaleString()}
					subtitle={timeWindowLabel}
				>
					{#snippet icon()}<ArrowUpDown class="h-4 w-4" />{/snippet}
				</StatCard>
				<StatCard
					label="Unique Pairs"
					value={($statsSummary?.uniquePairs ?? 0).toLocaleString()}
					subtitle="Communicating device pairs"
				>
					{#snippet icon()}<Link class="h-4 w-4" />{/snippet}
				</StatCard>
				<StatCard
					label="Active Devices"
					value={$topTalkers.length.toString()}
					subtitle="With traffic in window"
				>
					{#snippet icon()}<Network class="h-4 w-4" />{/snippet}
				</StatCard>
			</div>

			<!-- Distribution Charts -->
			<div class="mb-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
				<div class="rounded-lg border border-border bg-card p-4">
					<h3 class="mb-3 text-sm font-medium text-muted-foreground">
						Protocol Distribution
					</h3>
					<DonutChart segments={protoSegments} />
				</div>
				<div class="rounded-lg border border-border bg-card p-4">
					<h3 class="mb-3 text-sm font-medium text-muted-foreground">
						Traffic Type Distribution
					</h3>
					<DonutChart segments={trafficTypeSegments} />
				</div>
			</div>

			<!-- Rankings -->
			<div class="mb-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
				<!-- Top Talkers -->
				<div class="rounded-lg border border-border bg-card p-4">
					<h3 class="mb-3 text-sm font-medium text-muted-foreground">Top Talkers</h3>
					<div class="overflow-x-auto">
						<table class="w-full text-sm">
							<thead>
								<tr class="border-b border-border text-left text-muted-foreground">
									<th class="pb-2 pr-4">#</th>
									<th class="pb-2 pr-4">Device</th>
									<th
										class="cursor-pointer select-none pb-2 pr-4 text-right transition-colors hover:text-foreground"
										onclick={() => toggleTalkerSort('txBytes')}
									>
										TX{sortArrow(talkerSort === 'txBytes', talkerSortDir)}
									</th>
									<th
										class="cursor-pointer select-none pb-2 pr-4 text-right transition-colors hover:text-foreground"
										onclick={() => toggleTalkerSort('rxBytes')}
									>
										RX{sortArrow(talkerSort === 'rxBytes', talkerSortDir)}
									</th>
									<th
										class="cursor-pointer select-none pb-2 text-right transition-colors hover:text-foreground"
										onclick={() => toggleTalkerSort('totalBytes')}
									>
										Total{sortArrow(talkerSort === 'totalBytes', talkerSortDir)}
									</th>
								</tr>
							</thead>
							<tbody>
								{#each sortedTalkers as talker, i}
									<tr class="border-b border-border/50 transition-colors hover:bg-secondary/50">
										<td class="py-1.5 pr-4 text-muted-foreground">{i + 1}</td>
										<td class="max-w-[180px] truncate py-1.5 pr-4" title={talker.nodeId}>
											{#if talker.displayName}
												<span class="font-medium">{talker.displayName}</span>
											{:else}
												<span class="font-mono text-xs text-muted-foreground">
													{nodeLabel(talker.nodeId)}
												</span>
											{/if}
										</td>
										<td class="py-1.5 pr-4 text-right tabular-nums"
											>{formatBytes(talker.txBytes)}</td
										>
										<td class="py-1.5 pr-4 text-right tabular-nums"
											>{formatBytes(talker.rxBytes)}</td
										>
										<td class="py-1.5 text-right font-medium tabular-nums"
											>{formatBytes(talker.totalBytes)}</td
										>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</div>

				<!-- Top Pairs -->
				<div class="rounded-lg border border-border bg-card p-4">
					<h3 class="mb-3 text-sm font-medium text-muted-foreground">Top Pairs</h3>
					<div class="overflow-x-auto">
						<table class="w-full text-sm">
							<thead>
								<tr class="border-b border-border text-left text-muted-foreground">
									<th class="pb-2 pr-4">#</th>
									<th class="pb-2 pr-4">Source</th>
									<th class="pb-2 pr-4">Destination</th>
									<th
										class="cursor-pointer select-none pb-2 pr-4 text-right transition-colors hover:text-foreground"
										onclick={() => togglePairSort('totalBytes')}
									>
										Traffic{sortArrow(pairSort === 'totalBytes', pairSortDir)}
									</th>
									<th
										class="cursor-pointer select-none pb-2 text-right transition-colors hover:text-foreground"
										onclick={() => togglePairSort('flowCount')}
									>
										Flows{sortArrow(pairSort === 'flowCount', pairSortDir)}
									</th>
								</tr>
							</thead>
							<tbody>
								{#each sortedPairs as pair, i}
									<tr class="border-b border-border/50 transition-colors hover:bg-secondary/50">
										<td class="py-1.5 pr-4 text-muted-foreground">{i + 1}</td>
										<td
											class="max-w-[140px] truncate py-1.5 pr-4"
											title={pair.srcNodeId}
										>
											{#if pair.srcDisplayName}
												<span class="font-medium">{pair.srcDisplayName}</span>
											{:else}
												<span class="font-mono text-xs text-muted-foreground">
													{nodeLabel(pair.srcNodeId)}
												</span>
											{/if}
										</td>
										<td
											class="max-w-[140px] truncate py-1.5 pr-4"
											title={pair.dstNodeId}
										>
											{#if pair.dstDisplayName}
												<span class="font-medium">{pair.dstDisplayName}</span>
											{:else}
												<span class="font-mono text-xs text-muted-foreground">
													{nodeLabel(pair.dstNodeId)}
												</span>
											{/if}
										</td>
										<td class="py-1.5 pr-4 text-right font-medium tabular-nums"
											>{formatBytes(pair.totalBytes)}</td
										>
										<td class="py-1.5 text-right tabular-nums"
											>{pair.flowCount.toLocaleString()}</td
										>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</div>
			</div>

			<!-- Top Ports -->
			<div class="rounded-lg border border-border bg-card p-4">
				<h3 class="mb-3 text-sm font-medium text-muted-foreground">Top Ports</h3>
				{#if portBars.length > 0}
					<BarChart bars={portBars} height={400} />
				{:else}
					<p class="text-sm text-muted-foreground">No port data available yet</p>
				{/if}
			</div>
		{/if}
	</main>
</div>
