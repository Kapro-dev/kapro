package main

import (
	"context"
	"fmt"

	"kapro.io/kapro/pkg/kapro/actuator"
)

func main() {
	hello := actuator.NewBoolFunc("hello-world", func(_ context.Context, req actuator.ApplyRequest) (bool, string, error) {
		return true, fmt.Sprintf("hello-world delivered version %s", req.Version), nil
	})
	if err := hello.Apply(context.Background(), actuator.ApplyRequest{Version: "v1"}); err != nil {
		panic(err)
	}
}
