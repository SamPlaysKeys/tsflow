package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// FlowLog represents a stored network flow log entry
type FlowLog struct {
	ID          int64     `json:"id"`
	LoggedAt    time.Time `json:"loggedAt"`
	NodeID      string    `json:"nodeId"`
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
	TrafficType string    `json:"trafficType"` // virtual, subnet, physical
	Protocol    int       `json:"protocol"`
	SrcIP       string    `json:"srcIp"`
	SrcPort     int       `json:"srcPort"`
	DstIP       string    `json:"dstIp"`
	DstPort     int       `json:"dstPort"`
	TxBytes     int64     `json:"txBytes"`
	RxBytes     int64     `json:"rxBytes"`
	TxPkts      int64     `json:"txPkts"`
	RxPkts      int64     `json:"rxPkts"`
	CreatedAt   time.Time `json:"createdAt"`
}

// PollState tracks the polling state
type PollState struct {
	LastPollEnd time.Time `json:"lastPollEnd"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// DataRange represents the available data time range
type DataRange struct {
	Earliest time.Time `json:"earliest"`
	Latest   time.Time `json:"latest"`
	Count    int64     `json:"count"`
}

// BandwidthBucket represents aggregated bandwidth for a time bucket
type BandwidthBucket struct {
	Time    time.Time `json:"time"`
	TxBytes int64     `json:"txBytes"`
	RxBytes int64     `json:"rxBytes"`
}

// AggregatedFlow represents aggregated node-to-node traffic for scalable queries
type AggregatedFlow struct {
	NodeID       string    `json:"nodeId"`
	TrafficType  string    `json:"trafficType"`
	SrcIP        string    `json:"srcIp"`
	SrcPort      int       `json:"srcPort"`
	DstIP        string    `json:"dstIp"`
	DstPort      int       `json:"dstPort"`
	Protocol     int       `json:"protocol"`
	TotalTxBytes int64     `json:"totalTxBytes"`
	TotalRxBytes int64     `json:"totalRxBytes"`
	TotalTxPkts  int64     `json:"totalTxPkts"`
	TotalRxPkts  int64     `json:"totalRxPkts"`
	FlowCount    int64     `json:"flowCount"`
	FirstSeen    time.Time `json:"firstSeen"`
	LastSeen     time.Time `json:"lastSeen"`
}

// Store defines the interface for flow log storage
// This abstraction allows easy migration to PostgreSQL later
type Store interface {
	// Initialize the database schema
	Init(ctx context.Context) error

	// Close the database connection
	Close() error

	// InsertFlowLogs inserts multiple flow logs in a batch
	// Returns the logs that were actually inserted (excludes duplicates)
	InsertFlowLogs(ctx context.Context, logs []FlowLog) ([]FlowLog, error)

	// GetFlowLogs retrieves flow logs within a time range
	GetFlowLogs(ctx context.Context, start, end time.Time, limit int) ([]FlowLog, error)

	// GetDataRange returns the available data time range
	GetDataRange(ctx context.Context) (*DataRange, error)

	// GetPollState retrieves the current poll state
	GetPollState(ctx context.Context) (*PollState, error)

	// UpdatePollState updates the poll state
	UpdatePollState(ctx context.Context, lastPollEnd time.Time) error

	// CleanupOldLogs removes logs older than the retention period
	CleanupOldLogs(ctx context.Context, retention time.Duration) (int64, error)

	// CleanupOldRollups removes old rollup data (minutely: 24h, hourly: 30d)
	CleanupOldRollups(ctx context.Context) (int64, error)

	// GetStats returns database statistics
	GetStats(ctx context.Context) (map[string]interface{}, error)

	// GetBandwidthAggregated returns aggregated bandwidth data in time buckets
	GetBandwidthAggregated(ctx context.Context, start, end time.Time, bucketSeconds int) ([]BandwidthBucket, error)

	// GetBandwidthByIPs returns bandwidth aggregated by time bucket filtered by node IPs
	GetBandwidthByIPs(ctx context.Context, start, end time.Time, ips []string) ([]BandwidthBucket, error)

	// GetAggregatedFlows returns node-to-node traffic aggregated by src/dst IP pairs
	// This is the scalable alternative to GetFlowLogs for large datasets
	GetAggregatedFlows(ctx context.Context, start, end time.Time) ([]AggregatedFlow, error)

	// UpdateBandwidthRollups updates the pre-aggregated bandwidth rollup tables
	UpdateBandwidthRollups(ctx context.Context, logs []FlowLog) error

	// BackfillBandwidthRollups rebuilds rollups from existing flow_logs
	BackfillBandwidthRollups(ctx context.Context) error
}

// SQLiteStore implements Store using SQLite with WAL mode
type SQLiteStore struct {
	db     *sql.DB
	dbPath string
	mu     sync.RWMutex
}

// parseTime parses a time string from SQLite in various formats
func parseTime(s string) time.Time {
	formats := []string{
		"2006-01-02 15:04:05.999999999 -0700 MST", // Go's default time.Time.String() format
		"2006-01-02 15:04:05.999999999 +0000 UTC", // Common UTC format
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02T15:04:05.999999999-07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// NewSQLiteStore creates a new SQLite store
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	// Open with WAL mode and busy timeout for concurrent access
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_cache_size=10000", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool for concurrent reads
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	store := &SQLiteStore{
		db:     db,
		dbPath: dbPath,
	}

	return store, nil
}

// Init creates the database schema
func (s *SQLiteStore) Init(ctx context.Context) error {
	schema := `
	-- Flow logs table with optimized schema for time-series queries
	CREATE TABLE IF NOT EXISTS flow_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		logged_at DATETIME NOT NULL,
		node_id TEXT NOT NULL,
		period_start DATETIME NOT NULL,
		period_end DATETIME NOT NULL,
		traffic_type TEXT NOT NULL,
		protocol INTEGER DEFAULT 0,
		src_ip TEXT NOT NULL,
		src_port INTEGER DEFAULT 0,
		dst_ip TEXT NOT NULL,
		dst_port INTEGER DEFAULT 0,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0,
		tx_pkts INTEGER DEFAULT 0,
		rx_pkts INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- Unique constraint to prevent duplicate log entries (deduplication)
	CREATE UNIQUE INDEX IF NOT EXISTS idx_flow_logs_unique ON flow_logs(
		logged_at, node_id, traffic_type, protocol, src_ip, src_port, dst_ip, dst_port
	);

	-- Indexes for common query patterns
	CREATE INDEX IF NOT EXISTS idx_flow_logs_logged_at ON flow_logs(logged_at);
	CREATE INDEX IF NOT EXISTS idx_flow_logs_node_id ON flow_logs(node_id);
	CREATE INDEX IF NOT EXISTS idx_flow_logs_traffic_type ON flow_logs(traffic_type);
	CREATE INDEX IF NOT EXISTS idx_flow_logs_src_ip ON flow_logs(src_ip);
	CREATE INDEX IF NOT EXISTS idx_flow_logs_dst_ip ON flow_logs(dst_ip);
	CREATE INDEX IF NOT EXISTS idx_flow_logs_logged_at_type ON flow_logs(logged_at, traffic_type);

	-- Covering index for bandwidth aggregation (query can be answered entirely from index)
	CREATE INDEX IF NOT EXISTS idx_flow_logs_bandwidth ON flow_logs(logged_at, traffic_type, tx_bytes, rx_bytes);

	-- Composite index for efficient aggregation queries (GROUP BY node_id, traffic_type, src_ip, dst_ip)
	CREATE INDEX IF NOT EXISTS idx_flow_logs_aggregation ON flow_logs(logged_at, node_id, traffic_type, src_ip, dst_ip);

	-- Poll state tracking (single row table)
	CREATE TABLE IF NOT EXISTS poll_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		last_poll_end DATETIME,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- Initialize poll state if not exists
	INSERT OR IGNORE INTO poll_state (id, last_poll_end, updated_at)
	VALUES (1, NULL, CURRENT_TIMESTAMP);

	-- Bandwidth rollup tables for O(1) historical queries
	-- Minutely: 1-minute buckets, for last 24 hours of detail
	CREATE TABLE IF NOT EXISTS bandwidth_minutely (
		bucket INTEGER PRIMARY KEY,  -- Unix timestamp truncated to minute
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0
	);

	-- Hourly: 1-hour buckets, for last 30 days
	CREATE TABLE IF NOT EXISTS bandwidth_hourly (
		bucket INTEGER PRIMARY KEY,  -- Unix timestamp truncated to hour
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0
	);

	-- Daily: 1-day buckets, kept indefinitely
	CREATE TABLE IF NOT EXISTS bandwidth_daily (
		bucket INTEGER PRIMARY KEY,  -- Unix timestamp truncated to day (UTC midnight)
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0
	);
	`

	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	log.Printf("Database initialized at %s", s.dbPath)

	// Auto-backfill rollups if empty but flow_logs has data
	var rollupCount, logCount int64
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bandwidth_minutely").Scan(&rollupCount)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM flow_logs").Scan(&logCount)

	if rollupCount == 0 && logCount > 0 {
		log.Printf("Rollup tables empty but %d flow logs exist, starting backfill...", logCount)
		// Release lock for backfill (Init doesn't hold the lock)
		if err := s.BackfillBandwidthRollups(ctx); err != nil {
			log.Printf("Warning: backfill failed: %v", err)
		}
	}

	return nil
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// InsertFlowLogs inserts multiple flow logs in a batch transaction
// Returns the logs that were actually inserted (excludes duplicates)
func (s *SQLiteStore) InsertFlowLogs(ctx context.Context, logs []FlowLog) ([]FlowLog, error) {
	if len(logs) == 0 {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Use INSERT OR IGNORE to skip duplicates (based on unique index)
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO flow_logs (
			logged_at, node_id, period_start, period_end, traffic_type,
			protocol, src_ip, src_port, dst_ip, dst_port,
			tx_bytes, rx_bytes, tx_pkts, rx_pkts
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	var inserted []FlowLog
	for _, log := range logs {
		result, err := stmt.ExecContext(ctx,
			log.LoggedAt, log.NodeID, log.PeriodStart, log.PeriodEnd, log.TrafficType,
			log.Protocol, log.SrcIP, log.SrcPort, log.DstIP, log.DstPort,
			log.TxBytes, log.RxBytes, log.TxPkts, log.RxPkts,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to insert log: %w", err)
		}
		// Check if row was actually inserted (not ignored due to duplicate)
		if rowsAffected, _ := result.RowsAffected(); rowsAffected > 0 {
			inserted = append(inserted, log)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return inserted, nil
}

// GetFlowLogs retrieves flow logs within a time range
func (s *SQLiteStore) GetFlowLogs(ctx context.Context, start, end time.Time, limit int) ([]FlowLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, logged_at, node_id, period_start, period_end, traffic_type,
			   protocol, src_ip, src_port, dst_ip, dst_port,
			   tx_bytes, rx_bytes, tx_pkts, rx_pkts, created_at
		FROM flow_logs
		WHERE logged_at >= ? AND logged_at <= ?
		ORDER BY logged_at ASC
		LIMIT ?
	`

	// Use the same format that SQLite stores time.Time values
	const sqliteFormat = "2006-01-02 15:04:05"
	rows, err := s.db.QueryContext(ctx, query, start.UTC().Format(sqliteFormat), end.UTC().Format(sqliteFormat), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query logs: %w", err)
	}
	defer rows.Close()

	var logs []FlowLog
	for rows.Next() {
		var log FlowLog
		var loggedAt, periodStart, periodEnd, createdAt string
		err := rows.Scan(
			&log.ID, &loggedAt, &log.NodeID, &periodStart, &periodEnd, &log.TrafficType,
			&log.Protocol, &log.SrcIP, &log.SrcPort, &log.DstIP, &log.DstPort,
			&log.TxBytes, &log.RxBytes, &log.TxPkts, &log.RxPkts, &createdAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan log: %w", err)
		}
		log.LoggedAt = parseTime(loggedAt)
		log.PeriodStart = parseTime(periodStart)
		log.PeriodEnd = parseTime(periodEnd)
		log.CreatedAt = parseTime(createdAt)
		logs = append(logs, log)
	}

	return logs, rows.Err()
}

