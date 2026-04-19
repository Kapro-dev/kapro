// Package eks implements the KCI connector for Amazon EKS clusters.
//
// Auth model: IRSA (IAM Roles for Service Accounts) — no long-lived credentials.
// The hub operator's ServiceAccount must be annotated with
//   eks.amazonaws.com/role-arn: arn:aws:iam::ACCOUNT:role/ROLE
// That IAM role needs eks:DescribeCluster on the target cluster.
//
// Cluster describe: raw SigV4-signed HTTPS request to the EKS regional endpoint.
// No aws-sdk-go-v2/service/eks package needed — the REST API is trivial.
//
// Kubernetes auth: pre-signed STS GetCallerIdentity URL, prefixed "k8s-aws-v1.".
// This is the same mechanism as `aws eks get-token --cluster-name NAME`.
// Tokens are valid for 15 minutes; oauth2.ReuseTokenSource caches until expiry.
package eks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithymiddleware "github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"golang.org/x/oauth2"
	k8srest "k8s.io/client-go/rest"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// clusterInfo holds the fields we need from the EKS DescribeCluster response.
type clusterInfo struct {
	Endpoint string // e.g. "https://ABCDEF.gr7.us-east-1.eks.amazonaws.com"
	CAData   string // base64-encoded PEM CA certificate
}

// Connector implements the KCI interface for EKS clusters.
//
// It is constructed via New() for production use or via newTestConn() in tests.
// The two injected functions decouple the real AWS SDK calls from unit tests,
// following the same injection pattern used by the GKE connector.
type Connector struct {
	// getCluster fetches cluster connectivity details (endpoint + CA).
	// In production this calls the EKS DescribeCluster REST API via SigV4.
	// In tests it returns a pre-built *clusterInfo stub.
	getCluster func(ctx context.Context, region, clusterName string) (*clusterInfo, error)

	// newTokenSource returns a token source for Kubernetes bearer-token auth.
	// In production this generates EKS pre-signed STS tokens.
	// In tests it returns a static oauth2.TokenSource.
	newTokenSource func(ctx context.Context, region, clusterName string) (oauth2.TokenSource, error)
}

// New returns a Connector wired with real AWS SDK calls.
// Credentials are resolved via the standard AWS credential chain:
// environment variables → ~/.aws/credentials → EC2/ECS IMDS → IRSA projected token.
func New() *Connector {
	return &Connector{
		getCluster:     awsDescribeCluster,
		newTokenSource: awsTokenSource,
	}
}

// Connect builds a *k8srest.Config for the EKS cluster referenced by env.
// The returned config uses WrapTransport so the oauth2 token is refreshed
// automatically — no static BearerToken is ever stored.
func (c *Connector) Connect(ctx context.Context, env *kaprov1alpha1.Environment) (*k8srest.Config, error) {
	if env == nil {
		return nil, fmt.Errorf("eks connector: environment is nil")
	}
	spec := env.Spec.Provider
	if spec == nil || spec.EKS == nil {
		return nil, fmt.Errorf("eks connector: environment %q has no EKS provider spec", env.Name)
	}
	if err := validateSpec(spec.EKS, env.Name); err != nil {
		return nil, err
	}

	cluster, err := c.getCluster(ctx, spec.EKS.Region, spec.EKS.ClusterName)
	if err != nil {
		return nil, fmt.Errorf("eks connector: describe cluster %q in %s: %w",
			spec.EKS.ClusterName, spec.EKS.Region, err)
	}

	ts, err := c.newTokenSource(ctx, spec.EKS.Region, spec.EKS.ClusterName)
	if err != nil {
		return nil, fmt.Errorf("eks connector: token source for cluster %q: %w",
			spec.EKS.ClusterName, err)
	}

	return buildConfig(cluster, ts)
}

// IsReachable returns (true, nil) when the cluster's /readyz endpoint responds
// with HTTP 200.  A network failure returns (false, nil) — not an error — so
// the SyncReconciler retries instead of marking the Sync as Failed.
// Only configuration errors (missing spec, bad CA, etc.) are returned as errors.
func (c *Connector) IsReachable(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error) {
	cfg, err := c.Connect(ctx, env)
	if err != nil {
		return false, err
	}
	hc, err := k8srest.HTTPClientFor(cfg)
	if err != nil {
		return false, fmt.Errorf("eks connector: build HTTP client: %w", err)
	}
	url := cfg.Host + "/readyz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("eks connector: build readyz request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		// Network unreachability is not a permanent error — reconciler should retry.
		return false, nil
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// ── buildConfig ──────────────────────────────────────────────────────────────

// buildConfig assembles a *k8srest.Config from cluster connectivity info and
// an oauth2.TokenSource.  The token source is wrapped so tokens refresh
// automatically — the Config never holds a static BearerToken field.
func buildConfig(cluster *clusterInfo, ts oauth2.TokenSource) (*k8srest.Config, error) {
	if cluster.Endpoint == "" {
		return nil, fmt.Errorf("eks connector: cluster endpoint is empty")
	}
	caBytes, err := base64.StdEncoding.DecodeString(cluster.CAData)
	if err != nil {
		return nil, fmt.Errorf("eks connector: decode CA certificate: %w", err)
	}
	if len(caBytes) == 0 {
		return nil, fmt.Errorf("eks connector: CA certificate is empty")
	}

	// Ensure the host has the https:// scheme expected by client-go.
	host := cluster.Endpoint
	if !strings.HasPrefix(host, "https://") {
		host = "https://" + host
	}

	cfg := &k8srest.Config{
		Host: host,
		TLSClientConfig: k8srest.TLSClientConfig{
			CAData: caBytes,
		},
	}
	// WrapTransport injects the OAuth2 bearer token on every request.
	// ReuseTokenSource caches the token until five seconds before it expires,
	// then calls ts.Token() to obtain a fresh EKS pre-signed URL.
	cached := oauth2.ReuseTokenSource(nil, ts)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return &oauth2.Transport{Source: cached, Base: rt}
	}
	return cfg, nil
}

