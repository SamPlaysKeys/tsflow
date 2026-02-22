# Network Flow Log Statistics — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add deep network flow log statistics — protocol breakdown, port activity, top talkers, traffic type distribution — with a dedicated analytics dashboard and contextual node/edge stats.

**Architecture:** Hybrid approach. Enrich existing `node_pairs_*` tables with protocol/port data. Add one new `traffic_stats_*` table family for network-wide rollups. Derive rankings via SQL from existing tables. New `/analytics` SvelteKit route with SVG charts.

**Tech Stack:** Go (Gin, SQLite), SvelteKit 5, Tailwind CSS 4, SVG charts, Svelte stores (writable + derived)

**Design Doc:** `docs/plans/2026-02-22-network-stats-design.md`

---

### Task 1: Add TrafficStats Types and Schema

**Files:**
- Modify: `backend/internal/database/database.go`

**Step 1: Add new types to database.go**

Add after the `NodeBandwidth` struct (line 43):

```go
// TrafficStats represents network-wide statistics for a time bucket
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
	TopPorts        string `json:"topPorts"` // JSON: [{"port":443,"proto":6,"bytes":12345}]
}

// TopTalker represents a node ranked by total traffic
type TopTalker struct {
	NodeID     string `json:"nodeId"`
	TxBytes    int64  `json:"txBytes"`
	RxBytes    int64  `json:"rxBytes"`
	TotalBytes int64  `json:"totalBytes"`
}

// TopPair represents a node pair ranked by total traffic
type TopPair struct {
	SrcNodeID  string `json:"srcNodeId"`
	DstNodeID  string `json:"dstNodeId"`
	TxBytes    int64  `json:"txBytes"`
	RxBytes    int64  `json:"rxBytes"`
	TotalBytes int64  `json:"totalBytes"`
	FlowCount  int64  `json:"flowCount"`
}

// PortStat represents a single port's traffic statistics
type PortStat struct {
	Port  int    `json:"port"`
	Proto int    `json:"proto"`
	Bytes int64  `json:"bytes"`
}

// NodeDetailStats represents detailed statistics for a single node
type NodeDetailStats struct {
	NodeID       string     `json:"nodeId"`
	TotalTx      int64      `json:"totalTx"`
	TotalRx      int64      `json:"totalRx"`
	TCPBytes     int64      `json:"tcpBytes"`
	UDPBytes     int64      `json:"udpBytes"`
	OtherBytes   int64      `json:"otherBytes"`
	TopPeers     []TopPair  `json:"topPeers"`
	TopPorts     []PortStat `json:"topPorts"`
}
```

**Step 2: Add new tables to Init() schema**

Add after the `bandwidth_by_node_daily` table creation (before `-- Poll state tracking` comment, line 248):

```sql
-- Network-wide traffic statistics rollups
-- Pre-computed at poll time for fast dashboard queries
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
	top_ports TEXT DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS traffic_stats_hourly (
	bucket INTEGER PRIMARY KEY,
	tcp_bytes INTEGER DEFAULT 0,
	udp_bytes INTEGER DEFAULT 0,
	other_proto_bytes INTEGER DEFAULT 0,
	virtual_bytes INTEGER DEFAULT 0,
	subnet_bytes INTEGER DEFAULT 0,
	physical_bytes INTEGER DEFAULT 0,
	total_flows INTEGER DEFAULT 0,
	unique_pairs INTEGER DEFAULT 0,
	top_ports TEXT DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS traffic_stats_daily (
	bucket INTEGER PRIMARY KEY,
	tcp_bytes INTEGER DEFAULT 0,
	udp_bytes INTEGER DEFAULT 0,
	other_proto_bytes INTEGER DEFAULT 0,
	virtual_bytes INTEGER DEFAULT 0,
	subnet_bytes INTEGER DEFAULT 0,
	physical_bytes INTEGER DEFAULT 0,
	total_flows INTEGER DEFAULT 0,
	unique_pairs INTEGER DEFAULT 0,
	top_ports TEXT DEFAULT '[]'
);
```

**Step 3: Add new methods to Store interface**

Add to the `Store` interface (after `GetNodeBandwidth`, line 93):

```go
// Traffic statistics operations
UpsertTrafficStats(ctx context.Context, stats []TrafficStats) error
GetTrafficStats(ctx context.Context, start, end time.Time) ([]TrafficStats, error)
GetTopTalkers(ctx context.Context, start, end time.Time, limit int) ([]TopTalker, error)
GetTopPairs(ctx context.Context, start, end time.Time, limit int) ([]TopPair, error)
GetNodeStats(ctx context.Context, nodeID string, start, end time.Time) (*NodeDetailStats, error)
```

**Step 4: Verify it compiles**

Run: `cd /Users/rajsingh/Documents/GitHub/tsflow/backend && go build ./...`

Expected: Compile errors because SQLiteStore doesn't implement the new interface methods yet. This is expected — we'll implement them in Task 2.

**Step 5: Commit**

```bash
git add backend/internal/database/database.go
git commit -m "add TrafficStats types, schema, and Store interface methods"
```

---

### Task 2: Implement Store Methods for Traffic Stats

**Files:**
- Modify: `backend/internal/database/database.go`

**Step 1: Implement UpsertTrafficStats**

Add after the `UpsertNodeBandwidth` method (after line 623):

