# Cloudflare Tunnel (cloudflared) setup

Route external traffic to xray through a [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/), avoiding the need for a public IP or traditional ingress controller.

## How it works

```
Client (v2rayN) --WSS--> Cloudflare Edge --tunnel--> cloudflared pod --http--> xray Service (8443)
```

Cloudflare terminates TLS at the edge. The tunnel carries traffic over an outbound-only connection from your cluster to Cloudflare, so no inbound ports need to be open.

## Prerequisites

1. A Cloudflare account with a domain
2. A named tunnel created via `cloudflared tunnel create <name>`
3. DNS CNAME pointing your hostname to `<tunnel-id>.cfargotunnel.com`
4. Tunnel credentials JSON stored as a K8s Secret

## Setup

### 1. Create the tunnel

```bash
cloudflared tunnel create xray-tunnel
```

This generates a credentials file at `~/.cloudflared/<tunnel-id>.json`.

### 2. Create the DNS record

```bash
cloudflared tunnel route dns xray-tunnel hide.example.com
```

### 3. Create the K8s secret from credentials

```bash
kubectl create namespace cloudflared

kubectl create secret generic cloudflared-credentials \
  -n cloudflared \
  --from-file=credentials.json=$HOME/.cloudflared/<tunnel-id>.json
```

### 4. Deploy

Edit `config.yaml` — set your tunnel name and hostname:

```yaml
ingress:
  - hostname: hide.example.com
    service: http://xray.xray:8443
  - hostname: xadmin.example.com
    service: http://xray-admin.xray:8080
  - service: http_status:404
```

Apply the manifests:

```bash
kubectl apply -k examples/cloudflared/ -n cloudflared
```

### 5. Verify

```bash
kubectl logs -n cloudflared deploy/cloudflared
```

You should see `Connection registered` lines indicating the tunnel is active.

## Cloudflare Access (optional)

To protect the admin panel, add a Cloudflare Access application for `xadmin.example.com` with an identity provider (Google, GitHub, email OTP, etc.). This adds authentication at the edge without any changes to the xray-admin pod.
