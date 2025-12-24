# TSFlow - Tailscale Network Flow Visualizer

A modern, real-time web application for visualizing and analyzing network traffic flows within Tailscale networks.

## Installation

### Homebrew (macOS/Linux)

```bash
brew install rajsinghtech/tap/tsflow
```

### Docker

```bash
docker pull ghcr.io/rajsinghtech/tsflow:latest
```

### Binary Download

Download the latest release from [GitHub Releases](https://github.com/rajsinghtech/tsflow/releases).

## Quick Start

> **Important:** TSFlow requires the **Tailscale Network Flow Logs** feature. This is available on **Premium** and **Enterprise** plans and must be enabled in your Tailscale admin console. The application will not show any flow data otherwise.

### Run with Homebrew

```bash
# Set your credentials
export TAILSCALE_OAUTH_CLIENT_ID=your-client-id
export TAILSCALE_OAUTH_CLIENT_SECRET=your-client-secret

# Run TSFlow
tsflow
```

Navigate to `http://localhost:8080` to access the dashboard.

### Run with Docker

**Using OAuth (Recommended):**
```bash
docker run -d \
  --name tsflow \
  -p 8080:8080 \
  -e TAILSCALE_OAUTH_CLIENT_ID=your-client-id \
  -e TAILSCALE_OAUTH_CLIENT_SECRET=your-client-secret \
  --restart unless-stopped \
  ghcr.io/rajsinghtech/tsflow:latest
```

**Using API Key:**
```bash
docker run -d \
  --name tsflow \
  -p 8080:8080 \
  -e TAILSCALE_API_KEY=your-api-key \
  --restart unless-stopped \
  ghcr.io/rajsinghtech/tsflow:latest
```

## Configuration

### Tailscale Network Logs

Go to the [Logs tab](https://login.tailscale.com/admin/logs) in your Tailscale Admin Console and ensure that Network Flow Logs are **enabled**. **Note**: This requires a **Premium** or **Enterprise** plan.

### Authentication Methods

TSFlow supports two authentication methods with Tailscale. You only need to configure one method.

#### Method 1: OAuth Client Credentials (Recommended)

OAuth provides better security through automatic token refresh and fine-grained permissions.

1. Go to the [OAuth clients page](https://login.tailscale.com/admin/settings/oauth) in your Tailscale Admin Console
2. Create a new OAuth client
3. Copy the Client ID and Client Secret
4. Set the following environment variables:
   - `TAILSCALE_OAUTH_CLIENT_ID=your-client-id`
   - `TAILSCALE_OAUTH_CLIENT_SECRET=your-client-secret`

#### Method 2: API Key (Legacy)

1. Go to the [API keys page](https://login.tailscale.com/admin/settings/keys) in your Tailscale Admin Console
2. Create a new API key
3. Copy the generated API key (starts with `tskey-api-`)
4. Set `TAILSCALE_API_KEY=your-api-key`

#### Organization Name
1. Go to the [Settings page](https://login.tailscale.com/admin/settings/general) in your Tailscale Admin Console
2. Your organization name is displayed in the Organization section (used by the Tailscale API)
3. Use this exact organization name for the `TAILSCALE_TAILNET` variable

#### API URL (Optional)
For most users, the default API URL works fine. However, some users may need to use region-specific endpoints:
- Default: `https://api.tailscale.com`
- US-specific: `https://api.us.tailscale.com`

Set `TAILSCALE_API_URL=https://api.us.tailscale.com` if you need the US-specific endpoint.

### Environment Variables

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `TAILSCALE_TAILNET` | Your organization name | No | - |
| `TAILSCALE_API_URL` | Tailscale API endpoint URL | No | `https://api.tailscale.com` |
| **OAuth Method** |
| `TAILSCALE_OAUTH_CLIENT_ID` | OAuth client ID | Yes* | - |
| `TAILSCALE_OAUTH_CLIENT_SECRET` | OAuth client secret | Yes* | - |
| `TAILSCALE_OAUTH_SCOPES` | OAuth scopes (comma-separated) | No | `all:read` |
| **API Key Method** |
| `TAILSCALE_API_KEY` | Your Tailscale API key | Yes* | - |
| **Other** |
| `PORT` | Backend server port | No | `8080` |

*Either OAuth credentials OR API key must be provided

## Deployment Options

### Using Docker Compose

Create a `docker-compose.yml` file:

**Using OAuth (Recommended):**
```yaml
services:
  tsflow:
    image: ghcr.io/rajsinghtech/tsflow:latest
    container_name: tsflow
    ports:
      - "8080:8080"
    environment:
      - TAILSCALE_OAUTH_CLIENT_ID=your-client-id
      - TAILSCALE_OAUTH_CLIENT_SECRET=your-client-secret
    restart: unless-stopped
```

**Using API Key:**
```yaml
services:
  tsflow:
    image: ghcr.io/rajsinghtech/tsflow:latest
    container_name: tsflow
    ports:
      - "8080:8080"
    environment:
      - TAILSCALE_API_KEY=your-api-key
    restart: unless-stopped
```

**Commands:**
```bash
# Start the application
docker-compose up -d

# View logs
docker-compose logs -f tsflow

# Update to latest version
docker-compose pull && docker-compose up -d

# Stop the application
docker-compose down
```

### Kubernetes Deployment

Deploy TSFlow on Kubernetes using the provided manifests:

#### Quick Deploy with Kustomize

```bash
git clone https://github.com/rajsinghtech/tsflow.git
cd tsflow/k8s

# Edit kustomization.yaml with your credentials
kubectl apply -k .
```

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=rajsinghtech/tsflow&type=Date)](https://star-history.com/#rajsinghtech/tsflow&Date)

---

Built with ❤️ for the Tailscale community