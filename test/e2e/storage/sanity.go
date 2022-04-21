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
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	api "github.com/intel/pmem-csi/pkg/apis/pmemcsi/v1beta1"
	"github.com/kubernetes-csi/csi-test/v4/pkg/sanity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/clock"
	"k8s.io/client-go/kubernetes"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientexec "k8s.io/client-go/util/exec"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/ktesting"
	"k8s.io/kubernetes/test/e2e/framework"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	"k8s.io/kubernetes/test/e2e/framework/skipper"

	pmemexec "github.com/intel/pmem-csi/pkg/exec"
	pmemlog "github.com/intel/pmem-csi/pkg/logger"
	"github.com/intel/pmem-csi/pkg/pmem-csi-driver/parameters"
	"github.com/intel/pmem-csi/test/e2e/deploy"
	"github.com/intel/pmem-csi/test/e2e/pod"
	pmeme2epod "github.com/intel/pmem-csi/test/e2e/pod"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var (
	numSanityWorkers = flag.Int("pmem.sanity.workers", 10, "number of worker creating volumes in parallel and thus also the maximum number of volumes at any time")
	// 0 = default is overridden below.
	numSanityVolumes = flag.Int("pmem.sanity.volumes", 0, "number of total volumes to create")
	sanityVolumeSize = flag.String("pmem.sanity.volume-size", "15Mi", "size of each volume")
)

