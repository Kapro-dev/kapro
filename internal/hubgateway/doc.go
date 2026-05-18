// Package hubgateway implements the stateless HTTP gateway used by UI and CLI
// clients to read Kapro fleet state and create PromotionRun objects.
//
// Endpoints (all but /healthz require BearerToken auth):
//
//	GET  /healthz                       — liveness probe
//	GET  /api/v1/graph                  — paginated graph read of fleet resources
//	                                      (kapros, fleetclusters, promotionruns,
//	                                      promotiontargets, backendprofiles) with
//	                                      label selector + phase filter + bounded
//	                                      limit + truncation flag.
//	POST /api/v1/promotionruns          — create a PromotionRun from a JSON body.
//
// Source of truth: Kubernetes API. The gateway is a thin read/create facade —
// it caches nothing, owns no state, and never mutates objects beyond Create.
// Bounded reads (defaultGraphLimit=100, maxGraphLimit=500) prevent pathological
// label-selector queries from scanning the full fleet.
//
// This package is distinct from:
//   - internal/webhook/server.go      — approval webhook (/approve, /reject)
//     plus the Decision API HTTP mount.
//   - internal/webhook/decision_api.go — Decision API for AI agents: promotion
//     context, gate evaluation context,
//     decision submission with audit trail.
//
// The hub gateway is for humans-with-tools (UI, CLI, dashboards); the Decision
// API is for autonomous agents that need richer per-decision context.
//
// Wiring: cmd/operator/main.go mounts the gateway via a leader-only HTTP
// server using NewHandler(mgr.GetClient(), cc.ApprovalSecret).
package hubgateway
