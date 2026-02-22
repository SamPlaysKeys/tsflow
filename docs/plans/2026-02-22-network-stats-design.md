# Network Flow Log Statistics — Design

## Problem

TSFlow collects and aggregates network flow logs into tiered storage (minutely/hourly/daily) but only surfaces bandwidth over time and node-pair graphs. There is no visibility into protocol distribution, port activity, top talkers, traffic type breakdown, or per-node drill-down statistics.

## Approach: Hybrid — Enrich Existing + Targeted New Table

Maximize reuse of the existing schema. Populate the already-present `protocols`/`ports` JSON columns in `node_pairs_*` tables. Add one new `traffic_stats_*` table family for network-wide rollups. Derive node rankings from existing `bandwidth_by_node_*` and `node_pairs_*` with sorted SQL queries.

## Backend Changes

### 1. Enrich Poller Aggregation (`poller.go`)

The `aggregate()` function currently sets `Protocols: "[]"` and `Ports: "[]"`. Change it to:

- Track unique protocol numbers per node pair (map of proto → bytes)
- Track destination ports per node pair (map of `(proto, dstPort)` → bytes)
- Serialize protocols as `[6, 17]` and ports as `[{"port":443,"proto":6,"bytes":12345}]` (top 20 by bytes)
- Accumulate network-wide counters (protocol bytes, traffic type bytes, flow counts) for the new `traffic_stats` table

The ON CONFLICT upsert for `node_pairs_*` replaces protocols/ports with the latest batch's values (per-bucket granularity makes this acceptable — each bucket is populated once).

### 2. New Table: `traffic_stats_*` (minutely/hourly/daily)

```sql
CREATE TABLE IF NOT EXISTS traffic_stats_minutely (
    bucket INTEGER PRIMARY KEY,
    tcp_bytes INTEGER DEFAULT 0,
    udp_bytes INTEGER DEFAULT 0,
    other_proto_bytes INTEGER DEFAULT 0,
    virtual_bytes INTEGER DEFAULT 0,
    subnet_bytes INTEGER DEFAULT 0,
    physical_bytes INTEGER DEFAULT 0,
    total_flows INTEGER DEFAULT 0,
    unique_pairs INTEGER DEFAULT 0,
    top_ports TEXT DEFAULT '[]',  -- JSON: [{port, proto, bytes}] top 20
    PRIMARY KEY (bucket)
);
```

Same pattern for `traffic_stats_hourly` and `traffic_stats_daily`. Same tiered retention as existing tables.

Populated during the existing `aggregate()` pass — no additional API calls or DB reads required.

### 3. New Store Methods

```go
// New types
type TrafficStats struct {
    Bucket          int64  `json:"bucket"`
    TCPBytes        int64  `json:"tcpBytes"`
    UDPBytes        int64  `json:"udpBytes"`
    OtherProtoBytes int64  `json:"otherProtoBytes"`
    VirtualBytes    int64  `json:"virtualBytes"`
    SubnetBytes     int64  `json:"subnetBytes"`
    PhysicalBytes   int64  `json:"physicalBytes"`
    TotalFlows      int64  `json:"totalFlows"`
    UniquePairs     int64  `json:"uniquePairs"`
    TopPorts        string `json:"topPorts"`
}

type TopTalker struct {
    NodeID     string `json:"nodeId"`
    TxBytes    int64  `json:"txBytes"`
    RxBytes    int64  `json:"rxBytes"`
    TotalBytes int64  `json:"totalBytes"`
}

type TopPair struct {
    SrcNodeID  string `json:"srcNodeId"`
    DstNodeID  string `json:"dstNodeId"`
    TxBytes    int64  `json:"txBytes"`
    RxBytes    int64  `json:"rxBytes"`
    TotalBytes int64  `json:"totalBytes"`
    FlowCount  int64  `json:"flowCount"`
}

// New store interface methods
UpsertTrafficStats(ctx, stats []TrafficStats) error
GetTrafficStats(ctx, start, end time.Time) ([]TrafficStats, error)
GetTopTalkers(ctx, start, end time.Time, limit int) ([]TopTalker, error)
GetTopPairs(ctx, start, end time.Time, limit int) ([]TopPair, error)
GetNodeStats(ctx, nodeID string, start, end time.Time) (*NodeDetailStats, error)
```

