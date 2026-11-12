package core

import (
	"context"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Base is what every module works with: the component's runtime, the deployment's senders and the directory the
// positions expand against (nil without mdm/employee, NT06).
type Base struct {
	RT        *runtime.Runtime
	Senders   []string
	Directory Directory
}

// Directory answers a position's holders on a date (the SDK's employee projection, flow.Employees): complete is false
// while the projection cannot prove the answer (NT06).
type Directory interface {
	Holders(ctx context.Context, tx db.Tx, legalEntity, positionID, date string) (accounts []string, complete bool, err error)
}