// Run the csi-test sanity tests against a PMEM-CSI driver.
var _ = deploy.DescribeForSome("sanity", func(d *deploy.Deployment) bool {
	// This test expects that PMEM-CSI was deployed with
	// socat port forwarding enabled (see deploy/kustomize/testing/README.md).
	// This is not the case when deployed in production mode.
	return d.Testing
}, func(d *deploy.Deployment) {
	// This must be set before the grpcDialer gets used for the first time.
	var cfg *rest.Config
	var cs kubernetes.Interface
	grpcDialer := func(ctx context.Context, address string) (net.Conn, error) {
		addr, err := pod.ParseAddr(address)
		if err != nil {
			return nil, err
		}
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, err
		}
		dialer := pod.NewDialer(cs, cfg)
		logger := klog.FromContext(ctx)
		ctx = klog.NewContext(ctx, logger.WithName("gRPC socat"))
		return dialer.DialContainerPort(ctx, *addr)
	}
	dialOptions := []grpc.DialOption{
		// For our restart tests.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			PermitWithoutStream: true,
			// This is the minimum. Specifying it explicitly
			// avoids some log output from gRPC.
			Time: 10 * time.Second,
		}),
		// For plain HTTP.
		grpc.WithInsecure(),
		// Connect to socat pods through port-forwarding.
		grpc.WithContextDialer(grpcDialer),
	}

	config := sanity.NewTestConfig()
	// The size has to be large enough that even after rounding up to
	// the next alignment boundary, the final volume size is still about
	// the same. The "should fail when requesting to create a volume with already existing name and different capacity"
	// test assumes that doubling the size will be too large to reuse the
	// already created volume.
	//
	// In practice, the largest alignment that we have seen is 96MiB.
	config.TestVolumeSize = 96 * 1024 * 1024
	// The actual directories will be created as unique
	// temp directories inside these directories.
	// We intentionally do not use the real /var/lib/kubelet/pods as
	// root for the target path, because kubelet is monitoring it
	// and deletes all extra entries that it does not know about.
	config.TargetPath = "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/pmem-sanity-target.XXXXXX"
	config.StagingPath = "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/pmem-sanity-staging.XXXXXX"
	config.DialOptions = dialOptions
	config.ControllerDialOptions = dialOptions

	f := framework.NewDefaultFramework("pmem")
	f.SkipNamespaceCreation = true // We don't need a per-test namespace and skipping it makes the tests run faster.
	var execOnTestNode func(args ...string) string
	var cleanup func()
	var cluster *deploy.Cluster

	// Always test on the second node. We assume it has PMEM.
	const testNode = 1

	BeforeEach(func() {
		// Store them for grpcDialer above. We cannot let it reference f itself because
		// f.ClientSet gets unset at some point.
		cs = f.ClientSet
		cfg = f.ClientConfig()

		var err error
		cluster, err = deploy.NewCluster(cs, f.DynamicClient, f.ClientConfig())
		framework.ExpectNoError(err, "query cluster")

		config.Address, config.ControllerAddress, err = deploy.LookupCSIAddresses(context.Background(), cluster, d.Namespace)
		framework.ExpectNoError(err, "find CSI addresses")

		framework.Logf("sanity: using controller %s and node %s", config.ControllerAddress, config.Address)

		// f.ExecCommandInContainerWithFullOutput assumes that we want a pod in the test's namespace,
		// so we have to set one.
		f.Namespace = &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: d.Namespace,
			},
		}

		// Avoid looking for the socat pod unless we actually need it.
		// Some tests might just get skipped. This has to be thread-safe
		// for the sanity stress test.
		var socat *v1.Pod
		var mutex sync.Mutex
		getSocatPod := func() *v1.Pod {
			mutex.Lock()
			defer mutex.Unlock()
			if socat != nil {
				return socat
			}
			socat = cluster.WaitForAppInstance(labels.Set{
				"app.kubernetes.io/component": "node-testing",
				"app.kubernetes.io/part-of":   "pmem-csi",
			},
				cluster.NodeIP(testNode), d.Namespace)
			return socat
		}

		execOnTestNode = func(args ...string) string {
			// Wait for socat pod on that node. We need it for
			// creating directories.  We could use the PMEM-CSI
			// node container, but that then forces us to have
			// mkdir and rmdir in that container, which we might
			// not want long-term.
			socat := getSocatPod()

			for {
				stdout, stderr, err := f.ExecCommandInContainerWithFullOutput(socat.Name, "socat", args...)
				if err != nil {
					exitErr, ok := err.(clientexec.ExitError)
					if ok && exitErr.ExitStatus() == 126 {
						// This doesn't necessarily mean that the actual binary cannot
						// be executed. It also can be an error in the code which
						// prepares for running in the container.
						framework.Logf("126 = 'cannot execute' error, trying again")
						continue
					}
				}
				framework.ExpectNoError(err, "%s in socat container, stderr:\n%s", args, stderr)
				Expect(stderr).To(BeEmpty(), "unexpected stderr from %s in socat container", args)
				By("Exec Output: " + stdout)
				return stdout
			}
		}
		mkdir := func(path string) (string, error) {
			path = execOnTestNode("mktemp", "-d", path)
			// Ensure that the path that we created
			// survives a sudden power loss (as during the
			// restart tests below), otherwise rmdir will
			// fail when it's gone.
			execOnTestNode("sync", "-f", path)
			return path, nil
		}
		rmdir := func(path string) error {
			execOnTestNode("rmdir", path)
			return nil
		}
		checkpath := func(path string) (sanity.PathKind, error) {
			out := execOnTestNode("/bin/sh", "-c", fmt.Sprintf(`
if [ -f '%s' ]; then
    echo file;
elif [ -d '%s' ]; then
    echo directory;
elif [ -e '%s' ]; then
    echo other;
else
    echo not_found;
fi
`, path, path, path))
			kind, err := sanity.IsPathKind(out)
			if err != nil {
				return "", fmt.Errorf("unexpected output from node shell script: %v", err)
			}
			return kind, nil
		}

		config.CreateTargetDir = mkdir
		config.CreateStagingDir = mkdir
		config.RemoveTargetPath = rmdir
		config.RemoveStagingPath = rmdir
		config.CheckPath = checkpath
	})

	AfterEach(func() {
		if cleanup != nil {
			cleanup()
		}
	})

	// This adds several tests that just get skipped.
	// TODO: static definition of driver capabilities (https://github.com/kubernetes-csi/csi-test/issues/143)
	sc := sanity.GinkgoTest(&config)

	// The test context caches a connection to the CSI driver.
	// When we re-deploy, gRPC will try to reconnect for that connection,
	// leading to log messages about connection errors because the node port
	// is allocated dynamically and changes when redeploying. Therefore
	// we register a hook which clears the connection when PMEM-CSI
	// gets re-deployed.
	scFinalize := func() {
		sc.Finalize()
		// Not sure why this isn't in Finalize - a bug?
		sc.Conn = nil
		sc.ControllerConn = nil
	}
	deploy.AddUninstallHook(func(deploymentName string) {
		framework.Logf("sanity: deployment %s is gone, closing test connections to controller %s and node %s.",
			deploymentName,
			config.ControllerAddress,
			config.Address)
		scFinalize()
	})

	var _ = Describe("PMEM-CSI", func() {
		var (
			resources *sanity.Resources
			nc        csi.NodeClient
			cc, ncc   csi.ControllerClient
			nodeID    string
			v         volume
			cancel    func()
			rebooted  bool
		)

		BeforeEach(func() {
			sc.Setup()
			nc = csi.NewNodeClient(sc.Conn)
			cc = csi.NewControllerClient(sc.ControllerConn)
			ncc = csi.NewControllerClient(sc.Conn) // This works because PMEM-CSI exposes the node, controller, and ID server via its csi.sock.
			resources = &sanity.Resources{
				Context:                    sc,
				NodeClient:                 nc,
				ControllerClient:           cc,
				ControllerPublishSupported: true,
				NodeStageSupported:         true,
			}
			rebooted = false
			nid, err := nc.NodeGetInfo(
				context.Background(),
				&csi.NodeGetInfoRequest{})
			framework.ExpectNoError(err, "get node ID")
			nodeID = nid.GetNodeId()
			// Default timeout for tests.
			ctx, c := context.WithTimeout(context.Background(), 5*time.Minute)
			cancel = c
			v = volume{
				namePrefix: "unset",
				ctx:        ctx,
				sc:         sc,
				cc:         cc,
				nc:         nc,
				resources:  resources,
			}
		})

		AfterEach(func() {
			resources.Cleanup()
			cancel()
			sc.Teardown()

			if rebooted {
				// Remove all cached connections, too.
				scFinalize()

				// Rebooting a node increases the restart counter of
				// the containers. This is normal in that case, but
				// for the next test triggers the check that
				// containers shouldn't restart. To get around that,
				// we delete all PMEM-CSI pods after a reboot test.
				By("stopping all PMEM-CSI pods after rebooting some node(s)")
				err := d.DeleteAllPods(cluster)
				framework.ExpectNoError(err)
			}
		})

		deleteTestNodeDriver := func() error {
			nodeDriverPod, err := cluster.GetAppInstance(context.Background(), labels.Set{
				"app.kubernetes.io/name": "pmem-csi-node",
			}, cluster.NodeIP(testNode), d.Namespace)
			if err != nil {
				return fmt.Errorf("node driver pod not found on node %d: %v", testNode, err)
			}
			if err := e2epod.DeletePodWithWaitByName(f.ClientSet, nodeDriverPod.Name, nodeDriverPod.Namespace); err != nil {
				return fmt.Errorf("delete driver pod on node %d: %v", testNode, err)
			}
			return nil
		}

		It("stores state across reboots for single volume", func() {
			canRestartNode(nodeID)

			execOnTestNode("sync")
			v.namePrefix = "state-volume"

			initialVolumes, err := ncc.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
			framework.ExpectNoError(err, "list volumes")

			volName, vol := v.create(11*1024*1024, nodeID)
			createdVolumes, err := ncc.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
			framework.ExpectNoError(err, "Failed to list volumes after reboot")
			Expect(createdVolumes.Entries).To(HaveLen(len(initialVolumes.Entries)+1), "one more volume on : %s", nodeID)

			// Restart.
			rebooted = true
			restartNode(f.ClientSet, nodeID, sc)

			// Once we get an answer, it is expected to be the same as before.
			By("checking volumes")
			restartedVolumes, err := ncc.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
			framework.ExpectNoError(err, "Failed to list volumes after reboot")
			Expect(restartedVolumes.Entries).To(ConsistOf(createdVolumes.Entries), "same volumes as before node reboot")

			v.remove(vol, volName)
		})

		It("can mount again after reboot", func() {
			canRestartNode(nodeID)
			execOnTestNode("sync")
			v.namePrefix = "mount-volume"

			name, vol := v.create(22*1024*1024, nodeID)
			// Publish for the second time.
			nodeID := v.publish(name, vol)

			// Restart.
			rebooted = true
			restartNode(f.ClientSet, nodeID, sc)

			_, err := ncc.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
			framework.ExpectNoError(err, "Failed to list volumes after reboot")

			// No failure expected although rebooting already unmounted the volume.
			// We still need to remove the target path, if it has survived
			// the hard power off.
			v.unpublish(vol, nodeID)

			// Publish for the second time.
			v.publish(name, vol)

			v.unpublish(vol, nodeID)
			v.remove(vol, name)
		})

		It("can publish volume after a node driver restart", func() {
			var err error
			v.namePrefix = "mount-volume"

			name, vol := v.create(22*1024*1024, nodeID)
			defer v.remove(vol, name)

			nodeID := v.publish(name, vol)
			defer v.unpublish(vol, nodeID)

			capacityBefore, err := cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
			framework.ExpectNoError(err, "get capacity before restart")

			// delete driver on node
			err = deleteTestNodeDriver()
			framework.ExpectNoError(err)

			// Eventually a different pod will be created and listing volumes will
			// work again through the same socat pod as before.
			Eventually(func() error {
				_, err = ncc.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
				return err
			}, "3m", "5s").ShouldNot(HaveOccurred(), "Failed to list volumes after restart of node driver")

			capacityAfter, err := cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
			framework.ExpectNoError(err, "get capacity after restart")
			Expect(capacityAfter).To(Equal(capacityBefore), "capacity changed because node driver was restarted")

			// Try republish
			v.publish(name, vol)
		})

		It("LVM volume group expands during restart", func() {
			// We cannot reconfigure the driver. But we can destroy the volume group and namespace,
			// create a smaller namespace, then restart the driver. The result should be a volume
			// group containing two namespaces.

			if d.Mode != api.DeviceModeLVM {
				skipper.Skipf("test only works in LVM mode")
			}

			capacityBefore, err := cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
			framework.ExpectNoError(err, "get capacity before restart")

			defer func() {
				// Always clean up.
				By("destroying volume groups and namespaces again")
				if err := deploy.ResetPMEM(context.Background(), fmt.Sprintf("%d", testNode)); err != nil {
					framework.Logf("resetting PMEM during cleanup failed: %v", err)
				}

				// We need to delete the pod to ensure that it detects those changes.
				if err := deleteTestNodeDriver(); err != nil {
					framework.Logf("deleting test node driver during cleanup failed: %v", err)
				}
			}()

			sshcmd := fmt.Sprintf("%s/_work/%s/ssh.%d", os.Getenv("REPO_ROOT"), os.Getenv("CLUSTER"), testNode)
			mustRun := func(cmd string) {
				_, err := pmemexec.RunCommand(context.Background(), sshcmd, cmd)
				framework.ExpectNoError(err)
			}
			dump := func() {
				mustRun("sudo vgs")
				mustRun("sudo pvs")
				mustRun("sudo ndctl list -NRu")
			}

			// This runs twice, because it was observed that it worked once on a clean
			// node and then failed when run again. The reason where some lingering LVM
			// labels in the namespace device, presumably from a previous run. Now PMEM-CSI
			// wipes created namespaces, which avoids this issue.
			for i := 0; i < 2; i++ {
				By(fmt.Sprintf("Test iteration #%d", i))

				err = deploy.ResetPMEM(context.Background(), fmt.Sprintf("%d", testNode))
				framework.ExpectNoError(err, "reset during iteration #%d", i)

				// Create a small namespace and the corresponding volume group, as if the driver
				// had been started before with a small percentage.
				mustRun("sudo ndctl create-namespace -s 96M --name pmem-csi")
				mustRun("sudo wipefs -a -f /dev/pmem0")
				mustRun("sudo vgcreate -f ndbus0region0fsdax /dev/pmem0")
				dump()

				// Force it to restart.
				err = deleteTestNodeDriver()
				framework.ExpectNoError(err, "delete node driver during iteration #%d", i)

				// Once the driver restarts, it should extend that existing volume group
				// before responding to gRPC requests.
				var capacityAfter *csi.GetCapacityResponse
				Eventually(func() error {
					resp, err := cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
					if err != nil {
						return err
					}
					capacityAfter = resp
					return nil
				}, "3m", "5s").ShouldNot(HaveOccurred(), "get capacity after restart #%d", i)
				dump()

				// Capacity is going to be a bit lower due to some additional overhead for
				// managing two PVs instead of one.
				Expect(capacityAfter.AvailableCapacity).To(BeNumerically("<", capacityBefore.AvailableCapacity), "capacity not less than before during iteration #%d", i)
				Expect(capacityAfter.AvailableCapacity).To(BeNumerically(">=", capacityBefore.AvailableCapacity*95/100), "less capacity than expected during iteration #%d", i)
			}
		})

		It("capacity is restored after controller restart", func() {
			By("Fetching pmem-csi-controller pod name")
			pods, err := e2epod.WaitForPodsWithLabelRunningReady(f.ClientSet, d.Namespace,
				labels.Set{"app.kubernetes.io/name": "pmem-csi-controller"}.AsSelector(), 1 /* one replica */, time.Minute)
			framework.ExpectNoError(err, "PMEM-CSI controller running with one replica")
			controllerNode := pods.Items[0].Spec.NodeName
			canRestartNode(controllerNode)

			execOnTestNode("sync")
			capacity, err := cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
			framework.ExpectNoError(err, "get capacity before restart")

			rebooted = true
			restartNode(f.ClientSet, controllerNode, sc)

			_, err = e2epod.WaitForPodsWithLabelRunningReady(f.ClientSet, d.Namespace,
				labels.Set{"app.kubernetes.io/name": "pmem-csi-controller"}.AsSelector(), 1 /* one replica */, 5*time.Minute)
			framework.ExpectNoError(err, "PMEM-CSI controller running again with one replica")

			By("waiting for full capacity")
			Eventually(func() int64 {
				currentCapacity, err := cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
				if err != nil {
					// Probably not running again yet.
					return 0
				}
				return currentCapacity.AvailableCapacity
			}, "3m", "5s").Should(Equal(capacity.AvailableCapacity), "total capacity after controller restart")
		})

		It("should return right capacity", func() {
			resp, err := ncc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
			Expect(err).Should(BeNil(), "Failed to get node initial capacity")
			nodeCapacity := resp.AvailableCapacity
			i := 0
			volSize := int64(1) * 1024 * 1024 * 1024

			// Keep creating volumes till there is change in node capacity
			Eventually(func() bool {
				volName := fmt.Sprintf("get-capacity-check-%d", i)
				i++
				By(fmt.Sprintf("creating volume '%s' on node '%s'", volName, nodeID))
				req := &csi.CreateVolumeRequest{
					Name: volName,
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
							},
						},
					},
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: volSize,
					},
				}
				_, err := resources.CreateVolume(context.Background(), req)
				Expect(err).Should(BeNil(), "Failed to create volume on node")

				resp, err := ncc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
				Expect(err).Should(BeNil(), "Failed to get node capacity after volume creation")

				// find if change in capacity
				ret := nodeCapacity != resp.AvailableCapacity
				// capture the new capacity
				nodeCapacity = resp.AvailableCapacity

				return ret
			})

			By("Getting controller capacity")
			resp, err = cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{
				AccessibleTopology: &csi.Topology{
					Segments: map[string]string{
						"pmem-csi.intel.com/node": nodeID,
					},
				},
			})
			Expect(err).Should(BeNil(), "Failed to get capacity of controller")
			Expect(resp.AvailableCapacity).To(Equal(nodeCapacity), "capacity mismatch")
		})

		It("handle fragmentation", func() {
			// The key idea behind this test is to create
			// four volumes that consume all available
			// space.  Then all but the second volume get
			// deleted. That creates a situation where in
			// direct mode, the largest volume size is
			// smaller than the total available space.
			//
			// Because these volumes can be large, shredding gets disabled.
			v.sc.Config.TestVolumeParameters = map[string]string{
				parameters.EraseAfter: "false",
			}
			resp, err := ncc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
			Expect(err).Should(BeNil(), "Failed to get node initial capacity")
			nodeCapacity := resp.AvailableCapacity

			if nodeCapacity%4 != 0 {
				framework.Failf("total capacity %s not a multiple of 4", pmemlog.CapacityRef(nodeCapacity))
			}
			isFragmented := nodeCapacity > resp.MaximumVolumeSize.GetValue()
			volumeSize := nodeCapacity / 4

			// Round down to a 4Mi alignment for LVM mode.
			lvmAlign := int64(4 * 1024 * 1024)
			volumeSize = volumeSize / lvmAlign * lvmAlign

			By(fmt.Sprintf("creating four volumes of %s each", pmemlog.CapacityRef(volumeSize)))
			var volumes []*csi.Volume
			for i := 0; i < 4; i++ {
				v.namePrefix = fmt.Sprintf("frag-%d", i)
				_, vol := v.create(volumeSize, nodeID)
				volumes = append(volumes, vol)
				resp, err := ncc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
				Expect(err).Should(BeNil(), "Failed to get updated capacity")
				Expect(pmemlog.CapacityRef(resp.AvailableCapacity).String()).To(Equal(pmemlog.CapacityRef(nodeCapacity-int64(i+1)*volumeSize).String()), "remaining capacity after creating volume #%d", i)
				if i < 3 {
					Expect(resp.MaximumVolumeSize).NotTo(BeNil(), "have MaximumVolumeSize")
					Expect(resp.MaximumVolumeSize.Value).To(BeNumerically(">=", volumeSize), "MaximVolumeSize large enough for next volume")
				}
			}

			By("deleting all but the second volume")
			for i := 0; i < 4; i++ {
				if i == 1 {
					continue
				}
				vol := volumes[i]
				_, err := v.cc.DeleteVolume(v.ctx, &csi.DeleteVolumeRequest{
					VolumeId: vol.GetVolumeId(),
				})
				Expect(err).Should(BeNil(), fmt.Sprintf("Volume #%d cannot be deleted", i))
			}

			resp, err = ncc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
			Expect(err).Should(BeNil(), "Failed to get capacity after deleting three volumes")
			Expect(pmemlog.CapacityRef(resp.AvailableCapacity).String()).To(Equal(pmemlog.CapacityRef(nodeCapacity-volumeSize).String()), "available capacity while one volume exists")
			Expect(resp.MaximumVolumeSize).NotTo(BeNil(), "have MaximumVolumeSize")
			if d.Mode == api.DeviceModeLVM {
				if !isFragmented {
					// When there are, for example, two regions, then we have fragmentation also for LVM.
					// This assumption only applies when we have a single volume group.
					Expect(resp.MaximumVolumeSize.Value).To(BeNumerically(">=", 3*volumeSize), "MaximVolumeSize includes space of all three deleted volumes when using LVM")
				}
			} else {
				Expect(pmemlog.CapacityRef(resp.MaximumVolumeSize.Value).String()).To(Equal(pmemlog.CapacityRef(2*volumeSize).String()), "MaximVolumeSize includes space of the last two deleted volumes when using direct mode")
			}

			By("creating one larger volume")
			v.namePrefix = "frag-final"
			v.create(resp.MaximumVolumeSize.Value, nodeID)
		})

		It("excessive message sizes should be rejected", func() {
			req := &csi.GetCapacityRequest{
				AccessibleTopology: &csi.Topology{
					Segments: map[string]string{},
				},
			}
			for i := 0; i < 1000000; i++ {
				req.AccessibleTopology.Segments[fmt.Sprintf("pmem-csi.intel.com/node%d", i)] = nodeID
			}
			resp, err := cc.GetCapacity(context.Background(), req)
			Expect(err).ShouldNot(BeNil(), "unexpected success for too large request, got response: %+v", resp)
			status, ok := status.FromError(err)
			Expect(ok).Should(BeTrue(), "expected status in error, got: %v", err)
			Expect(status.Message()).Should(ContainSubstring("grpc: received message larger than max"))
		})

		It("delete volume should fail with appropriate error", func() {
			v.namePrefix = "delete-volume"

			name, vol := v.create(2*1024*1024, nodeID)
			// Publish for the second time.
			nodeID := v.publish(name, vol)

			_, err := v.cc.DeleteVolume(v.ctx, &csi.DeleteVolumeRequest{
				VolumeId: vol.GetVolumeId(),
			})
			Expect(err).ShouldNot(BeNil(), fmt.Sprintf("Volume(%s) in use cannot be deleted", name))
			s, ok := status.FromError(err)
			Expect(ok).Should(BeTrue(), "Expected a status error")
			Expect(s.Code()).Should(BeEquivalentTo(codes.FailedPrecondition), "Expected device busy error")

			v.unpublish(vol, nodeID)

			v.remove(vol, name)
		})

		It("CreateVolume handles zero size", func() {
			_, vol := v.create(0, nodeID)
			Expect(vol.CapacityBytes).Should(BeNumerically(">", int64(0)), "actual volume must have non-zero size")
		})

		It("CreateVolume should return ResourceExhausted", func() {
			v.namePrefix = "resource-exhausted"

			v.create(1024*1024*1024*1024*1024, nodeID, codes.ResourceExhausted)
		})

		It("NodeUnstageVolume for unknown volume", func() {
			_, err := v.nc.NodeUnstageVolume(v.ctx, &csi.NodeUnstageVolumeRequest{
				VolumeId:          "no-such-volume",
				StagingTargetPath: "/foo/bar",
			})
			Expect(err).ShouldNot(BeNil(), "NodeUnstageVolume should have failed")
			s, ok := status.FromError(err)
			if !ok {
				framework.Failf("Expected a status error, got %T: %v", err, err)
			}
			Expect(s.Code()).Should(BeEquivalentTo(codes.NotFound), "Expected volume not found")
		})

		It("stress test", func() {
			// The load here consists of n workers which
			// create and test volumes in parallel until
			// we've created m volumes.
			wg := sync.WaitGroup{}
			volumes := int64(0)
			volSize, err := resource.ParseQuantity(*sanityVolumeSize)
			framework.ExpectNoError(err, "parsing pmem.sanity.volume-size parameter value %s", *sanityVolumeSize)
			wg.Add(*numSanityWorkers)

			// Constant time plus variable component for shredding.
			// When using multiple workers, they either share IO bandwidth (parallel shredding)
			// or do it sequentially, therefore we have to multiply by the maximum number
			// of shredding operations.
			secondsPerGigabyte := 10 * time.Second // 2s/GB masured for direct mode in a VM on a fast machine, probably slower elsewhere
			timeout := 300*time.Second + time.Duration(int64(*numSanityWorkers)*volSize.Value()/1024/1024/1024)*secondsPerGigabyte

			// The default depends on the driver deployment and thus has to be calculated here.
			sanityVolumes := *numSanityVolumes
			if sanityVolumes == 0 {
				switch d.Mode {
				case api.DeviceModeDirect:
					// The minimum volume size in direct mode is 2GB, which makes
					// testing a lot slower than in LVM mode. Therefore we create less
					// volumes.
					sanityVolumes = 20
				default:
					sanityVolumes = 100
				}
			}

			// Also adapt the overall test timeout.
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(int64(timeout)*int64(sanityVolumes)))
			defer cancel()
			v.ctx = ctx

			By(fmt.Sprintf("creating %d volumes of size %s in %d workers, with a timeout per volume of %s", sanityVolumes, volSize.String(), *numSanityWorkers, timeout))
			for i := 0; i < *numSanityWorkers; i++ {
				i := i
				go func() {
					// Order is relevant (first-in-last-out): when returning,
					// we first let GinkgoRecover do its job, which
					// may include marking the test as failed, then tell the
					// parent goroutine that it can proceed.
					defer func() {
						By(fmt.Sprintf("worker-%d terminating", i))
						wg.Done()
					}()
					defer GinkgoRecover() // must be invoked directly by defer, otherwise it doesn't see the panic

					// Each worker must use its own pair of directories.
					targetPath := fmt.Sprintf("%s/worker-%d", sc.TargetPath, i)
					stagingPath := fmt.Sprintf("%s/worker-%d", sc.StagingPath, i)
					execOnTestNode("mkdir", targetPath)
					defer execOnTestNode("rmdir", targetPath)
					execOnTestNode("mkdir", stagingPath)
					defer execOnTestNode("rmdir", stagingPath)

					for {
						volume := atomic.AddInt64(&volumes, 1)
						if volume > int64(sanityVolumes) {
							return
						}

						lv := v
						lv.namePrefix = fmt.Sprintf("worker-%d-volume-%d", i, volume)
						lv.targetPath = targetPath
						lv.stagingPath = stagingPath
						func() {
							ctx, cancel := context.WithTimeout(v.ctx, timeout)
							start := time.Now()
							success := false
							defer func() {
								cancel()
								if !success {
									duration := time.Since(start)
									By(fmt.Sprintf("%s: failed after %s", lv.namePrefix, duration))

									// Stop testing.
									atomic.AddInt64(&volumes, int64(sanityVolumes))
								}
							}()
							lv.ctx = ctx
							volName, vol := lv.create(volSize.Value(), nodeID)
							lv.publish(volName, vol)
							lv.unpublish(vol, nodeID)
							lv.remove(vol, volName)

							// Success!
							duration := time.Since(start)
							success = true
							By(fmt.Sprintf("%s: done, in %s", lv.namePrefix, duration))
						}()
					}
				}()
			}
			wg.Wait()
		})

		Context("cluster", func() {
			type nodeClient struct {
				host    string
				conn    *grpc.ClientConn
				nc      csi.NodeClient
				cc      csi.ControllerClient
				volumes []*csi.ListVolumesResponse_Entry
			}
			var (
				nodes  map[string]nodeClient
				ctx    context.Context
				cancel func()
			)

			BeforeEach(func() {
				_, ctx := ktesting.NewTestContext(GinkgoT(0))
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()

				// Worker nodes with PMEM.
				nodes = make(map[string]nodeClient)

				// Find socat pods.
				pods, err := f.ClientSet.CoreV1().Pods("").List(ctx,
					metav1.ListOptions{
						LabelSelector: labels.FormatLabels(map[string]string{
							"app.kubernetes.io/component": "node-testing",
							"app.kubernetes.io/instance":  "pmem-csi.intel.com",
						}),
					})
				framework.ExpectNoError(err, "list socat pods")
				if len(pods.Items) == 0 {
					framework.Failf("expected some socat pods, found none")
				}

				for _, pod := range pods.Items {
					dialer := grpc.WithContextDialer(
						// Dial timeout has to be ignored because the pod dialer does not support that
						// because the underlying code doesn't support it.
						// The address is already known.
						func(ctx context.Context, _ string) (net.Conn, error) {
							return pmeme2epod.NewDialer(f.ClientSet, f.ClientConfig()).DialContainerPort(ctx, pmeme2epod.Addr{
								Namespace: pod.Namespace,
								PodName:   pod.Name,
								Port:      deploy.SocatPort,
							})
						})
					conn, err := grpc.Dial(pod.Spec.NodeName, dialer, grpc.WithInsecure())
					framework.ExpectNoError(err, "gRPC connection to socat instance on node %s", pod.Spec.NodeName)
					node := nodeClient{
						host: pod.Spec.NodeName,
						conn: conn,
						nc:   csi.NewNodeClient(conn),
						cc:   csi.NewControllerClient(conn),
					}
					info, err := node.nc.NodeGetInfo(ctx, &csi.NodeGetInfoRequest{})
					framework.ExpectNoError(err, "CSI node name for node %s", pod.Spec.NodeName)
					initialVolumes, err := node.cc.ListVolumes(ctx, &csi.ListVolumesRequest{})
					framework.ExpectNoError(err, "list volumes on node %s", pod.Spec.NodeName)
					node.volumes = initialVolumes.Entries

					nodes[info.NodeId] = node
				}
			})

			AfterEach(func() {
				for _, node := range nodes {
					node.conn.Close()
				}
				cancel()
			})

			It("supports persistent volumes", func() {
				sizeInBytes := int64(33 * 1024 * 1024)
				volName, vol := v.create(sizeInBytes, "")

				Expect(len(vol.AccessibleTopology)).To(Equal(1), "accessible topology mismatch")
				volNodeName := vol.AccessibleTopology[0].Segments["pmem-csi.intel.com/node"]
				Expect(volNodeName).NotTo(BeNil(), "wrong topology")

				// Node now should have one additional volume only one node,
				// and its size should match the requested one.
				for nodeName, node := range nodes {
					currentVolumes, err := node.cc.ListVolumes(ctx, &csi.ListVolumesRequest{})
					framework.ExpectNoError(err, "list volumes on node %s", nodeName)
					if nodeName == volNodeName {
						Expect(len(currentVolumes.Entries)).To(Equal(len(node.volumes)+1), "one additional volume on node %s", nodeName)
						for _, e := range currentVolumes.Entries {
							if e.Volume.VolumeId == vol.VolumeId {
								Expect(e.Volume.CapacityBytes).To(Equal(vol.CapacityBytes), "additional volume size on node %s", nodeName)
								break
							}
						}
					} else {
						// ensure that no new volume on other nodes
						Expect(len(currentVolumes.Entries)).To(Equal(len(node.volumes)), "volume count mismatch on node %s", nodeName)
					}
				}

				v.remove(vol, volName)
			})

			It("persistent volume retains data", func() {
				sizeInBytes := int64(33 * 1024 * 1024)
				volName, vol := v.create(sizeInBytes, nodeID)

				Expect(len(vol.AccessibleTopology)).To(Equal(1), "accessible topology mismatch")
				Expect(vol.AccessibleTopology[0].Segments["pmem-csi.intel.com/node"]).To(Equal(nodeID), "unexpected node")

				// Node now should have one additional volume only one node,
				// and its size should match the requested one.
				node := nodes[nodeID]
				currentVolumes, err := node.cc.ListVolumes(ctx, &csi.ListVolumesRequest{})
				framework.ExpectNoError(err, "list volumes on node %s", nodeID)
				Expect(len(currentVolumes.Entries)).To(Equal(len(node.volumes)+1), "one additional volume on node %s", nodeID)
				for _, e := range currentVolumes.Entries {
					if e.Volume.VolumeId == vol.VolumeId {
						Expect(e.Volume.CapacityBytes).To(Equal(vol.CapacityBytes), "additional volume size on node %s", nodeID)
						break
					}
				}

				v.publish(volName, vol)

				sshcmd := fmt.Sprintf("%s/_work/%s/ssh.%s", os.Getenv("REPO_ROOT"), os.Getenv("CLUSTER"), nodeID)
				// write some data to mounted volume
				cmd := "sudo sh -c 'echo -n hello > " + v.getTargetPath() + "/target/test-file'"
				ssh := exec.Command(sshcmd, cmd)
				out, err := ssh.CombinedOutput()
				framework.ExpectNoError(err, "write failure:\n%s", string(out))

				// unmount volume
				v.unpublish(vol, nodeID)

				// republish volume
				v.publish(volName, vol)

				// ensure the data retained
				cmd = "sudo cat " + v.getTargetPath() + "/target/test-file"
				ssh = exec.Command(sshcmd, cmd)
				out, err = ssh.CombinedOutput()
				framework.ExpectNoError(err, "read failure:\n%s", string(out))
				Expect(string(out)).To(Equal("hello"), "read failure")

				// end of test cleanup
				v.unpublish(vol, nodeID)
				v.remove(vol, volName)
			})

			Context("CSI ephemeral volumes", func() {
				doit := func(withFlag bool, repeatCalls int, fsType string) {
					targetPath := sc.TargetPath + "/ephemeral"
					params := map[string]string{
						"size": "100Mi",
					}
					if withFlag {
						params["csi.storage.k8s.io/ephemeral"] = "true"
					}
					req := csi.NodePublishVolumeRequest{
						VolumeId:      "fake-ephemeral-volume-id",
						VolumeContext: params,
						VolumeCapability: &csi.VolumeCapability{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: fsType,
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
							},
						},
						TargetPath: targetPath,
					}
					published := false
					var failedPublish, failedUnpublish error
					for i := 0; i < repeatCalls; i++ {
						_, err := nc.NodePublishVolume(ctx, &req)
						if err == nil {
							published = true
						} else if failedPublish == nil {
							failedPublish = fmt.Errorf("NodePublishVolume for ephemeral volume, attempt #%d: %v", i, err)
						}
					}
					if published {
						req := csi.NodeUnpublishVolumeRequest{
							VolumeId:   "fake-ephemeral-volume-id",
							TargetPath: targetPath,
						}
						for i := 0; i < repeatCalls; i++ {
							_, err := nc.NodeUnpublishVolume(ctx, &req)
							if err != nil && failedUnpublish == nil {
								failedUnpublish = fmt.Errorf("NodeUnpublishVolume for ephemeral volume, attempt #%d: %v", i, err)
							}
						}
					}
					framework.ExpectNoError(failedPublish)
					framework.ExpectNoError(failedUnpublish)
				}

				doall := func(withFlag bool) {
					for _, fs := range []string{"default", "ext4", "xfs"} {
						fsType := fs
						if fsType == "default" {
							fsType = ""
						}
						Context(fs+" FS", func() {
							It("work", func() {
								doit(withFlag, 1, fsType)
							})

							It("are idempotent", func() {
								doit(withFlag, 10, fsType)
							})
						})
					}
				}

				Context("with csi.storage.k8s.io/ephemeral", func() {
					doall(true)
				})

				Context("without csi.storage.k8s.io/ephemeral", func() {
					doall(false)
				})
			})

			It("reports errors properly", func() {
				for nodeName, node := range nodes {
					_, err := node.cc.DeleteVolume(ctx, &csi.DeleteVolumeRequest{})
					Expect(err).ToNot(BeNil(), "DeleteVolume with empty volume ID did not fail on node %s", nodeName)
					Expect(err.Error()).To(ContainSubstring(nodeName+": "), "errors should contain node name")
					status, ok := status.FromError(err)
					Expect(ok).To(BeTrue(), "error type %T should have contained a gRPC status: %v", err, err)
					Expect(status.Code().String()).To(Equal(codes.InvalidArgument.String()), "status code should be InvalidArgument")
				}
			})
		})
	})
})

