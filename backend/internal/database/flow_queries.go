package database

import (
	"context"
	"fmt"
	"time"
)

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
