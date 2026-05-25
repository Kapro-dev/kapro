# ClusterTemplate

Example `ClusterTemplate` manifest for generating cluster records from a
template.

```text
ClusterTemplate -> generated Cluster objects
```

Apply with:

```bash
kubectl apply -f examples/04-substrates/03-clustertemplate/clustertemplate.yaml
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/04-substrates/03-clustertemplate/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/04-substrates/03-clustertemplate/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/04-substrates/03-clustertemplate --ignore-not-found
```
