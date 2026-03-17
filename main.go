package main

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/bartei/terraform-provider-nixos/internal/provider"
)

func main() {
	err := providerserver.Serve(context.Background(), provider.New("0.1.0"), providerserver.ServeOpts{
		Address: "local/providers/nixos",
	})
	if err != nil {
		log.Fatal(err)
	}
}
