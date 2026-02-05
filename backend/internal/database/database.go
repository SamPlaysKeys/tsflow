package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// NodePairAggregate represents pre-computed node-to-node traffic
// This is the primary data structure for graph rendering
type NodePairAggregate struct {
	Bucket      int64  `json:"bucket"`      // Time bucket (unix timestamp)
	SrcNodeID   string `json:"srcNodeId"`   // Source device ID or IP
	DstNodeID   string `json:"dstNodeId"`   // Destination device ID or IP
	TrafficType string `json:"trafficType"` // virtual, subnet, physical
	TxBytes     int64  `json:"txBytes"`
	RxBytes     int64  `json:"rxBytes"`
	TxPkts      int64  `json:"txPkts"`
	RxPkts      int64  `json:"rxPkts"`
	FlowCount   int64  `json:"flowCount"`
	Protocols   string `json:"protocols"` // JSON array of protocols seen
	Ports       string `json:"ports"`     // JSON array of top ports
}

// BandwidthBucket represents aggregated bandwidth for a time bucket
type BandwidthBucket struct {
	Time    time.Time `json:"time"`
	TxBytes int64     `json:"txBytes"`
	RxBytes int64     `json:"rxBytes"`
}

// NodeBandwidth represents bandwidth for a specific node
type NodeBandwidth struct {
	Bucket  int64  `json:"bucket"`
	NodeID  string `json:"nodeId"`
	TxBytes int64  `json:"txBytes"`
	RxBytes int64  `json:"rxBytes"`
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
	Count    int64     `json:"count"` // Total records in the range
}

// FlowLog represents a raw flow log entry (kept temporarily for current period)
type FlowLog struct {
	ID          int64     `json:"id"`
	LoggedAt    time.Time `json:"loggedAt"`
	NodeID      string    `json:"nodeId"`
	TrafficType string    `json:"trafficType"`
	Protocol    int       `json:"protocol"`
	SrcIP       string    `json:"srcIp"`
	SrcPort     int       `json:"srcPort"`
	DstIP       string    `json:"dstIp"`
	DstPort     int       `json:"dstPort"`
	TxBytes     int64     `json:"txBytes"`
	RxBytes     int64     `json:"rxBytes"`
	TxPkts      int64     `json:"txPkts"`
	RxPkts      int64     `json:"rxPkts"`
}

// Store defines the interface for flow log storage
type Store interface {
	Init(ctx context.Context) error
	Close() error

	// Raw log operations (for current incomplete period only)
	InsertFlowLogs(ctx context.Context, logs []FlowLog) (int, error)
	GetRecentFlowLogs(ctx context.Context, since time.Time) ([]FlowLog, error)
	GetFlowLogsInRange(ctx context.Context, start, end time.Time, limit int) ([]FlowLog, error)

	// Pre-aggregated data operations
	UpsertNodePairAggregates(ctx context.Context, aggregates []NodePairAggregate) error
	GetNodePairAggregates(ctx context.Context, start, end time.Time, bucketSize int64) ([]NodePairAggregate, error)

	// Bandwidth operations
	UpsertBandwidth(ctx context.Context, buckets []BandwidthBucket, bucketSize int64) error
	UpsertNodeBandwidth(ctx context.Context, buckets []NodeBandwidth, bucketSize int64) error
	GetBandwidth(ctx context.Context, start, end time.Time) ([]BandwidthBucket, error)
	GetNodeBandwidth(ctx context.Context, start, end time.Time, nodeID string) ([]BandwidthBucket, error)

	// State operations
	GetPollState(ctx context.Context) (*PollState, error)
	UpdatePollState(ctx context.Context, lastPollEnd time.Time) error
	GetDataRange(ctx context.Context) (*DataRange, error)

	// Maintenance
	Cleanup(ctx context.Context, retentionMinutely, retentionHourly, retentionDaily time.Duration) (int64, error)
	GetStats(ctx context.Context) (map[string]any, error)
}

// SQLiteStore implements Store using SQLite
type SQLiteStore struct {
	db     *sql.DB
	dbPath string
	mu     sync.RWMutex
}

// NewSQLiteStore creates a new SQLite store
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_cache_size=10000", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	return &SQLiteStore{db: db, dbPath: dbPath}, nil
}

