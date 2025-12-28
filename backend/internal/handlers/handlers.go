package handlers

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rajsinghtech/tsflow/backend/internal/database"
	"github.com/rajsinghtech/tsflow/backend/internal/services"
	tailscale "tailscale.com/client/tailscale/v2"
)

const (
	// ChunkThreshold is the duration above which log queries are chunked
	ChunkThreshold = 7 * 24 * time.Hour
	// ChunkSize is the size of each chunk for large log queries
	ChunkSize = 24 * time.Hour
	// MaxParallelChunks limits concurrent chunk fetches
	MaxParallelChunks = 2
	// MaxLogsInMemory limits logs held in memory during chunked queries
	MaxLogsInMemory = 10000
	// MaxLogsInResponse limits logs returned in a single response
	MaxLogsInResponse = 50000
	// DefaultQueryTimeout is the default timeout for database queries
	DefaultQueryTimeout = 30 * time.Second
	// ShortQueryTimeout is the timeout for quick database queries
	ShortQueryTimeout = 10 * time.Second
	// AggregationQueryTimeout is the timeout for heavy aggregation queries
	AggregationQueryTimeout = 60 * time.Second
)

type Handlers struct {
	tailscaleService *services.TailscaleService
	store            database.Store
	poller           *services.Poller
}

func NewHandlers(tailscaleService *services.TailscaleService, store database.Store, poller *services.Poller) *Handlers {
	return &Handlers{
		tailscaleService: tailscaleService,
		store:            store,
		poller:           poller,
	}
}

func (h *Handlers) HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"timestamp": time.Now().UTC(),
		"service":   "tsflow-backend",
	})
}

func (h *Handlers) GetDevices(c *gin.Context) {
	devices, err := h.tailscaleService.GetDevices()
	if err != nil {
		log.Printf("ERROR GetDevices failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch devices",
			"message": err.Error(),
		})
		return
	}

	log.Printf("SUCCESS GetDevices: returned devices successfully")
	c.JSON(http.StatusOK, devices)
}

func (h *Handlers) GetServicesAndRecords(c *gin.Context) {
	ctx := c.Request.Context()

	// Fetch VIP services
	vipServices, servicesErr := h.tailscaleService.GetVIPServices(ctx)
	if servicesErr != nil {
		log.Printf("WARNING GetVIPServices failed: %v", servicesErr)
		vipServices = make(map[string]services.VIPServiceInfo)
	}

	// Fetch static records
	staticRecords, recordsErr := h.tailscaleService.GetStaticRecords(ctx)
	if recordsErr != nil {
		log.Printf("WARNING GetStaticRecords failed: %v", recordsErr)
		staticRecords = make(map[string]services.StaticRecordInfo)
	}

	response := gin.H{
		"services": vipServices,
		"records":  staticRecords,
	}

	log.Printf("SUCCESS GetServicesAndRecords: returned %d services and %d records", len(vipServices), len(staticRecords))
	c.JSON(http.StatusOK, response)
}