// GetAggregatedFlows returns node-to-node traffic aggregated by src/dst IP pairs and ports
// This dramatically reduces data volume for large networks by grouping flows
func (s *SQLiteStore) GetAggregatedFlows(ctx context.Context, start, end time.Time) ([]AggregatedFlow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT
			node_id,
			traffic_type,
			MAX(protocol) as protocol,
			src_ip,
			0 as src_port,
			dst_ip,
			dst_port,
			SUM(tx_bytes) as total_tx_bytes,
			SUM(rx_bytes) as total_rx_bytes,
			SUM(tx_pkts) as total_tx_pkts,
			SUM(rx_pkts) as total_rx_pkts,
			COUNT(*) as flow_count,
			MIN(logged_at) as first_seen,
			MAX(logged_at) as last_seen
		FROM flow_logs
		WHERE logged_at >= ? AND logged_at <= ?
		GROUP BY node_id, traffic_type, src_ip, dst_ip, dst_port
		ORDER BY total_tx_bytes + total_rx_bytes DESC
	`

	const sqliteFormat = "2006-01-02 15:04:05"
	rows, err := s.db.QueryContext(ctx, query, start.UTC().Format(sqliteFormat), end.UTC().Format(sqliteFormat))
	if err != nil {
		return nil, fmt.Errorf("failed to query aggregated flows: %w", err)
	}
	defer rows.Close()

	var flows []AggregatedFlow
	for rows.Next() {
		var flow AggregatedFlow
		var firstSeen, lastSeen string
		err := rows.Scan(
			&flow.NodeID, &flow.TrafficType, &flow.Protocol,
			&flow.SrcIP, &flow.SrcPort, &flow.DstIP, &flow.DstPort,
			&flow.TotalTxBytes, &flow.TotalRxBytes, &flow.TotalTxPkts, &flow.TotalRxPkts,
			&flow.FlowCount, &firstSeen, &lastSeen,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan aggregated flow: %w", err)
		}
		flow.FirstSeen = parseTime(firstSeen)
		flow.LastSeen = parseTime(lastSeen)
		flows = append(flows, flow)
	}

	return flows, rows.Err()
}

// GetDataRange returns the available data time range
func (s *SQLiteStore) GetDataRange(ctx context.Context) (*DataRange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var dataRange DataRange
	var earliest, latest sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(logged_at), MAX(logged_at), COUNT(*)
		FROM flow_logs
	`).Scan(&earliest, &latest, &dataRange.Count)
	if err != nil {
		return nil, fmt.Errorf("failed to get data range: %w", err)
	}

	if earliest.Valid && earliest.String != "" {
		dataRange.Earliest = parseTime(earliest.String)
	}
	if latest.Valid && latest.String != "" {
		dataRange.Latest = parseTime(latest.String)
	}

	return &dataRange, nil
}

