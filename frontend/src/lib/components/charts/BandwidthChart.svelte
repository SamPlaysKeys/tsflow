<script lang="ts">
	import { dataSourceStore } from '$lib/stores/data-source-store';
	import { uiStore, filteredNodes, timeRangeStore, TIME_RANGES } from '$lib/stores';
	import { tailscaleService, type BandwidthBucket } from '$lib/services';
	import { formatBytes } from '$lib/utils';

	// Chart dimensions
	const height = 80;
	const padding = { top: 8, right: 12, bottom: 20, left: 50 };

	// Reactive width based on container
	let containerWidth = $state(800);
	let container: HTMLDivElement;

	// Chart data from API
	let chartData = $state<{ time: Date; txBytes: number; rxBytes: number }[]>([]);
	let isLoading = $state(false);

	// Get IPs of selected node (if any)
	const selectedNodeIPs = $derived.by(() => {
		const selectedId = $uiStore.selectedNodeId;
		if (!selectedId) return null;
		const node = $filteredNodes.find((n) => n.id === selectedId);
		return node?.ips || null;
	});

	// Selected node name for display
	const selectedNodeName = $derived.by(() => {
		const selectedId = $uiStore.selectedNodeId;
		if (!selectedId) return null;
		const node = $filteredNodes.find((n) => n.id === selectedId);
		return node?.displayName || null;
	});

	$effect(() => {
		if (container) {
			const observer = new ResizeObserver((entries) => {
				containerWidth = entries[0].contentRect.width;
			});
			observer.observe(container);
			return () => observer.disconnect();
		}
	});

	// Get current time range for live mode
	const liveTimeRange = $derived.by(() => {
		const selected = $timeRangeStore.selected;
		if (selected === 'custom' && $timeRangeStore.customStart && $timeRangeStore.customEnd) {
			return { start: $timeRangeStore.customStart, end: $timeRangeStore.customEnd };
		}
		const preset = TIME_RANGES.find((r) => r.value === selected);
		const minutes = preset?.minutes || 5;
		const end = new Date();
		const start = new Date(end.getTime() - minutes * 60 * 1000);
		return { start, end };
	});

	// Fetch bandwidth data when data range, mode, time range, or selected node changes
	$effect(() => {
		const dataRange = $dataSourceStore.dataRange;
		const mode = $dataSourceStore.mode;
		const nodeIPs = selectedNodeIPs;
		const timeRange = liveTimeRange;

		if (mode === 'live') {
			// In live mode, only fetch data for the selected time range
			fetchBandwidth(timeRange.start, timeRange.end, nodeIPs);
		} else if (dataRange?.earliest && dataRange?.latest) {
			// In historical mode, fetch full available range
			fetchBandwidth(new Date(dataRange.earliest), new Date(dataRange.latest), nodeIPs);
		}
	});

	async function fetchBandwidth(start: Date, end: Date, ips: string[] | null) {
		isLoading = true;
		try {
			// Let backend decide optimal bucket size based on time range
			const response = await tailscaleService.getBandwidth(start, end, ips || undefined);
			chartData = (response.buckets || [])
				.map((b) => ({
					time: new Date(b.time),
					txBytes: b.txBytes,
					rxBytes: b.rxBytes
				}))
				.sort((a, b) => a.time.getTime() - b.time.getTime());
		} catch (err) {
			console.error('Failed to fetch bandwidth:', err);
			chartData = [];
		} finally {
			isLoading = false;
		}
	}

	// Calculate chart bounds from the full data range
	const chartBounds = $derived.by(() => {
		if (chartData.length === 0) {
			return { minTime: 0, maxTime: 1, maxBytes: 1 };
		}

		const times = chartData.map((d) => d.time.getTime());
		const maxBytes = Math.max(...chartData.map((d) => Math.max(d.txBytes, d.rxBytes)), 1);

		return {
			minTime: Math.min(...times),
			maxTime: Math.max(...times),
			maxBytes: maxBytes * 1.1
		};
	});

	// Scale functions
	const chartWidth = $derived(containerWidth - padding.left - padding.right);
	const chartHeight = $derived(height - padding.top - padding.bottom);

	function scaleX(time: number): number {
		const { minTime, maxTime } = chartBounds;
		if (maxTime === minTime) return padding.left;
		return padding.left + ((time - minTime) / (maxTime - minTime)) * chartWidth;
	}

	function scaleY(bytes: number): number {
		const { maxBytes } = chartBounds;
		return padding.top + chartHeight - (bytes / maxBytes) * chartHeight;
	}

	// Generate SVG paths
	const txPath = $derived.by(() => {
		if (chartData.length === 0) return '';
		const points = chartData.map((d) => `${scaleX(d.time.getTime())},${scaleY(d.txBytes)}`);
		return `M${points.join('L')}`;
	});

	const rxPath = $derived.by(() => {
		if (chartData.length === 0) return '';
		const points = chartData.map((d) => `${scaleX(d.time.getTime())},${scaleY(d.rxBytes)}`);
		return `M${points.join('L')}`;
	});

	// Area paths (filled)
	const txArea = $derived.by(() => {
		if (chartData.length === 0) return '';
		const baseline = scaleY(0);
		const points = chartData.map((d) => `${scaleX(d.time.getTime())},${scaleY(d.txBytes)}`);
		const first = chartData[0];
		const last = chartData[chartData.length - 1];
		return `M${scaleX(first.time.getTime())},${baseline}L${points.join('L')}L${scaleX(last.time.getTime())},${baseline}Z`;
	});

	const rxArea = $derived.by(() => {
		if (chartData.length === 0) return '';
		const baseline = scaleY(0);
		const points = chartData.map((d) => `${scaleX(d.time.getTime())},${scaleY(d.rxBytes)}`);
		const first = chartData[0];
		const last = chartData[chartData.length - 1];
		return `M${scaleX(first.time.getTime())},${baseline}L${points.join('L')}L${scaleX(last.time.getTime())},${baseline}Z`;
	});

	// Selected range indicator (for historical mode)
	const selectedRangeX = $derived.by(() => {
		const selectedStart = $dataSourceStore.selectedStart;
		const selectedEnd = $dataSourceStore.selectedEnd;
		if (!selectedStart || !selectedEnd || $dataSourceStore.mode !== 'historical') return null;
		return {
			start: scaleX(selectedStart.getTime()),
			end: scaleX(selectedEnd.getTime())
		};
	});

	// Format time for axis - include date if range spans multiple days
	function formatTime(time: Date): string {
		const { minTime, maxTime } = chartBounds;
		const rangeMs = maxTime - minTime;
		const dayMs = 24 * 60 * 60 * 1000;

		if (rangeMs > dayMs) {
			// Show date + time for multi-day ranges
			return time.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) +
				' ' + time.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
		}
		return time.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
	}

	// Calculate full totals (all chart data)
	const fullTotals = $derived.by(() => {
		const tx = chartData.reduce((sum, d) => sum + d.txBytes, 0);
		const rx = chartData.reduce((sum, d) => sum + d.rxBytes, 0);
		return { tx, rx, total: tx + rx };
	});

	// Calculate totals for selected range (historical mode only) or all data
	const totals = $derived.by(() => {
		const selectedStart = $dataSourceStore.selectedStart;
		const selectedEnd = $dataSourceStore.selectedEnd;
		const mode = $dataSourceStore.mode;

		// In live mode, we only fetched the relevant time range, so use all data
		if (mode === 'live') {
			return fullTotals;
		}

		// In historical mode with selection, sum data within selected range
		if (selectedStart && selectedEnd) {
			const dataToSum = chartData.filter((d) => {
				const t = d.time.getTime();
				return t >= selectedStart.getTime() && t <= selectedEnd.getTime();
			});
			const tx = dataToSum.reduce((sum, d) => sum + d.txBytes, 0);
			const rx = dataToSum.reduce((sum, d) => sum + d.rxBytes, 0);
			return { tx, rx, total: tx + rx };
		}

		// Otherwise use full totals
		return fullTotals;
	});

	// Check if we're showing a subset of data (only in historical mode with selection)
	const isShowingSubset = $derived(
		$dataSourceStore.mode === 'historical' &&
		$dataSourceStore.selectedStart !== null &&
		$dataSourceStore.selectedEnd !== null &&
		totals.total !== fullTotals.total
	);

	// Generate time axis ticks
	const timeAxisTicks = $derived.by(() => {
		if (chartData.length === 0) return [];
		const { minTime, maxTime } = chartBounds;
		const range = maxTime - minTime;
		const tickCount = Math.min(6, Math.max(2, Math.floor(chartWidth / 100)));
		const ticks: { x: number; label: string }[] = [];

		for (let i = 0; i <= tickCount; i++) {
			const time = minTime + (range * i) / tickCount;
			ticks.push({
				x: scaleX(time),
				label: formatTime(new Date(time))
			});
		}
		return ticks;
	});

	// Y-axis ticks
	const yAxisTicks = $derived.by(() => {
		const { maxBytes } = chartBounds;
		return [0, maxBytes / 2, maxBytes].map((bytes) => ({
			y: scaleY(bytes),
			label: formatBytes(bytes)
		}));
	});