type volume struct {
	namePrefix  string
	ctx         context.Context
	sc          *sanity.TestContext
	cc          csi.ControllerClient
	nc          csi.NodeClient
	resources   *sanity.Resources
	stagingPath string
	targetPath  string
}

func (v volume) getStagingPath() string {
	if v.stagingPath != "" {
		return v.stagingPath
	}
	return v.sc.StagingPath
}

func (v volume) getTargetPath() string {
	if v.targetPath != "" {
		return v.targetPath
	}
	return v.sc.TargetPath
}

func (v volume) create(sizeInBytes int64, nodeID string, expectedStatus ...codes.Code) (string, *csi.Volume) {
	var err error
	name := sanity.UniqueString(v.namePrefix)

	// Create Volume First
	create := fmt.Sprintf("%s: creating a single node writer volume", v.namePrefix)
	By(create)
	req := &csi.CreateVolumeRequest{
		Name: name,
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
				},
			},
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: sizeInBytes,
		},
		Parameters: v.sc.Config.TestVolumeParameters,
	}
	if nodeID != "" {
		req.AccessibilityRequirements = &csi.TopologyRequirement{
			Requisite: []*csi.Topology{
				{
					Segments: map[string]string{
						"pmem-csi.intel.com/node": nodeID,
					},
				},
			},
			Preferred: []*csi.Topology{
				{
					Segments: map[string]string{
						"pmem-csi.intel.com/node": nodeID,
					},
				},
			},
		}
	}
	var vol *csi.CreateVolumeResponse
	if len(expectedStatus) > 0 {
		// Expected to fail, no retries.
		vol, err = v.resources.CreateVolume(v.ctx, req)
	} else {
		// With retries.
		err = v.retry(func() error {
			vol, err = v.resources.CreateVolume(
				v.ctx, req,
			)
			return err
		}, "CreateVolume")
	}
	if len(expectedStatus) > 0 {
		framework.ExpectError(err, create)
		status, ok := status.FromError(err)
		Expect(ok).To(BeTrue(), "have gRPC status error")
		Expect(status.Code()).To(Equal(expectedStatus[0]), "expected gRPC status code")
		return name, nil
	}
	framework.ExpectNoError(err, create)
	Expect(vol).NotTo(BeNil())
	Expect(vol.GetVolume()).NotTo(BeNil())
	Expect(vol.GetVolume().GetVolumeId()).NotTo(BeEmpty())
	Expect(vol.GetVolume().GetCapacityBytes()).To(BeNumerically(">=", sizeInBytes), "volume capacity")

	return name, vol.GetVolume()
}