```go
// UpsertTrafficStats upserts network-wide traffic statistics
func (s *SQLiteStore) UpsertTrafficStats(ctx context.Context, stats []TrafficStats) error {
	if len(stats) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	tables := []string{"traffic_stats_minutely", "traffic_stats_hourly", "traffic_stats_daily"}
	bucketSizes := []int64{60, 3600, 86400}

	for i, table := range tables {
		bs := bucketSizes[i]
		stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (bucket, tcp_bytes, udp_bytes, other_proto_bytes,
				virtual_bytes, subnet_bytes, physical_bytes,
				total_flows, unique_pairs, top_ports)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(bucket) DO UPDATE SET
				tcp_bytes = tcp_bytes + excluded.tcp_bytes,
				udp_bytes = udp_bytes + excluded.udp_bytes,
				other_proto_bytes = other_proto_bytes + excluded.other_proto_bytes,
				virtual_bytes = virtual_bytes + excluded.virtual_bytes,
				subnet_bytes = subnet_bytes + excluded.subnet_bytes,
				physical_bytes = physical_bytes + excluded.physical_bytes,
				total_flows = total_flows + excluded.total_flows,
				unique_pairs = excluded.unique_pairs,
				top_ports = excluded.top_ports
		`, table))
		if err != nil {
			return fmt.Errorf("failed to prepare statement for %s: %w", table, err)
		}
		err = func() error {
			defer stmt.Close()
			for _, st := range stats {
				bucket := (st.Bucket / bs) * bs
				_, err := stmt.ExecContext(ctx,
					bucket, st.TCPBytes, st.UDPBytes, st.OtherProtoBytes,
					st.VirtualBytes, st.SubnetBytes, st.PhysicalBytes,
					st.TotalFlows, st.UniquePairs, st.TopPorts,
				)
				if err != nil {
					return fmt.Errorf("failed to upsert traffic stats: %w", err)
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
```

**Step 2: Implement GetTrafficStats**

```go
// GetTrafficStats retrieves aggregated traffic statistics for a time range
func (s *SQLiteStore) GetTrafficStats(ctx context.Context, start, end time.Time) ([]TrafficStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()
	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix
	var table string
	switch {
	case rangeSeconds <= 24*3600:
		table = "traffic_stats_minutely"
	case rangeSeconds <= 7*24*3600:
		table = "traffic_stats_hourly"
	default:
		table = "traffic_stats_daily"
	}

	query := fmt.Sprintf(`
		SELECT bucket, tcp_bytes, udp_bytes, other_proto_bytes,
			   virtual_bytes, subnet_bytes, physical_bytes,
			   total_flows, unique_pairs, top_ports
		FROM %s
		WHERE bucket >= ? AND bucket <= ?
		ORDER BY bucket ASC
	`, table)

	rows, err := s.db.QueryContext(ctx, query, startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("failed to query traffic stats: %w", err)
	}
	defer rows.Close()

	var stats []TrafficStats
	for rows.Next() {
		var st TrafficStats
		err := rows.Scan(
			&st.Bucket, &st.TCPBytes, &st.UDPBytes, &st.OtherProtoBytes,
			&st.VirtualBytes, &st.SubnetBytes, &st.PhysicalBytes,
			&st.TotalFlows, &st.UniquePairs, &st.TopPorts,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan traffic stats: %w", err)
		}
		stats = append(stats, st)
	}
	return stats, rows.Err()
}
```

**Step 3: Implement GetTopTalkers**

```go
// GetTopTalkers returns top N nodes by total traffic
func (s *SQLiteStore) GetTopTalkers(ctx context.Context, start, end time.Time, limit int) ([]TopTalker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()
	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix
	var table string
	switch {
	case rangeSeconds <= 24*3600:
		table = "bandwidth_by_node_minutely"
	case rangeSeconds <= 7*24*3600:
		table = "bandwidth_by_node_hourly"
	default:
		table = "bandwidth_by_node_daily"
	}

	if limit <= 0 {
		limit = 10
	}

	query := fmt.Sprintf(`
		SELECT node_id, SUM(tx_bytes) as tx, SUM(rx_bytes) as rx,
			   SUM(tx_bytes + rx_bytes) as total
		FROM %s
		WHERE bucket >= ? AND bucket <= ?
		GROUP BY node_id
		ORDER BY total DESC
		LIMIT ?
	`, table)

	rows, err := s.db.QueryContext(ctx, query, startUnix, endUnix, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query top talkers: %w", err)
	}
	defer rows.Close()

	var talkers []TopTalker
	for rows.Next() {
		var t TopTalker
		err := rows.Scan(&t.NodeID, &t.TxBytes, &t.RxBytes, &t.TotalBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to scan top talker: %w", err)
		}
		talkers = append(talkers, t)
	}
	return talkers, rows.Err()
}
```

**Step 4: Implement GetTopPairs**

```go
// GetTopPairs returns top N node pairs by total traffic
func (s *SQLiteStore) GetTopPairs(ctx context.Context, start, end time.Time, limit int) ([]TopPair, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()
	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix
	var table string
	switch {
	case rangeSeconds <= 24*3600:
		table = "node_pairs_minutely"
	case rangeSeconds <= 7*24*3600:
		table = "node_pairs_hourly"
	default:
		table = "node_pairs_daily"
	}

	if limit <= 0 {
		limit = 10
	}

	query := fmt.Sprintf(`
		SELECT src_node_id, dst_node_id,
			   SUM(tx_bytes) as tx, SUM(rx_bytes) as rx,
			   SUM(tx_bytes + rx_bytes) as total, SUM(flow_count) as flows
		FROM %s
		WHERE bucket >= ? AND bucket <= ?
		GROUP BY src_node_id, dst_node_id
		ORDER BY total DESC
		LIMIT ?
	`, table)

	rows, err := s.db.QueryContext(ctx, query, startUnix, endUnix, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query top pairs: %w", err)
	}
	defer rows.Close()

	var pairs []TopPair
	for rows.Next() {
		var p TopPair
		err := rows.Scan(&p.SrcNodeID, &p.DstNodeID, &p.TxBytes, &p.RxBytes, &p.TotalBytes, &p.FlowCount)
		if err != nil {
			return nil, fmt.Errorf("failed to scan top pair: %w", err)
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}
```

**Step 5: Implement GetNodeStats**

```go
// GetNodeStats returns detailed statistics for a specific node
func (s *SQLiteStore) GetNodeStats(ctx context.Context, nodeID string, start, end time.Time) (*NodeDetailStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()
	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix
	var npTable, bwTable string
	switch {
	case rangeSeconds <= 24*3600:
		npTable = "node_pairs_minutely"
		bwTable = "bandwidth_by_node_minutely"
	case rangeSeconds <= 7*24*3600:
		npTable = "node_pairs_hourly"
		bwTable = "bandwidth_by_node_hourly"
	default:
		npTable = "node_pairs_daily"
		bwTable = "bandwidth_by_node_daily"
	}

	stats := &NodeDetailStats{NodeID: nodeID}

	// Get total TX/RX for this node
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(tx_bytes), 0), COALESCE(SUM(rx_bytes), 0)
		FROM %s WHERE bucket >= ? AND bucket <= ? AND node_id = ?
	`, bwTable), startUnix, endUnix, nodeID).Scan(&stats.TotalTx, &stats.TotalRx)
	if err != nil {
		return nil, fmt.Errorf("failed to get node bandwidth: %w", err)
	}

	// Get top peers (nodes this node communicates with most)
	peerQuery := fmt.Sprintf(`
		SELECT
			CASE WHEN src_node_id = ? THEN dst_node_id ELSE src_node_id END as peer,
			SUM(tx_bytes) as tx, SUM(rx_bytes) as rx,
			SUM(tx_bytes + rx_bytes) as total, SUM(flow_count) as flows
		FROM %s
		WHERE bucket >= ? AND bucket <= ?
			AND (src_node_id = ? OR dst_node_id = ?)
		GROUP BY peer
		ORDER BY total DESC
		LIMIT 10
	`, npTable)

	rows, err := s.db.QueryContext(ctx, peerQuery, nodeID, startUnix, endUnix, nodeID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query node peers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p TopPair
		var peerID string
		err := rows.Scan(&peerID, &p.TxBytes, &p.RxBytes, &p.TotalBytes, &p.FlowCount)
		if err != nil {
			return nil, fmt.Errorf("failed to scan peer: %w", err)
		}
		p.SrcNodeID = nodeID
		p.DstNodeID = peerID
		stats.TopPeers = append(stats.TopPeers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}
```

**Step 6: Add traffic_stats tables to Cleanup**

In the `Cleanup` method, add `traffic_stats_minutely` to the minutely cleanup list, `traffic_stats_hourly` to hourly, and `traffic_stats_daily` to daily. Specifically:

Change line 810:
```go
for _, table := range []string{"node_pairs_minutely", "bandwidth_minutely", "bandwidth_by_node_minutely", "traffic_stats_minutely"} {
```

Change line 823:
```go
for _, table := range []string{"node_pairs_hourly", "bandwidth_hourly", "bandwidth_by_node_hourly", "traffic_stats_hourly"} {
```

Change line 837:
```go
for _, table := range []string{"node_pairs_daily", "bandwidth_daily", "bandwidth_by_node_daily", "traffic_stats_daily"} {
```

Also add `traffic_stats_minutely`, `traffic_stats_hourly`, `traffic_stats_daily` to the `GetStats` table counts list (line 871).

**Step 7: Verify it compiles**

Run: `cd /Users/rajsingh/Documents/GitHub/tsflow/backend && go build ./...`

Expected: PASS (all interface methods implemented)

**Step 8: Commit**

```bash
git add backend/internal/database/database.go
git commit -m "implement store methods for traffic stats, top talkers, top pairs, node stats"
```

---

### Task 3: Enrich Poller Aggregation with Protocol/Port Data

**Files:**
- Modify: `backend/internal/services/poller.go`

**Step 1: Add encoding/json import**

Add `"encoding/json"` and `"sort"` to the import block at the top of `poller.go` (line 3). `sort` is already imported.

**Step 2: Modify the aggregate() function**

The existing `aggregate()` function (line 566) needs to:
1. Track protocols and ports per node pair
2. Accumulate network-wide traffic stats
3. Return `TrafficStats` as a 4th return value

Change the function signature (line 566):
```go
func (p *Poller) aggregate(logs []database.FlowLog) (
	[]database.NodePairAggregate,
	[]database.BandwidthBucket,
	[]database.NodeBandwidth,
	[]database.TrafficStats,
) {
```

Add helper types inside the function (after the `nodePairKey` struct, line 578):

```go
	// Protocol/port tracking per node pair
	type protoPortKey struct {
		proto int
		port  int
	}
	type pairProtoData struct {
		protocols map[int]int64          // proto number → total bytes
		ports     map[protoPortKey]int64 // (proto, port) → total bytes
	}
	pairProtoMap := make(map[nodePairKey]*pairProtoData)

	// Network-wide traffic stats per bucket
	type trafficStatsAccum struct {
		tcpBytes        int64
		udpBytes        int64
		otherProtoBytes int64
		virtualBytes    int64
		subnetBytes     int64
		physicalBytes   int64
		totalFlows      int64
		uniquePairs     map[string]bool // "nodeA|nodeB" for unique pair counting
		ports           map[protoPortKey]int64
	}
	trafficStatsMap := make(map[int64]*trafficStatsAccum)
```

Inside the flow log loop (after the node pair aggregate section, around line 650), add protocol/port tracking:

```go
		// Track protocols and ports for this pair
		ppKey := npKey
		if _, ok := pairProtoMap[ppKey]; !ok {
			pairProtoMap[ppKey] = &pairProtoData{
				protocols: make(map[int]int64),
				ports:     make(map[protoPortKey]int64),
			}
		}
		ppd := pairProtoMap[ppKey]
		ppd.protocols[log.Protocol] += log.TxBytes
		if log.DstPort > 0 {
			ppd.ports[protoPortKey{proto: log.Protocol, port: log.DstPort}] += log.TxBytes
		}

		// Accumulate network-wide traffic stats
		ts, ok := trafficStatsMap[bucket]
		if !ok {
			ts = &trafficStatsAccum{
				uniquePairs: make(map[string]bool),
				ports:       make(map[protoPortKey]int64),
			}
			trafficStatsMap[bucket] = ts
		}
		ts.totalFlows++
		ts.uniquePairs[nodeA+"|"+nodeB] = true
		switch log.Protocol {
		case 6:
			ts.tcpBytes += log.TxBytes
		case 17:
			ts.udpBytes += log.TxBytes
		default:
			ts.otherProtoBytes += log.TxBytes
		}
		switch log.TrafficType {
		case "virtual":
			ts.virtualBytes += log.TxBytes
		case "subnet":
			ts.subnetBytes += log.TxBytes
		case "physical":
			ts.physicalBytes += log.TxBytes
		}
		if log.DstPort > 0 {
			ts.ports[protoPortKey{proto: log.Protocol, port: log.DstPort}] += log.TxBytes
		}
```

**Step 3: Serialize protocols/ports into node pair aggregates**

After the existing loop, before converting maps to slices (before line 694), add:

```go
	// Enrich node pair aggregates with protocol/port data
	for key, agg := range nodePairMap {
		if ppd, ok := pairProtoMap[key]; ok {
			// Serialize protocols
			protos := make([]int, 0, len(ppd.protocols))
			for proto := range ppd.protocols {
				protos = append(protos, proto)
			}
			sort.Ints(protos)
			protosJSON, _ := json.Marshal(protos)
			agg.Protocols = string(protosJSON)

			// Serialize top 20 ports by bytes
			type portEntry struct {
				Port  int   `json:"port"`
				Proto int   `json:"proto"`
				Bytes int64 `json:"bytes"`
			}
			portEntries := make([]portEntry, 0, len(ppd.ports))
			for ppk, bytes := range ppd.ports {
				portEntries = append(portEntries, portEntry{Port: ppk.port, Proto: ppk.proto, Bytes: bytes})
			}
			sort.Slice(portEntries, func(i, j int) bool {
				return portEntries[i].Bytes > portEntries[j].Bytes
			})
			if len(portEntries) > 20 {
				portEntries = portEntries[:20]
			}
			portsJSON, _ := json.Marshal(portEntries)
			agg.Ports = string(portsJSON)
		}
	}
```

**Step 4: Build TrafficStats slice**

After converting nodePairMap to slice, add:

```go
	// Build traffic stats
	trafficStats := make([]database.TrafficStats, 0, len(trafficStatsMap))
	for bucket, ts := range trafficStatsMap {
		// Top 20 ports network-wide
		type portEntry struct {
			Port  int   `json:"port"`
			Proto int   `json:"proto"`
			Bytes int64 `json:"bytes"`
		}
		portEntries := make([]portEntry, 0, len(ts.ports))
		for ppk, bytes := range ts.ports {
			portEntries = append(portEntries, portEntry{Port: ppk.port, Proto: ppk.proto, Bytes: bytes})
		}
		sort.Slice(portEntries, func(i, j int) bool {
			return portEntries[i].Bytes > portEntries[j].Bytes
		})
		if len(portEntries) > 20 {
			portEntries = portEntries[:20]
		}
		topPortsJSON, _ := json.Marshal(portEntries)

		trafficStats = append(trafficStats, database.TrafficStats{
			Bucket:          bucket,
			TCPBytes:        ts.tcpBytes,
			UDPBytes:        ts.udpBytes,
			OtherProtoBytes: ts.otherProtoBytes,
			VirtualBytes:    ts.virtualBytes,
			SubnetBytes:     ts.subnetBytes,
			PhysicalBytes:   ts.physicalBytes,
			TotalFlows:      ts.totalFlows,
			UniquePairs:     int64(len(ts.uniquePairs)),
			TopPorts:        string(topPortsJSON),
		})
	}
```

Change the return statement (line 709) to:
```go
	return nodePairs, totalBandwidth, nodeBandwidth, trafficStats
```

**Step 5: Update poll() to handle 4th return value**

Change the aggregate call in `poll()` (line 517):
```go
	nodePairs, totalBandwidth, nodeBandwidth, trafficStats := p.aggregate(flowLogs)
```

After the existing upsert calls (after line 536), add:
```go
	if len(trafficStats) > 0 {
		if err := p.store.UpsertTrafficStats(ctx, trafficStats); err != nil {
			log.Printf("Warning: failed to upsert traffic stats: %v", err)
		}
	}
```

**Step 6: Update the ON CONFLICT for UpsertNodePairAggregates to merge protocols/ports**

In `database.go`, modify the `UpsertNodePairAggregates` ON CONFLICT clause (line 424-429) to also update protocols and ports:

```go
		ON CONFLICT(bucket, src_node_id, dst_node_id, traffic_type) DO UPDATE SET
			tx_bytes = tx_bytes + excluded.tx_bytes,
			rx_bytes = rx_bytes + excluded.rx_bytes,
			tx_pkts = tx_pkts + excluded.tx_pkts,
			rx_pkts = rx_pkts + excluded.rx_pkts,
			flow_count = flow_count + excluded.flow_count,
			protocols = excluded.protocols,
			ports = excluded.ports
```

**Step 7: Verify it compiles**

Run: `cd /Users/rajsingh/Documents/GitHub/tsflow/backend && go build ./...`

Expected: PASS

**Step 8: Commit**

```bash
git add backend/internal/services/poller.go backend/internal/database/database.go
git commit -m "enrich poller aggregation with protocol/port tracking and traffic stats"
```

---

### Task 4: Extend Rolling Cache for Traffic Stats

**Files:**
- Modify: `backend/internal/services/poller.go`

**Step 1: Add TrafficStats to RollingWindowCache**

Add a `trafficStats` field to the `RollingWindowCache` struct (line 122):

```go
type RollingWindowCache struct {
	mu sync.RWMutex
	nodePairs     map[int64][]database.NodePairAggregate
	bandwidth     map[int64]*database.BandwidthBucket
	nodeBandwidth map[int64]map[string]*database.NodeBandwidth
	trafficStats  map[int64]*database.TrafficStats
	maxAge        time.Duration
}
```

Initialize in `NewRollingWindowCache` (line 136):
```go
func NewRollingWindowCache(maxAge time.Duration) *RollingWindowCache {
	return &RollingWindowCache{
		nodePairs:     make(map[int64][]database.NodePairAggregate),
		bandwidth:     make(map[int64]*database.BandwidthBucket),
		nodeBandwidth: make(map[int64]map[string]*database.NodeBandwidth),
		trafficStats:  make(map[int64]*database.TrafficStats),
		maxAge:        maxAge,
	}
}
```

**Step 2: Update the Update() method signature**

Change the Update method (line 145) to accept traffic stats:

```go
func (c *RollingWindowCache) Update(
	nodePairs []database.NodePairAggregate,
	bandwidth []database.BandwidthBucket,
	nodeBandwidth []database.NodeBandwidth,
	trafficStats []database.TrafficStats,
) {
```

Add traffic stats handling inside the method (after nodeBandwidth handling, before prune):

```go
	// Add traffic stats by bucket
	for _, ts := range trafficStats {
		if existing, ok := c.trafficStats[ts.Bucket]; ok {
			existing.TCPBytes += ts.TCPBytes
			existing.UDPBytes += ts.UDPBytes
			existing.OtherProtoBytes += ts.OtherProtoBytes
			existing.VirtualBytes += ts.VirtualBytes
			existing.SubnetBytes += ts.SubnetBytes
			existing.PhysicalBytes += ts.PhysicalBytes
			existing.TotalFlows += ts.TotalFlows
			existing.UniquePairs = ts.UniquePairs // Replace (snapshot)
			existing.TopPorts = ts.TopPorts       // Replace (snapshot)
		} else {
			tsCopy := ts
			c.trafficStats[ts.Bucket] = &tsCopy
		}
	}
```

**Step 3: Update prune() to include trafficStats**

Add to the `prune` method (line 197):

```go
	for bucket := range c.trafficStats {
		if bucket < cutoff {
			delete(c.trafficStats, bucket)
		}
	}
```

**Step 4: Add GetTrafficStats to cache**

```go
// GetTrafficStats returns cached traffic stats for a time range
func (c *RollingWindowCache) GetTrafficStats(start, end time.Time) []database.TrafficStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	startUnix := start.Unix()
	endUnix := end.Unix()
	var result []database.TrafficStats

	for bucket, ts := range c.trafficStats {
		if bucket >= startUnix && bucket <= endUnix {
			result = append(result, *ts)
		}
	}
	return result
}
```

**Step 5: Update the poll() call to pass trafficStats to cache**

In the `poll()` method, update the `rollingCache.Update` call (line 539):

```go
	p.rollingCache.Update(nodePairs, totalBandwidth, nodeBandwidth, trafficStats)
```

**Step 6: Verify it compiles**

Run: `cd /Users/rajsingh/Documents/GitHub/tsflow/backend && go build ./...`

Expected: PASS

**Step 7: Commit**

```bash
git add backend/internal/services/poller.go
git commit -m "extend rolling cache with traffic stats for fast live-view queries"
```

---

### Task 5: Add Stats API Endpoints

**Files:**
- Modify: `backend/internal/handlers/handlers.go`
- Modify: `backend/main.go`

**Step 1: Add stats handler methods to handlers.go**

Add after the `GetDNSNameservers` method (after line 705):

```go
// GetStatsOverview returns network-wide statistics for a time range
func (h *Handlers) GetStatsOverview(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return
	}

	startTime, endTime, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var stats []database.TrafficStats
	source := "database"

	// Try rolling cache first for recent data
	if h.poller != nil {
		cache := h.poller.GetRollingCache()
		if cache.HasDataFor(startTime, endTime) {
			stats = cache.GetTrafficStats(startTime, endTime)
			if len(stats) > 0 {
				source = "cache"
			}
		}
	}

	if len(stats) == 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), AggregationQueryTimeout)
		defer cancel()
		stats, err = h.store.GetTrafficStats(ctx, startTime, endTime)
		if err != nil {
			log.Printf("ERROR GetStatsOverview: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch stats", "message": err.Error()})
			return
		}
		source = "database"
	}

	// Aggregate all buckets into summary
	var summary struct {
		TCPBytes        int64 `json:"tcpBytes"`
		UDPBytes        int64 `json:"udpBytes"`
		OtherProtoBytes int64 `json:"otherProtoBytes"`
		VirtualBytes    int64 `json:"virtualBytes"`
		SubnetBytes     int64 `json:"subnetBytes"`
		PhysicalBytes   int64 `json:"physicalBytes"`
		TotalFlows      int64 `json:"totalFlows"`
		MaxUniquePairs  int64 `json:"uniquePairs"`
	}
	for _, st := range stats {
		summary.TCPBytes += st.TCPBytes
		summary.UDPBytes += st.UDPBytes
		summary.OtherProtoBytes += st.OtherProtoBytes
		summary.VirtualBytes += st.VirtualBytes
		summary.SubnetBytes += st.SubnetBytes
		summary.PhysicalBytes += st.PhysicalBytes
		summary.TotalFlows += st.TotalFlows
		if st.UniquePairs > summary.MaxUniquePairs {
			summary.MaxUniquePairs = st.UniquePairs
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"summary":  summary,
		"buckets":  stats,
		"metadata": gin.H{"start": startTime, "end": endTime, "bucketCount": len(stats), "source": source},
	})
}

// GetTopTalkers returns the top N nodes by total traffic
func (h *Handlers) GetTopTalkers(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return
	}

	startTime, endTime, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	limit := 10
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	talkers, err := h.store.GetTopTalkers(ctx, startTime, endTime, limit)
	if err != nil {
		log.Printf("ERROR GetTopTalkers: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch top talkers", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"talkers":  talkers,
		"metadata": gin.H{"start": startTime, "end": endTime, "limit": limit, "count": len(talkers)},
	})
}

// GetTopPairs returns the top N node pairs by total traffic
func (h *Handlers) GetTopPairs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return
	}

	startTime, endTime, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	limit := 10
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	pairs, err := h.store.GetTopPairs(ctx, startTime, endTime, limit)
	if err != nil {
		log.Printf("ERROR GetTopPairs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch top pairs", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"pairs":    pairs,
		"metadata": gin.H{"start": startTime, "end": endTime, "limit": limit, "count": len(pairs)},
	})
}

// GetNodeDetailStats returns detailed statistics for a specific node
func (h *Handlers) GetNodeDetailStats(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Database not configured"})
		return
	}

	nodeID := c.Param("id")
	if nodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node ID required"})
		return
	}

	startTime, endTime, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	stats, err := h.store.GetNodeStats(ctx, nodeID, startTime, endTime)
	if err != nil {
		log.Printf("ERROR GetNodeDetailStats: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch node stats", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, stats)
}

// parseTimeRange extracts and validates start/end from query params
func (h *Handlers) parseTimeRange(c *gin.Context) (time.Time, time.Time, error) {
	start := c.Query("start")
	end := c.Query("end")

	var startTime, endTime time.Time
	var err error

	if start == "" {
		startTime = time.Now().Add(-1 * time.Hour)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start time: %w", err)
		}
	}

	if end == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, end)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end time: %w", err)
		}
	}

	return startTime, endTime, nil
}
```

**Step 2: Register routes in main.go**

Add the stats routes to the api group in `main.go` (after the poller endpoints, around line 186):

```go
	// Stats endpoints
	statsCache := middleware.CacheMiddleware(middleware.LongCacheConfig())
	stats := api.Group("/stats")
	{
		stats.GET("/overview", statsCache, handlerService.GetStatsOverview)
		stats.GET("/top-talkers", statsCache, handlerService.GetTopTalkers)
		stats.GET("/top-pairs", statsCache, handlerService.GetTopPairs)
		stats.GET("/node/:id", statsCache, handlerService.GetNodeDetailStats)
	}
```

**Step 3: Verify it compiles**

Run: `cd /Users/rajsingh/Documents/GitHub/tsflow/backend && go build ./...`

Expected: PASS

**Step 4: Commit**

```bash
git add backend/internal/handlers/handlers.go backend/main.go
git commit -m "add stats API endpoints: overview, top talkers, top pairs, node detail"
```

---

### Task 6: Add Frontend Types and API Service Methods

**Files:**
- Modify: `frontend/src/lib/types/index.ts`
- Modify: `frontend/src/lib/services/tailscale-service.ts`

**Step 1: Add stats types to index.ts**

Add at the end of `frontend/src/lib/types/index.ts`:

```typescript
// Stats types
export interface PortStat {
	port: number;
	proto: number;
	bytes: number;
}

export interface TrafficStatsBucket {
	bucket: number;
	tcpBytes: number;
	udpBytes: number;
	otherProtoBytes: number;
	virtualBytes: number;
	subnetBytes: number;
	physicalBytes: number;
	totalFlows: number;
	uniquePairs: number;
	topPorts: string; // JSON string of PortStat[]
}

export interface TrafficStatsSummary {
	tcpBytes: number;
	udpBytes: number;
	otherProtoBytes: number;
	virtualBytes: number;
	subnetBytes: number;
	physicalBytes: number;
	totalFlows: number;
	uniquePairs: number;
}

export interface TopTalker {
	nodeId: string;
	txBytes: number;
	rxBytes: number;
	totalBytes: number;
}

export interface TopPair {
	srcNodeId: string;
	dstNodeId: string;
	txBytes: number;
	rxBytes: number;
	totalBytes: number;
	flowCount: number;
}

export interface NodeDetailStats {
	nodeId: string;
	totalTx: number;
	totalRx: number;
	tcpBytes: number;
	udpBytes: number;
	otherBytes: number;
	topPeers: TopPair[];
	topPorts: PortStat[];
}
```

**Step 2: Add API methods to tailscale-service.ts**

Add the following imports at the top of `tailscale-service.ts`:
```typescript
import type { Device, NetworkLogsResponse, TrafficStatsBucket, TrafficStatsSummary, TopTalker, TopPair, NodeDetailStats } from '$lib/types';
```

Add these methods to the `tailscaleService` object (after `getBandwidth`):

```typescript
	async getStatsOverview(start: Date, end: Date): Promise<{
		summary: TrafficStatsSummary;
		buckets: TrafficStatsBucket[];
		metadata: { start: string; end: string; bucketCount: number; source: string };
	}> {
		const startISO = start.toISOString();
		const endISO = end.toISOString();
		return api.get(`/stats/overview?start=${startISO}&end=${endISO}`);
	},

	async getTopTalkers(start: Date, end: Date, limit = 10): Promise<{
		talkers: TopTalker[];
		metadata: { start: string; end: string; limit: number; count: number };
	}> {
		const startISO = start.toISOString();
		const endISO = end.toISOString();
		return api.get(`/stats/top-talkers?start=${startISO}&end=${endISO}&limit=${limit}`);
	},

	async getTopPairs(start: Date, end: Date, limit = 10): Promise<{
		pairs: TopPair[];
		metadata: { start: string; end: string; limit: number; count: number };
	}> {
		const startISO = start.toISOString();
		const endISO = end.toISOString();
		return api.get(`/stats/top-pairs?start=${startISO}&end=${endISO}&limit=${limit}`);
	},

	async getNodeStats(nodeId: string, start: Date, end: Date): Promise<NodeDetailStats> {
		const startISO = start.toISOString();
		const endISO = end.toISOString();
		return api.get(`/stats/node/${encodeURIComponent(nodeId)}?start=${startISO}&end=${endISO}`);
	}
```

**Step 3: Commit**

```bash
git add frontend/src/lib/types/index.ts frontend/src/lib/services/tailscale-service.ts
git commit -m "add frontend types and API service methods for network stats"
```

---

### Task 7: Create Stats Store

**Files:**
- Create: `frontend/src/lib/stores/stats-store.ts`
- Modify: `frontend/src/lib/stores/index.ts`

**Step 1: Create stats-store.ts**

```typescript
import { writable, derived } from 'svelte/store';
import { tailscaleService } from '$lib/services/tailscale-service';
import { queryTimeWindow } from './data-source-store';
import type { TrafficStatsSummary, TrafficStatsBucket, TopTalker, TopPair, PortStat } from '$lib/types';

interface StatsState {
	summary: TrafficStatsSummary | null;
	buckets: TrafficStatsBucket[];
	topTalkers: TopTalker[];
	topPairs: TopPair[];
	isLoading: boolean;
	error: string | null;
}

const defaultState: StatsState = {
	summary: null,
	buckets: [],
	topTalkers: [],
	topPairs: [],
	isLoading: false,
	error: null
};

const statsState = writable<StatsState>(defaultState);

let refreshTimer: ReturnType<typeof setInterval> | null = null;

export async function loadStats() {
	statsState.update((s) => ({ ...s, isLoading: true, error: null }));

	try {
		let start: Date;
		let end: Date;

		// Read current time window from data source store
		const unsubscribe = queryTimeWindow.subscribe((tw) => {
			start = tw.start;
			end = tw.end;
		});
		unsubscribe();

		const [overviewRes, talkersRes, pairsRes] = await Promise.all([
			tailscaleService.getStatsOverview(start!, end!),
			tailscaleService.getTopTalkers(start!, end!, 15),
			tailscaleService.getTopPairs(start!, end!, 15)
		]);

		statsState.set({
			summary: overviewRes.summary,
			buckets: overviewRes.buckets,
			topTalkers: talkersRes.talkers || [],
			topPairs: pairsRes.pairs || [],
			isLoading: false,
			error: null
		});
	} catch (err) {
		statsState.update((s) => ({
			...s,
			isLoading: false,
			error: err instanceof Error ? err.message : 'Failed to load stats'
		}));
	}
}

export function startStatsRefresh(intervalMs = 60_000) {
	stopStatsRefresh();
	loadStats();
	refreshTimer = setInterval(loadStats, intervalMs);
}

export function stopStatsRefresh() {
	if (refreshTimer) {
		clearInterval(refreshTimer);
		refreshTimer = null;
	}
}

// Derived stores for individual stat sections
export const statsSummary = derived(statsState, ($s) => $s.summary);
export const statsBuckets = derived(statsState, ($s) => $s.buckets);
export const topTalkers = derived(statsState, ($s) => $s.topTalkers);
export const topPairs = derived(statsState, ($s) => $s.topPairs);
export const statsLoading = derived(statsState, ($s) => $s.isLoading);
export const statsError = derived(statsState, ($s) => $s.error);

// Derived: parse top ports from the most recent bucket
export const topPorts = derived(statsState, ($s): PortStat[] => {
	if ($s.buckets.length === 0) return [];
	// Aggregate ports across all buckets
	const portMap = new Map<string, PortStat>();
	for (const bucket of $s.buckets) {
		try {
			const ports: PortStat[] = JSON.parse(bucket.topPorts || '[]');
			for (const p of ports) {
				const key = `${p.proto}:${p.port}`;
				const existing = portMap.get(key);
				if (existing) {
					existing.bytes += p.bytes;
				} else {
					portMap.set(key, { ...p });
				}
			}
		} catch {
			// skip malformed JSON
		}
	}
	return Array.from(portMap.values())
		.sort((a, b) => b.bytes - a.bytes)
		.slice(0, 15);
});
```

**Step 2: Export from stores/index.ts**

Add to `frontend/src/lib/stores/index.ts`:

```typescript
export {
	loadStats,
	startStatsRefresh,
	stopStatsRefresh,
	statsSummary,
	statsBuckets,
	topTalkers,
	topPairs,
	topPorts,
	statsLoading,
	statsError
} from './stats-store';
```

**Step 3: Commit**

```bash
git add frontend/src/lib/stores/stats-store.ts frontend/src/lib/stores/index.ts
git commit -m "add stats store with derived stores for overview, talkers, pairs, ports"
```

---

### Task 8: Add Port Name Utility

**Files:**
- Modify: `frontend/src/lib/utils/protocol.ts`

**Step 1: Add well-known port names**

Add to `frontend/src/lib/utils/protocol.ts`:

```typescript
const WELL_KNOWN_PORTS: Record<number, string> = {
	22: 'SSH',
	53: 'DNS',
	80: 'HTTP',
	443: 'HTTPS',
	853: 'DoT',
	3389: 'RDP',
	5432: 'PostgreSQL',
	3306: 'MySQL',
	6379: 'Redis',
	8080: 'HTTP-Alt',
	8443: 'HTTPS-Alt',
	41641: 'Tailscale',
};

export function getPortName(port: number): string {
	return WELL_KNOWN_PORTS[port] || `${port}`;
}

export function getPortLabel(port: number, proto: number): string {
	const name = WELL_KNOWN_PORTS[port];
	const protoName = getProtocolName(proto).toUpperCase();
	return name ? `${name} (${port}/${protoName})` : `${port}/${protoName}`;
}
```

**Step 2: Commit**

```bash
git add frontend/src/lib/utils/protocol.ts
git commit -m "add well-known port name mappings for stats display"
```

---

### Task 9: Create Analytics Page with Charts

**Files:**
- Create: `frontend/src/routes/analytics/+page.svelte`
- Create: `frontend/src/lib/components/charts/DonutChart.svelte`
- Create: `frontend/src/lib/components/charts/BarChart.svelte`
- Create: `frontend/src/lib/components/charts/StatCard.svelte`

**Step 1: Create DonutChart.svelte**

Create `frontend/src/lib/components/charts/DonutChart.svelte`:

```svelte
<script lang="ts">
	interface Segment {
		label: string;
		value: number;
		color: string;
	}

	let { segments, size = 200, strokeWidth = 32 }: { segments: Segment[]; size?: number; strokeWidth?: number } = $props();

	const radius = $derived((size - strokeWidth) / 2);
	const circumference = $derived(2 * Math.PI * radius);
	const center = $derived(size / 2);
	const total = $derived(segments.reduce((sum, s) => sum + s.value, 0));

	const arcs = $derived.by(() => {
		if (total === 0) return [];
		let offset = 0;
		return segments.filter(s => s.value > 0).map((s) => {
			const pct = s.value / total;
			const dashLen = circumference * pct;
			const dashOff = circumference * offset;
			offset += pct;
			return { ...s, pct, dashLen, dashOff };
		});
	});
</script>

<div class="flex items-center gap-4">
	<svg width={size} height={size} viewBox="0 0 {size} {size}" class="shrink-0">
		{#if total === 0}
			<circle cx={center} cy={center} r={radius} fill="none"
				stroke="currentColor" stroke-width={strokeWidth} class="text-muted/20" />
		{:else}
			{#each arcs as arc}
				<circle cx={center} cy={center} r={radius} fill="none"
					stroke={arc.color} stroke-width={strokeWidth}
					stroke-dasharray="{arc.dashLen} {circumference - arc.dashLen}"
					stroke-dashoffset={-arc.dashOff}
					transform="rotate(-90 {center} {center})"
					class="transition-all duration-300" />
			{/each}
		{/if}
	</svg>
	<div class="flex flex-col gap-1.5 text-sm">
		{#each arcs as arc}
			<div class="flex items-center gap-2">
				<div class="h-3 w-3 rounded-sm" style="background-color: {arc.color}"></div>
				<span class="text-muted-foreground">{arc.label}</span>
				<span class="font-medium">{(arc.pct * 100).toFixed(1)}%</span>
			</div>
		{/each}
	</div>
</div>
```

**Step 2: Create BarChart.svelte**

Create `frontend/src/lib/components/charts/BarChart.svelte`:

```svelte
<script lang="ts">
	import { formatBytes } from '$lib/utils';

	interface Bar {
		label: string;
		value: number;
		color?: string;
	}

	let { bars, height = 300 }: { bars: Bar[]; height?: number } = $props();

	const maxVal = $derived(Math.max(...bars.map((b) => b.value), 1));
	const barHeight = $derived(Math.max(20, Math.min(32, (height - 16) / Math.max(bars.length, 1))));
</script>

<div class="flex flex-col gap-1" style="max-height: {height}px; overflow-y: auto;">
	{#each bars as bar, i}
		<div class="flex items-center gap-2 text-sm">
			<span class="w-36 truncate text-right text-muted-foreground" title={bar.label}>{bar.label}</span>
			<div class="relative flex-1" style="height: {barHeight - 4}px">
				<div
					class="absolute inset-y-0 left-0 rounded-r transition-all duration-300"
					style="width: {(bar.value / maxVal) * 100}%; background-color: {bar.color || 'var(--color-primary)'}; min-width: 2px;"
				></div>
			</div>
			<span class="w-20 text-right font-mono text-xs">{formatBytes(bar.value)}</span>
		</div>
	{/each}
</div>
```

**Step 3: Create StatCard.svelte**

Create `frontend/src/lib/components/charts/StatCard.svelte`:

```svelte
<script lang="ts">
	import type { Snippet } from 'svelte';

	let { label, value, icon }: { label: string; value: string; icon?: Snippet } = $props();
</script>

<div class="rounded-lg border border-border bg-card p-4">
	<div class="flex items-center gap-2 text-sm text-muted-foreground">
		{#if icon}
			{@render icon()}
		{/if}
		{label}
	</div>
	<div class="mt-1 text-2xl font-bold">{value}</div>
</div>
```

**Step 4: Create the analytics page**

Create `frontend/src/routes/analytics/+page.svelte`:

```svelte
<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Activity, Network, Link, ArrowUpDown, Loader2 } from 'lucide-svelte';
	import Header from '$lib/components/layout/Header.svelte';
	import DonutChart from '$lib/components/charts/DonutChart.svelte';
	import BarChart from '$lib/components/charts/BarChart.svelte';
	import StatCard from '$lib/components/charts/StatCard.svelte';
	import {
		startStatsRefresh, stopStatsRefresh,
		statsSummary, topTalkers, topPairs, topPorts,
		statsLoading, statsError
	} from '$lib/stores';
	import { formatBytes } from '$lib/utils';
	import { getPortLabel } from '$lib/utils/protocol';

	onMount(() => {
		startStatsRefresh(60_000);
	});

	onDestroy(() => {
		stopStatsRefresh();
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
			color: p.proto === 6 ? 'var(--color-primary)' : 'var(--color-traffic-subnet)'
		}));
	});

	const totalBytes = $derived($statsSummary ? $statsSummary.tcpBytes + $statsSummary.udpBytes + $statsSummary.otherProtoBytes : 0);
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
				<StatCard label="Total Traffic" value={formatBytes(totalBytes)}>
					{#snippet icon()}<Activity class="h-4 w-4" />{/snippet}
				</StatCard>
				<StatCard label="Total Flows" value={($statsSummary?.totalFlows ?? 0).toLocaleString()}>
					{#snippet icon()}<ArrowUpDown class="h-4 w-4" />{/snippet}
				</StatCard>
				<StatCard label="Unique Pairs" value={($statsSummary?.uniquePairs ?? 0).toLocaleString()}>
					{#snippet icon()}<Link class="h-4 w-4" />{/snippet}
				</StatCard>
				<StatCard label="Active Devices" value={$topTalkers.length.toString()}>
					{#snippet icon()}<Network class="h-4 w-4" />{/snippet}
				</StatCard>
			</div>

			<!-- Distribution Charts -->
			<div class="mb-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
				<div class="rounded-lg border border-border bg-card p-4">
					<h3 class="mb-3 text-sm font-medium text-muted-foreground">Protocol Distribution</h3>
					<DonutChart segments={protoSegments} />
				</div>
				<div class="rounded-lg border border-border bg-card p-4">
					<h3 class="mb-3 text-sm font-medium text-muted-foreground">Traffic Type Distribution</h3>
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
									<th class="pb-2 pr-4 text-right">TX</th>
									<th class="pb-2 pr-4 text-right">RX</th>
									<th class="pb-2 text-right">Total</th>
								</tr>
							</thead>
							<tbody>
								{#each $topTalkers as talker, i}
									<tr class="border-b border-border/50 hover:bg-secondary/50">
										<td class="py-1.5 pr-4 text-muted-foreground">{i + 1}</td>
										<td class="py-1.5 pr-4 font-mono text-xs">{talker.nodeId}</td>
										<td class="py-1.5 pr-4 text-right">{formatBytes(talker.txBytes)}</td>
										<td class="py-1.5 pr-4 text-right">{formatBytes(talker.rxBytes)}</td>
										<td class="py-1.5 text-right font-medium">{formatBytes(talker.totalBytes)}</td>
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
									<th class="pb-2 pr-4 text-right">Traffic</th>
									<th class="pb-2 text-right">Flows</th>
								</tr>
							</thead>
							<tbody>
								{#each $topPairs as pair, i}
									<tr class="border-b border-border/50 hover:bg-secondary/50">
										<td class="py-1.5 pr-4 text-muted-foreground">{i + 1}</td>
										<td class="py-1.5 pr-4 font-mono text-xs">{pair.srcNodeId}</td>
										<td class="py-1.5 pr-4 font-mono text-xs">{pair.dstNodeId}</td>
										<td class="py-1.5 pr-4 text-right font-medium">{formatBytes(pair.totalBytes)}</td>
										<td class="py-1.5 text-right">{pair.flowCount.toLocaleString()}</td>
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
```

**Step 5: Commit**

```bash
git add frontend/src/routes/analytics/+page.svelte \
       frontend/src/lib/components/charts/DonutChart.svelte \
       frontend/src/lib/components/charts/BarChart.svelte \
       frontend/src/lib/components/charts/StatCard.svelte
git commit -m "add analytics dashboard page with donut charts, bar charts, and ranking tables"
```

---

### Task 10: Add Navigation to Header

**Files:**
- Modify: `frontend/src/lib/components/layout/Header.svelte`

**Step 1: Add navigation links**

In `Header.svelte`, add `BarChart3` to the lucide-svelte import (line 2):

```typescript
import { RefreshCw, PanelLeft, ScrollText, Sun, Moon, Monitor, Network, Link, Activity, BarChart3 } from 'lucide-svelte';
```

Add a nav section between the logo div and center stats div. After the closing `</div>` of the logo section (after the `<h1>TSFlow</h1>` parent div), add:

```svelte
		<!-- Navigation -->
		<nav class="flex items-center gap-1">
			<a
				href="/"
				class="flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm hover:bg-secondary"
				class:bg-secondary={currentPath === '/'}
			>
				<Network class="h-4 w-4" />
				<span class="hidden sm:inline">Graph</span>
			</a>
			<a
				href="/analytics"
				class="flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm hover:bg-secondary"
				class:bg-secondary={currentPath === '/analytics'}
			>
				<BarChart3 class="h-4 w-4" />
				<span class="hidden sm:inline">Analytics</span>
			</a>
		</nav>
```

Add page detection at the top of the script block (after the imports):

```typescript
	import { page } from '$app/stores';
	const currentPath = $derived($page.url.pathname);
```

**Step 2: Commit**

```bash
git add frontend/src/lib/components/layout/Header.svelte
git commit -m "add graph/analytics navigation links to header"
```

---

### Task 11: Build and Verify

**Step 1: Build backend**

Run: `cd /Users/rajsingh/Documents/GitHub/tsflow/backend && go build ./...`

Expected: PASS

**Step 2: Build frontend**

Run: `cd /Users/rajsingh/Documents/GitHub/tsflow/frontend && npm run build`

Expected: PASS (outputs to `backend/frontend/dist/`)

**Step 3: Manual verification**

Start the app:
```bash
cd /Users/rajsingh/Documents/GitHub/tsflow/backend && go run main.go
```

Verify:
1. Visit `http://localhost:3000` — graph view works as before
2. Click "Analytics" in header — navigates to `/analytics`
3. Dashboard shows stat cards, donut charts, tables, and port bar chart
4. Hit `http://localhost:8080/api/stats/overview` — returns JSON with protocol/traffic breakdowns
5. Hit `http://localhost:8080/api/stats/top-talkers` — returns ranked node list
6. Hit `http://localhost:8080/api/stats/top-pairs` — returns ranked pair list

**Step 4: Final commit if any fixes needed**

```bash
git add -A && git commit -m "fix any build issues from integration"
```