// Init creates the database schema
func (s *SQLiteStore) Init(ctx context.Context) error {
	schema := `
	-- Temporary raw flow logs (kept for current poll period only, ~10 minutes)
	-- Used for real-time queries before aggregation
	CREATE TABLE IF NOT EXISTS flow_logs_current (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		logged_at DATETIME NOT NULL,
		node_id TEXT NOT NULL,
		traffic_type TEXT NOT NULL,
		protocol INTEGER DEFAULT 0,
		src_ip TEXT NOT NULL,
		src_port INTEGER DEFAULT 0,
		dst_ip TEXT NOT NULL,
		dst_port INTEGER DEFAULT 0,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0,
		tx_pkts INTEGER DEFAULT 0,
		rx_pkts INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_flow_logs_current_logged ON flow_logs_current(logged_at);

	-- Node-pair aggregates: the primary data for graph rendering
	-- Pre-computed at poll time with IP->device resolution
	-- Tiered: minutely (24h), hourly (7d), daily (forever)
	CREATE TABLE IF NOT EXISTS node_pairs_minutely (
		bucket INTEGER NOT NULL,
		src_node_id TEXT NOT NULL,
		dst_node_id TEXT NOT NULL,
		traffic_type TEXT NOT NULL,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0,
		tx_pkts INTEGER DEFAULT 0,
		rx_pkts INTEGER DEFAULT 0,
		flow_count INTEGER DEFAULT 0,
		protocols TEXT DEFAULT '[]',
		ports TEXT DEFAULT '[]',
		PRIMARY KEY (bucket, src_node_id, dst_node_id, traffic_type)
	);
	CREATE INDEX IF NOT EXISTS idx_node_pairs_minutely_bucket ON node_pairs_minutely(bucket);

	CREATE TABLE IF NOT EXISTS node_pairs_hourly (
		bucket INTEGER NOT NULL,
		src_node_id TEXT NOT NULL,
		dst_node_id TEXT NOT NULL,
		traffic_type TEXT NOT NULL,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0,
		tx_pkts INTEGER DEFAULT 0,
		rx_pkts INTEGER DEFAULT 0,
		flow_count INTEGER DEFAULT 0,
		protocols TEXT DEFAULT '[]',
		ports TEXT DEFAULT '[]',
		PRIMARY KEY (bucket, src_node_id, dst_node_id, traffic_type)
	);
	CREATE INDEX IF NOT EXISTS idx_node_pairs_hourly_bucket ON node_pairs_hourly(bucket);

	CREATE TABLE IF NOT EXISTS node_pairs_daily (
		bucket INTEGER NOT NULL,
		src_node_id TEXT NOT NULL,
		dst_node_id TEXT NOT NULL,
		traffic_type TEXT NOT NULL,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0,
		tx_pkts INTEGER DEFAULT 0,
		rx_pkts INTEGER DEFAULT 0,
		flow_count INTEGER DEFAULT 0,
		protocols TEXT DEFAULT '[]',
		ports TEXT DEFAULT '[]',
		PRIMARY KEY (bucket, src_node_id, dst_node_id, traffic_type)
	);
	CREATE INDEX IF NOT EXISTS idx_node_pairs_daily_bucket ON node_pairs_daily(bucket);

	-- Total bandwidth rollups (for bandwidth chart without node filter)
	CREATE TABLE IF NOT EXISTS bandwidth_minutely (
		bucket INTEGER PRIMARY KEY,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS bandwidth_hourly (
		bucket INTEGER PRIMARY KEY,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS bandwidth_daily (
		bucket INTEGER PRIMARY KEY,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0
	);

	-- Per-node bandwidth rollups (for bandwidth chart with node filter)
	CREATE TABLE IF NOT EXISTS bandwidth_by_node_minutely (
		bucket INTEGER NOT NULL,
		node_id TEXT NOT NULL,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0,
		PRIMARY KEY (bucket, node_id)
	);
	CREATE INDEX IF NOT EXISTS idx_bandwidth_node_minutely_node ON bandwidth_by_node_minutely(node_id, bucket);

	CREATE TABLE IF NOT EXISTS bandwidth_by_node_hourly (
		bucket INTEGER NOT NULL,
		node_id TEXT NOT NULL,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0,
		PRIMARY KEY (bucket, node_id)
	);
	CREATE INDEX IF NOT EXISTS idx_bandwidth_node_hourly_node ON bandwidth_by_node_hourly(node_id, bucket);

	CREATE TABLE IF NOT EXISTS bandwidth_by_node_daily (
		bucket INTEGER NOT NULL,
		node_id TEXT NOT NULL,
		tx_bytes INTEGER DEFAULT 0,
		rx_bytes INTEGER DEFAULT 0,
		PRIMARY KEY (bucket, node_id)
	);
	CREATE INDEX IF NOT EXISTS idx_bandwidth_node_daily_node ON bandwidth_by_node_daily(node_id, bucket);

	-- Poll state tracking
	CREATE TABLE IF NOT EXISTS poll_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		last_poll_end DATETIME,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	INSERT OR IGNORE INTO poll_state (id, last_poll_end, updated_at) VALUES (1, NULL, CURRENT_TIMESTAMP);
	`

	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	log.Printf("Database initialized at %s", s.dbPath)
	return nil
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// InsertFlowLogs inserts raw flow logs for the current period
func (s *SQLiteStore) InsertFlowLogs(ctx context.Context, logs []FlowLog) (int, error) {
	if len(logs) == 0 {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO flow_logs_current (
			logged_at, node_id, traffic_type, protocol,
			src_ip, src_port, dst_ip, dst_port,
			tx_bytes, rx_bytes, tx_pkts, rx_pkts
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	const sqliteFormat = "2006-01-02 15:04:05"
	count := 0
	for _, log := range logs {
		_, err := stmt.ExecContext(ctx,
			log.LoggedAt.UTC().Format(sqliteFormat), log.NodeID, log.TrafficType, log.Protocol,
			log.SrcIP, log.SrcPort, log.DstIP, log.DstPort,
			log.TxBytes, log.RxBytes, log.TxPkts, log.RxPkts,
		)
		if err != nil {
			return count, fmt.Errorf("failed to insert log: %w", err)
		}
		count++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return count, nil
}

// GetRecentFlowLogs retrieves raw flow logs since a given time
func (s *SQLiteStore) GetRecentFlowLogs(ctx context.Context, since time.Time) ([]FlowLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	const sqliteFormat = "2006-01-02 15:04:05"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, logged_at, node_id, traffic_type, protocol,
			   src_ip, src_port, dst_ip, dst_port,
			   tx_bytes, rx_bytes, tx_pkts, rx_pkts
		FROM flow_logs_current
		WHERE logged_at >= ?
		ORDER BY logged_at ASC
	`, since.UTC().Format(sqliteFormat))
	if err != nil {
		return nil, fmt.Errorf("failed to query logs: %w", err)
	}
	defer rows.Close()

	var logs []FlowLog
	for rows.Next() {
		var log FlowLog
		var loggedAt string
		err := rows.Scan(
			&log.ID, &loggedAt, &log.NodeID, &log.TrafficType, &log.Protocol,
			&log.SrcIP, &log.SrcPort, &log.DstIP, &log.DstPort,
			&log.TxBytes, &log.RxBytes, &log.TxPkts, &log.RxPkts,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan log: %w", err)
		}
		log.LoggedAt = parseTime(loggedAt)
		logs = append(logs, log)
	}

	return logs, rows.Err()
}

// GetFlowLogsInRange retrieves raw flow logs within a time range with optional limit
func (s *SQLiteStore) GetFlowLogsInRange(ctx context.Context, start, end time.Time, limit int) ([]FlowLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	const sqliteFormat = "2006-01-02 15:04:05"
	query := `
		SELECT id, logged_at, node_id, traffic_type, protocol,
			   src_ip, src_port, dst_ip, dst_port,
			   tx_bytes, rx_bytes, tx_pkts, rx_pkts
		FROM flow_logs_current
		WHERE logged_at >= ? AND logged_at <= ?
		ORDER BY logged_at ASC
	`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.QueryContext(ctx, query, start.UTC().Format(sqliteFormat), end.UTC().Format(sqliteFormat))
	if err != nil {
		return nil, fmt.Errorf("failed to query logs: %w", err)
	}
	defer rows.Close()

	var logs []FlowLog
	for rows.Next() {
		var log FlowLog
		var loggedAt string
		err := rows.Scan(
			&log.ID, &loggedAt, &log.NodeID, &log.TrafficType, &log.Protocol,
			&log.SrcIP, &log.SrcPort, &log.DstIP, &log.DstPort,
			&log.TxBytes, &log.RxBytes, &log.TxPkts, &log.RxPkts,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan log: %w", err)
		}
		log.LoggedAt = parseTime(loggedAt)
		logs = append(logs, log)
	}

	return logs, rows.Err()
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

	// Prepare statements for each tier
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
				flow_count = flow_count + excluded.flow_count
		`, table))
		if err != nil {
			return fmt.Errorf("failed to prepare statement for %s: %w", table, err)
		}

		for _, agg := range aggregates {
			bucket := (agg.Bucket / bucketSize) * bucketSize
			_, err := stmt.ExecContext(ctx,
				bucket, agg.SrcNodeID, agg.DstNodeID, agg.TrafficType,
				agg.TxBytes, agg.RxBytes, agg.TxPkts, agg.RxPkts,
				agg.FlowCount, agg.Protocols, agg.Ports,
			)
			if err != nil {
				stmt.Close()
				return fmt.Errorf("failed to upsert aggregate: %w", err)
			}
		}
		stmt.Close()
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

	// Choose table based on range
	var table string
	switch {
	case rangeSeconds <= 24*3600:
		table = "node_pairs_minutely"
	case rangeSeconds <= 7*24*3600:
		table = "node_pairs_hourly"
	default:
		table = "node_pairs_daily"
	}

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

		for _, b := range buckets {
			bucket := (b.Time.UTC().Unix() / bs) * bs
			_, err := stmt.ExecContext(ctx, bucket, b.TxBytes, b.RxBytes)
			if err != nil {
				stmt.Close()
				return fmt.Errorf("failed to upsert bandwidth: %w", err)
			}
		}
		stmt.Close()
	}

	return tx.Commit()
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

		for _, b := range buckets {
			bucket := (b.Bucket / bs) * bs
			_, err := stmt.ExecContext(ctx, bucket, b.NodeID, b.TxBytes, b.RxBytes)
			if err != nil {
				stmt.Close()
				return fmt.Errorf("failed to upsert node bandwidth: %w", err)
			}
		}
		stmt.Close()
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

	var table string
	switch {
	case rangeSeconds <= 24*3600:
		table = "bandwidth_minutely"
	case rangeSeconds <= 7*24*3600:
		table = "bandwidth_hourly"
	default:
		table = "bandwidth_daily"
	}

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

	var buckets []BandwidthBucket
	for rows.Next() {
		var bucket int64
		var b BandwidthBucket
		err := rows.Scan(&bucket, &b.TxBytes, &b.RxBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to scan bucket: %w", err)
		}
		b.Time = time.Unix(bucket, 0).UTC()
		buckets = append(buckets, b)
	}

	return buckets, rows.Err()
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

	var table string
	switch {
	case rangeSeconds <= 24*3600:
		table = "bandwidth_by_node_minutely"
	case rangeSeconds <= 7*24*3600:
		table = "bandwidth_by_node_hourly"
	default:
		table = "bandwidth_by_node_daily"
	}

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

	var buckets []BandwidthBucket
	for rows.Next() {
		var bucket int64
		var b BandwidthBucket
		err := rows.Scan(&bucket, &b.TxBytes, &b.RxBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to scan bucket: %w", err)
		}
		b.Time = time.Unix(bucket, 0).UTC()
		buckets = append(buckets, b)
	}

	return buckets, rows.Err()
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

	const sqliteFormat = "2006-01-02 15:04:05"
	_, err := s.db.ExecContext(ctx, `
		UPDATE poll_state SET last_poll_end = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1
	`, lastPollEnd.UTC().Format(sqliteFormat))
	if err != nil {
		return fmt.Errorf("failed to update poll state: %w", err)
	}

	return nil
}

// GetDataRange returns the available data time range
func (s *SQLiteStore) GetDataRange(ctx context.Context) (*DataRange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var dataRange DataRange

	// Get range and count from node_pairs_minutely (most granular pre-aggregated data)
	var minBucket, maxBucket sql.NullInt64
	var count int64
	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(bucket), MAX(bucket), COUNT(*) FROM node_pairs_minutely
	`).Scan(&minBucket, &maxBucket, &count)
	if err != nil {
		return nil, fmt.Errorf("failed to get data range: %w", err)
	}

	if minBucket.Valid {
		dataRange.Earliest = time.Unix(minBucket.Int64, 0).UTC()
	}
	if maxBucket.Valid {
		dataRange.Latest = time.Unix(maxBucket.Int64, 0).UTC()
	}
	dataRange.Count = count

	return &dataRange, nil
}

// Cleanup removes old data based on retention periods
func (s *SQLiteStore) Cleanup(ctx context.Context, retentionMinutely, retentionHourly, retentionDaily time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Unix()
	var totalDeleted int64

	// Clean up minutely tables
	minutelyCutoff := now - int64(retentionMinutely.Seconds())
	for _, table := range []string{"node_pairs_minutely", "bandwidth_minutely", "bandwidth_by_node_minutely"} {
		result, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE bucket < ?", table), minutelyCutoff)
		if err != nil {
			log.Printf("Warning: failed to cleanup %s: %v", table, err)
			continue
		}
		if deleted, _ := result.RowsAffected(); deleted > 0 {
			totalDeleted += deleted
		}
	}

	// Clean up hourly tables
	hourlyCutoff := now - int64(retentionHourly.Seconds())
	for _, table := range []string{"node_pairs_hourly", "bandwidth_hourly", "bandwidth_by_node_hourly"} {
		result, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE bucket < ?", table), hourlyCutoff)
		if err != nil {
			log.Printf("Warning: failed to cleanup %s: %v", table, err)
			continue
		}
		if deleted, _ := result.RowsAffected(); deleted > 0 {
			totalDeleted += deleted
		}
	}

	// Clean up daily tables (if retention specified)
	if retentionDaily > 0 {
		dailyCutoff := now - int64(retentionDaily.Seconds())
		for _, table := range []string{"node_pairs_daily", "bandwidth_daily", "bandwidth_by_node_daily"} {
			result, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE bucket < ?", table), dailyCutoff)
			if err != nil {
				log.Printf("Warning: failed to cleanup %s: %v", table, err)
				continue
			}
			if deleted, _ := result.RowsAffected(); deleted > 0 {
				totalDeleted += deleted
			}
		}
	}

	// Clean up raw flow logs (keep only last 10 minutes)
	const sqliteFormat = "2006-01-02 15:04:05"
	flowLogCutoff := time.Now().Add(-10 * time.Minute).UTC().Format(sqliteFormat)
	result, err := s.db.ExecContext(ctx, "DELETE FROM flow_logs_current WHERE logged_at < ?", flowLogCutoff)
	if err != nil {
		log.Printf("Warning: failed to cleanup flow_logs_current: %v", err)
	} else if deleted, _ := result.RowsAffected(); deleted > 0 {
		totalDeleted += deleted
	}

	return totalDeleted, nil
}

// GetStats returns database statistics
func (s *SQLiteStore) GetStats(ctx context.Context) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make(map[string]any)

	// Count records in each table
	tables := []string{
		"flow_logs_current",
		"node_pairs_minutely", "node_pairs_hourly", "node_pairs_daily",
		"bandwidth_minutely", "bandwidth_hourly", "bandwidth_daily",
		"bandwidth_by_node_minutely", "bandwidth_by_node_hourly", "bandwidth_by_node_daily",
	}

	tableCounts := make(map[string]int64)
	for _, table := range tables {
		var count int64
		// Table names are hardcoded constants, safe to use fmt.Sprintf
		if err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
			// Log but don't fail - stats are best-effort
			count = 0
		}
		tableCounts[table] = count
	}
	stats["tableCounts"] = tableCounts

	// Database size
	var pageCount, pageSize int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		pageCount = 0
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		pageSize = 0
	}
	stats["dbSizeBytes"] = pageCount * pageSize

	// Data range (inline to avoid nested lock acquisition)
	var minBucket, maxBucket sql.NullInt64
	_ = s.db.QueryRowContext(ctx, `SELECT MIN(bucket), MAX(bucket) FROM node_pairs_minutely`).Scan(&minBucket, &maxBucket)
	dataRange := &DataRange{}
	if minBucket.Valid {
		dataRange.Earliest = time.Unix(minBucket.Int64, 0).UTC()
	}
	if maxBucket.Valid {
		dataRange.Latest = time.Unix(maxBucket.Int64, 0).UTC()
	}
	stats["dataRange"] = dataRange

	return stats, nil
}

// parseTime parses a time string from SQLite
func parseTime(s string) time.Time {
	formats := []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999 +0000 UTC",
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
