/*
Copyright 2020 The Kubernetes Authors.

SPDX-License-Identifier: Apache-2.0
*/

package pmemoperator

import (
	"context"
	"flag"
	"fmt"
	"runtime"

	"k8s.io/klog"

	"github.com/intel/pmem-csi/pkg/apis"
	"github.com/intel/pmem-csi/pkg/pmem-csi-operator/controller"
	"github.com/intel/pmem-csi/pkg/pmem-csi-operator/controller/deployment"
	"github.com/intel/pmem-csi/pkg/pmem-csi-operator/utils"

	//"github.com/intel/pmem-csi/pkg/pmem-operator/version"
	pmemcommon "github.com/intel/pmem-csi/pkg/pmem-common"

	"github.com/operator-framework/operator-sdk/pkg/leader"
	"github.com/operator-framework/operator-sdk/pkg/restmapper"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

func printVersion() {
	//klog.Info(fmt.Sprintf("Operator Version: %s", version.Version))
	klog.Info(fmt.Sprintf("Go Version: %s", runtime.Version()))
	klog.Info(fmt.Sprintf("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH))
	klog.Info(fmt.Sprintf("Version of operator-sdk: %v", sdkVersion.Version))
}

func init() {
	klog.InitFlags(nil)
	flag.Set("logtostderr", "true")
}

func Main() int {
	flag.Parse()

	printVersion()

	// Get a config to talk to the apiserver
	cfg, err := config.GetConfig()
	if err != nil {
		pmemcommon.ExitError("Failed to get configuration: ", err)
		return 1
	}

	ctx := context.TODO()
	// Become the leader before proceeding
	err = leader.Become(ctx, "pmem-csi-operator-lock")
	if err != nil {
		pmemcommon.ExitError("Failed to become leader: ", err)
		return 1
	}

	version, err := utils.GetKubernetesVersion()
	if err != nil {
		pmemcommon.ExitError("Failed retrieve kubernetes version: ", err)
		return 1
	}
	klog.Info("Kubernetes Version: ", version)

	klog.Info("Registering Deployment CRD.")
	if err := deployment.EnsureCRDInstalled(cfg); err != nil {
		pmemcommon.ExitError("Failed to install deployment CRD: ", err)
		return 1
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := manager.New(cfg, manager.Options{
		Namespace:      utils.GetNamespace(),
		MapperProvider: restmapper.NewDynamicRESTMapper,
	})
	if err != nil {
		pmemcommon.ExitError("Failed to create controller manager: ", err)
		return 1
	}

	klog.Info("Registering Components.")

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		pmemcommon.ExitError("Failed to add API schema: ", err)
		return 1
	}

	// Setup all Controllers
	if err := controller.AddToManager(mgr); err != nil {
		pmemcommon.ExitError("Failed to add controller to manager: ", err)
		return 1
	}

	klog.Info("Starting the Cmd.")

	// Start the Cmd
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		pmemcommon.ExitError("Manager exited non-zero: ", err)
		return 1
	}

	if err := deployment.DeleteCRD(cfg); err != nil {
		pmemcommon.ExitError("Failed to delete deployment CRD: ", err)
		return 1
	}

	return 0
}