// GetPollState retrieves the current poll state
func (s *SQLiteStore) GetPollState(ctx context.Context) (*PollState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var state PollState
	var lastPollEnd, updatedAt sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT last_poll_end, updated_at FROM poll_state WHERE id = 1
	`).Scan(&lastPollEnd, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to get poll state: %w", err)
	}

	if lastPollEnd.Valid && lastPollEnd.String != "" {
		state.LastPollEnd = parseTime(lastPollEnd.String)
	}
	if updatedAt.Valid && updatedAt.String != "" {
		state.UpdatedAt = parseTime(updatedAt.String)
	}

	return &state, nil
}

// UpdatePollState updates the poll state
func (s *SQLiteStore) UpdatePollState(ctx context.Context, lastPollEnd time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		UPDATE poll_state SET last_poll_end = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1
	`, lastPollEnd)
	if err != nil {
		return fmt.Errorf("failed to update poll state: %w", err)
	}

	return nil
}

// CleanupOldLogs removes logs older than the retention period
func (s *SQLiteStore) CleanupOldLogs(ctx context.Context, retention time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-retention)

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM flow_logs WHERE logged_at < ?
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old logs: %w", err)
	}

	deleted, _ := result.RowsAffected()
	return deleted, nil
}

// CleanupOldRollups removes old rollup data to prevent unbounded growth
// - Minutely: keep 24 hours (for detailed recent queries)
// - Hourly: keep 30 days (for medium-term queries)
// - Daily: kept indefinitely (for long-term trends)
func (s *SQLiteStore) CleanupOldRollups(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Unix()
	var totalDeleted int64

	// Delete minutely buckets older than 24 hours
	minutelyCutoff := now - 24*3600
	result, err := s.db.ExecContext(ctx, "DELETE FROM bandwidth_minutely WHERE bucket < ?", minutelyCutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup minutely rollups: %w", err)
	}
	if deleted, _ := result.RowsAffected(); deleted > 0 {
		totalDeleted += deleted
	}

	// Delete hourly buckets older than 30 days
	hourlyCutoff := now - 30*24*3600
	result, err = s.db.ExecContext(ctx, "DELETE FROM bandwidth_hourly WHERE bucket < ?", hourlyCutoff)
	if err != nil {
		return totalDeleted, fmt.Errorf("failed to cleanup hourly rollups: %w", err)
	}
	if deleted, _ := result.RowsAffected(); deleted > 0 {
		totalDeleted += deleted
	}

	// Daily is kept indefinitely

	return totalDeleted, nil
}

