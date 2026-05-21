package main

import (
	"context"
	"fmt"
	"log"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kapro "kapro.io/kapro/pkg/kapro"
)

func main() {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		log.Fatal(err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	promotion := kapro.NewPromotion("checkout-v123").
		ForFleet("checkout").
		AtVersion("v1.2.3").
		Build()

	if err := client.Create(ctx, promotion); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("created Promotion %s for Fleet %s at %s\n", promotion.Name, promotion.Spec.FleetRef, promotion.Spec.Version)
}
