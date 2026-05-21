package main

import (
	"context"
	"log"

	"kapro.io/kapro/pkg/events"
	kapro "kapro.io/kapro/pkg/kapro"
)

func main() {
	sub := kapro.NewSubscriber(":8080")
	sub.On(events.EventPromotionSucceeded, func(event events.Event) {
		log.Printf("promotion %s succeeded at version %s", event.PromotionName, event.Version)
	})

	if err := sub.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