func (h *Handlers) GetNetworkLogs(c *gin.Context) {
	start := c.Query("start")
	end := c.Query("end")

	if start == "" || end == "" {
		now := time.Now()
		start = now.Add(-5 * time.Minute).Format(time.RFC3339)
		end = now.Format(time.RFC3339)
	}

	st, err := time.Parse(time.RFC3339, start)
	if err != nil {
		log.Printf("ERROR GetNetworkLogs: invalid start time %s: %v", start, err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "bad start time",
			"message": err.Error(),
		})
		return
	}

	et, err := time.Parse(time.RFC3339, end)
	if err != nil {
		log.Printf("ERROR GetNetworkLogs: invalid end time %s: %v", end, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad end time", "message": err.Error()})
		return
	}

	if et.Before(st) {
		log.Printf("ERROR GetNetworkLogs: end time before start time: %s < %s", end, start)
		c.JSON(http.StatusBadRequest, gin.H{"error": "end time before start time"})
		return
	}

	now := time.Now()
	if st.After(now) {
		log.Printf("ERROR GetNetworkLogs: future start time not allowed: %s", start)
		c.JSON(http.StatusBadRequest, gin.H{"error": "future start time not allowed"})
		return
	}

	duration := et.Sub(st)
	// Use chunking for queries longer than threshold to prevent response size issues
	if duration > ChunkThreshold {
		chunks, err := h.tailscaleService.GetNetworkLogsChunkedParallel(start, end, ChunkSize, MaxParallelChunks)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Failed to fetch network logs",
				"message": err.Error(),
				"hint":    "Try selecting a smaller time range",
			})
			return
		}

		var allLogs []interface{}

		for _, chunk := range chunks {
			if logsArray, ok := chunk.([]interface{}); ok {
				if len(allLogs)+len(logsArray) > MaxLogsInMemory {
					remaining := MaxLogsInMemory - len(allLogs)
					if remaining > 0 {
						allLogs = append(allLogs, logsArray[:remaining]...)
					}
					break
				}
				allLogs = append(allLogs, logsArray...)
			} else if logsMap, ok := chunk.(map[string]interface{}); ok {
				if logs, exists := logsMap["logs"]; exists {
					if logsArray, ok := logs.([]interface{}); ok {
						if len(allLogs)+len(logsArray) > MaxLogsInMemory {
							remaining := MaxLogsInMemory - len(allLogs)
							if remaining > 0 {
								allLogs = append(allLogs, logsArray[:remaining]...)
							}
							break
						}
						allLogs = append(allLogs, logsArray...)
					} else if logsArray, ok := logs.([]tailscale.NetworkFlowLog); ok {
						for _, log := range logsArray {
							allLogs = append(allLogs, log)
						}
					}
				}
			}
		}

		// Sample logs if too many to prevent response size issues
		finalLogs := allLogs
		if len(allLogs) > MaxLogsInResponse {
			sampleRate := len(allLogs) / MaxLogsInResponse
			if sampleRate < 1 {
				sampleRate = 1
			}
			sampledLogs := make([]interface{}, 0, MaxLogsInResponse)
			for i := 0; i < len(allLogs); i += sampleRate {
				sampledLogs = append(sampledLogs, allLogs[i])
			}
			finalLogs = sampledLogs
		}

		sampleRate := 1
		if len(finalLogs) > 0 {
			sampleRate = len(allLogs) / len(finalLogs)
		}
		c.JSON(http.StatusOK, gin.H{
			"logs": finalLogs,
			"metadata": gin.H{
				"chunked":    true,
				"chunks":     len(chunks),
				"duration":   duration.String(),
				"totalLogs":  len(allLogs),
				"sampled":    len(finalLogs) < len(allLogs),
				"sampleRate": sampleRate,
			},
		})
		return
	}

	logs, err := h.tailscaleService.GetNetworkLogs(start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch network logs",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, logs)
}

// GetStoredFlowLogs retrieves recent flow logs from the local SQLite database
func (h *Handlers) GetStoredFlowLogs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	start := c.Query("start")

	var startTime time.Time
	var err error

	if start == "" {
		startTime = time.Now().Add(-10 * time.Minute)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time"})
			return
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	logs, err := h.store.GetRecentFlowLogs(ctx, startTime)
	if err != nil {
		log.Printf("ERROR GetStoredFlowLogs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch stored logs",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs": logs,
		"metadata": gin.H{
			"count":  len(logs),
			"since":  startTime,
			"source": "database",
		},
	})
}

