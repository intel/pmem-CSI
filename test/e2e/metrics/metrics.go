/*
Copyright 2019,2020 Intel Corporation.

SPDX-License-Identifier: Apache-2.0
*/

package gotests

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/klogr"
	"k8s.io/kubernetes/test/e2e/framework"

	"github.com/intel/pmem-csi/test/e2e/deploy"
	"github.com/intel/pmem-csi/test/e2e/pod"
)

// We only need to test this in one deployment because the tests
// do not depend on the driver mode or how it was deployed.
var _ = deploy.Describe("direct-testing", "direct-testing-metrics", "", func(d *deploy.Deployment) {
	f := framework.NewDefaultFramework("metrics")
	f.SkipNamespaceCreation = true

	var (
		metricsURL string
		client     *http.Client
		timeout    = 10 * time.Second
	)

	BeforeEach(func() {
		cluster, err := deploy.NewCluster(f.ClientSet, f.DynamicClient, f.ClientConfig())
		framework.ExpectNoError(err, "get cluster information")
		metricsURLs := deploy.WaitForPMEMDriver(cluster, d, 1 /* controller replicas */)
		if len(metricsURLs) != 1 {
			framework.Failf("expected one controller, got multiple metrics URLs: %v", metricsURLs)
		}
		metricsURL = metricsURLs[0]
		client = &http.Client{
			Transport: pod.NewTransport(klogr.New().WithName("port forwarding"), f.ClientSet, f.ClientConfig()),
			Timeout:   timeout,
		}
	})

	Context("data", func() {
		testData := func(simple bool, pods *corev1.PodList) {
			numPods := 0
			for _, pod := range pods.Items {
				if pod.Annotations["pmem-csi.intel.com/scrape"] != "containers" {
					continue
				}
				numPods++

				numPorts := 0
				for _, container := range pod.Spec.Containers {
					isPmemCSI := strings.HasPrefix(container.Name, "pmem")
					if simple && !isPmemCSI {
						continue
					}
					for _, port := range container.Ports {
						if port.Name == "metrics" {
							numPorts++

							ip := pod.Status.PodIP
							portNum := port.ContainerPort
							Expect(ip).ToNot(BeEmpty(), "have pod IP for pod %s", pod)
							Expect(portNum).ToNot(Equal(0), "have container port for pod %s", pod)

							url := fmt.Sprintf("http://%s.%s:%d/metrics",
								pod.Namespace, pod.Name, port.ContainerPort)
							if simple {
								url += "/simple"
							}
							resp, err := client.Get(url)
							framework.ExpectNoError(err, "GET failed")
							// When wrapped with InterceptGomegaFailures, err == nil doesn't
							// cause the function to abort. We have to do that ourselves before
							// using resp to avoid a panic.
							// https://github.com/onsi/gomega/issues/198#issuecomment-856630787
							if err != nil {
								return
							}
							data, err := io.ReadAll(resp.Body)
							framework.ExpectNoError(err, "read GET response for pod %s and url %s", pod, url)
							name := pod.Name + "/" + container.Name
							if isPmemCSI {
								Expect(data).To(ContainSubstring("build_info"), name)
								expect := Expect(data).To
								if simple {
									// All other metrices are not part of metrics/simple.
									expect = Expect(data).NotTo
								}
								expect(ContainSubstring("go_threads "), name)
								expect(ContainSubstring("process_open_fds "), name)
								if !strings.Contains(pod.Name, "controller") {
									// Only the node driver implements CSI and manages volumes.
									expect(ContainSubstring("csi_plugin_operations_seconds "), name)
									expect(ContainSubstring("pmem_amount_available "), name)
									expect(ContainSubstring("pmem_amount_managed "), name)
									expect(ContainSubstring("pmem_amount_max_volume_size "), name)
									expect(ContainSubstring("pmem_amount_total "), name)
								}
							} else {
								Expect(data).To(ContainSubstring("csi_sidecar_operations_seconds "), name)
							}
						}
					}
				}
				Expect(numPorts).NotTo(Equal(0), "at least one container should have a 'metrics' port in pod %s", pod)
			}
			Expect(numPods).NotTo(Equal(0), "at least one container should have a 'metrics' port")
		}

		test := func(simple bool) {
			// WaitForPMEMDriver already verified that "version"
			// is returned and that "pmem_nodes" is correct.
			// Here we check metrics support of each pod (= annotations +
			// metrics endpoint).
			pods, err := f.ClientSet.CoreV1().Pods(d.Namespace).List(context.Background(), metav1.ListOptions{})
			framework.ExpectNoError(err, "list pods")

			Eventually(func() string {
				return strings.Join(InterceptGomegaFailures(func() {
					testData(simple, pods)
				}), "\n")
			}, "60s", "1s").Should(BeEmpty())
		}

		It("full", func() {
			test(false)
		})

		It("simple", func() {
			test(true)
		})
	})

	It("rejects large headers", func() {
		req, err := http.NewRequest("GET", metricsURL, nil)
		framework.ExpectNoError(err, "create GET request for %s", metricsURL)
		for i := 0; i < 100000; i++ {
			req.Header.Add(fmt.Sprintf("My-Header-%d", i), "foobar")
		}
		resp, err := client.Do(req)
		framework.ExpectNoError(err, "execute GET request for %s", metricsURL)
		Expect(resp.StatusCode).Should(Equal(http.StatusRequestHeaderFieldsTooLarge), "header too large")
	})

	It("rejects invalid path", func() {
		url := metricsURL + "/extra-path"
		resp, err := client.Get(url)
		framework.ExpectNoError(err, "execute GET request for %s", url)
		Expect(resp.StatusCode).Should(Equal(http.StatusNotFound), "not found")
	})
})
