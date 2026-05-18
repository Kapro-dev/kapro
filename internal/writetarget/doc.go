// Package writetarget renders PromotionTarget version writes as concrete
// in-tree file mutations for backends that consume YAML/Kustomize trees
// (e.g. a Git-based source-of-truth). It does not talk to a cluster API;
// it produces deterministic byte-level patches.
//
// Consumers: actuator implementations and the kapro CLI when staging a
// promotion to a Git repo.
//
// Non-goals: cluster reconciliation, OCI artifact pulls, server-side apply —
// those live in internal/delivery and the actuator packages.
package writetarget