// GetStats returns database statistics
func (s *SQLiteStore) GetStats(ctx context.Context) (map[string]interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make(map[string]interface{})

	// Total logs
	var totalLogs int64
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM flow_logs").Scan(&totalLogs)
	stats["totalLogs"] = totalLogs

	// Logs by traffic type
	rows, err := s.db.QueryContext(ctx, `
		SELECT traffic_type, COUNT(*) FROM flow_logs GROUP BY traffic_type
	`)
	if err == nil {
		defer rows.Close()
		byType := make(map[string]int64)
		for rows.Next() {
			var trafficType string
			var count int64
			rows.Scan(&trafficType, &count)
			byType[trafficType] = count
		}
		stats["byTrafficType"] = byType
	}

	// Database size (approximate)
	var pageCount, pageSize int64
	s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount)
	s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize)
	stats["dbSizeBytes"] = pageCount * pageSize

	// Data range (inline to avoid recursive lock)
	var earliest, latest sql.NullString
	var count int64
	err = s.db.QueryRowContext(ctx, `
		SELECT MIN(logged_at), MAX(logged_at), COUNT(*) FROM flow_logs
	`).Scan(&earliest, &latest, &count)
	if err == nil {
		dataRange := &DataRange{Count: count}
		if earliest.Valid && earliest.String != "" {
			dataRange.Earliest = parseTime(earliest.String)
		}
		if latest.Valid && latest.String != "" {
			dataRange.Latest = parseTime(latest.String)
		}
		stats["dataRange"] = dataRange
	}

	return stats, nil
}

