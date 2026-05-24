package admission

import (
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestValidateApprovalBypassComment_RequiresComment(t *testing.T) {
	approval := &kaprov1alpha1.Approval{
		Spec: kaprov1alpha1.ApprovalSpec{
			Bypass:  true,
			Comment: "",
		},
	}
	resp := validateApprovalBypassComment(approval)
	if resp.Allowed {
		t.Fatal("expected bypass approval without comment to be denied")
	}
}
