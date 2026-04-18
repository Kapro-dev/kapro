# Kapro MCP Server

Kapro ships a built-in [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server
that lets AI assistants (Claude, GitHub Copilot, Cursor) query and control promotions in real time.

No other GitOps tool has a native MCP server. With Kapro, your AI assistant becomes a first-class
operator for progressive delivery.

## Quickstart

The MCP server starts automatically on `:8090`. Control the address with `KAPRO_MCP_ADDR`.

Add it to your AI assistant config:

### Claude Desktop (`claude_desktop_config.json`)

```json
{
  "mcpServers": {
    "kapro": {
      "url": "http://kapro-operator.kapro-system.svc.cluster.local:8090/mcp"
    }
  }
}
```

### Cursor / VS Code MCP extension

```json
{
  "mcp": {
    "servers": {
      "kapro": {
        "url": "http://localhost:8090/mcp"
      }
    }
  }
}
```

> **Port-forward for local access:**
> ```bash
> kubectl port-forward svc/kapro-operator -n kapro-system 8090:8090
> ```

---

## Available Tools

| Tool | Description |
|---|---|
| `kapro_list_releases` | List all active releases with phase |
| `kapro_get_release_status` | Detailed release + pipeline status |
| `kapro_list_promotions` | List promotions (filter by release or pending-approval) |
| `kapro_approve_promotion` | Approve a manual gate (creates Approval CR) |
| `kapro_get_environment_health` | Environment health + active release |
| `kapro_rollback` | Trigger emergency rollback (bypass gate) |

## Available Resources

| Resource URI | Description |
|---|---|
| `kapro://releases` | All releases across all namespaces |
| `kapro://releases/{namespace}/{name}` | Full Release spec + status |
| `kapro://environments` | All environments with health status |
| `kapro://promotions/pending-approval` | Promotions awaiting manual approval |

---

## Example AI Prompts

**Check rollout status:**
> "What's the rollout status of ocs-v1.2.4 across all country clusters?"

**See what needs approval:**
> "Which promotions are waiting for manual approval right now?"

**Approve a promotion:**
> "Approve the DE prod promotion for ocs-v1.2.4. Accuracy gate passed, p95 latency is 320ms."

**Trigger a rollback:**
> "Roll back llm-retail-v2.1.0 in de-llm-prod — checkout error rate spiked."

**AI model rollout:**
> "Show me all AI model environments and their active release versions."

---

## Protocol Details

- **Transport:** HTTP POST to `/mcp`
- **Protocol:** JSON-RPC 2.0
- **MCP version:** 2024-11-05
- **Auth:** None by default — use a Kubernetes NetworkPolicy or Ingress auth to restrict access

## Health Check

```bash
curl http://localhost:8090/healthz
# → 200 OK
```
