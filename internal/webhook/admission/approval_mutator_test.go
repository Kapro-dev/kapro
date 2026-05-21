package admission

import (
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestValidateApprovalBypassComment_RequiresComment(t *testing.T) {
	approval := &kaprov1alpha2.Approval{
		Spec: kaprov1alpha2.ApprovalSpec{
			Bypass:  true,
			Comment: "",
		},
	}
	resp := validateApprovalBypassComment(approval)
	if resp.Allowed {
		t.Fatal("expected bypass approval without comment to be denied")
	}
}