func (v volume) publish(name string, vol *csi.Volume) string {
	var err error

	By(fmt.Sprintf("%s: getting a node id", v.namePrefix))
	nid, err := v.nc.NodeGetInfo(
		v.ctx,
		&csi.NodeGetInfoRequest{})
	framework.ExpectNoError(err, "get node ID")
	Expect(nid).NotTo(BeNil())
	Expect(nid.GetNodeId()).NotTo(BeEmpty())

	var conpubvol *csi.ControllerPublishVolumeResponse
	stage := fmt.Sprintf("%s: node staging volume", v.namePrefix)
	By(stage)
	var nodestagevol interface{}
	err = v.retry(func() error {
		nodestagevol, err = v.nc.NodeStageVolume(
			v.ctx,
			&csi.NodeStageVolumeRequest{
				VolumeId: vol.GetVolumeId(),
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
				StagingTargetPath: v.getStagingPath(),
				VolumeContext:     vol.GetVolumeContext(),
				PublishContext:    conpubvol.GetPublishContext(),
			},
		)
		return err
	}, "NodeStageVolume")
	framework.ExpectNoError(err, stage)
	Expect(nodestagevol).NotTo(BeNil())

	// NodePublishVolume
	publish := fmt.Sprintf("%s: publishing the volume on a node", v.namePrefix)
	By(publish)
	var nodepubvol interface{}
	err = v.retry(func() error {
		nodepubvol, err = v.nc.NodePublishVolume(
			v.ctx,
			&csi.NodePublishVolumeRequest{
				VolumeId:          vol.GetVolumeId(),
				TargetPath:        v.getTargetPath() + "/target",
				StagingTargetPath: v.getStagingPath(),
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
				VolumeContext:  vol.GetVolumeContext(),
				PublishContext: conpubvol.GetPublishContext(),
			},
		)
		return err
	}, "NodePublishVolume")
	framework.ExpectNoError(err, publish)
	Expect(nodepubvol).NotTo(BeNil())

	return nid.GetNodeId()
}