// GetBandwidthAggregated returns bandwidth data from pre-aggregated rollup tables
// Uses tiered approach: minutely for <24h, hourly for <7d, daily for longer
func (s *SQLiteStore) GetBandwidthAggregated(ctx context.Context, start, end time.Time, bucketSeconds int) ([]BandwidthBucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	startUnix := start.UTC().Unix()
	endUnix := end.UTC().Unix()
	rangeSeconds := endUnix - startUnix

	// Choose rollup table based on time range
	var table string
	var bucketSize int64

	switch {
	case rangeSeconds <= 24*3600: // <= 24 hours: use minutely
		table = "bandwidth_minutely"
		bucketSize = 60
	case rangeSeconds <= 7*24*3600: // <= 7 days: use hourly
		table = "bandwidth_hourly"
		bucketSize = 3600
	default: // > 7 days: use daily
		table = "bandwidth_daily"
		bucketSize = 86400
	}

	// Truncate start/end to bucket boundaries
	startBucket := (startUnix / bucketSize) * bucketSize
	endBucket := (endUnix / bucketSize) * bucketSize

	query := fmt.Sprintf(`
		SELECT bucket, tx_bytes, rx_bytes
		FROM %s
		WHERE bucket >= ? AND bucket <= ?
		ORDER BY bucket ASC
	`, table)

	rows, err := s.db.QueryContext(ctx, query, startBucket, endBucket)
	if err != nil {
		return nil, fmt.Errorf("failed to query bandwidth rollup: %w", err)
	}
	defer rows.Close()

	var buckets []BandwidthBucket
	for rows.Next() {
		var bucket int64
		var txBytes, rxBytes int64
		if err := rows.Scan(&bucket, &txBytes, &rxBytes); err != nil {
			return nil, fmt.Errorf("failed to scan bucket: %w", err)
		}
		buckets = append(buckets, BandwidthBucket{
			Time:    time.Unix(bucket, 0).UTC(),
			TxBytes: txBytes,
			RxBytes: rxBytes,
		})
	}

	return buckets, rows.Err()
}

