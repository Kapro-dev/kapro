package main

import (
	"flag"
	"log"

	ctrl "sigs.k8s.io/controller-runtime"

	"kapro.io/kapro/pkg/kapro/server"
)

// Manager-level RBAC requirements not tied to a specific controller.
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=kapro.io,resources=policies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=policies/status,verbs=get;update;patch

func main() {
	opts := server.OptionsFromEnv()
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	s, err := server.New(opts)
	if err != nil {
		log.Fatal(err)
	}
	if err := s.Run(ctrl.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}