// GetAggregatedFlowLogs returns pre-aggregated node-to-node traffic
// This is the scalable endpoint for large networks - uses pre-computed node pairs
func (h *Handlers) GetAggregatedFlowLogs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	start := c.Query("start")
	end := c.Query("end")

	var startTime, endTime time.Time
	var err error

	if start == "" {
		startTime = time.Now().Add(-1 * time.Hour)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time"})
			return
		}
	}

	if end == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, end)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time"})
			return
		}
	}

	// Calculate bucket size based on time range
	duration := endTime.Sub(startTime)
	var bucketSize int64 = 60 // 1 minute
	if duration > 24*time.Hour {
		bucketSize = 3600 // 1 hour
	}
	if duration > 7*24*time.Hour {
		bucketSize = 86400 // 1 day
	}

	var aggregates []database.NodePairAggregate
	source := "database"

	// Try rolling cache first for recent data (within last hour)
	if h.poller != nil && duration <= time.Hour {
		cache := h.poller.GetRollingCache()
		if cache.HasDataFor(startTime, endTime) {
			aggregates = cache.GetNodePairs(startTime, endTime)
			source = "cache"
		}
	}

	// Fall back to database if cache miss or no data
	if len(aggregates) == 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), AggregationQueryTimeout)
		defer cancel()

		// Use pre-computed node pair aggregates
		aggregates, err = h.store.GetNodePairAggregates(ctx, startTime, endTime, bucketSize)
		if err != nil {
			log.Printf("ERROR GetAggregatedFlowLogs: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Failed to fetch aggregated flows",
				"message": err.Error(),
			})
			return
		}
		source = "database"
	}

	// Convert to frontend-expected format
	flows := make([]gin.H, 0, len(aggregates))
	for _, agg := range aggregates {
		flows = append(flows, gin.H{
			"srcNodeId":    agg.SrcNodeID,
			"dstNodeId":    agg.DstNodeID,
			"trafficType":  agg.TrafficType,
			"totalTxBytes": agg.TxBytes,
			"totalRxBytes": agg.RxBytes,
			"totalTxPkts":  agg.TxPkts,
			"totalRxPkts":  agg.RxPkts,
			"flowCount":    agg.FlowCount,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"flows": flows,
		"metadata": gin.H{
			"count":      len(flows),
			"start":      startTime,
			"end":        endTime,
			"bucketSize": bucketSize,
			"source":     source,
		},
	})
}

// GetDataRange returns the available time range of stored data
func (h *Handlers) GetDataRange(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), ShortQueryTimeout)
	defer cancel()

	dataRange, err := h.store.GetDataRange(ctx)
	if err != nil {
		log.Printf("ERROR GetDataRange: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get data range",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, dataRange)
}

// GetPollerStatus returns the current status of the background poller
func (h *Handlers) GetPollerStatus(c *gin.Context) {
	if h.poller == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Poller not configured",
		})
		return
	}

	stats := h.poller.Stats()

	// Add database stats if available
	if h.store != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), ShortQueryTimeout)
		defer cancel()

		dbStats, err := h.store.GetStats(ctx)
		if err == nil {
			stats["database"] = dbStats
		}
	}

	c.JSON(http.StatusOK, stats)
}

// TriggerPoll manually triggers a poll (for testing/debugging)
func (h *Handlers) TriggerPoll(c *gin.Context) {
	if h.poller == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Poller not configured",
		})
		return
	}

	// The poller will poll on next tick, but we can return current stats
	stats := h.poller.Stats()
	c.JSON(http.StatusOK, gin.H{
		"message": "Poll will occur on next interval",
		"stats":   stats,
	})
}

// GetBandwidthAggregated returns aggregated bandwidth data for the chart
func (h *Handlers) GetBandwidthAggregated(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	start := c.Query("start")
	end := c.Query("end")
	nodeID := c.Query("nodeId") // Optional: filter by device ID

	var err error
	var startTime, endTime time.Time

	if start == "" {
		startTime = time.Now().Add(-1 * time.Hour)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time"})
			return
		}
	}

	if end == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, end)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time"})
			return
		}
	}

	var buckets []database.BandwidthBucket
	source := "database"

	// Try rolling cache first for recent data (within last hour)
	if h.poller != nil {
		cache := h.poller.GetRollingCache()
		if cache.HasDataFor(startTime, endTime) {
			if nodeID != "" {
				buckets = cache.GetNodeBandwidth(startTime, endTime, nodeID)
			} else {
				buckets = cache.GetBandwidth(startTime, endTime)
			}
			source = "cache"
		}
	}

	// Fall back to database if cache miss or no data
	if len(buckets) == 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
		defer cancel()

		// If nodeId provided, filter by that node
		if nodeID != "" {
			buckets, err = h.store.GetNodeBandwidth(ctx, startTime, endTime, nodeID)
		} else {
			buckets, err = h.store.GetBandwidth(ctx, startTime, endTime)
		}

		if err != nil {
			log.Printf("ERROR GetBandwidthAggregated: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Failed to fetch bandwidth data",
				"message": err.Error(),
			})
			return
		}
		source = "database"
	}

	c.JSON(http.StatusOK, gin.H{
		"buckets": buckets,
		"metadata": gin.H{
			"count":  len(buckets),
			"start":  startTime,
			"end":    endTime,
			"nodeId": nodeID,
			"source": source,
		},
	})
}