// GetBandwidthByIPs returns bandwidth aggregated by time bucket filtered by node IPs
// When IPs are provided, only traffic where src_ip or dst_ip matches is included
func (s *SQLiteStore) GetBandwidthByIPs(ctx context.Context, start, end time.Time, ips []string) ([]BandwidthBucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(ips) == 0 {
		// No IPs provided, return all bandwidth
		return s.GetBandwidthAggregated(ctx, start, end, 0)
	}

	// Use the same format that SQLite stores time.Time values
	const sqliteFormat = "2006-01-02 15:04:05"
	startStr := start.UTC().Format(sqliteFormat)
	endStr := end.UTC().Format(sqliteFormat)

	// Determine bucket size based on time range
	rangeSeconds := int64(end.Sub(start).Seconds())
	var bucketSize int64

	switch {
	case rangeSeconds <= 24*3600: // <= 24 hours: minutely buckets
		bucketSize = 60
	case rangeSeconds <= 7*24*3600: // <= 7 days: hourly buckets
		bucketSize = 3600
	default: // > 7 days: daily buckets
		bucketSize = 86400
	}

	// Build placeholder list for IPs
	placeholders := make([]string, len(ips))
	args := make([]interface{}, 0, len(ips)*2+2)
	args = append(args, startStr, endStr)
	for i, ip := range ips {
		placeholders[i] = "?"
		args = append(args, ip)
	}
	ipList := strings.Join(placeholders, ", ")

	// Also add IPs again for the second IN clause
	for _, ip := range ips {
		args = append(args, ip)
	}

	query := fmt.Sprintf(`
		SELECT
			(CAST(strftime('%%s', substr(logged_at, 1, 19)) AS INTEGER) / %d) * %d as bucket,
			SUM(tx_bytes) as tx_bytes,
			SUM(rx_bytes) as rx_bytes
		FROM flow_logs
		WHERE logged_at >= ? AND logged_at <= ?
		AND (src_ip IN (%s) OR dst_ip IN (%s))
		GROUP BY bucket
		ORDER BY bucket ASC
	`, bucketSize, bucketSize, ipList, ipList)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query bandwidth by IPs: %w", err)
	}
	defer rows.Close()

	var buckets []BandwidthBucket
	for rows.Next() {
		var bucket int64
		var txBytes, rxBytes int64
		if err := rows.Scan(&bucket, &txBytes, &rxBytes); err != nil {
			return nil, fmt.Errorf("failed to scan bucket: %w", err)
		}
		buckets = append(buckets, BandwidthBucket{
			Time:    time.Unix(bucket, 0).UTC(),
			TxBytes: txBytes,
			RxBytes: rxBytes,
		})
	}

	return buckets, rows.Err()
}

