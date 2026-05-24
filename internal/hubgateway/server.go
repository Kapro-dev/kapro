package hubgateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// Server is the stateless Hub Gateway facade used by UI and CLI clients.
// Kubernetes CRDs remain the durable source of truth.
type Server struct {
	Client client.Client
	// BearerToken gates graph reads and promotion creation. /healthz is public.
	BearerToken []byte
}

const (
	defaultGraphLimit = 100
	maxGraphLimit     = 500
)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/api/v1/graph", s.requireAuth(s.graph))
	mux.HandleFunc("/api/v1/promotions", s.requireAuth(s.promotions))
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	opts, err := graphOptionsFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	response := GraphResponse{
		Page: GraphPage{
			Resource:      opts.resource,
			Limit:         opts.limit,
			LabelSelector: opts.labelSelector,
			Phase:         opts.phase,
			Counts:        map[string]int{},
		},
	}

	if opts.wants("fleets") {
		items, count, truncated, err := listGraphItems(
			ctx,
			s.Client,
			opts,
			func() client.ObjectList { return &kaprov1alpha1.FleetList{} },
			func(list client.ObjectList) []kaprov1alpha1.Fleet { return list.(*kaprov1alpha1.FleetList).Items },
			filterFleetsByPhase,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response.Fleets = items
		response.Page.Counts["fleets"] = count
		response.Page.Truncated = response.Page.Truncated || truncated
	}
	if opts.wants("clusters") {
		items, count, truncated, err := listGraphItems(
			ctx,
			s.Client,
			opts,
			func() client.ObjectList { return &kaprov1alpha1.ClusterList{} },
			func(list client.ObjectList) []kaprov1alpha1.Cluster {
				return list.(*kaprov1alpha1.ClusterList).Items
			},
			filterClustersByPhase,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response.Clusters = items
		response.Page.Counts["clusters"] = count
		response.Page.Truncated = response.Page.Truncated || truncated
	}
	if opts.wants("promotions") {
		items, count, truncated, err := listGraphItems(
			ctx,
			s.Client,
			opts,
			func() client.ObjectList { return &kaprov1alpha1.PromotionList{} },
			func(list client.ObjectList) []kaprov1alpha1.Promotion {
				return list.(*kaprov1alpha1.PromotionList).Items
			},
			filterPromotionsByPhase,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response.Promotions = items
		response.Page.Counts["promotions"] = count
		response.Page.Truncated = response.Page.Truncated || truncated
	}
	if opts.wants("promotionruns") {
		items, count, truncated, err := listGraphItems(
			ctx,
			s.Client,
			opts,
			func() client.ObjectList { return &kaproruntimev1alpha1.PromotionRunList{} },
			func(list client.ObjectList) []kaproruntimev1alpha1.PromotionRun {
				return list.(*kaproruntimev1alpha1.PromotionRunList).Items
			},
			filterPromotionRunsByPhase,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response.PromotionRuns = items
		response.Page.Counts["promotionruns"] = count
		response.Page.Truncated = response.Page.Truncated || truncated
	}
	if opts.wants("targets") {
		items, count, truncated, err := listGraphItems(
			ctx,
			s.Client,
			opts,
			func() client.ObjectList { return &kaproruntimev1alpha1.TargetList{} },
			func(list client.ObjectList) []kaproruntimev1alpha1.Target {
				return list.(*kaproruntimev1alpha1.TargetList).Items
			},
			filterTargetsByPhase,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response.Targets = items
		response.Page.Counts["targets"] = count
		response.Page.Truncated = response.Page.Truncated || truncated
	}
	if opts.wants("substrates") {
		items, count, truncated, err := listGraphItems(
			ctx,
			s.Client,
			opts,
			func() client.ObjectList { return &kaprov1alpha1.SubstrateList{} },
			func(list client.ObjectList) []kaprov1alpha1.Substrate {
				return list.(*kaprov1alpha1.SubstrateList).Items
			},
			func(items []kaprov1alpha1.Substrate, _ string) []kaprov1alpha1.Substrate { return items },
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response.Substrates = items
		response.Page.Counts["substrates"] = count
		response.Page.Truncated = response.Page.Truncated || truncated
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) promotions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createPromotion(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createPromotion(w http.ResponseWriter, r *http.Request) {
	var req CreatePromotionRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateCreatePromotionRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	promotion := &kaprov1alpha1.Promotion{
		TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Promotion"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   req.Name,
			Labels: req.Labels,
		},
		Spec: kaprov1alpha1.PromotionSpec{
			FleetRef:  req.FleetRef,
			Version:   req.Version,
			Versions:  req.Versions,
			Plans:     req.Plans,
			Timeout:   req.Timeout,
			Suspended: req.Suspended,
		},
	}
	if len(req.Targets) > 0 {
		promotion.Spec.Scope = &kaprov1alpha1.PromotionRunScope{Targets: req.Targets}
	}
	if err := s.Client.Create(r.Context(), promotion); err != nil {
		if apierrors.IsAlreadyExists(err) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, promotion)
}

type CreatePromotionRequest struct {
	Name      string                  `json:"name"`
	FleetRef  string                  `json:"fleetRef"`
	Version   string                  `json:"version,omitempty"`
	Versions  map[string]string       `json:"versions,omitempty"`
	Plans     []kaprov1alpha1.PlanRef `json:"plans,omitempty"`
	Targets   []string                `json:"targets,omitempty"`
	Timeout   string                  `json:"timeout,omitempty"`
	Suspended bool                    `json:"suspended,omitempty"`
	Labels    map[string]string       `json:"labels,omitempty"`
}

type GraphResponse struct {
	Fleets        []kaprov1alpha1.Fleet               `json:"fleets"`
	Clusters      []kaprov1alpha1.Cluster             `json:"clusters"`
	Promotions    []kaprov1alpha1.Promotion           `json:"promotions"`
	PromotionRuns []kaproruntimev1alpha1.PromotionRun `json:"promotionruns"`
	Targets       []kaproruntimev1alpha1.Target       `json:"targets"`
	Substrates    []kaprov1alpha1.Substrate           `json:"substrates"`
	Page          GraphPage                           `json:"page"`
}

type GraphPage struct {
	Resource      string         `json:"resource"`
	Limit         int            `json:"limit"`
	LabelSelector string         `json:"labelSelector,omitempty"`
	Phase         string         `json:"phase,omitempty"`
	Truncated     bool           `json:"truncated"`
	Counts        map[string]int `json:"counts"`
}

type graphOptions struct {
	resource      string
	resources     map[string]bool
	limit         int
	labelSelector string
	selector      labels.Selector
	phase         string
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil && !strings.Contains(err.Error(), "broken pipe") {
		_, _ = fmt.Fprintf(w, `{"error":%q}`, err.Error())
	}
}

// maxBearerHeaderLen bounds the Authorization header to keep the
// constant-time compare and the surrounding string ops finite. Real Kapro
// hub-gateway bearer tokens are <= 256 bytes; 4 KiB leaves room for future
// signed tokens (JWT etc.) without ever crossing into pathological territory.
const maxBearerHeaderLen = 4 * 1024

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.BearerToken) == 0 {
			http.Error(w, "hub gateway bearer token is not configured", http.StatusServiceUnavailable)
			return
		}
		raw := r.Header.Get("Authorization")
		if len(raw) > maxBearerHeaderLen {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token, ok := strings.CutPrefix(raw, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), s.BearerToken) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func validateCreatePromotionRequest(req CreatePromotionRequest) error {
	if req.Name == "" || req.FleetRef == "" {
		return fmt.Errorf("name and fleetRef are required")
	}
	if req.Version == "" && len(req.Versions) == 0 {
		return fmt.Errorf("version or versions is required")
	}
	if errs := validation.IsDNS1123Subdomain(req.Name); len(errs) > 0 {
		return fmt.Errorf("name must be a DNS-1123 subdomain: %s", strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Subdomain(req.FleetRef); len(errs) > 0 {
		return fmt.Errorf("fleetRef must be a DNS-1123 subdomain: %s", strings.Join(errs, "; "))
	}
	if len(req.Plans) > 64 {
		return fmt.Errorf("plans must contain at most 64 entries")
	}
	for unit, version := range req.Versions {
		if unit == "" || version == "" {
			return fmt.Errorf("versions must use non-empty unit and version values")
		}
	}
	for i, p := range req.Plans {
		if p.Name == "" || p.Plan == "" {
			return fmt.Errorf("plans[%d].name and plans[%d].plan are required", i, i)
		}
		if errs := validation.IsDNS1123Subdomain(p.Name); len(errs) > 0 {
			return fmt.Errorf("plans[%d].name must be a DNS-1123 subdomain: %s", i, strings.Join(errs, "; "))
		}
		if errs := validation.IsDNS1123Subdomain(p.Plan); len(errs) > 0 {
			return fmt.Errorf("plans[%d].plan must be a DNS-1123 subdomain: %s", i, strings.Join(errs, "; "))
		}
	}
	if req.Timeout != "" {
		if _, err := time.ParseDuration(req.Timeout); err != nil {
			return fmt.Errorf("timeout must be a Go duration: %w", err)
		}
	}
	for i, target := range req.Targets {
		if errs := validation.IsDNS1123Subdomain(target); len(errs) > 0 {
			return fmt.Errorf("targets[%d] must be a DNS-1123 subdomain: %s", i, strings.Join(errs, "; "))
		}
	}
	return nil
}

func NewHandler(c client.Client, bearerToken []byte) http.Handler {
	return (&Server{Client: c, BearerToken: bearerToken}).Handler()
}

func graphOptionsFromRequest(r *http.Request) (graphOptions, error) {
	q := r.URL.Query()
	limit := defaultGraphLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxGraphLimit {
			return graphOptions{}, fmt.Errorf("limit must be an integer between 1 and %d", maxGraphLimit)
		}
		limit = parsed
	}

	selector := labels.Everything()
	labelSelector := q.Get("labelSelector")
	if labelSelector != "" {
		parsed, err := labels.Parse(labelSelector)
		if err != nil {
			return graphOptions{}, fmt.Errorf("labelSelector is invalid: %w", err)
		}
		selector = parsed
	}

	resource := q.Get("resource")
	resources, canonical, err := parseGraphResources(resource)
	if err != nil {
		return graphOptions{}, err
	}
	return graphOptions{
		resource:      canonical,
		resources:     resources,
		limit:         limit,
		labelSelector: labelSelector,
		selector:      selector,
		phase:         q.Get("phase"),
	}, nil
}

func parseGraphResources(raw string) (map[string]bool, string, error) {
	if raw == "" || raw == "all" {
		return map[string]bool{
			"fleets":        true,
			"clusters":      true,
			"promotions":    true,
			"promotionruns": true,
			"targets":       true,
			"substrates":    true,
		}, "all", nil
	}

	out := map[string]bool{}
	var canonical []string
	for _, part := range strings.Split(raw, ",") {
		name := canonicalGraphResource(strings.TrimSpace(part))
		if name == "" {
			return nil, "", fmt.Errorf("unsupported resource %q", part)
		}
		out[name] = true
	}
	for _, name := range []string{"fleets", "clusters", "promotions", "promotionruns", "targets", "substrates"} {
		if out[name] {
			canonical = append(canonical, name)
		}
	}
	return out, strings.Join(canonical, ","), nil
}

func canonicalGraphResource(raw string) string {
	switch strings.ToLower(raw) {
	case "fleet", "fleets":
		return "fleets"
	case "cluster", "clusters":
		return "clusters"
	case "promotion", "promotions":
		return "promotions"
	case "promotionrun", "promotionruns":
		return "promotionruns"
	case "target", "targets":
		return "targets"
	case "substrate", "substrates":
		return "substrates"
	default:
		return ""
	}
}

func (o graphOptions) wants(resource string) bool {
	return o.resources[resource]
}

func listGraphItems[T any](
	ctx context.Context,
	c client.Client,
	opts graphOptions,
	newList func() client.ObjectList,
	itemsFromList func(client.ObjectList) []T,
	filter func([]T, string) []T,
) ([]T, int, bool, error) {
	var out []T
	continueToken := ""
	pageSize := int64(opts.limit + 1)
	for {
		list := newList()
		listOpts := []client.ListOption{
			client.MatchingLabelsSelector{Selector: opts.selector},
			client.Limit(pageSize),
		}
		if continueToken != "" {
			listOpts = append(listOpts, client.Continue(continueToken))
		}
		if err := c.List(ctx, list, listOpts...); err != nil {
			return nil, 0, false, err
		}

		out = append(out, filter(itemsFromList(list), opts.phase)...)
		if len(out) > opts.limit {
			return out[:opts.limit], opts.limit, true, nil
		}

		continueToken = list.GetContinue()
		if continueToken == "" {
			return out, len(out), false, nil
		}
	}
}

func filterFleetsByPhase(items []kaprov1alpha1.Fleet, phase string) []kaprov1alpha1.Fleet {
	if phase == "" {
		return items
	}
	out := items[:0]
	for _, item := range items {
		ready := metav1.ConditionUnknown
		for _, cond := range item.Status.Conditions {
			if cond.Type == "Ready" {
				ready = cond.Status
				break
			}
		}
		if strings.EqualFold(string(ready), phase) || strings.EqualFold(item.Status.Version, phase) {
			out = append(out, item)
		}
	}
	return out
}

func filterClustersByPhase(items []kaprov1alpha1.Cluster, phase string) []kaprov1alpha1.Cluster {
	if phase == "" {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if strings.EqualFold(string(item.Status.Phase), phase) {
			out = append(out, item)
		}
	}
	return out
}

func filterPromotionsByPhase(items []kaprov1alpha1.Promotion, phase string) []kaprov1alpha1.Promotion {
	if phase == "" {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if strings.EqualFold(string(item.Status.Phase), phase) {
			out = append(out, item)
		}
	}
	return out
}

func filterPromotionRunsByPhase(items []kaproruntimev1alpha1.PromotionRun, phase string) []kaproruntimev1alpha1.PromotionRun {
	if phase == "" {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if strings.EqualFold(string(item.Status.Phase), phase) {
			out = append(out, item)
		}
	}
	return out
}

func filterTargetsByPhase(items []kaproruntimev1alpha1.Target, phase string) []kaproruntimev1alpha1.Target {
	if phase == "" {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if strings.EqualFold(string(item.Status.Phase), phase) {
			out = append(out, item)
		}
	}
	return out
}

func Start(ctx context.Context, addr string, c client.Client, bearerToken []byte) error {
	server := &http.Server{Addr: addr, Handler: NewHandler(c, bearerToken)}
	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		return server.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}
