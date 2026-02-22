# VIP Service Visibility Design

## Problem

VIP services are indistinguishable from regular machines in the network graph. Their traffic is categorized as subnet traffic, which is off by default, making them invisible unless users manually enable the subnet filter.

## Changes

### 1. Enable subnet filter by default

Change `FilterPanel.svelte` default `selectedTrafficTypes` from `['virtual']` to `['virtual', 'subnet']`.

### 2. Add `isVIPService` to NetworkNode

Add `isVIPService: boolean` field to the `NetworkNode` type. Set it in `network-processor.ts` when `getServiceData()` returns a match.

### 3. VIP node color

Add CSS variable `--color-node-vip` (teal: `#06b6d4` light / `#0891b2` dark). VIP nodes use this color with higher priority than the generic tailscale blue.

### 4. VIP node rendering

In `NetworkNode.svelte`:
- Teal border and header tint for VIP nodes
- `Cloud` icon (lucide) instead of `Server`
- "VIP" pill badge next to the display name
- Footer shows "VIP Service" label

## Files Changed

- `frontend/src/lib/types/index.ts` — add `isVIPService` field
- `frontend/src/lib/utils/network-processor.ts` — set `isVIPService` from service data
- `frontend/src/app.css` — add `--color-node-vip` variable
- `frontend/src/lib/components/graph/NetworkNode.svelte` — VIP rendering
- `frontend/src/lib/components/filters/FilterPanel.svelte` — default subnet on