// GetBandwidthByIPs returns bandwidth data filtered by IP addresses
// This is for backwards compatibility - converts IPs to node IDs using device cache
func (h *Handlers) GetBandwidthByIPs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Database not configured",
		})
		return
	}

	start := c.Query("start")
	end := c.Query("end")
	ipsStr := c.Query("ips") // Comma-separated list of IPs

	var err error
	var startTime, endTime time.Time

	if start == "" {
		startTime = time.Now().Add(-1 * time.Hour)
	} else {
		startTime, err = time.Parse(time.RFC3339, start)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time"})
			return
		}
	}

	if end == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, end)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time"})
			return
		}
	}

	// If no IPs provided, return total bandwidth
	if ipsStr == "" {
		c.Redirect(http.StatusTemporaryRedirect, "/api/bandwidth?start="+start+"&end="+end)
		return
	}

	// Parse IPs and resolve to node IDs using device cache
	ips := strings.Split(ipsStr, ",")
	for i := range ips {
		ips[i] = strings.TrimSpace(ips[i])
	}

	// Use poller's device cache to resolve IPs to node IDs
	nodeIDs := make(map[string]bool)
	if h.poller != nil {
		cache := h.poller.GetDeviceCache()
		for _, ip := range ips {
			nodeID := cache.ResolveIP(ip)
			nodeIDs[nodeID] = true
		}
	} else {
		// Fallback: use IPs as node IDs (for external IPs)
		for _, ip := range ips {
			nodeIDs[ip] = true
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), DefaultQueryTimeout)
	defer cancel()

	// Aggregate bandwidth for all node IDs
	var allBuckets []database.BandwidthBucket
	bucketMap := make(map[int64]*database.BandwidthBucket)

	for nodeID := range nodeIDs {
		buckets, err := h.store.GetNodeBandwidth(ctx, startTime, endTime, nodeID)
		if err != nil {
			continue // Skip errors for individual nodes
		}
		for _, b := range buckets {
			bucket := b.Time.Unix()
			if existing, ok := bucketMap[bucket]; ok {
				existing.TxBytes += b.TxBytes
				existing.RxBytes += b.RxBytes
			} else {
				bucketMap[bucket] = &database.BandwidthBucket{
					Time:    b.Time,
					TxBytes: b.TxBytes,
					RxBytes: b.RxBytes,
				}
			}
		}
	}

	// Convert map to slice
	for _, b := range bucketMap {
		allBuckets = append(allBuckets, *b)
	}

	c.JSON(http.StatusOK, gin.H{
		"buckets": allBuckets,
		"metadata": gin.H{
			"count": len(allBuckets),
			"start": startTime,
			"end":   endTime,
			"ips":   ipsStr,
		},
	})
}

func (h *Handlers) GetNetworkMap(c *gin.Context) {
	networkMap, err := h.tailscaleService.GetNetworkMap()
	if err != nil {
		log.Printf("ERROR GetNetworkMap failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch network map",
			"message": err.Error(),
		})
		return
	}

	log.Printf("SUCCESS GetNetworkMap: returned network map")
	c.JSON(http.StatusOK, networkMap)
}

func (h *Handlers) GetDeviceFlows(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "Not implemented",
		"message": "Tailscale does not expose per-device flow data",
	})
}

func (h *Handlers) GetDNSNameservers(c *gin.Context) {
	nameservers, err := h.tailscaleService.GetDNSNameservers()
	if err != nil {
		log.Printf("ERROR GetDNSNameservers failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to fetch DNS nameservers",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, nameservers)
}
