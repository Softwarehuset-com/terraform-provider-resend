// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/nicolajhartmann/terraform-provider-resend/internal/provider"
	"github.com/nicolajhartmann/terraform-provider-resend/version"
)

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/nicolajhartmann/resend",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.New(version.Version), opts)
	if err != nil {
		log.Fatal(err.Error())
	}
}
