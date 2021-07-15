/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package storage

import (
	"context"
	"flag"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/intel/pmem-csi/test/e2e/deploy"
	"github.com/intel/pmem-csi/test/e2e/driver"
	"github.com/intel/pmem-csi/test/e2e/ephemeral"
	"github.com/intel/pmem-csi/test/e2e/storage/dax"
	"github.com/intel/pmem-csi/test/e2e/storage/scheduler"
	"github.com/intel/pmem-csi/test/e2e/versionskew"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/skipper"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	"k8s.io/kubernetes/test/e2e/storage/podlogs"
	"k8s.io/kubernetes/test/e2e/storage/testsuites"

	. "github.com/onsi/ginkgo"
)

var (
	numWorkers = flag.Int("pmem.binding.workers", 10, "number of worker creating volumes in parallel and thus also the maximum number of volumes at any time")
	numVolumes = flag.Int("pmem.binding.volumes", 100, "number of total volumes to create")
)

var _ = deploy.DescribeForAll("E2E", func(d *deploy.Deployment) {
	csiTestDriver := driver.New(d.Name(), d.DriverName, nil, nil)

	// List of testSuites to be added below.
	var csiTestSuites = []func() storageframework.TestSuite{
		// TODO: investigate how useful these tests are and enable them.
		// testsuites.InitMultiVolumeTestSuite,
		testsuites.InitProvisioningTestSuite,
		// testsuites.InitSnapshottableTestSuite,
		// testsuites.InitSubPathTestSuite,
		testsuites.InitVolumeIOTestSuite,
		testsuites.InitVolumeModeTestSuite,
		testsuites.InitVolumesTestSuite,
		dax.InitDaxTestSuite,
		scheduler.InitSchedulerTestSuite,
		versionskew.InitSkewTestSuite,
	}

	It("deployment works", func() {
		// If we get here, the deployment is up and running.
	})

	if ephemeral.Supported {
		csiTestSuites = append(csiTestSuites, testsuites.InitEphemeralTestSuite)
	}

	storageframework.DefineTestSuites(csiTestDriver, csiTestSuites)
	DefineLateBindingTests(d)
	DefineImmediateBindingTests(d)
	DefineKataTests(d)
})

func DefineLateBindingTests(d *deploy.Deployment) {
	f := framework.NewDefaultFramework("latebinding")

	Context("late binding", func() {
		var (
			cleanup func()
			sc      *storagev1.StorageClass
			claim   v1.PersistentVolumeClaim
		)

		BeforeEach(func() {
			csiTestDriver := driver.New(d.Name(), d.DriverName, nil, nil)
			config, cl := csiTestDriver.PrepareTest(f)
			cleanup = cl
			sc = csiTestDriver.(storageframework.DynamicPVTestDriver).GetDynamicProvisionStorageClass(config, "ext4")
			lateBindingMode := storagev1.VolumeBindingWaitForFirstConsumer
			sc.VolumeBindingMode = &lateBindingMode

			// Create or replace storage class.
			err := f.ClientSet.StorageV1().StorageClasses().Delete(context.Background(), sc.Name, metav1.DeleteOptions{})
			if !errors.IsNotFound(err) {
				framework.ExpectNoError(err, "delete old storage class %s", sc.Name)
			}
			_, err = f.ClientSet.StorageV1().StorageClasses().Create(context.Background(), sc, metav1.CreateOptions{})
			framework.ExpectNoError(err, "create storage class %s", sc.Name)
			claim = CreateClaim(f.Namespace.Name, sc.Name)
		})

		AfterEach(func() {
			err := f.ClientSet.StorageV1().StorageClasses().Delete(context.Background(), sc.Name, metav1.DeleteOptions{})
			framework.ExpectNoError(err, "delete old storage class %s", sc.Name)
			if cleanup != nil {
				cleanup()
			}
		})

		It("works", func() {
			TestDynamicProvisioning(f.ClientSet, f.Timeouts, &claim, *sc.VolumeBindingMode, "latebinding")
		})

		Context("unsets unsuitable selected node", func() {
			It("with defaults", func() {
				TestReschedule(f.ClientSet, f.Timeouts, &claim, d.DriverName, "latebinding")
			})

			It("with three replicas", func() {
				if !d.HasOperator {
					skipper.Skipf("need PMEM-CSI operator to reconfigure driver")
				}

				c, err := deploy.NewCluster(f.ClientSet, f.DynamicClient, f.ClientConfig())
				framework.ExpectNoError(err, "create cluster")

				By("increase replicas")
				deployment := deploy.GetDeploymentCR(f, d.DriverName)
				oldReplicas := deployment.Spec.ControllerReplicas
				newReplicas := 3
				deployment.Spec.ControllerReplicas = newReplicas
				deploy.UpdateDeploymentCR(f, deployment)
				deploy.WaitForPMEMDriver(c, d, int32(newReplicas))

				defer func() {
					By("reset replicas")
					deployment.Spec.ControllerReplicas = oldReplicas
					deploy.UpdateDeploymentCR(f, deployment)
					if oldReplicas == 0 {
						oldReplicas = 1
					}
					deploy.WaitForPMEMDriver(c, d, int32(oldReplicas))
				}()

				TestReschedule(f.ClientSet, f.Timeouts, &claim, d.DriverName, "latebinding")
			})
		})

		It("stress test [Slow]", func() {
			// We cannot test directly whether pod and
			// volume were created on the same node by
			// chance or because the code enforces it.
			// But if it works reliably under load, then
			// we can be reasonably sure that it works not
			// by chance.
			//
			// The load here consists of n workers which
			// create and test volumes in parallel until
			// we've tested m volumes.

			// Because this test creates a lot of pods, it is useful to
			// log their progress.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			to := podlogs.LogOutput{
				StatusWriter: GinkgoWriter,
				LogWriter:    GinkgoWriter,
			}
			podlogs.CopyAllLogs(ctx, f.ClientSet, f.Namespace.Name, to)
			podlogs.WatchPods(ctx, f.ClientSet, f.Namespace.Name, GinkgoWriter)

			wg := sync.WaitGroup{}
			volumes := int64(0)
			wg.Add(*numWorkers)
			for i := 0; i < *numWorkers; i++ {
				i := i
				go func() {
					defer wg.Done()
					defer GinkgoRecover()

					for {
						volume := atomic.AddInt64(&volumes, 1)
						if volume > int64(*numVolumes) {
							return
						}
						id := fmt.Sprintf("worker-%d-volume-%d", i, volume)
						TestDynamicProvisioning(f.ClientSet, f.Timeouts, &claim, *sc.VolumeBindingMode, id)
					}
				}()
			}
			wg.Wait()
		})
	})
}

func DefineKataTests(d *deploy.Deployment) {
	// Also run some limited tests with Kata Containers, using different
	// storage classes than usual.
	kataDriver := driver.New(d.Name()+"-pmem-csi-kata", "pmem-csi.intel.com",
		[]string{"xfs", "ext4"},
		map[string]string{
			"ext4": "deploy/common/pmem-storageclass-ext4-kata.yaml",
			"xfs":  "deploy/common/pmem-storageclass-xfs-kata.yaml",
		},
	)
	Context("Kata Containers", func() {
		storageframework.DefineTestSuites(kataDriver, []func() storageframework.TestSuite{
			dax.InitDaxTestSuite,
		})
	})
}
