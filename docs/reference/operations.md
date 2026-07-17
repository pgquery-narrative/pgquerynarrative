# Operations

Monitoring and runbooks for production. Build and deploy: [Deployment](deployment.md). Common issues: [Troubleshooting](troubleshooting.md).

---

## Monitoring

### Health checks {#health-checks}

The app exposes dedicated liveness and readiness endpoints:

| Endpoint | Purpose |
|----------|---------|
| **GET /health** | Liveness: process up, HTTP responding. Use for restart-on-failure. |
| **GET /ready** | Readiness: DB connection checked. If the DB is down, the probe fails. Use to remove from load balancer. |
| **GET /version** | JSON `{"version":"..."}`. Build-time version (e.g. 1.0.0). |
| **GET /metrics** | JSON with `version` and `pool` (acquired, idle, total, max connections). For pool visibility. |

All responses include `X-Request-ID` when request ID middleware is used. **Limitation:** Probes do not check LLM; report generation can fail while the app is "healthy."

| Environment | Probe | Endpoint |
|-------------|--------|----------|
| Docker Compose | healthcheck | GET http://localhost:8080/health (liveness), GET http://localhost:8080/ready (readiness) |
| Kubernetes / Helm | livenessProbe | GET /health (port 8080) |
| Kubernetes / Helm | readinessProbe | GET /ready (port 8080) |

### What to monitor

| Area | Watch | How |
|------|--------|-----|
| **Application** | Process up, HTTP responding | GET /health, GET /ready; alert on N consecutive failures. |
| **Database** | Connectivity, pool exhaustion | App logs (connection errors, pool health). Postgres metrics if available. |
| **LLM** | Availability, latency, errors | App logs (report errors, timeouts). Report success rate. |
| **Logs** | Errors, slow requests | Stdout/stderr; aggregator (Loki, CloudWatch). Look for pool/report failures, 5xx. |
| **Resources** | Memory, CPU | Container/pod metrics (cAdvisor, Prometheus). |

---

## Runbooks

Formal incident playbooks: [Incident runbooks](../ops/INCIDENT_RUNBOOKS.md) (5xx, auth, timeouts, scheduler, webhooks, LLM budget, pool saturation, credential rotation).

### Deploy (standard)

1. Build and push: `docker build -f deploy/docker/Dockerfile -t your-registry/pgquerynarrative:<tag> .` then `docker push ...`.
2. **Compose:** `docker compose -f deploy/docker/docker-compose.yml up -d` (or set `image:` to new tag).
3. **Kubernetes:** Update `image` in `deployment.yaml`; `kubectl apply -f deploy/kubernetes/deployment.yaml`.
4. **Helm:** `helm upgrade pgqn ./deploy/helm/pgquerynarrative -n pgquerynarrative --set image.tag=<tag>`.
5. Verify: UI at http://localhost:8080 or `curl -s http://localhost:8080/ready`; check logs.

### Rollback

1. **Compose:** Revert image; `docker compose -f deploy/docker/docker-compose.yml up -d`.
2. **Kubernetes:** Revert `deployment.yaml` and apply, or `kubectl rollout undo deployment/pgquerynarrative -n pgquerynarrative`.
3. **Helm:** `helm rollback pgqn -n pgquerynarrative` or upgrade with previous `image.tag`.
4. Confirm: same verification as deploy.

### Incident: Database unreachable

**Symptoms:** Probe failures, "connection refused" or pool health errors in logs, 503 or failed API calls.

**Actions:** Confirm Postgres is running and reachable (network, firewall, credentials). Compose: check `postgres` service and logs; restart if needed. External Postgres: check instance, connectivity, credentials. After DB is back, app should pass readiness. See [Troubleshooting – Database](troubleshooting.md#database).

### Incident: LLM unreachable or report generation failing

**Symptoms:** Report generation fails or times out; LLM errors in logs.

**Actions:** Check `LLM_BASE_URL`, `LLM_PROVIDER`, `LLM_MODEL`, `LLM_API_KEY`. [Configuration – LLM](../configuration.md#llm), [LLM setup](../getting-started/llm-setup.md). Ollama: ensure `ollama serve` and model pulled; Docker: `http://host.docker.internal:11434`. Cloud: verify API key, quota, region. See [Troubleshooting – Reports and LLM](troubleshooting.md#reports-and-llm).

### Incident: High memory or CPU

**Symptoms:** OOM, slow responses, resource alerts.

**Actions:** Check usage (e.g. `kubectl top pod`). Correlate with load (concurrent reports). Short term: scale replicas or increase limits. Long term: tune `DATABASE_MAX_CONNECTIONS` ([Configuration](../configuration.md)), consider rate limiting or queueing for reports.

### Incident: Bad deployment (wrong config or image)

**Symptoms:** Wrong behavior after deploy.

**Actions:** Roll back (see Rollback above). Fix ConfigMap/Secret or values; redeploy with correct image and config; verify.

---

## See also

- [Deployment](deployment.md) — Build, Docker, Kubernetes, Helm
- [Troubleshooting](troubleshooting.md) — Common issues
- [Configuration](../configuration.md) — Environment variables
- [Documentation index](../README.md)
