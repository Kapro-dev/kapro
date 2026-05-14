# Kapro Hub Config Example

Copy this directory into your own hub config repository. The nested `.github/workflows/` is designed for that repo, not for the Kapro source repo.

## Structure

```
clusters/       MemberCluster definitions (one per spoke)
apps/           KaproApp definitions (components, registries, overrides)
pipelines/      Pipeline definitions (stage DAG, selectors, gates)
releases/       Release objects (version + pipeline references)
.github/        CI workflow that validates and applies config to the hub
```

## Usage

1. Copy this directory into a new git repo
2. Configure hub cluster auth in the CI workflow
3. Edit the YAMLs for your fleet
4. Push to main
5. CI validates (server-side dry-run) and applies to the hub cluster
6. Kapro operator reconciles and rolls out to spokes

## Apply manually

```bash
kubectl apply -f clusters/
kubectl apply -f apps/
kubectl apply -f pipelines/
kubectl apply -f releases/
```
