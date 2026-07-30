// Command lumen runs the open-source (community edition) Lumen event
// ingestion service.
//
// This binary is fully functional and unrestricted: no license key, no event
// caps, no phone-home. See OPEN_CORE.md for what the commercial build adds.
package main

import (
	"context"
	"log"

	"github.com/SyneHQ/lumen/app"
	"github.com/SyneHQ/lumen/ee"
)

func main() {
	if err := app.Run(context.Background(), app.Options{
		Hooks: ee.CommunityHooks(),
	}); err != nil {
		log.Fatalf("lumen: %v", err)
	}
}
