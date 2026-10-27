// Command terraform-provider-masterdaweb is the Master da Web Terraform
// provider.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/provider"
)

// version is set by GoReleaser at release time.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/masterdaweb/masterdaweb",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
