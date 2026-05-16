package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type cloudEvent struct {
	SpecVersion string          `json:"specversion"`
	Type        string          `json:"type"`
	Source      string          `json:"source"`
	ID          string          `json:"id"`
	Time        string          `json:"time"`
	Subject     string          `json:"subject"`
	Data        json.RawMessage `json:"data"`
}

type kaproEvent struct {
	Type          string `json:"type"`
	Phase         string `json:"phase"`
	Version       string `json:"version"`
	Target        string `json:"target"`
	PromotionRun  string `json:"promotionrun"`
	PromotionPlan string `json:"promotionplan"`
	Stage         string `json:"stage"`
	Message       string `json:"message"`
	IsFailure     bool   `json:"isFailure"`
}

func main() {
	auditRepo := os.Getenv("AUDIT_REPO")
	if auditRepo == "" {
		auditRepo = "./audit-repo"
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeAuditRecord(auditRepo, body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted\n"))
	})

	fmt.Printf("git-audit consumer listening on %s, AUDIT_REPO=%s\n", addr, auditRepo)
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}

func writeAuditRecord(auditRepo string, body []byte) error {
	var ce cloudEvent
	if err := json.Unmarshal(body, &ce); err != nil {
		return fmt.Errorf("decode CloudEvent: %w", err)
	}
	var event kaproEvent
	if err := json.Unmarshal(ce.Data, &event); err != nil {
		return fmt.Errorf("decode Kapro event data: %w", err)
	}
	if !isAuditEvent(ce.Type) {
		return nil
	}
	if event.PromotionRun == "" {
		return fmt.Errorf("event data.promotionrun is required")
	}

	path := filepath.Join(auditRepo, "promotionruns", safeName(event.PromotionRun)+".yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderAuditYAML(ce, event)), 0o644); err != nil {
		return err
	}
	if os.Getenv("GIT_COMMIT") == "true" {
		return commitAuditRecord(auditRepo, path, event.PromotionRun)
	}
	return nil
}

func isAuditEvent(eventType string) bool {
	switch eventType {
	case "kapro.promotionrun.completed", "kapro.promotionrun.failed", "kapro.promotionrun.rollback.started":
		return true
	default:
		return false
	}
}

func renderAuditYAML(ce cloudEvent, event kaproEvent) string {
	return fmt.Sprintf(`promotionrun: %q
eventType: %q
phase: %q
version: %q
promotionplan: %q
stage: %q
target: %q
source: %q
subject: %q
eventID: %q
time: %q
message: %q
isFailure: %t
`, event.PromotionRun, ce.Type, event.Phase, event.Version, event.PromotionPlan, event.Stage, event.Target, ce.Source, ce.Subject, ce.ID, ce.Time, event.Message, event.IsFailure)
}

func commitAuditRecord(auditRepo, path, promotionrun string) error {
	relPath, err := filepath.Rel(auditRepo, path)
	if err != nil {
		return err
	}
	if err := git(auditRepo, "add", relPath); err != nil {
		return err
	}
	changed, err := gitOutput(auditRepo, "status", "--porcelain", "--", relPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(changed) == "" {
		return nil
	}
	return git(auditRepo, "commit", "-m", "Record Kapro promotionrun "+promotionrun)
}

func git(dir string, args ...string) error {
	_, err := gitOutput(dir, args...)
	return err
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, string(output))
	}
	return string(output), nil
}

func safeName(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	return replacer.Replace(name)
}