func (v volume) unpublish(vol *csi.Volume, nodeID string) {
	var err error

	unpublish := fmt.Sprintf("%s: cleaning up calling nodeunpublish", v.namePrefix)
	By(unpublish)
	var nodeunpubvol interface{}
	err = v.retry(func() error {
		nodeunpubvol, err = v.nc.NodeUnpublishVolume(
			v.ctx,
			&csi.NodeUnpublishVolumeRequest{
				VolumeId:   vol.GetVolumeId(),
				TargetPath: v.getTargetPath() + "/target",
			})
		return err
	}, "NodeUnpublishVolume")
	framework.ExpectNoError(err, unpublish)
	Expect(nodeunpubvol).NotTo(BeNil())

	unstage := fmt.Sprintf("%s: cleaning up calling nodeunstage", v.namePrefix)
	By(unstage)
	var nodeunstagevol interface{}
	err = v.retry(func() error {
		nodeunstagevol, err = v.nc.NodeUnstageVolume(
			v.ctx,
			&csi.NodeUnstageVolumeRequest{
				VolumeId:          vol.GetVolumeId(),
				StagingTargetPath: v.getStagingPath(),
			},
		)
		return err
	}, "NodeUnstageVolume")
	framework.ExpectNoError(err, unstage)
	Expect(nodeunstagevol).NotTo(BeNil())
}

