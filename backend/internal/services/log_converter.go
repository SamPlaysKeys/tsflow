package services

import (
	"log"
	"strings"
	"time"

	"github.com/rajsinghtech/tsflow/backend/internal/database"
	tailscale "tailscale.com/client/tailscale/v2"
)

// convertLogs converts Tailscale API response to database FlowLog entries
func (p *Poller) convertLogs(logsResp any) []database.FlowLog {
	var flowLogs []database.FlowLog

	logsMap, ok := logsResp.(map[string]any)
	if !ok {
		log.Printf("Warning: unexpected logs response type %T, expected map[string]any", logsResp)
		return flowLogs
	}

	logs, ok := logsMap["logs"]
	if !ok {
		log.Printf("Warning: logs response missing 'logs' key, available keys: %v", mapKeys(logsMap))
		return flowLogs
	}

	// Handle []tailscale.NetworkFlowLog
	if tsLogs, ok := logs.([]tailscale.NetworkFlowLog); ok {
		for _, tsLog := range tsLogs {
			flowLogs = append(flowLogs, p.convertTailscaleLog(tsLog)...)
		}
		return flowLogs
	}

	// Handle []any (generic JSON)
	if logsArray, ok := logs.([]any); ok {
		for _, logItem := range logsArray {
			if logMap, ok := logItem.(map[string]any); ok {
				flowLogs = append(flowLogs, p.convertMapLog(logMap)...)
			}
		}
	}

	return flowLogs
}

func (p *Poller) convertTailscaleLog(tsLog tailscale.NetworkFlowLog) []database.FlowLog {
	var flowLogs []database.FlowLog

	// Use Start (when traffic actually occurred) instead of Logged (when server captured it)
	// to avoid 5-10 second timing skew in bucket assignment
	logTime := tsLog.Start
	if logTime.IsZero() {
		logTime = tsLog.Logged // fallback if Start not populated
	}

	// Process virtual traffic
	for _, traffic := range tsLog.VirtualTraffic {
		flowLogs = append(flowLogs, database.FlowLog{
			LoggedAt:    logTime,
			NodeID:      tsLog.NodeID,
			TrafficType: "virtual",
			Protocol:    traffic.Proto,
			SrcIP:       extractIP(traffic.Src),
			SrcPort:     extractPort(traffic.Src),
			DstIP:       extractIP(traffic.Dst),
			DstPort:     extractPort(traffic.Dst),
			TxBytes:     int64(traffic.TxBytes),
			RxBytes:     int64(traffic.RxBytes),
			TxPkts:      int64(traffic.TxPkts),
			RxPkts:      int64(traffic.RxPkts),
		})
	}

	// Process subnet traffic
	for _, traffic := range tsLog.SubnetTraffic {
		flowLogs = append(flowLogs, database.FlowLog{
			LoggedAt:    logTime,
			NodeID:      tsLog.NodeID,
			TrafficType: "subnet",
			Protocol:    traffic.Proto,
			SrcIP:       extractIP(traffic.Src),
			SrcPort:     extractPort(traffic.Src),
			DstIP:       extractIP(traffic.Dst),
			DstPort:     extractPort(traffic.Dst),
			TxBytes:     int64(traffic.TxBytes),
			RxBytes:     int64(traffic.RxBytes),
			TxPkts:      int64(traffic.TxPkts),
			RxPkts:      int64(traffic.RxPkts),
		})
	}

	// Process exit traffic (traffic via exit nodes)
	for _, traffic := range tsLog.ExitTraffic {
		flowLogs = append(flowLogs, database.FlowLog{
			LoggedAt:    logTime,
			NodeID:      tsLog.NodeID,
			TrafficType: "exit",
			Protocol:    traffic.Proto,
			SrcIP:       extractIP(traffic.Src),
			SrcPort:     extractPort(traffic.Src),
			DstIP:       extractIP(traffic.Dst),
			DstPort:     extractPort(traffic.Dst),
			TxBytes:     int64(traffic.TxBytes),
			RxBytes:     int64(traffic.RxBytes),
			TxPkts:      int64(traffic.TxPkts),
			RxPkts:      int64(traffic.RxPkts),
		})
	}

	// Process physical traffic
	for _, traffic := range tsLog.PhysicalTraffic {
		flowLogs = append(flowLogs, database.FlowLog{
			LoggedAt:    logTime,
			NodeID:      tsLog.NodeID,
			TrafficType: "physical",
			Protocol:    traffic.Proto,
			SrcIP:       extractIP(traffic.Src),
			SrcPort:     extractPort(traffic.Src),
			DstIP:       extractIP(traffic.Dst),
			DstPort:     extractPort(traffic.Dst),
			TxBytes:     int64(traffic.TxBytes),
			RxBytes:     0,
			TxPkts:      int64(traffic.TxPkts),
			RxPkts:      0,
		})
	}

	return flowLogs
}