</script>

<div bind:this={container} class="w-full bg-card border-b border-border">
	<div class="flex items-center justify-between px-3 py-1.5">
		<div class="flex items-center gap-4">
			<span class="text-xs font-medium text-muted-foreground">
				{#if selectedNodeName}
					<span class="text-primary">{selectedNodeName}</span> Bandwidth
				{:else}
					Bandwidth Over Time
				{/if}
				{#if $dataSourceStore.mode === 'live'}
					<span class="text-primary/70">
						({$timeRangeStore.selected})
					</span>
				{:else if isShowingSubset}
					<span class="text-primary/70">
						(selected range)
					</span>
				{/if}
			</span>
			<div class="flex items-center gap-3 text-xs">
				<span class="flex items-center gap-1">
					<span class="h-2 w-2 rounded-full bg-blue-500"></span>
					TX: {formatBytes(totals.tx)}
				</span>
				<span class="flex items-center gap-1">
					<span class="h-2 w-2 rounded-full bg-emerald-500"></span>
					RX: {formatBytes(totals.rx)}
				</span>
				<span class="text-muted-foreground">
					Total: {formatBytes(totals.total)}
				</span>
				{#if isShowingSubset}
					<span class="text-muted-foreground/60">
						| All: {formatBytes(fullTotals.total)}
					</span>
				{/if}
			</div>
		</div>
		{#if chartData.length > 0}
			<span class="text-xs text-muted-foreground">
				{chartData.length} data points
			</span>
		{/if}
	</div>

	{#if isLoading}
		<div class="flex items-center justify-center text-xs text-muted-foreground" style="height: {height}px">
			Loading bandwidth data...
		</div>
	{:else if chartData.length > 0}
		<svg width={containerWidth} {height} class="overflow-visible">
			<!-- Grid lines -->
			<g class="text-border">
				{#each yAxisTicks as tick}
					<line
						x1={padding.left}
						y1={tick.y}
						x2={containerWidth - padding.right}
						y2={tick.y}
						stroke="currentColor"
						stroke-opacity="0.2"
						stroke-dasharray="2,2"
					/>
				{/each}
			</g>

			<!-- RX area (behind) -->
			<path d={rxArea} fill="rgb(16, 185, 129)" fill-opacity="0.15" />
			<path d={rxPath} fill="none" stroke="rgb(16, 185, 129)" stroke-width="1.5" />

			<!-- TX area (front) -->
			<path d={txArea} fill="rgb(59, 130, 246)" fill-opacity="0.15" />
			<path d={txPath} fill="none" stroke="rgb(59, 130, 246)" stroke-width="1.5" />

			<!-- Selected range indicator (historical mode) -->
			{#if selectedRangeX !== null}
				{@const startX = Math.max(padding.left, selectedRangeX.start)}
				{@const endX = Math.min(containerWidth - padding.right, selectedRangeX.end)}
				{#if endX > startX}
					<rect
						x={startX}
						y={padding.top}
						width={endX - startX}
						height={chartHeight}
						fill="rgb(59, 130, 246)"
						fill-opacity="0.2"
						stroke="rgb(59, 130, 246)"
						stroke-width="2"
						stroke-opacity="0.6"
					/>
				{/if}
			{/if}

			<!-- Y-axis labels -->
			{#each yAxisTicks as tick}
				<text
					x={padding.left - 4}
					y={tick.y}
					text-anchor="end"
					dominant-baseline="middle"
					class="fill-muted-foreground text-[9px]"
				>
					{tick.label}
				</text>
			{/each}

			<!-- X-axis labels -->
			{#each timeAxisTicks as tick}
				<text
					x={tick.x}
					y={height - 4}
					text-anchor="middle"
					class="fill-muted-foreground text-[9px]"
				>
					{tick.label}
				</text>
			{/each}
		</svg>
	{:else}
		<div class="flex items-center justify-center text-xs text-muted-foreground" style="height: {height}px">
			No bandwidth data available
		</div>
	{/if}
</div>