func (v volume) remove(vol *csi.Volume, volName string) {
	var err error

	delete := fmt.Sprintf("%s: deleting the volume %s", v.namePrefix, vol.GetVolumeId())
	By(delete)
	var deletevol interface{}
	err = v.retry(func() error {
		deletevol, err = v.resources.DeleteVolume(
			v.ctx,
			&csi.DeleteVolumeRequest{
				VolumeId: vol.GetVolumeId(),
			},
		)
		return err
	}, "DeleteVolume")
	framework.ExpectNoError(err, delete)
	Expect(deletevol).NotTo(BeNil())
}

// retry will execute the operation (rapidly initially, then with
// exponential backoff) until it succeeds or the context times
// out. Each failure gets logged.
func (v volume) retry(operation func() error, what string) error {
	if v.ctx.Err() != nil {
		return fmt.Errorf("%s: not calling %s, the deadline has been reached already", v.namePrefix, what)
	}

	// Something failed. Retry with exponential backoff.
	// TODO: use wait.NewExponentialBackoffManager once we use K8S v1.18.
	backoff := NewExponentialBackoffManager(
		time.Second,    // initial backoff
		10*time.Second, // maximum backoff
		30*time.Second, // reset duration
		2,              // backoff factor
		0,              // no jitter
		clock.RealClock{})
	for i := 0; ; i++ {
		err := operation()
		if err == nil {
			return nil
		}
		framework.Logf("%s: %s failed at attempt %#d: %v", v.namePrefix, what, i, err)
		select {
		case <-v.ctx.Done():
			framework.Logf("%s: %s failed %d times and deadline exceeded, giving up after error: %v", v.namePrefix, what, i+1, err)
			return err
		case <-backoff.Backoff().C():
		}
	}
}

