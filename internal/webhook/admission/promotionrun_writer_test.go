package admission

import (
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
)

func TestIsAllowedPromotionRunWriter(t *testing.T) {
	t.Run("default controller SA is allowed", func(t *testing.T) {
		u := authenticationv1.UserInfo{Username: "system:serviceaccount:kapro-system:kapro-operator"}
		if !isAllowedPromotionRunWriter(u) {
			t.Fatal("expected default controller SA to be allowed")
		}
	})

	t.Run("normal user is denied", func(t *testing.T) {
		u := authenticationv1.UserInfo{Username: "alice@example.com"}
		if isAllowedPromotionRunWriter(u) {
			t.Fatal("expected normal user to be denied")
		}
	})

	t.Run("other service account is denied", func(t *testing.T) {
		u := authenticationv1.UserInfo{Username: "system:serviceaccount:default:default"}
		if isAllowedPromotionRunWriter(u) {
			t.Fatal("expected other SA to be denied")
		}
	})

	t.Run("system:masters group is allowed (break-glass)", func(t *testing.T) {
		u := authenticationv1.UserInfo{
			Username: "kubernetes-admin",
			Groups:   []string{"system:masters", "system:authenticated"},
		}
		if !isAllowedPromotionRunWriter(u) {
			t.Fatal("expected system:masters group to be allowed for break-glass")
		}
	})

	t.Run("env override widens allowed list", func(t *testing.T) {
		t.Setenv("KAPRO_PROMOTIONRUN_WRITERS",
			"system:serviceaccount:ns:custom-op, system:serviceaccount:kapro-system:kapro-operator")
		u := authenticationv1.UserInfo{Username: "system:serviceaccount:ns:custom-op"}
		if !isAllowedPromotionRunWriter(u) {
			t.Fatal("expected env-listed SA to be allowed")
		}
		// Default SA should also be present (we listed it explicitly).
		u2 := authenticationv1.UserInfo{Username: "system:serviceaccount:kapro-system:kapro-operator"}
		if !isAllowedPromotionRunWriter(u2) {
			t.Fatal("expected explicit default SA in env list to remain allowed")
		}
	})

	t.Run("env override that omits default denies it", func(t *testing.T) {
		t.Setenv("KAPRO_PROMOTIONRUN_WRITERS", "system:serviceaccount:ns:only")
		u := authenticationv1.UserInfo{Username: "system:serviceaccount:kapro-system:kapro-operator"}
		if isAllowedPromotionRunWriter(u) {
			t.Fatal("expected default SA to be denied when env override omits it")
		}
	})
}