func (p *Poller) convertMapLog(logMap map[string]any) []database.FlowLog {
	var flowLogs []database.FlowLog

	nodeID, ok := logMap["nodeId"].(string)
	if !ok {
		log.Printf("Warning: skipping log entry with invalid nodeId type: %T", logMap["nodeId"])
		return flowLogs
	}
	// Prefer "start" over "logged" for bucket alignment (consistent with convertTailscaleLog)
	logTimeStr := getString(logMap, "start")
	if logTimeStr == "" {
		logTimeStr = getString(logMap, "logged")
	}
	logged, err := time.Parse(time.RFC3339, logTimeStr)
	if err != nil {
		log.Printf("Warning: skipping log entry with invalid timestamp for node %s", nodeID)
		return flowLogs
	}

	// Process each traffic type
	for _, trafficType := range []string{"virtualTraffic", "subnetTraffic", "exitTraffic", "physicalTraffic"} {
		if traffic, ok := logMap[trafficType].([]any); ok {
			typeName := strings.TrimSuffix(trafficType, "Traffic")
			isPhysical := typeName == "physical"
			for _, t := range traffic {
				if tMap, ok := t.(map[string]any); ok {
					rxBytes := getInt64(tMap, "rxBytes")
					rxPkts := getInt64(tMap, "rxPkts")
					// Physical traffic has no RX data in the Tailscale API
					if isPhysical {
						rxBytes = 0
						rxPkts = 0
					}
					flowLogs = append(flowLogs, database.FlowLog{
						LoggedAt:    logged,
						NodeID:      nodeID,
						TrafficType: typeName,
						Protocol:    getInt(tMap, "proto"),
						SrcIP:       extractIP(getString(tMap, "src")),
						SrcPort:     extractPort(getString(tMap, "src")),
						DstIP:       extractIP(getString(tMap, "dst")),
						DstPort:     extractPort(getString(tMap, "dst")),
						TxBytes:     getInt64(tMap, "txBytes"),
						RxBytes:     rxBytes,
						TxPkts:      getInt64(tMap, "txPkts"),
						RxPkts:      rxPkts,
					})
				}
			}
		}
	}

	return flowLogs
}

// Helper functions
func extractIP(addr string) string {
	// Handle IPv6 with brackets: [::1]:443
	if strings.HasPrefix(addr, "[") {
		end := strings.Index(addr, "]")
		if end > 0 {
			return addr[1:end]
		}
	}
	// Handle IPv4: 192.168.1.1:443
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		return addr[:idx]
	}
	return addr
}

func extractPort(addr string) int {
	// Handle IPv6 with brackets: [::1]:443
	if strings.HasPrefix(addr, "[") {
		end := strings.Index(addr, "]:")
		if end > 0 {
			var port int
			_, _ = parsePort(addr[end+2:], &port)
			return port
		}
		return 0
	}
	// Handle IPv4: 192.168.1.1:443
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		var port int
		_, _ = parsePort(addr[idx+1:], &port)
		return port
	}
	return 0
}

func parsePort(s string, port *int) (bool, error) {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false, nil
		}
		*port = *port*10 + int(c-'0')
		// Prevent overflow and validate port range
		if *port > 65535 {
			*port = 0
			return false, nil
		}
	}
	return *port > 0 && *port <= 65535, nil
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		// Validate float is within int range and not NaN/Inf
		if v != v || v > float64(int(^uint(0)>>1)) || v < float64(-int(^uint(0)>>1)-1) {
			return 0
		}
		return int(v)
	}
	return 0
}

func getInt64(m map[string]any, key string) int64 {
	if v, ok := m[key].(float64); ok {
		// Validate float is within int64 range and not NaN/Inf
		// Note: float64 can't exactly represent all int64 values, but this catches major issues
		if v != v || v > float64(1<<63-1) || v < float64(-1<<63) {
			return 0
		}
		return int64(v)
	}
	return 0
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