func canRestartNode(nodeID string) {
	if !regexp.MustCompile(`worker\d+$`).MatchString(nodeID) {
		skipper.Skipf("node %q not one of the expected QEMU nodes (worker<number>))", nodeID)
	}
}

// restartNode works only for one of the nodes in the QEMU virtual cluster.
// It does a hard poweroff via SysRq and relies on Docker to restart the
// "failed" node.
func restartNode(cs clientset.Interface, nodeID string, sc *sanity.TestContext) {
	cc := csi.NewControllerClient(sc.ControllerConn)
	capacity, err := cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
	framework.ExpectNoError(err, "get capacity before restart")

	node := strings.Split(nodeID, "worker")[1]
	ssh := fmt.Sprintf("%s/_work/%s/ssh.%s",
		os.Getenv("REPO_ROOT"),
		os.Getenv("CLUSTER"),
		node)
	// We detect a successful reboot because a temporary file in the
	// tmpfs /run will be gone after the reboot.
	out, err := exec.Command(ssh, "sudo", "touch", "/run/delete-me").CombinedOutput()
	framework.ExpectNoError(err, "%s touch /run/delete-me:\n%s", ssh, string(out))

	// Shutdown via SysRq b (https://major.io/2009/01/29/linux-emergency-reboot-or-shutdown-with-magic-commands/).
	// TCPKeepAlive is necessary because otherwise ssh can hang for a long time when the remote end dies without
	// closing the TCP connection.
	By(fmt.Sprintf("shutting down node %s", nodeID))
	shutdown := exec.Command(ssh)
	shutdown.Stdin = bytes.NewBufferString(`sudo sh -c 'echo 1 > /proc/sys/kernel/sysrq'
sudo sh -c 'echo b > /proc/sysrq-trigger'`)
	// This always fails, ignore error.
	_, _ = shutdown.CombinedOutput()

	// Wait for node to reboot.
	By("waiting for node to restart")
	Eventually(func() bool {
		test := exec.Command(ssh)
		test.Stdin = bytes.NewBufferString("test ! -e /run/delete-me")
		out, err := test.CombinedOutput()
		if err == nil {
			return true
		}
		framework.Logf("test for /run/delete-me with %s:\n%s\n%s", ssh, err, out)
		return false
	}, "5m", "1s").Should(Equal(true), "node up again")

	By("Node reboot success! Waiting for driver restore connections")
	Eventually(func() int64 {
		currentCapacity, err := cc.GetCapacity(context.Background(), &csi.GetCapacityRequest{})
		if err != nil {
			// Probably not running again yet.
			return 0
		}
		return currentCapacity.AvailableCapacity
	}, "3m", "2s").Should(Equal(capacity.AvailableCapacity), "total capacity after node restart")

	By("Probing node")
	Eventually(func() bool {
		if _, err := csi.NewIdentityClient(sc.Conn).Probe(context.Background(), &csi.ProbeRequest{}); err != nil {
			return false
		}
		By("Node driver: Probe success")
		return true
	}, "5m", "2s").Should(Equal(true), "node driver not ready")
}