// BackfillBandwidthRollups rebuilds rollup tables from existing flow_logs
// Call this once after adding rollup tables to populate from historical data
func (s *SQLiteStore) BackfillBandwidthRollups(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Println("Starting bandwidth rollup backfill...")

	// Clear existing rollups
	for _, table := range []string{"bandwidth_minutely", "bandwidth_hourly", "bandwidth_daily"} {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", table)); err != nil {
			return fmt.Errorf("failed to clear %s: %w", table, err)
		}
	}

	// Backfill minutely (aggregate from raw logs)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bandwidth_minutely (bucket, tx_bytes, rx_bytes)
		SELECT
			(CAST(strftime('%s', substr(logged_at, 1, 19)) AS INTEGER) / 60) * 60 as bucket,
			SUM(tx_bytes),
			SUM(rx_bytes)
		FROM flow_logs
		WHERE traffic_type IN ('virtual', 'subnet')
		GROUP BY bucket
		HAVING bucket IS NOT NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to backfill minutely: %w", err)
	}

	// Backfill hourly (aggregate from minutely)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO bandwidth_hourly (bucket, tx_bytes, rx_bytes)
		SELECT
			(bucket / 3600) * 3600 as hour_bucket,
			SUM(tx_bytes),
			SUM(rx_bytes)
		FROM bandwidth_minutely
		GROUP BY hour_bucket
	`)
	if err != nil {
		return fmt.Errorf("failed to backfill hourly: %w", err)
	}

	// Backfill daily (aggregate from hourly)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO bandwidth_daily (bucket, tx_bytes, rx_bytes)
		SELECT
			(bucket / 86400) * 86400 as day_bucket,
			SUM(tx_bytes),
			SUM(rx_bytes)
		FROM bandwidth_hourly
		GROUP BY day_bucket
	`)
	if err != nil {
		return fmt.Errorf("failed to backfill daily: %w", err)
	}

	// Log counts
	var minCount, hourCount, dayCount int64
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bandwidth_minutely").Scan(&minCount)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bandwidth_hourly").Scan(&hourCount)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bandwidth_daily").Scan(&dayCount)

	log.Printf("Bandwidth rollup backfill complete: %d minutely, %d hourly, %d daily buckets", minCount, hourCount, dayCount)
	return nil
}

// UpdateBandwidthRollups updates all rollup tables with new flow log data
func (s *SQLiteStore) UpdateBandwidthRollups(ctx context.Context, logs []FlowLog) error {
	if len(logs) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Aggregate logs by minute, hour, and day buckets
	minutely := make(map[int64][2]int64) // bucket -> [tx, rx]
	hourly := make(map[int64][2]int64)
	daily := make(map[int64][2]int64)

	for _, log := range logs {
		// Only count virtual and subnet traffic
		if log.TrafficType != "virtual" && log.TrafficType != "subnet" {
			continue
		}

		ts := log.LoggedAt.UTC().Unix()

		// Minutely bucket (truncate to minute)
		minBucket := (ts / 60) * 60
		m := minutely[minBucket]
		m[0] += log.TxBytes
		m[1] += log.RxBytes
		minutely[minBucket] = m

		// Hourly bucket (truncate to hour)
		hourBucket := (ts / 3600) * 3600
		h := hourly[hourBucket]
		h[0] += log.TxBytes
		h[1] += log.RxBytes
		hourly[hourBucket] = h

		// Daily bucket (truncate to day)
		dayBucket := (ts / 86400) * 86400
		d := daily[dayBucket]
		d[0] += log.TxBytes
		d[1] += log.RxBytes
		daily[dayBucket] = d
	}

	// Upsert into rollup tables
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Helper to upsert rollups
	upsert := func(table string, data map[int64][2]int64) error {
		stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (bucket, tx_bytes, rx_bytes) VALUES (?, ?, ?)
			ON CONFLICT(bucket) DO UPDATE SET
				tx_bytes = tx_bytes + excluded.tx_bytes,
				rx_bytes = rx_bytes + excluded.rx_bytes
		`, table))
		if err != nil {
			return err
		}
		defer stmt.Close()

		for bucket, bytes := range data {
			if _, err := stmt.ExecContext(ctx, bucket, bytes[0], bytes[1]); err != nil {
				return err
			}
		}
		return nil
	}

	if err := upsert("bandwidth_minutely", minutely); err != nil {
		return fmt.Errorf("failed to upsert minutely: %w", err)
	}
	if err := upsert("bandwidth_hourly", hourly); err != nil {
		return fmt.Errorf("failed to upsert hourly: %w", err)
	}
	if err := upsert("bandwidth_daily", daily); err != nil {
		return fmt.Errorf("failed to upsert daily: %w", err)
	}

	return tx.Commit()
}
