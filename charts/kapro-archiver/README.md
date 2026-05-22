# kapro-archiver chart

`kapro-archiver` is an optional receiver for Kapro's operator-level
CloudEvents sink. It runs outside the operator and archives the original
CloudEvents structured-mode request body plus request metadata for dedupe.

Install with the default file sink:

```sh
helm install kapro-archiver ./charts/kapro-archiver \
  --namespace kapro-system \
  --set persistence.enabled=true
```

Then configure the operator with:

```sh
KAPRO_EVENTS_SINK_URL=http://kapro-archiver.kapro-system.svc:8080/
```

Use S3 instead:

```sh
helm install kapro-archiver ./charts/kapro-archiver \
  --namespace kapro-system \
  --set archiver.sink=s3 \
  --set s3.bucket=my-kapro-archive \
  --set s3.region=us-east-1
```

Provide AWS credentials with IRSA/workload identity, node credentials, or
`envFrom`/`extraEnv` values that expose standard AWS SDK environment variables.
