import type { 
  TailscaleDevice, 
  TailscaleConfig,
  ApiError,
  NetworkFlowLog,
  NetworkTopology
} from '@/types/tailscale'
// Note: Validation imports removed as they're not critical for functionality
// import { validateDeviceResponse, validateNetworkLogsResponse } from './validation'

// Backend configuration - uses relative URLs in both dev and prod
// Dev: Vite proxy forwards /api/* to backend at localhost:8080
// Prod: Same origin (frontend embedded in backend binary)
const BACKEND_BASE_URL = ''

class TailscaleAPI {
  private baseUrl: string

  constructor() {
    this.baseUrl = BACKEND_BASE_URL
  }

  private async request<T>(endpoint: string, options: Record<string, unknown> = {}): Promise<T> {
    const url = `${this.baseUrl}${endpoint}`

    try {
      const response = await fetch(url, {
        method: 'GET',
        headers: {
          'Content-Type': 'application/json',
        },
        ...options,
      })

      if (!response.ok) {
        const errorData = await response.json().catch(() => ({}))
        throw {
          message: errorData.message || errorData.error || `HTTP ${response.status}: ${response.statusText}`,
          status: response.status,
          details: errorData
        } as ApiError
      }

      return await response.json()
    } catch (error) {
      // Handle network errors
      if (error instanceof TypeError) {
        if (error.message.includes('Failed to fetch')) {
          throw {
            message: 'Cannot connect to backend server. Make sure the Go backend is running on port 8080.',
            status: 0,
          } as ApiError
        }
      }
      
      // Re-throw API errors
      throw error
    }
  }

  // Get all devices in the tailnet
  async getDevices(): Promise<TailscaleDevice[]> {
    const response = await this.request<{ devices: TailscaleDevice[] } | TailscaleDevice[]>(
      `/api/devices`
    )
    
    // Handle both array and object responses
    if (Array.isArray(response)) {
      return response
    } else if (response && typeof response === 'object' && 'devices' in response) {
      return response.devices || []
    }
    
    return []
  }

  // Get network flow logs
  async getNetworkLogs(queryParams?: string): Promise<NetworkFlowLog[]> {
    const endpoint = queryParams ? `/api/network-logs?${queryParams}` : `/api/network-logs`
    const response = await this.request<NetworkFlowLog[] | { logs: NetworkFlowLog[], metadata?: Record<string, unknown> }>(endpoint)
    
    // Handle both array and object responses
    if (Array.isArray(response)) {
      return response
    } else if (response && typeof response === 'object' && 'logs' in response) {
      return response.logs || []
    }
    
    return []
  }

  // Get network map data
  async getNetworkMap(): Promise<NetworkTopology> {
    return await this.request(`/api/network-map`)
  }

  // Get device flows
  async getDeviceFlows(deviceId: string): Promise<NetworkFlowLog[]> {
    return await this.request(`/api/devices/${deviceId}/flows`)
  }

  // Get DNS nameservers
  async getDNSNameservers(): Promise<{ dns: string[], magicDNS: boolean, domains: string[] }> {
    return await this.request('/api/dns/nameservers')
  }

  // Get services and static records
  async getServicesAndRecords(): Promise<{ services: Record<string, any>, records: Record<string, any> }> {
    return await this.request('/api/services-records')
  }

  // Health check
  async healthCheck(): Promise<{ status: string, version?: string }> {
    return await this.request('/health')
  }

  // Legacy method for backwards compatibility
  updateConfig(_newConfig: Partial<TailscaleConfig>) {
    console.warn('updateConfig is deprecated when using backend API')
  }

  // Legacy method for backwards compatibility
  getConfig(): Omit<TailscaleConfig, 'apiKey'> {
    return {
      tailnet: 'configured-on-backend',
      baseUrl: this.baseUrl
    }
  }

  // Check if backend is configured and available
  async isConfigured(): Promise<boolean> {
    try {
      await this.healthCheck()
      return true
    } catch {
      return false
    }
  }

  // Legacy method for backwards compatibility
  getMaskedApiKey(): string {
    return 'Configured on backend'
  }
}

// Create singleton instance
export const tailscaleAPI = new TailscaleAPI()

// Type-specific fetchers
export const devicesFetcher = async (): Promise<TailscaleDevice[]> => {
  return tailscaleAPI.getDevices()
}

export const networkLogsFetcher = async (url: string): Promise<NetworkFlowLog[]> => {
  const urlObj = new URL(url, 'http://localhost')
  const queryParams = urlObj.search.substring(1)
  return tailscaleAPI.getNetworkLogs(queryParams)
}

export const dnsNameserversFetcher = async (): Promise<{ dns: string[], magicDNS: boolean, domains: string[] }> => {
  return tailscaleAPI.getDNSNameservers()
}

// SWR fetcher function with better error handling
export const fetcher = async (url: string) => {
  try {
    // Handle different types of requests based on URL pattern
    if (url.startsWith('/devices')) {
      return await tailscaleAPI.getDevices()
    }
    
    if (url.startsWith('/network-logs')) {
      // Extract query parameters from URL
      const urlObj = new URL(url, 'http://localhost') // dummy base for parsing
      const queryParams = urlObj.search.substring(1) // Remove the '?' prefix
      return await tailscaleAPI.getNetworkLogs(queryParams)
    }

    if (url.startsWith('/network-map')) {
      return await tailscaleAPI.getNetworkMap()
    }
    
    if (url.startsWith('/services-records')) {
      return await tailscaleAPI.getServicesAndRecords()
    }
    
    if (url.includes('/flows')) {
      const deviceId = url.split('/')[2] // Extract device ID from URL like /devices/123/flows
      return await tailscaleAPI.getDeviceFlows(deviceId)
    }
    
    throw new Error(`Unknown API endpoint: ${url}`)
  } catch (error: unknown) {
    // Re-throw with better context
    if (typeof error === 'object' && error !== null && 'status' in error) {
      const apiError = error as ApiError
      if (apiError.status === 500) {
        throw new Error('Backend server error. Please check backend configuration and logs.')
      } else if (apiError.status === 401 || apiError.status === 403) {
        throw new Error('Backend authentication error. Please check backend Tailscale API credentials.')
      }
    }
    throw error
  }
}

// Utility function to format RFC3339 timestamp
export const toRFC3339 = (date: Date): string => {
  return date.toISOString()
}

// Utility function to parse search queries
export const parseSearchQuery = (query: string) => {
  const parts = query.toLowerCase().split(/\s+/)
  const parsed: Record<string, string> = {}
  
  for (const part of parts) {
    if (part.includes(':')) {
      const [key, value] = part.split(':', 2)
      parsed[key] = value
    } else {
      parsed.text = part
    }
  }
  
  return parsed
}

// Utility function to format bytes
export const formatBytes = (bytes: number): string => {
  if (bytes === 0) return '0 B'
  
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(2))} ${sizes[i]}`
}

// Utility function to format duration
export const formatDuration = (ms: number): string => {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  if (ms < 3600000) return `${(ms / 60000).toFixed(1)}m`
  return `${(ms / 3600000).toFixed(1)}h`
}

export default tailscaleAPI 