`GetTopTalkers` is a derived query on `bandwidth_by_node_*`:
```sql
SELECT node_id, SUM(tx_bytes), SUM(rx_bytes), SUM(tx_bytes + rx_bytes) as total
FROM bandwidth_by_node_{tier}
WHERE bucket >= ? AND bucket <= ?
GROUP BY node_id
ORDER BY total DESC
LIMIT ?
```

`GetTopPairs` is a derived query on `node_pairs_*`:
```sql
SELECT src_node_id, dst_node_id, SUM(tx_bytes), SUM(rx_bytes),
       SUM(tx_bytes + rx_bytes) as total, SUM(flow_count)
FROM node_pairs_{tier}
WHERE bucket >= ? AND bucket <= ?
GROUP BY src_node_id, dst_node_id
ORDER BY total DESC
LIMIT ?
```

### 4. New API Endpoints

| Endpoint | Method | Params | Returns |
|----------|--------|--------|---------|
| `/api/stats/overview` | GET | start, end | Protocol mix, traffic type distribution, top ports, total flows, unique pairs |
| `/api/stats/top-talkers` | GET | start, end, limit (default 10) | Top N nodes by total bytes |
| `/api/stats/top-pairs` | GET | start, end, limit (default 10) | Top N node pairs by total bytes |
| `/api/stats/node/:id` | GET | start, end | Per-node: top peers, protocol breakdown, port activity |

All auto-select tier table based on time range duration (same logic as existing endpoints).

### 5. Rolling Cache Extension

Add `TrafficStats` to `RollingWindowCache` for fast live-view stats queries. Same pattern as existing bandwidth cache.

## Frontend Changes

### 1. New Route: `/analytics`

Add SvelteKit route at `frontend/src/routes/analytics/+page.svelte`. Add navigation link in the Header component.

### 2. Analytics Dashboard Layout

Dashboard grid with the following cards:

**Row 1: Overview Cards**
- Total Bandwidth (TX+RX for period)
- Total Flows
- Unique Device Pairs
- Active Devices

Each card shows the current value with a small sparkline showing the trend over the selected time range.

**Row 2: Distribution Charts**
- Protocol Distribution (donut chart: TCP / UDP / Other, with bytes + percentage)
- Traffic Type Distribution (donut chart: Virtual / Subnet / Physical)

**Row 3: Rankings**
- Top Talkers Table: rank, device name, TX, RX, total bytes, flow count. Rows are clickable (navigates to graph filtered on that node).
- Top Pairs Table: rank, source device, destination device, total bytes, flow count.

**Row 4: Port Activity**
- Top Ports (horizontal bar chart, top 15 ports by bytes, labeled with well-known names: 443=HTTPS, 80=HTTP, 22=SSH, etc.)

**Time Controls:** Reuse existing time range infrastructure (live mode / historical picker). Share state with the graph view so switching between pages preserves the selected range.

### 3. New Stores

`frontend/src/lib/stores/stats-store.ts`:
- Fetches from `/api/stats/overview`, `/api/stats/top-talkers`, `/api/stats/top-pairs`
- Polling interval matches existing network-store pattern
- Exports reactive stores for each stat type

### 4. Contextual Stats Enhancement

When a node is selected on the graph (existing PortDetails panel):
- Add "Top Peers" section (data from `/api/stats/node/:id`)
- Add protocol breakdown for that node's traffic
- Add port activity for that node

When an edge is selected:
- Show total bytes between the pair
- Protocol breakdown for that specific pair
- Top ports used between the pair

Data comes from the enriched `protocols`/`ports` columns in node pair aggregates (already fetched by the graph) and the new node stats endpoint.

### 5. Chart Library

Use lightweight SVG-based charts built directly in Svelte components (donut charts, bar charts, sparklines). No external charting library — keeps bundle size small and matches the existing pattern of custom Svelte components.

## What This Does NOT Include

- Latency metrics (not available in Tailscale API)
- Application-level classification (only IP:port based)
- Alerting/anomaly detection
- Data export/download
- Cross-tailnet comparison

These can be added incrementally in future work.

## Data Flow Summary

```
Tailscale API
    ↓ (every 5 min)
Poller.poll()
    ↓
convertLogs() → FlowLog[]
    ↓
aggregate() ← ENHANCED: extract protocols, ports, traffic stats
    ↓
    ├── UpsertNodePairAggregates() ← now with real protocols/ports JSON
    ├── UpsertBandwidth()
    ├── UpsertNodeBandwidth()
    └── UpsertTrafficStats()      ← NEW
    ↓
RollingWindowCache.Update() ← extended with TrafficStats
    ↓
API endpoints serve frontend queries
```
