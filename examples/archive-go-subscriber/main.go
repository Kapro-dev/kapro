package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"kapro.io/kapro/pkg/events"
	"kapro.io/kapro/pkg/kapro"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	subscriber := kapro.NewSubscriber(addr)
	subscriber.On(events.EventType("kapro.io/promotion.succeeded"), func(event events.Event) {
		fmt.Printf("archive promotion=%s type=%s phase=%s version=%s\n",
			event.PromotionName, event.Type, event.Phase, event.Version)
	})

	log.Printf("listening on %s", addr)
	if err := subscriber.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
