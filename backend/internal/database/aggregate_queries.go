package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

var allowedTableGroups = map[string]bool{
	"node_pairs":        true,
	"bandwidth":         true,
	"bandwidth_by_node": true,
	"traffic_stats":     true,
}

// selectTable picks the best aggregation table for a time range, cascading to coarser
// tables when the preferred (finest-granularity) table has no data in the range.
// tableGroup should be one of: "node_pairs", "bandwidth", "bandwidth_by_node", "traffic_stats".
// Caller must already hold s.mu (read or write lock).
func (s *SQLiteStore) selectTable(ctx context.Context, tableGroup string, startUnix, endUnix, rangeSeconds int64) string {
	if !allowedTableGroups[tableGroup] {
		// Defensive: reject unknown table groups to prevent SQL injection
		return "node_pairs_daily"
	}

	tiers := []struct {
		suffix    string
		threshold int64 // range must be <= this to prefer this tier (0 = always)
	}{
		{"_minutely", 24 * 3600},
		{"_hourly", 7 * 24 * 3600},
		{"_daily", 0},
	}

	// Build candidate list: preferred tier first, then coarser fallbacks
	var candidates []string
	started := false
	for _, tier := range tiers {
		if !started {
			if tier.threshold == 0 || rangeSeconds <= tier.threshold {
				started = true
			}
		}
		if started {
			candidates = append(candidates, tableGroup+tier.suffix)
		}
	}

	for _, t := range candidates {
		var count int64
		_ = s.db.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT COUNT(*) FROM %s WHERE bucket >= ? AND bucket <= ?", t,
		), startUnix, endUnix).Scan(&count)
		if count > 0 {
			return t
		}
	}
	// No data anywhere; return the coarsest table (will return empty results)
	return candidates[len(candidates)-1]
}

// CommitPollResults atomically writes all aggregates and updates poll state in a single transaction.
// This prevents double-counting on crash recovery: either everything commits or nothing does.
func (s *SQLiteStore) CommitPollResults(ctx context.Context, results PollResults) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := upsertNodePairsTx(ctx, tx, results.NodePairs); err != nil {
		return err
	}
	if err := upsertBandwidthTx(ctx, tx, results.Bandwidth, 60); err != nil {
		return err
	}
	if err := upsertNodeBandwidthTx(ctx, tx, results.NodeBandwidth, 60); err != nil {
		return err
	}
	if err := upsertTrafficStatsTx(ctx, tx, results.TrafficStats); err != nil {
		return err
	}

	// Update poll state in the same transaction
	const sqliteFormat = "2006-01-02 15:04:05"
	_, err = tx.ExecContext(ctx,
		"UPDATE poll_state SET last_poll_end = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
		results.PollEnd.UTC().Format(sqliteFormat),
	)
	if err != nil {
		return fmt.Errorf("failed to update poll state: %w", err)
	}

	return tx.Commit()
}

