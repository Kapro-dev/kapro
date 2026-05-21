package controllermanager

import "testing"

func TestParseControllerNamesWildcardUsesCanonicalNames(t *testing.T) {
	selected := ParseControllerNames("*")

	for _, name := range []string{"fleet", "plan", "promotion", "promotionrun", "target", "cluster", "gateexpression", "approval", "backend", "plugin", "trigger", "cluster-bootstrap", "clustertemplate"} {
		if !selected[name] {
			t.Fatalf("wildcard selection missing canonical controller %q: %#v", name, selected)
		}
	}
	for _, alias := range []string{"kapro", "fleetcluster-heartbeat"} {
		if selected[alias] {
			t.Fatalf("wildcard selection included alias %q: %#v", alias, selected)
		}
	}
}

func TestParseControllerNamesAllowsExplicitAliases(t *testing.T) {
	for alias, canonical := range map[string]string{
		"kapro":                  "fleet",
		"promotion-target":       "target",
		"fleetcluster-heartbeat": "cluster",
		"gate-expression":        "gateexpression",
		"backend-profile":        "backend",
		"plugin-registration":    "plugin",
		"promotion-trigger":      "trigger",
		"fleetcluster-bootstrap": "cluster-bootstrap",
		"fleetcluster-template":  "clustertemplate",
	} {
		t.Run(alias, func(t *testing.T) {
			selected := ParseControllerNames(alias)
			if !selected[canonical] {
				t.Fatalf("explicit alias selection missing canonical controller %q: %#v", canonical, selected)
			}
			if selected[alias] {
				t.Fatalf("explicit alias selection retained alias %q: %#v", alias, selected)
			}
		})
	}
}

func TestParseControllerNamesExcludesAlias(t *testing.T) {
	selected := ParseControllerNames("*,-promotion-trigger")

	if selected["trigger"] {
		t.Fatalf("alias exclusion did not remove canonical trigger controller: %#v", selected)
	}
	if selected["promotion-trigger"] {
		t.Fatalf("alias exclusion retained alias key: %#v", selected)
	}
}

func TestDefaultControllersFlagUsesCoreControllers(t *testing.T) {
	selected := ParseControllerNames(DefaultControllersFlag())

	for _, name := range []string{"fleet", "plan", "promotion", "promotionrun", "target", "cluster"} {
		if !selected[name] {
			t.Fatalf("default controller selection missing %q: %#v", name, selected)
		}
	}
	for _, name := range []string{"approval", "backend", "gateexpression", "plugin", "trigger", "cluster-bootstrap", "clustertemplate"} {
		if selected[name] {
			t.Fatalf("default controller selection included preview controller %q: %#v", name, selected)
		}
	}
}

func TestParseControllerNamesAllowsGateExpressionOptIn(t *testing.T) {
	selected := ParseControllerNames("gateexpression")

	if !selected["gateexpression"] {
		t.Fatalf("gateexpression opt-in did not select controller: %#v", selected)
	}
	if selected["promotionrun"] || selected["target"] {
		t.Fatalf("gateexpression opt-in unexpectedly selected runtime controllers: %#v", selected)
	}
}

func TestParseControllerNamesAddsImplicitTargetDependency(t *testing.T) {
	selected := ParseControllerNames("promotionrun")

	for _, name := range []string{"promotionrun", "target"} {
		if !selected[name] {
			t.Fatalf("promotionrun selection missing %q: %#v", name, selected)
		}
	}
}

func TestControllerSelectionHelpers(t *testing.T) {
	selected := ParseControllerNames("promotionrun,does-not-exist")

	if got := SelectedControllerNames(selected); !contains(got, "promotionrun") || !contains(got, "target") || contains(got, "does-not-exist") {
		t.Fatalf("SelectedControllerNames() = %v", got)
	}
	if got := UnknownControllerNames(selected); len(got) != 1 || got[0] != "does-not-exist" {
		t.Fatalf("UnknownControllerNames() = %v, want [does-not-exist]", got)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