// ── validateSpec ─────────────────────────────────────────────────────────────

// validateSpec returns a descriptive error if any required EKS spec fields are empty.
func validateSpec(spec *kaprov1alpha1.EKSProviderSpec, envName string) error {
	var missing []string
	if spec.Region == "" {
		missing = append(missing, "region")
	}
	if spec.ClusterName == "" {
		missing = append(missing, "clusterName")
	}
	if len(missing) > 0 {
		return fmt.Errorf("eks connector: environment %q missing required EKS spec field(s): %s",
			envName, strings.Join(missing, ", "))
	}
	return nil
}

// ── Production AWS implementations ───────────────────────────────────────────

// eksDescribeResponse is the JSON shape returned by
//
//	GET https://eks.{region}.amazonaws.com/clusters/{name}
//
// We only unmarshal the fields Kapro needs.
type eksDescribeResponse struct {
	Cluster struct {
		Endpoint            string `json:"endpoint"`
		CertificateAuthority struct {
			Data string `json:"data"`
		} `json:"certificateAuthority"`
	} `json:"cluster"`
}

// awsDescribeCluster calls the EKS regional REST API with SigV4 signing and
// returns the cluster's API server endpoint and CA certificate.
//
// Credentials are resolved via the default AWS credential chain (env vars,
// ~/.aws, IMDS, IRSA projected token).  No extra EKS SDK package is required —
// the DescribeCluster API is a single signed GET request.
func awsDescribeCluster(ctx context.Context, region, clusterName string) (*clusterInfo, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	url := fmt.Sprintf("https://eks.%s.amazonaws.com/clusters/%s", region, clusterName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build describe-cluster request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	// Retrieve credentials and sign with SigV4.
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve AWS credentials: %w", err)
	}

	// The EKS describe-cluster endpoint has an empty body; the canonical
	// SHA-256 of an empty string is well-known.
	const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	signer := v4.NewSigner()
	if err := signer.SignHTTP(ctx, creds, req, emptyPayloadSHA256, "eks", region, time.Now()); err != nil {
		return nil, fmt.Errorf("sign describe-cluster request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call EKS describe-cluster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("EKS API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var r eksDescribeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode describe-cluster response: %w", err)
	}
	return &clusterInfo{
		Endpoint: r.Cluster.Endpoint,
		CAData:   r.Cluster.CertificateAuthority.Data,
	}, nil
}

// awsTokenSource returns an oauth2.TokenSource that generates EKS bearer tokens.
//
// EKS authenticates Kubernetes clients via a pre-signed STS GetCallerIdentity
// URL — this is the same mechanism as `aws eks get-token`.  The pre-signed URL
// is encoded as:
//
//	"k8s-aws-v1." + base64url(presigned_url)
//
// The x-k8s-aws-id header (signed into the URL) tells the EKS API server
// which cluster this token is for, preventing cross-cluster replay attacks.
// Tokens are valid for 15 minutes; ReuseTokenSource in buildConfig caches them.
func awsTokenSource(ctx context.Context, region, clusterName string) (oauth2.TokenSource, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config for token source: %w", err)
	}
	return &eksTokenSource{
		stsClient:   sts.NewFromConfig(cfg),
		clusterName: clusterName,
		region:      region,
	}, nil
}

// eksTokenSource implements oauth2.TokenSource for EKS using pre-signed STS URLs.
type eksTokenSource struct {
	stsClient   *sts.Client
	clusterName string
	region      string
}

// Token generates a fresh EKS bearer token by pre-signing a STS
// GetCallerIdentity request.  The x-k8s-aws-id header is added as a signed
// header so the EKS authenticator can identify which cluster is being targeted.
func (s *eksTokenSource) Token() (*oauth2.Token, error) {
	ctx := context.Background()

	presignClient := sts.NewPresignClient(s.stsClient)

	// clusterName is captured in the closure so the middleware is specific to
	// this token source instance.
	clusterName := s.clusterName
	presigned, err := presignClient.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{},
		func(o *sts.PresignOptions) {
			o.ClientOptions = append(o.ClientOptions, func(o *sts.Options) {
				// Inject x-k8s-aws-id as a signed header before presigning.
				// The EKS token authenticator validates this header is present
				// and matches the cluster's name to prevent cross-cluster reuse.
				o.APIOptions = append(o.APIOptions, func(stack *smithymiddleware.Stack) error {
					return stack.Build.Add(
						smithymiddleware.BuildMiddlewareFunc("AddEKSClusterHeader",
							func(ctx context.Context,
								in smithymiddleware.BuildInput,
								next smithymiddleware.BuildHandler,
							) (smithymiddleware.BuildOutput, smithymiddleware.Metadata, error) {
								if r, ok := in.Request.(*smithyhttp.Request); ok {
									r.Header.Set("x-k8s-aws-id", clusterName)
								}
								return next.HandleBuild(ctx, in)
							}),
						smithymiddleware.Before,
					)
				})
			})
		},
	)
	if err != nil {
		return nil, fmt.Errorf("eks token: presign STS GetCallerIdentity: %w", err)
	}

	// EKS bearer token format: "k8s-aws-v1." + base64url(presigned_url)
	token := "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(presigned.URL))
	return &oauth2.Token{
		AccessToken: token,
		// STS pre-signed URLs for EKS have a 15-minute validity window.
		// We set expiry to 14 minutes so ReuseTokenSource refreshes before expiry.
		Expiry: time.Now().Add(14 * time.Minute),
	}, nil
}
