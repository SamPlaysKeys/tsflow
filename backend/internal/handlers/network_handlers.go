package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handlers) GetNetworkMap(c *gin.Context) {
	networkMap, err := h.tailscaleService.GetNetworkMap()
	if err != nil {
		log.Printf("ERROR GetNetworkMap: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to fetch network map",
		})
		return
	}

	c.JSON(http.StatusOK, networkMap)
}

func (h *Handlers) GetDeviceFlows(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "Not implemented",
		"message": "Tailscale does not expose per-device flow data",
	})
}
