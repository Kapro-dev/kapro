package hubgateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Server is the stateless Hub Gateway facade used by UI and CLI clients.
// Kubernetes CRDs remain the durable source of truth.
type Server struct {
	Client client.Client
	// BearerToken gates graph reads and promotionrun creation. /healthz is public.
	BearerToken []byte
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/api/v1/graph", s.requireAuth(s.graph))
	mux.HandleFunc("/api/v1/promotionruns", s.requireAuth(s.promotionruns))
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

	ctx := r.Context()
	var kapros kaprov1alpha1.KaproList
	var clusters kaprov1alpha1.FleetClusterList
	var promotionruns kaprov1alpha1.PromotionRunList
	var targets kaprov1alpha1.PromotionTargetList
	var backends kaprov1alpha1.BackendProfileList
	if err := firstErr(
		s.Client.List(ctx, &kapros),
		s.Client.List(ctx, &clusters),
		s.Client.List(ctx, &promotionruns),
		s.Client.List(ctx, &targets),
		s.Client.List(ctx, &backends),
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, GraphResponse{
		Kapros:           kapros.Items,
		FleetClusters:    clusters.Items,
		PromotionRuns:    promotionruns.Items,
		PromotionTargets: targets.Items,
		BackendProfiles:  backends.Items,
	})
}

func (s *Server) promotionruns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createPromotionRun(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createPromotionRun(w http.ResponseWriter, r *http.Request) {
	var req CreatePromotionRunRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateCreatePromotionRunRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	promotionrun := &kaprov1alpha1.PromotionRun{
		TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "PromotionRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   req.Name,
			Labels: req.Labels,
		},
		Spec: kaprov1alpha1.PromotionRunSpec{
			Version:        req.Version,
			Versions:       req.Versions,
			PromotionPlans: req.PromotionPlans,
			Timeout:        req.Timeout,
		},
	}
	if len(req.Targets) > 0 {
		promotionrun.Spec.Scope = &kaprov1alpha1.PromotionRunScope{Targets: req.Targets}
	}
	if err := s.Client.Create(r.Context(), promotionrun); err != nil {
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
	writeJSON(w, http.StatusCreated, promotionrun)
}

type CreatePromotionRunRequest struct {
	Name           string                           `json:"name"`
	Version        string                           `json:"version,omitempty"`
	Versions       map[string]string                `json:"versions,omitempty"`
	PromotionPlans []kaprov1alpha1.PromotionPlanRef `json:"promotionplans"`
	Targets        []string                         `json:"targets,omitempty"`
	Timeout        string                           `json:"timeout,omitempty"`
	Labels         map[string]string                `json:"labels,omitempty"`
}

type GraphResponse struct {
	Kapros           []kaprov1alpha1.Kapro           `json:"kapros"`
	FleetClusters    []kaprov1alpha1.FleetCluster    `json:"fleetClusters"`
	PromotionRuns    []kaprov1alpha1.PromotionRun    `json:"promotionruns"`
	PromotionTargets []kaprov1alpha1.PromotionTarget `json:"promotionTargets"`
	BackendProfiles  []kaprov1alpha1.BackendProfile  `json:"backendProfiles"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil && !strings.Contains(err.Error(), "broken pipe") {
		_, _ = fmt.Fprintf(w, `{"error":%q}`, err.Error())
	}
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.BearerToken) == 0 {
			http.Error(w, "hub gateway bearer token is not configured", http.StatusServiceUnavailable)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), s.BearerToken) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func validateCreatePromotionRunRequest(req CreatePromotionRunRequest) error {
	if req.Name == "" || len(req.PromotionPlans) == 0 {
		return fmt.Errorf("name and promotionplans are required")
	}
	if req.Version == "" && len(req.Versions) == 0 {
		return fmt.Errorf("version or versions is required")
	}
	if errs := validation.IsDNS1123Subdomain(req.Name); len(errs) > 0 {
		return fmt.Errorf("name must be a DNS-1123 subdomain: %s", strings.Join(errs, "; "))
	}
	if len(req.PromotionPlans) > 64 {
		return fmt.Errorf("promotionplans must contain at most 64 entries")
	}
	for unit, version := range req.Versions {
		if unit == "" || version == "" {
			return fmt.Errorf("versions must use non-empty unit and version values")
		}
	}
	for i, p := range req.PromotionPlans {
		if p.Name == "" || p.PromotionPlan == "" {
			return fmt.Errorf("promotionplans[%d].name and promotionplans[%d].promotionplan are required", i, i)
		}
		if errs := validation.IsDNS1123Subdomain(p.Name); len(errs) > 0 {
			return fmt.Errorf("promotionplans[%d].name must be a DNS-1123 subdomain: %s", i, strings.Join(errs, "; "))
		}
		if errs := validation.IsDNS1123Subdomain(p.PromotionPlan); len(errs) > 0 {
			return fmt.Errorf("promotionplans[%d].promotionplan must be a DNS-1123 subdomain: %s", i, strings.Join(errs, "; "))
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

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func NewHandler(c client.Client, bearerToken []byte) http.Handler {
	return (&Server{Client: c, BearerToken: bearerToken}).Handler()
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
