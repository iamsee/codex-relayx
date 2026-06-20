# Deployment

## Docker Compose (single host)

```yaml
version: "3.8"

services:
  codex-relayx:
    image: isvbytes/codex-relayx:latest
    container_name: codex-relayx
    restart: unless-stopped
    ports:
      - "8001:8001"
    volumes:
      - ./data:/var/lib/codex-relayx
    environment:
      - TZ=Asia/Shanghai
      - RELAYX_DATA_DIR=/var/lib/codex-relayx
```

```bash
mkdir -p data
docker compose up -d
# → http://localhost:8001/
```

## Kubernetes

The recommended approach is to mount a hostPath or PVC for `config.json`
persistence and inject the upstream API key via a Secret. A full example is
[`k8s.yaml.example`](../k8s.yaml.example) — copy it to `k8s.yaml` and edit
before applying.

```bash
# 1. Build / pull the image
docker pull isvbytes/codex-relayx:latest

# 2. Apply
kubectl apply -f k8s.yaml

# 3. Verify
kubectl -n app get pod -l app=codex-relayx -w
kubectl -n app logs -f deployment/codex-relayx
```

### Resource sizing

The single-binary runtime is tiny. The following baseline is plenty for a
sidecar-style deployment with a few RPS:

```yaml
resources:
  requests:
    cpu: 10m
    memory: 16Mi
  limits:
    cpu: 200m
    memory: 128Mi
```

### Exposing externally

Two common options:

- **NodePort** — expose port 30011 → 8001, access via `http://<node-ip>:30011`
- **Ingress** — terminate TLS at the cluster ingress, route by host
  (e.g. `codex-relayx.example.com`)

Ingress example:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: codex-relayx
  annotations:
    traefik.ingress.kubernetes.io/router.entrypoints: websecure
    traefik.ingress.kubernetes.io/router.tls: "true"
spec:
  rules:
    - host: codex-relayx.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: codex-relayx
                port:
                  number: 8001
```

### Persistence

`config.json` is written atomically by the admin API. A `hostPath` mount is
fine for single-replica deployments:

```yaml
volumes:
  - name: data
    hostPath:
      path: /var/lib/codex-relayx
      type: DirectoryOrCreate
```

For multi-replica, use a `PersistentVolumeClaim` with `ReadWriteOnce` and run a
single replica (admin API writes are not multi-writer safe today).

## Bare metal / systemd

```ini
# /etc/systemd/system/codex-relayx.service
[Unit]
Description=Codex-Relayx
After=network.target

[Service]
Type=simple
User=codex
WorkingDirectory=/opt/codex-relayx
ExecStart=/opt/codex-relayx/codex-relayx --port 8001 --data-dir /var/lib/codex-relayx
Restart=on-failure
RestartSec=5
Environment=TZ=Asia/Shanghai

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd -r -s /usr/sbin/nologin codex
sudo install -d -o codex -g codex /opt/codex-relayx /var/lib/codex-relayx
sudo install -m 0755 codex-relayx /opt/codex-relayx/codex-relayx
sudo systemctl daemon-reload
sudo systemctl enable --now codex-relayx
```

## Reverse proxy / TLS

When running behind nginx / Caddy / Traefik, make sure to forward the original
`Authorization` and `x-api-key` headers; the upstream auth is decided by
`api_format` and the headers are re-set by codex-relayx itself, so the proxy
does not need to inspect bodies.
