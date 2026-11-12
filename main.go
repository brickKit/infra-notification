// Command infra-notification runs the notification component alone (serve, migrate); the shells compile in
// notification.Definition().
package main

import (
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/infra-notification/v3/notification"
)

func main() { launcher.Main(notification.Definition()) }
