// Copyright 2026 The garm-provider-agent37 contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudbase/garm-provider-common/execution"
	"github.com/cloudbase/garm-provider-common/execution/common"
	"github.com/jtatum/garm-provider-agent37/provider"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	env, err := execution.GetEnvironment()
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid GARM execution environment")
		os.Exit(common.ResolveErrorToExitCode(err))
	}
	prov, err := provider.New(env.ProviderConfigFile, env.ControllerID, os.Getenv("GARM_POOL_ID"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(common.ResolveErrorToExitCode(err))
	}
	result, err := env.Run(ctx, prov)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(common.ResolveErrorToExitCode(err))
	}
	if result != "" {
		fmt.Fprint(os.Stdout, result)
	}
}
