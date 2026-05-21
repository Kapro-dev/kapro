## Summary

<!-- 1-3 bullets: what changed, why -->

## Test plan

- [ ] `go test ./...`
- [ ] If CRDs changed: `make manifests` clean and bootstrap/helm CRD copies regenerated
- [ ] If `pkg/events` vocabulary changed: `docs/events.md` updated to match (see `.github/CONTRIBUTING_EVENTS.md`)
- [ ] If a new feature: example added under `examples/` or `docs/` updated
- [ ] Manual test on `kind-kind` if behavior touches reconcile

## Linked issues / ADRs

<!-- Closes #N, Refs #M, ADR-NNNN -->
