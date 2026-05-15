package bundle

import (
	"fmt"
	"strings"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ValidationError holds multiple validation errors.
type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%d validation errors:\n  - %s", len(e.Errors), strings.Join(e.Errors, "\n  - "))
}

// Validate checks a KaproBundle for common mistakes before bundle generation.
// Returns nil if valid, or a *ValidationError with all issues found.
func Validate(app *kaprov1alpha1.KaproBundle) error {
	var errs []string

	if len(app.Spec.Components) == 0 {
		errs = append(errs, "no components defined")
	}

	// Check registries.
	if len(app.Spec.Registries) == 0 && (app.Spec.Defaults == nil || app.Spec.Defaults.Repo == "") {
		errs = append(errs, "no registries defined and no default repo set")
	}
	registryNames := map[string]bool{}
	for _, reg := range app.Spec.Registries {
		if reg.Name == "" {
			errs = append(errs, "registry has empty name")
		}
		if reg.URL == "" {
			errs = append(errs, fmt.Sprintf("registry %q has empty URL", reg.Name))
		}
		if reg.Type == "oci" && !strings.HasPrefix(reg.URL, "oci://") {
			errs = append(errs, fmt.Sprintf("registry %q type is 'oci' but URL doesn't start with oci://", reg.Name))
		}
		if registryNames[reg.Name] {
			errs = append(errs, fmt.Sprintf("duplicate registry name %q", reg.Name))
		}
		registryNames[reg.Name] = true
	}

	// Check components.
	componentNames := map[string]bool{}
	componentsByWave := map[int32][]string{}
	for _, comp := range app.Spec.Components {
		// Name.
		if comp.Name == "" {
			errs = append(errs, "component has empty name")
			continue
		}
		if componentNames[comp.Name] {
			errs = append(errs, fmt.Sprintf("duplicate component name %q", comp.Name))
		}
		componentNames[comp.Name] = true

		// Version.
		if comp.Version == "" {
			errs = append(errs, fmt.Sprintf("component %q has empty version", comp.Name))
		}

		// Wave must be non-negative.
		if comp.Wave < 0 {
			errs = append(errs, fmt.Sprintf("component %q has negative wave %d", comp.Name, comp.Wave))
		}

		// Repo must reference a valid registry.
		repo := comp.Repo
		if repo == "" && app.Spec.Defaults != nil {
			repo = app.Spec.Defaults.Repo
		}
		if repo != "" && len(app.Spec.Registries) > 0 && !registryNames[repo] {
			errs = append(errs, fmt.Sprintf("component %q references unknown registry %q", comp.Name, repo))
		}

		// DependsOn must reference existing components.
		for _, dep := range comp.DependsOn {
			if !componentNames[dep] {
				// Could be forward reference — check all components.
				found := false
				for _, c := range app.Spec.Components {
					if c.Name == dep {
						found = true
						break
					}
				}
				if !found {
					errs = append(errs, fmt.Sprintf("component %q depends on unknown component %q", comp.Name, dep))
				}
			}
		}

		componentsByWave[comp.Wave] = append(componentsByWave[comp.Wave], comp.Name)
	}

	// Check wave ordering — dependsOn should reference components in same or earlier wave.
	for _, comp := range app.Spec.Components {
		for _, dep := range comp.DependsOn {
			depWave := int32(-1)
			for _, c := range app.Spec.Components {
				if c.Name == dep {
					depWave = c.Wave
					break
				}
			}
			if depWave > comp.Wave {
				errs = append(errs, fmt.Sprintf(
					"component %q (wave %d) depends on %q (wave %d) — dependency must be in same or earlier wave",
					comp.Name, comp.Wave, dep, depWave))
			}
		}
	}

	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	return nil
}
