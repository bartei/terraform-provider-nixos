package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/bartei/terraform-provider-nixos/internal/provider"
)

// version is overridden at release time by goreleaser via
// -ldflags "-X main.version={{.Version}}".
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider in debug mode so a debugger can attach")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/bartei/nixos",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
