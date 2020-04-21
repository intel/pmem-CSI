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
	"strings"
	"sync"
	"sync/atomic"

	"github.com/intel/pmem-csi/test/e2e/deploy"
	"github.com/intel/pmem-csi/test/e2e/ephemeral"
	"github.com/intel/pmem-csi/test/e2e/storage/dax"
	"github.com/intel/pmem-csi/test/e2e/storage/scheduler"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/podlogs"
	"k8s.io/kubernetes/test/e2e/framework/skipper"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	"k8s.io/kubernetes/test/e2e/storage/testpatterns"
	"k8s.io/kubernetes/test/e2e/storage/testsuites"
	"k8s.io/kubernetes/test/e2e/storage/utils"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var (
	numWorkers = flag.Int("pmem.latebinding.workers", 10, "number of worker creating volumes in parallel and thus also the maximum number of volumes at any time")
	numVolumes = flag.Int("pmem.latebinding.volumes", 100, "number of total volumes to create")
)

var _ = deploy.DescribeForAll("E2E", func(d *deploy.Deployment) {
	// List of testDrivers to be executed in below loop
	var csiTestDrivers = []func() testsuites.TestDriver{
		// pmem-csi
		func() testsuites.TestDriver {
			return &manifestDriver{
				driverInfo: testsuites.DriverInfo{
					Name:        d.Name + "-pmem-csi",
					MaxFileSize: testpatterns.FileSizeMedium,
					SupportedFsType: sets.NewString(
						"", "ext4", "xfs",
					),
					Capabilities: map[testsuites.Capability]bool{
						testsuites.CapPersistence: true,
						testsuites.CapFsGroup:     true,
						testsuites.CapExec:        true,
						testsuites.CapBlock:       true,
					},
					SupportedSizeRange: e2evolume.SizeRange{
						// There is test in VolumeIO suite creating 102 MB of content
						// so we use 110 MB as minimum size to fit that with some margin.
						// TODO: fix that upstream test to have a suitable minimum size
						//
						// Without VolumeIO suite, 16Mi would be enough as smallest xfs system size.
						// Ref: http://man7.org/linux/man-pages/man8/mkfs.xfs.8.html
						Min: "110Mi",
					},
				},
				scManifest: map[string]string{
					"":     "deploy/common/pmem-storageclass-ext4.yaml",
					"ext4": "deploy/common/pmem-storageclass-ext4.yaml",
					"xfs":  "deploy/common/pmem-storageclass-xfs.yaml",
				},
				csiDriverName: "pmem-csi.intel.com",
			}
		},
	}

	// List of testSuites to be executed in below loop
	var csiTestSuites = []func() testsuites.TestSuite{
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
	}

	if ephemeral.Supported {
		csiTestSuites = append(csiTestSuites, testsuites.InitEphemeralTestSuite)
	}

	for _, initDriver := range csiTestDrivers {
		curDriver := initDriver()
		Context(testsuites.GetDriverNameWithFeatureTags(curDriver), func() {
			testsuites.DefineTestSuite(curDriver, csiTestSuites)
		})
	}

	Context("late binding", func() {
		var (
			storageClassLateBindingName = "pmem-csi-sc-late-binding" // from deploy/common/pmem-storageclass-late-binding.yaml
			claim                       v1.PersistentVolumeClaim
		)
		f := framework.NewDefaultFramework("latebinding")
		BeforeEach(func() {
			// Check whether storage class exists before trying to use it.
			_, err := f.ClientSet.StorageV1().StorageClasses().Get(context.Background(), storageClassLateBindingName, metav1.GetOptions{})
			if errors.IsNotFound(err) {
				skipper.Skipf("storage class %s not found, late binding not supported", storageClassLateBindingName)
			}
			framework.ExpectNoError(err, "get storage class %s", storageClassLateBindingName)

			claim = v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "pvc-",
					Namespace:    f.Namespace.Name,
				},
				Spec: v1.PersistentVolumeClaimSpec{
					AccessModes: []v1.PersistentVolumeAccessMode{
						v1.ReadWriteOnce,
					},
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceName(v1.ResourceStorage): resource.MustParse("1Mi"),
						},
					},
					StorageClassName: &storageClassLateBindingName,
				},
			}
		})

		It("works", func() {
			TestDynamicLateBindingProvisioning(f.ClientSet, &claim, "latebinding")
		})

		// This test is pending because pod startup itself failed
		// occasionally for reasons that are out of our control
		// (https://github.com/clearlinux/distribution/issues/966).
		PIt("stress test", func() {
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
						TestDynamicLateBindingProvisioning(f.ClientSet, &claim, id)
					}
				}()
			}
			wg.Wait()
		})
	})
})

type manifestDriver struct {
	driverInfo    testsuites.DriverInfo
	csiDriverName string
	patchOptions  utils.PatchCSIOptions
	manifests     []string
	scManifest    map[string]string
	cleanup       func()
}

var _ testsuites.TestDriver = &manifestDriver{}
var _ testsuites.DynamicPVTestDriver = &manifestDriver{}

func (m *manifestDriver) GetDriverInfo() *testsuites.DriverInfo {
	return &m.driverInfo
}

func (m *manifestDriver) SkipUnsupportedTest(testpatterns.TestPattern) {
}

func (m *manifestDriver) GetDynamicProvisionStorageClass(config *testsuites.PerTestConfig, fsType string) *storagev1.StorageClass {
	f := config.Framework

	scManifest, ok := m.scManifest[fsType]
	Expect(ok).To(BeTrue(), "Unsupported filesystem type %s", fsType)

	items, err := utils.LoadFromManifests(scManifest)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(items)).To(Equal(1), "exactly one item from %s", scManifest)

	err = utils.PatchItems(f, items...)
	Expect(err).NotTo(HaveOccurred())
	err = utils.PatchCSIDeployment(f, m.finalPatchOptions(f), items[0])

	sc, ok := items[0].(*storagev1.StorageClass)
	Expect(ok).To(BeTrue(), "storage class from %s", scManifest)
	return sc
}

func (m *manifestDriver) PrepareTest(f *framework.Framework) (*testsuites.PerTestConfig, func()) {
	By(fmt.Sprintf("deploying %s driver", m.driverInfo.Name))
	config := &testsuites.PerTestConfig{
		Driver:    m,
		Prefix:    "pmem",
		Framework: f,
	}
	cleanup, err := utils.CreateFromManifests(f, func(item interface{}) error {
		return utils.PatchCSIDeployment(f, m.finalPatchOptions(f), item)
	},
		m.manifests...,
	)
	framework.ExpectNoError(err, "deploying driver %s", m.driverInfo.Name)
	return config, func() {
		By(fmt.Sprintf("uninstalling %s driver", m.driverInfo.Name))
		cleanup()
	}
}

func (m *manifestDriver) finalPatchOptions(f *framework.Framework) utils.PatchCSIOptions {
	o := m.patchOptions
	// Unique name not available yet when configuring the driver.
	if strings.HasSuffix(o.NewDriverName, "-") {
		o.NewDriverName += f.UniqueName
	}
	return o
}

func (m *manifestDriver) GetVolume(config *testsuites.PerTestConfig, volumeNumber int) (map[string]string, bool, bool) {
	attributes := map[string]string{"size": m.driverInfo.SupportedSizeRange.Min}
	shared := false
	readOnly := false

	return attributes, shared, readOnly
}

func (m *manifestDriver) GetCSIDriverName(config *testsuites.PerTestConfig) string {
	// Return real driver name.
	// We can't use m.driverInfo.Name as its not the real driver name
	return m.csiDriverName
}