// upsertNodePairsTx inserts node-pair aggregates within an existing transaction.
// Protocols are merged (union of existing + new) to avoid data loss across poll batches.
func upsertNodePairsTx(ctx context.Context, tx *sql.Tx, aggregates []NodePairAggregate) error {
	if len(aggregates) == 0 {
		return nil
	}

	tables := []string{"node_pairs_minutely", "node_pairs_hourly", "node_pairs_daily"}
	bucketSizes := []int64{60, 3600, 86400}

	for i, table := range tables {
		bucketSize := bucketSizes[i]
		stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (bucket, src_node_id, dst_node_id, traffic_type, tx_bytes, rx_bytes, tx_pkts, rx_pkts, flow_count, protocols, ports)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(bucket, src_node_id, dst_node_id, traffic_type) DO UPDATE SET
				tx_bytes = tx_bytes + excluded.tx_bytes,
				rx_bytes = rx_bytes + excluded.rx_bytes,
				tx_pkts = tx_pkts + excluded.tx_pkts,
				rx_pkts = rx_pkts + excluded.rx_pkts,
				flow_count = flow_count + excluded.flow_count,
				protocols = (SELECT json_group_array(value) FROM (
					SELECT value FROM json_each(%s.protocols)
					UNION
					SELECT value FROM json_each(excluded.protocols)
				)),
				ports = excluded.ports
		`, table, table))
		if err != nil {
			return fmt.Errorf("failed to prepare statement for %s: %w", table, err)
		}
		err = func() error {
			defer stmt.Close()
			for _, agg := range aggregates {
				bucket := (agg.Bucket / bucketSize) * bucketSize
				_, err := stmt.ExecContext(ctx,
					bucket, agg.SrcNodeID, agg.DstNodeID, agg.TrafficType,
					agg.TxBytes, agg.RxBytes, agg.TxPkts, agg.RxPkts,
					agg.FlowCount, agg.Protocols, agg.Ports,
				)
				if err != nil {
					return fmt.Errorf("failed to upsert aggregate: %w", err)
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

// UpsertNodePairAggregates upserts node-pair aggregates into appropriate tier tables
func (s *SQLiteStore) UpsertNodePairAggregates(ctx context.Context, aggregates []NodePairAggregate) error {
	if len(aggregates) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := upsertNodePairsTx(ctx, tx, aggregates); err != nil {
		return err
	}

	return tx.Commit()
}

// GetNodePairAggregates retrieves node-pair aggregates for a time range
func (s *SQLiteStore) GetNodePairAggregates(ctx context.Context, start, end time.Time, bucketSize int64) ([]NodePairAggregate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()

	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix

	table := s.selectTable(ctx, "node_pairs", startUnix, endUnix, rangeSeconds)

	query := fmt.Sprintf(`
		SELECT MIN(bucket), src_node_id, dst_node_id, traffic_type,
			   SUM(tx_bytes), SUM(rx_bytes), SUM(tx_pkts), SUM(rx_pkts),
			   SUM(flow_count),
			   COALESCE((SELECT protocols FROM %s sub
			    WHERE sub.src_node_id = main.src_node_id
			      AND sub.dst_node_id = main.dst_node_id
			      AND sub.traffic_type = main.traffic_type
			      AND sub.bucket >= ? AND sub.bucket <= ?
			    ORDER BY sub.bucket DESC LIMIT 1), '[]') as protocols,
			   COALESCE((SELECT ports FROM %s sub
			    WHERE sub.src_node_id = main.src_node_id
			      AND sub.dst_node_id = main.dst_node_id
			      AND sub.traffic_type = main.traffic_type
			      AND sub.bucket >= ? AND sub.bucket <= ?
			    ORDER BY sub.bucket DESC LIMIT 1), '[]') as ports
		FROM %s main
		WHERE bucket >= ? AND bucket <= ?
		GROUP BY src_node_id, dst_node_id, traffic_type
		ORDER BY SUM(tx_bytes) + SUM(rx_bytes) DESC
	`, table, table, table)

	rows, err := s.db.QueryContext(ctx, query, startUnix, endUnix, startUnix, endUnix, startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("failed to query aggregates: %w", err)
	}
	defer rows.Close()

	var aggregates []NodePairAggregate
	for rows.Next() {
		var agg NodePairAggregate
		err := rows.Scan(
			&agg.Bucket, &agg.SrcNodeID, &agg.DstNodeID, &agg.TrafficType,
			&agg.TxBytes, &agg.RxBytes, &agg.TxPkts, &agg.RxPkts,
			&agg.FlowCount, &agg.Protocols, &agg.Ports,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan aggregate: %w", err)
		}
		aggregates = append(aggregates, agg)
	}

	return aggregates, rows.Err()
}

func upsertBandwidthTx(ctx context.Context, tx *sql.Tx, buckets []BandwidthBucket, _ int64) error {
	if len(buckets) == 0 {
		return nil
	}

	tables := []string{"bandwidth_minutely", "bandwidth_hourly", "bandwidth_daily"}
	bucketSizes := []int64{60, 3600, 86400}

	for i, table := range tables {
		bs := bucketSizes[i]
		stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (bucket, tx_bytes, rx_bytes) VALUES (?, ?, ?)
			ON CONFLICT(bucket) DO UPDATE SET
				tx_bytes = tx_bytes + excluded.tx_bytes,
				rx_bytes = rx_bytes + excluded.rx_bytes
		`, table))
		if err != nil {
			return fmt.Errorf("failed to prepare statement for %s: %w", table, err)
		}
		err = func() error {
			defer stmt.Close()
			for _, b := range buckets {
				bucket := (b.Time.UTC().Unix() / bs) * bs
				_, err := stmt.ExecContext(ctx, bucket, b.TxBytes, b.RxBytes)
				if err != nil {
					return fmt.Errorf("failed to upsert bandwidth: %w", err)
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

// UpsertBandwidth upserts total bandwidth into appropriate tier tables
func (s *SQLiteStore) UpsertBandwidth(ctx context.Context, buckets []BandwidthBucket, bucketSize int64) error {
	if len(buckets) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := upsertBandwidthTx(ctx, tx, buckets, bucketSize); err != nil {
		return err
	}

	return tx.Commit()
}

func upsertNodeBandwidthTx(ctx context.Context, tx *sql.Tx, buckets []NodeBandwidth, _ int64) error {
	if len(buckets) == 0 {
		return nil
	}

	tables := []string{"bandwidth_by_node_minutely", "bandwidth_by_node_hourly", "bandwidth_by_node_daily"}
	bucketSizes := []int64{60, 3600, 86400}

	for i, table := range tables {
		bs := bucketSizes[i]
		stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (bucket, node_id, tx_bytes, rx_bytes) VALUES (?, ?, ?, ?)
			ON CONFLICT(bucket, node_id) DO UPDATE SET
				tx_bytes = tx_bytes + excluded.tx_bytes,
				rx_bytes = rx_bytes + excluded.rx_bytes
		`, table))
		if err != nil {
			return fmt.Errorf("failed to prepare statement for %s: %w", table, err)
		}
		err = func() error {
			defer stmt.Close()
			for _, b := range buckets {
				bucket := (b.Bucket / bs) * bs
				_, err := stmt.ExecContext(ctx, bucket, b.NodeID, b.TxBytes, b.RxBytes)
				if err != nil {
					return fmt.Errorf("failed to upsert node bandwidth: %w", err)
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

// UpsertNodeBandwidth upserts per-node bandwidth into appropriate tier tables
func (s *SQLiteStore) UpsertNodeBandwidth(ctx context.Context, buckets []NodeBandwidth, bucketSize int64) error {
	if len(buckets) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := upsertNodeBandwidthTx(ctx, tx, buckets, bucketSize); err != nil {
		return err
	}

	return tx.Commit()
}

// GetBandwidth retrieves total bandwidth for a time range
func (s *SQLiteStore) GetBandwidth(ctx context.Context, start, end time.Time) ([]BandwidthBucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()

	// Validate time range - start must be before end
	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix

	table := s.selectTable(ctx, "bandwidth", startUnix, endUnix, rangeSeconds)

	query := fmt.Sprintf(`
		SELECT bucket, tx_bytes, rx_bytes
		FROM %s
		WHERE bucket >= ? AND bucket <= ?
		ORDER BY bucket ASC
	`, table)

	rows, err := s.db.QueryContext(ctx, query, startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("failed to query bandwidth: %w", err)
	}
	defer rows.Close()

	var result []BandwidthBucket
	for rows.Next() {
		var bucket int64
		var b BandwidthBucket
		err := rows.Scan(&bucket, &b.TxBytes, &b.RxBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to scan bucket: %w", err)
		}
		b.Time = time.Unix(bucket, 0).UTC()
		result = append(result, b)
	}

	return result, rows.Err()
}

// GetNodeBandwidth retrieves bandwidth for a specific node
func (s *SQLiteStore) GetNodeBandwidth(ctx context.Context, start, end time.Time, nodeID string) ([]BandwidthBucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()

	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix

	table := s.selectTable(ctx, "bandwidth_by_node", startUnix, endUnix, rangeSeconds)

	query := fmt.Sprintf(`
		SELECT bucket, tx_bytes, rx_bytes
		FROM %s
		WHERE bucket >= ? AND bucket <= ? AND node_id = ?
		ORDER BY bucket ASC
	`, table)

	rows, err := s.db.QueryContext(ctx, query, startUnix, endUnix, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query node bandwidth: %w", err)
	}
	defer rows.Close()

	var result []BandwidthBucket
	for rows.Next() {
		var bucket int64
		var b BandwidthBucket
		err := rows.Scan(&bucket, &b.TxBytes, &b.RxBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to scan bucket: %w", err)
		}
		b.Time = time.Unix(bucket, 0).UTC()
		result = append(result, b)
	}

	return result, rows.Err()
}

func upsertTrafficStatsTx(ctx context.Context, tx *sql.Tx, stats []TrafficStats) error {
	if len(stats) == 0 {
		return nil
	}

	tables := []string{"traffic_stats_minutely", "traffic_stats_hourly", "traffic_stats_daily"}
	bucketSizes := []int64{60, 3600, 86400}

	for i, table := range tables {
		bs := bucketSizes[i]
		stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (bucket, tcp_bytes, udp_bytes, other_proto_bytes, virtual_bytes, subnet_bytes, physical_bytes, total_flows, unique_pairs, top_ports)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(bucket) DO UPDATE SET
				tcp_bytes = tcp_bytes + excluded.tcp_bytes,
				udp_bytes = udp_bytes + excluded.udp_bytes,
				other_proto_bytes = other_proto_bytes + excluded.other_proto_bytes,
				virtual_bytes = virtual_bytes + excluded.virtual_bytes,
				subnet_bytes = subnet_bytes + excluded.subnet_bytes,
				physical_bytes = physical_bytes + excluded.physical_bytes,
				total_flows = total_flows + excluded.total_flows,
				unique_pairs = MAX(unique_pairs, excluded.unique_pairs),
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
	return nil
}

// UpsertTrafficStats upserts network-wide traffic statistics into tiered tables
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

	if err := upsertTrafficStatsTx(ctx, tx, stats); err != nil {
		return err
	}

	return tx.Commit()
}

// GetTrafficStats retrieves network-wide traffic statistics for a time range
func (s *SQLiteStore) GetTrafficStats(ctx context.Context, start, end time.Time) ([]TrafficStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()

	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix

	table := s.selectTable(ctx, "traffic_stats", startUnix, endUnix, rangeSeconds)

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

	results := make([]TrafficStats, 0)
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
		results = append(results, st)
	}

	return results, rows.Err()
}

// GetTrafficStatsFromNodePairs synthesizes traffic stats from node_pairs data
// Used as fallback when traffic_stats tables are empty (e.g. historical data predating that table)
func (s *SQLiteStore) GetTrafficStatsFromNodePairs(ctx context.Context, start, end time.Time) ([]TrafficStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()

	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix
	table := s.selectTable(ctx, "node_pairs", startUnix, endUnix, rangeSeconds)

	query := fmt.Sprintf(`
		SELECT bucket, traffic_type,
		       SUM(tx_bytes + rx_bytes) as total_bytes,
		       SUM(flow_count) as total_flows,
		       COUNT(DISTINCT src_node_id || '|' || dst_node_id) as unique_pairs
		FROM %s
		WHERE bucket >= ? AND bucket <= ?
		GROUP BY bucket, traffic_type
		ORDER BY bucket ASC
	`, table)

	rows, err := s.db.QueryContext(ctx, query, startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("failed to query node pairs for traffic stats: %w", err)
	}
	defer rows.Close()

	// Track unique pairs across all traffic types per bucket to avoid overcounting
	bucketUniquePairs := make(map[int64]map[string]struct{})
	bucketMap := make(map[int64]*TrafficStats)
	for rows.Next() {
		var bucket int64
		var trafficType string
		var totalBytes, totalFlows, uniquePairs int64
		if err := rows.Scan(&bucket, &trafficType, &totalBytes, &totalFlows, &uniquePairs); err != nil {
			return nil, fmt.Errorf("failed to scan: %w", err)
		}
		st, ok := bucketMap[bucket]
		if !ok {
			st = &TrafficStats{Bucket: bucket, TopPorts: "[]"}
			bucketMap[bucket] = st
		}
		switch trafficType {
		case "virtual":
			st.VirtualBytes += totalBytes
		case "subnet":
			st.SubnetBytes += totalBytes
		case "exit":
			st.VirtualBytes += totalBytes // Count exit as virtual for traffic type distribution
		case "physical":
			st.PhysicalBytes += totalBytes
		}
		// Don't guess protocol here - will be derived from protocols/ports columns below
		st.TotalFlows += totalFlows
		// Don't sum uniquePairs across traffic types - will compute below
		_ = uniquePairs
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Compute unique pairs per bucket across all traffic types (avoids overcounting
	// the same node pair appearing in both virtual and subnet traffic)
	for _, table2 := range []string{table} {
		pairRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT bucket, COUNT(DISTINCT src_node_id || '|' || dst_node_id) as unique_pairs
			FROM %s
			WHERE bucket >= ? AND bucket <= ?
			GROUP BY bucket
		`, table2), startUnix, endUnix)
		if err != nil {
			break // best effort
		}
		for pairRows.Next() {
			var bucket, up int64
			if err := pairRows.Scan(&bucket, &up); err != nil {
				break
			}
			if st, ok := bucketMap[bucket]; ok {
				st.UniquePairs = up
			}
		}
		pairRows.Close()
	}
	_ = bucketUniquePairs

	// Derive per-protocol breakdown from the protocols JSON column in node_pairs.
	// Each row has a JSON array like [6], [17], or [6,17] indicating protocols seen.
	// We split the row's bytes proportionally across its protocols.
	protoRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT bucket, protocols, SUM(tx_bytes + rx_bytes) as total_bytes
		FROM %s
		WHERE bucket >= ? AND bucket <= ? AND traffic_type != 'physical'
		GROUP BY bucket, protocols
	`, table), startUnix, endUnix)
	if err == nil {
		for protoRows.Next() {
			var bucket int64
			var protosJSON string
			var totalBytes int64
			if err := protoRows.Scan(&bucket, &protosJSON, &totalBytes); err != nil {
				continue
			}
			st, ok := bucketMap[bucket]
			if !ok {
				continue
			}
			var protos []int
			if err := json.Unmarshal([]byte(protosJSON), &protos); err != nil || len(protos) == 0 {
				// Unknown protocol - leave unattributed for now
				continue
			}
			// Split bytes across protocols seen in this group
			perProto := totalBytes / int64(len(protos))
			remainder := totalBytes - perProto*int64(len(protos))
			for i, p := range protos {
				share := perProto
				if i == 0 {
					share += remainder
				}
				switch p {
				case 6:
					st.TCPBytes += share
				case 17:
					st.UDPBytes += share
				default:
					st.OtherProtoBytes += share
				}
			}
		}
		protoRows.Close()
	}

	// Try to derive more accurate per-protocol breakdown and top ports from the ports JSON column
	portRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT bucket, ports FROM %s
		WHERE bucket >= ? AND bucket <= ? AND ports != '[]'
	`, table), startUnix, endUnix)
	if err == nil {
		// Accumulate per-protocol bytes and per-port bytes from port entries
		type protoBucket struct {
			tcpBytes   int64
			udpBytes   int64
			otherBytes int64
		}
		type protoPortKey struct {
			proto int
			port  int
		}
		protoMap := make(map[int64]*protoBucket)
		portAggMap := make(map[int64]map[protoPortKey]int64) // bucket -> port -> bytes
		for portRows.Next() {
			var bucket int64
			var portsJSON string
			if err := portRows.Scan(&bucket, &portsJSON); err != nil {
				continue
			}
			var entries []PortStat
			if err := json.Unmarshal([]byte(portsJSON), &entries); err != nil {
				continue
			}
			pb, ok := protoMap[bucket]
			if !ok {
				pb = &protoBucket{}
				protoMap[bucket] = pb
			}
			if _, ok := portAggMap[bucket]; !ok {
				portAggMap[bucket] = make(map[protoPortKey]int64)
			}
			for _, e := range entries {
				switch e.Proto {
				case 6:
					pb.tcpBytes += e.Bytes
				case 17:
					pb.udpBytes += e.Bytes
				default:
					pb.otherBytes += e.Bytes
				}
				portAggMap[bucket][protoPortKey{proto: e.Proto, port: e.Port}] += e.Bytes
			}
		}
		portRows.Close()

		// Override the TCP-only estimate with actual per-protocol data where available
		// and compute top ports per bucket
		for bucket, pb := range protoMap {
			if st, ok := bucketMap[bucket]; ok {
				totalProtoBytes := pb.tcpBytes + pb.udpBytes + pb.otherBytes
				if totalProtoBytes > 0 {
					st.TCPBytes = pb.tcpBytes
					st.UDPBytes = pb.udpBytes
					st.OtherProtoBytes = pb.otherBytes
				}
			}
		}
		for bucket, portMap := range portAggMap {
			if st, ok := bucketMap[bucket]; ok {
				ports := make([]PortStat, 0, len(portMap))
				for ppk, bytes := range portMap {
					ports = append(ports, PortStat{Port: ppk.port, Proto: ppk.proto, Bytes: bytes})
				}
				sort.Slice(ports, func(i, j int) bool {
					return ports[i].Bytes > ports[j].Bytes
				})
				if len(ports) > 20 {
					ports = ports[:20]
				}
				if topPortsJSON, err := json.Marshal(ports); err == nil {
					st.TopPorts = string(topPortsJSON)
				}
			}
		}
	}

	// Final fallback: if protocol breakdown is still empty (legacy data with no
	// protocols/ports columns populated), attribute non-physical traffic bytes
	// as OtherProtoBytes so the Protocol Distribution chart shows something.
	for _, st := range bucketMap {
		if st.TCPBytes == 0 && st.UDPBytes == 0 && st.OtherProtoBytes == 0 {
			nonPhysical := st.VirtualBytes + st.SubnetBytes
			if nonPhysical > 0 {
				st.OtherProtoBytes = nonPhysical
			}
		}
	}

	results := make([]TrafficStats, 0, len(bucketMap))
	for _, st := range bucketMap {
		results = append(results, *st)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Bucket < results[j].Bucket
	})
	return results, nil
}

