package controller

import (
	"context"
	"time"
)

const fieldIndexerSetupTimeout = 30 * time.Second

func fieldIndexerSetupContext() (context.Context, context.CancelFunc) {
	// Controller-runtime registers field indexes during SetupWithManager, before
	// Manager.Start(ctx) provides a runtime context. Keep setup bounded anyway so
	// a broken cache/indexer cannot hang process startup forever.
	return context.WithTimeout(context.TODO(), fieldIndexerSetupTimeout)
}
