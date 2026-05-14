# Kapro Hub Config Example

This directory shows how to manage Kapro hub cluster configuration in git.

## Structure

```
clusters/       MemberCluster definitions (one per spoke)
apps/           KaproApp definitions (components, registries, overrides)
pipelines/      Pipeline definitions (stage DAG, selectors, gates)
releases/       Release triggers (version + pipeline references)
.github/        CI workflow that applies config to the hub
```

## Usage

1. Clone this repo
2. Edit the YAMLs for your fleet
3. Push to main
4. CI validates and applies to the hub cluster
5. Kapro operator reconciles and rolls out to spokes

## Apply manually

```bash
kubectl apply -f clusters/
kubectl apply -f apps/
kubectl apply -f pipelines/
kubectl apply -f releases/
```