// GetTopTalkers returns nodes ranked by total traffic volume
func (s *SQLiteStore) GetTopTalkers(ctx context.Context, start, end time.Time, limit int) ([]TopTalker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()

	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix
	table := s.selectTable(ctx, "bandwidth_by_node", startUnix, endUnix, rangeSeconds)

	if limit <= 0 {
		limit = 10
	}

	query := fmt.Sprintf(`
		SELECT node_id, SUM(tx_bytes), SUM(rx_bytes), SUM(tx_bytes + rx_bytes) as total
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

	results := make([]TopTalker, 0)
	for rows.Next() {
		var t TopTalker
		err := rows.Scan(&t.NodeID, &t.TxBytes, &t.RxBytes, &t.TotalBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to scan top talker: %w", err)
		}
		results = append(results, t)
	}

	return results, rows.Err()
}

// GetTopPairs returns node pairs ranked by total traffic volume
func (s *SQLiteStore) GetTopPairs(ctx context.Context, start, end time.Time, limit int) ([]TopPair, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()

	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix
	table := s.selectTable(ctx, "node_pairs", startUnix, endUnix, rangeSeconds)

	if limit <= 0 {
		limit = 10
	}

	query := fmt.Sprintf(`
		SELECT src_node_id, dst_node_id, SUM(tx_bytes), SUM(rx_bytes),
		       SUM(tx_bytes + rx_bytes) as total, SUM(flow_count)
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

	results := make([]TopPair, 0)
	for rows.Next() {
		var p TopPair
		err := rows.Scan(&p.SrcNodeID, &p.DstNodeID, &p.TxBytes, &p.RxBytes, &p.TotalBytes, &p.FlowCount)
		if err != nil {
			return nil, fmt.Errorf("failed to scan top pair: %w", err)
		}
		results = append(results, p)
	}

	return results, rows.Err()
}

// GetNodeStats returns detailed traffic statistics for a single node
func (s *SQLiteStore) GetNodeStats(ctx context.Context, nodeID string, start, end time.Time) (*NodeDetailStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()

	if startUnix >= endUnix {
		return nil, fmt.Errorf("invalid time range: start (%v) must be before end (%v)", start, end)
	}

	rangeSeconds := endUnix - startUnix

	bwTable := s.selectTable(ctx, "bandwidth_by_node", startUnix, endUnix, rangeSeconds)
	pairsTable := s.selectTable(ctx, "node_pairs", startUnix, endUnix, rangeSeconds)

	result := &NodeDetailStats{
		NodeID:   nodeID,
		TopPeers: make([]TopPair, 0),
		TopPorts: make([]PortStat, 0),
	}

	// Get total TX/RX from bandwidth_by_node
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(tx_bytes), 0), COALESCE(SUM(rx_bytes), 0)
		FROM %s
		WHERE node_id = ? AND bucket >= ? AND bucket <= ?
	`, bwTable), nodeID, startUnix, endUnix).Scan(&result.TotalTx, &result.TotalRx)
	if err != nil {
		return nil, fmt.Errorf("failed to query node bandwidth: %w", err)
	}

	// Get top peers from node_pairs (where this node is src or dst)
	peerQuery := fmt.Sprintf(`
		SELECT peer_id, SUM(tx), SUM(rx), SUM(tx + rx) as total, SUM(fc)
		FROM (
			SELECT dst_node_id as peer_id, SUM(tx_bytes) as tx, SUM(rx_bytes) as rx, SUM(flow_count) as fc
			FROM %s
			WHERE src_node_id = ? AND bucket >= ? AND bucket <= ?
			GROUP BY dst_node_id
			UNION ALL
			SELECT src_node_id as peer_id, SUM(rx_bytes) as tx, SUM(tx_bytes) as rx, SUM(flow_count) as fc
			FROM %s
			WHERE dst_node_id = ? AND bucket >= ? AND bucket <= ?
			GROUP BY src_node_id
		)
		GROUP BY peer_id
		ORDER BY total DESC
		LIMIT 10
	`, pairsTable, pairsTable)

	rows, err := s.db.QueryContext(ctx, peerQuery,
		nodeID, startUnix, endUnix,
		nodeID, startUnix, endUnix,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query node peers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p TopPair
		err := rows.Scan(&p.DstNodeID, &p.TxBytes, &p.RxBytes, &p.TotalBytes, &p.FlowCount)
		if err != nil {
			return nil, fmt.Errorf("failed to scan peer: %w", err)
		}
		p.SrcNodeID = nodeID
		result.TopPeers = append(result.TopPeers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Get protocol breakdown and top ports from node_pairs ports column
	portsQuery := fmt.Sprintf(`
		SELECT ports
		FROM %s
		WHERE (src_node_id = ? OR dst_node_id = ?)
			AND bucket >= ? AND bucket <= ?
			AND ports != '[]'
	`, pairsTable)

	portRows, err := s.db.QueryContext(ctx, portsQuery, nodeID, nodeID, startUnix, endUnix)
	if err != nil {
		return nil, fmt.Errorf("failed to query node ports: %w", err)
	}
	defer portRows.Close()

	// Aggregate ports and protocol bytes across all pairs for this node
	type protoPortKey struct {
		proto int
		port  int
	}
	portAgg := make(map[protoPortKey]int64)

	for portRows.Next() {
		var portsJSON string
		if err := portRows.Scan(&portsJSON); err != nil {
			continue
		}
		var entries []PortStat
		if err := json.Unmarshal([]byte(portsJSON), &entries); err != nil {
			continue
		}
		for _, e := range entries {
			portAgg[protoPortKey{proto: e.Proto, port: e.Port}] += e.Bytes
		}
	}

	// Compute protocol totals
	for ppk, bytes := range portAgg {
		switch ppk.proto {
		case 6:
			result.TCPBytes += bytes
		case 17:
			result.UDPBytes += bytes
		default:
			result.OtherBytes += bytes
		}
	}

	// Build top ports list (top 15 by bytes)
	for ppk, bytes := range portAgg {
		result.TopPorts = append(result.TopPorts, PortStat{Port: ppk.port, Proto: ppk.proto, Bytes: bytes})
	}
	sort.Slice(result.TopPorts, func(i, j int) bool {
		return result.TopPorts[i].Bytes > result.TopPorts[j].Bytes
	})
	if len(result.TopPorts) > 15 {
		result.TopPorts = result.TopPorts[:15]
	}

	return result, nil
}

