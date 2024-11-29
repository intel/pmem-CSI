/*
Copyright 2020 Intel Corporation.

SPDX-License-Identifier: Apache-2.0
*/

package deploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	cm "github.com/prometheus/client_model/go"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/skipper"

	api "github.com/intel/pmem-csi/pkg/apis/pmemcsi/v1beta1"
	pmemexec "github.com/intel/pmem-csi/pkg/exec"
	pmemlog "github.com/intel/pmem-csi/pkg/logger"
	"github.com/intel/pmem-csi/pkg/version"
	"github.com/intel/pmem-csi/test/e2e/pod"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

const (
	deploymentLabel = "pmem-csi.intel.com/deployment"
	SocatPort       = 9735
)

// InstallHook is the callback function for AddInstallHook.
type InstallHook func(Deployment *Deployment)

// UninstallHook is the callback function for AddUninstallHook.
type UninstallHook func(deploymentName string)

var (
	installHooks   []InstallHook
	uninstallHooks []UninstallHook

	// If WaitForPMEMDriver timed out once, then it is likely to
	// time out again, which just makes overall testing very slow,
	// in particular in the CI where usually no-one is monitoring
	// progress. Therefore we only allow it to fail once and then
	// skip all future tests.
	waitForPMEMDriverTimedOut bool
)

// AddInstallHook registers a callback which is invoked after a successful driver installation.
func AddInstallHook(h InstallHook) {
	installHooks = append(installHooks, h)
}

// AddUninstallHook registers a callback which is invoked before a driver removal.
func AddUninstallHook(h UninstallHook) {
	uninstallHooks = append(uninstallHooks, h)
}

// WaitForOperator ensures that the PMEM-CSI operator is ready for use, which is
// currently defined as the operator pod in Running phase.
func WaitForOperator(c *Cluster, namespace string) *v1.Pod {
	// TODO(avalluri): At later point of time we should add readiness support
	// for the operator. Then we can query directly the operator if its ready.
	// As intrem solution we are just checking Pod.Status.
	operator := c.WaitForAppInstance(labels.Set{"app": "pmem-csi-operator"}, "", namespace)
	ginkgo.By("Operator is ready!")
	return operator
}

// GetMetricsURLs retrieves the metrics URLs provided by all pods that
// match with the label set passed in podLabelSet.
func GetMetricsURLs(ctx context.Context, c *Cluster, namespace string, podLabels labels.Set) ([]string, error) {
	var urls []string
	list, err := c.cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: podLabels.String()})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no pod(s) found for given labels(%v) in '%s' namespace", podLabels, namespace)
	}

	for _, pod := range list.Items {
		if pod.GetDeletionTimestamp() == nil {
			for _, container := range pod.Spec.Containers {
				for _, port := range container.Ports {
					if port.Name == "metrics" {
						urls = append(urls, fmt.Sprintf("http://%s.%s:%d/metrics", pod.Namespace, pod.Name, port.ContainerPort))
					}
				}
			}
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("no metrics ports found (pods: %+v)", list)
	}

	return urls, nil
}

// GetMetricsPortForward retrieves metrics from given metricsURL
// by Pod's container port forwarding. The metricsURL could be retrieved
// using deploy.GetMetricsURL() and must be in the form of
// 'scheme://pod_namespace.pod_name:container_port/metric_endpoint>'.
func GetMetrics(ctx context.Context, c *Cluster, url string) (map[string]*cm.MetricFamily, error) {
	tr := pod.NewTransport(klogr.New().WithName("port forwarding"), c.cs, c.cfg)
	defer tr.CloseIdleConnections()

	httpClient := http.Client{
		Transport: tr,
		Timeout:   time.Minute,
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("get controller metrics: %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		suffix := ""
		if len(body) > 0 {
			suffix = "\n" + string(body)
		}
		return nil, fmt.Errorf("HTTP GET %s failed: %d%s", url, resp.StatusCode, suffix)
	}

	return (&expfmt.TextParser{}).TextToMetricFamilies(resp.Body)
}

// WaitForPMEMDriver ensures that the PMEM-CSI driver is ready for use, which is
// defined as:
// - controller service is up and running
// - all nodes have registered
// - for testing deployments: TCP CSI endpoints are ready
func WaitForPMEMDriver(c *Cluster, d *Deployment, controllerReplicas int32) (metricsURLs []string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	info := time.NewTicker(time.Minute)
	defer info.Stop()
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if waitForPMEMDriverTimedOut {
		// Abort early.
		skipper.Skipf("installing PMEM-CSI driver during previous test was too slow")
	}

	framework.Logf("Waiting for PMEM-CSI driver.")

	var lastError error
	var version string
	check := func() error {
		// Do not linger too long here, we rather want to
		// abort and print the error instead of getting stuck.
		const timeout = 10 * time.Second
		deadline, cancel := context.WithTimeout(deadline, timeout)
		defer cancel()

		if d.HasController {
			var err error
			metricsURLs, err = GetMetricsURLs(deadline, c, d.Namespace, labels.Set{
				"pmem-csi.intel.com/deployment": d.Label(),
				"app.kubernetes.io/name":        "pmem-csi-controller",
			})
			if err != nil {
				return err
			}
			var oldCount uint64
			checkCount := false
			for _, url := range metricsURLs {
				metrics, err := GetMetrics(deadline, c, url)
				if err != nil {
					return fmt.Errorf("parse metrics response: %v", err)
				}
				buildInfo, ok := metrics["build_info"]
				if !ok {
					return fmt.Errorf("expected build_info not found in metrics: %v", metrics)
				}
				if len(buildInfo.Metric) != 1 {
					return fmt.Errorf("expected build_info to have one metric, got: %v", buildInfo.Metric)
				}
				buildMetric := buildInfo.Metric[0]
				if len(buildMetric.Label) != 1 {
					return fmt.Errorf("expected build_info to have one label, got: %v", buildMetric.Label)
				}
				label := buildMetric.Label[0]
				if *label.Name != "version" {
					return fmt.Errorf("expected build_info to contain a version label, got: %s", *label.Name)
				}
				version = *label.Value

				switch {
				case d.Version == "0.7" || d.Version == "0.8":
					// With the older, centralized provisioning we
					// can also check that the controller knows
					// about all nodes.
					pmemNodes, ok := metrics["pmem_nodes"]
					if !ok {
						return fmt.Errorf("expected pmem_nodes not found in metrics: %v", metrics)
					}

					if len(pmemNodes.Metric) != 1 {
						return fmt.Errorf("expected pmem_nodes to have one metric, got: %v", pmemNodes.Metric)
					}
					nodesMetric := pmemNodes.Metric[0]
					actualNodes := int(*nodesMetric.Gauge.Value)
					if actualNodes != c.NumNodes()-1 {
						return fmt.Errorf("only %d of %d nodes have registered", actualNodes, c.NumNodes()-1)
					}
				case !c.StorageCapacitySupported():
					// It is possible that we just
					// (re)configured the mutating pod
					// webhook. We must be sure that
					// apiserver has adapted to that
					// change. We test that by submitting
					// a pod and checking that the webhook
					// was called, which is visible in its
					// metrics data since PMEM-CSI 0.9.0.
					count, err := findHistogramCount(metrics, "scheduler_request_duration_seconds", map[string]string{"handler": "mutate"})
					if err != nil {
						return nil
					}
					oldCount += count
					checkCount = true
				default:
					// Nothing to wait for. We could check for CSIStorageCapacity objects, but those
					// are not needed yet when starting a test. The scheduler will wait for them by
					// itself.
				}
			}

			if checkCount {
				// Pods get validated after
				// mutation. We can therefore submit
				// an invalid pod that will be sent to
				// our webhook without having to worry
				// about it actually getting created.
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "#&^^%$%@%!@$@$##% invalid name",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{Name: "fake"},
						},
					},
				}
				_, err = c.cs.CoreV1().Pods("default").Create(deadline, pod, metav1.CreateOptions{})
				if err == nil {
					return fmt.Errorf("invalid pod with name %q got created unexpectedly", pod.Name)
				}
				if !apierrs.IsInvalid(err) {
					return fmt.Errorf("expected 'Invalid' as error for pod creation, got: %v", err)
				}

				// Now check that the webhook was called.
				var newCount uint64
				for _, url := range metricsURLs {
					metrics, err := GetMetrics(deadline, c, url)
					if err != nil {
						return fmt.Errorf("parse metrics response: %v", err)
					}

					count, err := findHistogramCount(metrics, "scheduler_request_duration_seconds", map[string]string{"handler": "mutate"})
					if err != nil {
						return fmt.Errorf("parse metrics response: %v", err)
					}
					newCount += count
				}
				if newCount == oldCount {
					return fmt.Errorf("mutating webhook was not called, total request count still %d", oldCount)
				}
			}
		}

		// Check status of every node driver. This is crucial for 0.9.0
		// because this is no longer covered by the controller
		// metrics check that was used previously.
		switch d.Version {
		case "0.7", "0.8":
			// No need to test and doesn't have the necessary labels.
		default:
			pods, err := c.cs.CoreV1().Pods(d.Namespace).List(context.Background(),
				metav1.ListOptions{
					LabelSelector: labels.Set{
						"app.kubernetes.io/instance":  d.DriverName,
						"app.kubernetes.io/component": "node",
					}.String(),
				},
			)
			if err != nil {
				return fmt.Errorf("list node pods: %v", err)
			}
			if len(pods.Items) != c.NumNodes()-1 {
				return fmt.Errorf("only %d of %d node driver pods exist", len(pods.Items), c.NumNodes()-1)
			}
			for _, pod := range pods.Items {
				if !podIsReady(pod.Status) {
					return fmt.Errorf("node driver pod %s on node %s is not ready", pod.Name, pod.Spec.NodeName)
				}
				csiNode, err := c.cs.StorageV1().CSINodes().Get(context.Background(),
					pod.Spec.NodeName,
					metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("get CSINode %s: %v", pod.Spec.NodeName, err)
				}
				if !driverHasRegistered(*csiNode, d.DriverName) {
					return fmt.Errorf("PMEM-CSI driver %s not added to CSINode %+v yet", d.DriverName, csiNode)
				}

				// It would be nice to check the metrics endpoint here, but reaching it from outside
				// the cluster relies on port forwarding (tricky to set up) or some way to run curl
				// inside the cluster (expensive because we would need to start a pod for it).
			}
		}

		// Check controller pods.
		switch d.Version {
		case "0.7", "0.8", "0.9":
			// No need to test and doesn't have the necessary labels.
		default:
			deployments, err := c.cs.AppsV1().Deployments(d.Namespace).List(context.Background(),
				metav1.ListOptions{
					LabelSelector: labels.Set{
						"app.kubernetes.io/instance":  d.DriverName,
						"app.kubernetes.io/component": "controller",
					}.String(),
				},
			)
			if err != nil {
				return fmt.Errorf("list controller deployments: %v", err)
			}
			if len(deployments.Items) != 1 {
				return fmt.Errorf("need one deployment, have %d", len(deployments.Items))
			}
			dep := deployments.Items[0]
			if *dep.Spec.Replicas != controllerReplicas ||
				dep.Status.Replicas != controllerReplicas ||
				dep.Status.AvailableReplicas != controllerReplicas ||
				dep.Status.ReadyReplicas != controllerReplicas {
				return fmt.Errorf("expect controller deployment with %d replicas, have spec.Replicas=%d, status.Replicas=%d, status.AvailableReplicase=%d, status.ReadyReplicas=%d",
					controllerReplicas,
					*dep.Spec.Replicas,
					dep.Status.Replicas,
					dep.Status.AvailableReplicas,
					dep.Status.ReadyReplicas)
			}
		}

		// Done for normal deployments.
		if !d.Testing {
			return nil
		}

		// For testing deployments, also ensure that the CSI endpoints can be reached.
		nodeAddress, controllerAddress, err := LookupCSIAddresses(deadline, c, d.Namespace)
		if err != nil {
			return fmt.Errorf("look up CSI addresses: %v", err)
		}
		tryConnect := func(address string) error {
			addr, err := pod.ParseAddr(address)
			if err != nil {
				return err
			}
			dialer := pod.NewDialer(c.ClientSet(), c.Config())
			// Here we discard error messages because those are expected while
			// the driver starts up. TODO: we could redirect
			// the output into a string buffer via https://github.com/kubernetes/klog/issues/317
			// and include it in the error message.
			ctx := klog.NewContext(deadline, logr.Discard())
			conn, err := dialer.DialContainerPort(ctx, *addr)
			if err != nil {
				return fmt.Errorf("dial %s: %v", address, err)
			}
			defer conn.Close()
			return nil
		}
		if err := tryConnect(controllerAddress); err != nil {
			return fmt.Errorf("connect to controller: %v", err)
		}
		if err := tryConnect(nodeAddress); err != nil {
			return fmt.Errorf("connect to node: %v", err)
		}

		return nil
	}
	ready := func() error {
		newError := check()
		if newError == nil {
			framework.Logf("Done with waiting, PMEM-CSI driver %s is ready.", version)
		}
		// Only overwrite the last error if we haven't reached the deadline yet, because
		// in that case the new error is probably just "context deadline exceeded".
		if lastError == nil || deadline.Err() == nil {
			lastError = newError
		}
		return lastError
	}

	if ready() == nil {
		return
	}
	for {
		select {
		case <-info.C:
			framework.Logf("Still waiting for PMEM-CSI driver, last error: %v", lastError)
		case <-deadline.Done():
			waitForPMEMDriverTimedOut = true
			framework.Failf("Giving up waiting for PMEM-CSI to start up, check the previous warnings and log output. Last error: %v", lastError)
		case <-ticker.C:
			if ready() == nil {
				return
			}
		}
	}
}

func podIsReady(podStatus v1.PodStatus) bool {
	if podStatus.Phase != v1.PodRunning {
		return false
	}
	for _, condition := range podStatus.Conditions {
		if condition.Type == v1.ContainersReady {
			return condition.Status == v1.ConditionTrue
		}
	}
	return false
}

func driverHasRegistered(csiNode storagev1.CSINode, driverName string) bool {
	for _, driver := range csiNode.Spec.Drivers {
		if driver.Name == driverName {
			return true
		}
	}
	return false
}

func findHistogramCount(metrics map[string]*dto.MetricFamily, name string, labels map[string]string) (uint64, error) {
	family, ok := metrics[name]
	if !ok {
		return 0, nil
	}
	for _, metric := range family.Metric {
		if hasAllLabels(metric.Label, labels) {
			if metric.Histogram == nil {
				return 0, fmt.Errorf("expected a histogram for %s, got: %v", name, metric)
			}
			return metric.Histogram.GetSampleCount(), nil
		}
	}

	return 0, nil
}

func hasAllLabels(actualLabels []*dto.LabelPair, requiredLabels map[string]string) bool {
	for key, value := range requiredLabels {
		if !hasLabel(actualLabels, key, value) {
			return false
		}
	}
	return true
}

func hasLabel(labels []*dto.LabelPair, key, value string) bool {
	for _, label := range labels {
		if label.GetName() == key &&
			label.GetValue() == value {
			return true
		}
	}
	return false
}

// https://github.com/containerd/containerd/issues/4068
var containerdTaskError = regexp.MustCompile(`failed to (start|create) containerd task`)

// CheckPMEMDriver does some sanity checks for a running deployment.
func CheckPMEMDriver(c *Cluster, deployment *Deployment) {
	pods, err := c.cs.CoreV1().Pods(deployment.Namespace).List(context.Background(),
		metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s in (%s)", deploymentLabel, deployment.Label()),
		},
	)
	framework.ExpectNoError(err, "list PMEM-CSI pods")
	gomega.Expect(len(pods.Items)).Should(gomega.BeNumerically(">", 0), "should have PMEM-CSI pods")
	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if containerStatus.RestartCount > 0 {
				print := framework.Failf
				if containerdTaskError.MatchString(fmt.Sprintf("%v", containerStatus.LastTerminationState)) {
					// This is a known issue in containerd, only document it.
					print = framework.Logf
				}
				print("container %q in pod %q restarted %d times, last state: %+v",
					containerStatus.Name,
					pod.Name,
					containerStatus.RestartCount,
					containerStatus.LastTerminationState,
				)
			}
		}
	}
}

// RemoveObjects deletes everything that might have been created for a
// PMEM-CSI driver or operator installation (pods, daemonsets,
// statefulsets, driver info, storage classes, etc.).
func RemoveObjects(c *Cluster, deployment *Deployment) error {
	// Try repeatedly, in case that communication with the API server fails temporarily.
	deadline, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)

	name := deployment.Name()
	framework.Logf("deleting the %s PMEM-CSI deployment", name)
	for _, h := range uninstallHooks {
		h(name)
	}

	filter := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s in (%s)", deploymentLabel, deployment.Label()),
	}
	infoDelay := 5 * time.Second
	infoTimestamp := time.Now().Add(infoDelay)
	for {
		success := true // No failures so far.
		done := true    // Nothing left.
		now := time.Now()
		showInfo := infoTimestamp.Before(now)
		if showInfo {
			infoTimestamp = now.Add(infoDelay)
		}
		failure := func(err error) bool {
			if err != nil && !apierrs.IsNotFound(err) {
				framework.Logf("remove PMEM-CSI: %v", err)
				success = false
				return true
			}
			return false
		}
		del := func(objectMeta metav1.ObjectMeta, object interface{}, deletor func() error) {
			// We found something in this loop iteration. Let's do another one
			// to verify that it really is gone.
			done = false

			// Already getting deleted?
			if objectMeta.DeletionTimestamp != nil {
				if showInfo {
					framework.Logf("waiting for deletion of %s (%T, %s)", objectMeta.Name, object, objectMeta.UID)
				}
				return
			}

			// It would be nice if we could print the runtime group/kind information
			// here, but TypeMeta in the objects returned by the client-go interfaces
			// is empty. If there is a way to retrieve it, then it wasn't obvious...
			framework.Logf("deleting %s (%T, %s)", objectMeta.Name, object, objectMeta.UID)
			err := deletor()
			failure(err)
		}

		// Delete all PMEM-CSI deployment objects first to avoid races with the operator
		// restarting things that we want removed.
		if list, err := c.dc.Resource(DeploymentResource).List(context.Background(), filter); !failure(err) && list != nil {
			for _, object := range list.Items {
				deployment := api.PmemCSIDeployment{}
				err := Scheme.Convert(&object, &deployment, nil)
				framework.ExpectNoError(err, "convert %v to PMEM-CSI deployment", object)
				del(deployment.ObjectMeta, deployment, func() error {
					return c.dc.Resource(DeploymentResource).Delete(context.Background(), deployment.Name, metav1.DeleteOptions{})
				})
			}
		}

		// We intentionally delete deployment and stateful sets last because
		// findDriver checks for it. Here we just scale it down
		// to trigger pod deletion.
		if list, err := c.cs.AppsV1().StatefulSets("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				if *object.Spec.Replicas != 0 {
					*object.Spec.Replicas = 0
					_, err := c.cs.AppsV1().StatefulSets(object.Namespace).Update(context.Background(), &object, metav1.UpdateOptions{})
					failure(err)
				}
			}
		}
		if list, err := c.cs.AppsV1().Deployments("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				if *object.Spec.Replicas != 0 {
					*object.Spec.Replicas = 0
					_, err := c.cs.AppsV1().Deployments(object.Namespace).Update(context.Background(), &object, metav1.UpdateOptions{})
					failure(err)
				}
			}
		}

		if list, err := c.cs.AdmissionregistrationV1().MutatingWebhookConfigurations().List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.AppsV1().DaemonSets("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.AppsV1().DaemonSets(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.CoreV1().Pods("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.CoreV1().Pods(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.RbacV1().Roles("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.RbacV1().Roles(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.RbacV1().RoleBindings("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.RbacV1().RoleBindings(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.RbacV1().ClusterRoles().List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.RbacV1().ClusterRoles().Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.RbacV1().ClusterRoleBindings().List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.RbacV1().ClusterRoleBindings().Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.CoreV1().Services("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.CoreV1().Services(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.CoreV1().Endpoints("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.CoreV1().Endpoints(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.CoreV1().ServiceAccounts("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.CoreV1().ServiceAccounts(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.CoreV1().Secrets("").List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.CoreV1().Secrets(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if list, err := c.cs.StorageV1().CSIDrivers().List(context.Background(), filter); !failure(err) {
			for _, object := range list.Items {
				del(object.ObjectMeta, object, func() error {
					return c.cs.StorageV1().CSIDrivers().Delete(context.Background(), object.Name, metav1.DeleteOptions{})
				})
			}
		}

		if done {
			// Nothing else left, now delete the deployments and statefulsets.
			if list, err := c.cs.AppsV1().Deployments("").List(context.Background(), filter); !failure(err) {
				for _, object := range list.Items {
					del(object.ObjectMeta, object, func() error {
						return c.cs.AppsV1().Deployments(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
					})
				}
			}
			if list, err := c.cs.AppsV1().StatefulSets("").List(context.Background(), filter); !failure(err) {
				for _, object := range list.Items {
					del(object.ObjectMeta, object, func() error {
						return c.cs.AppsV1().StatefulSets(object.Namespace).Delete(context.Background(), object.Name, metav1.DeleteOptions{})
					})
				}
			}
		}

		if done && success {
			return nil
		}

		// The actual API calls above are quick, actual deletion
		// is slower. Here we wait for a short while and then
		// check again whether all objects have been deleted.
		select {
		case <-deadline.Done():
			return fmt.Errorf("timed out while trying to delete the %s PMEM-CSI deployment", name)
		case <-ticker.C:
		}
	}
}

// Deployment contains some information about a some deployed PMEM-CSI component(s).
// Those components can be a full driver installation and/or just the operator.
type Deployment struct {
	// HasDriver is true if the driver itself is running.
	HasDriver bool

	// The CSI driver name that the driver is using. Usually
	// pmem-csi.intel.com.
	DriverName string

	// HasOperator is true if the operator is running.
	HasOperator bool

	// HasOLM is true if the OLM(OperatorLifecycleManager) is running.
	HasOLM bool

	// HasController is true if the controller part with the webhooks is enabled.
	HasController bool

	// Mode is the driver mode of the deployment.
	Mode api.DeviceMode

	// Namespace where the namespaced objects of the deployment
	// were created.
	Namespace string

	// Testing is true when socat pods are available.
	Testing bool

	// A version of the format X.Y when installing an older
	// release from the release-X.Y branch.
	Version string
}

func (d Deployment) ParseVersion() version.Version {
	if d.Version == "" {
		// Not specified explicitly, i.e. the current version.
		// We assume that this is more recent than any explicitly
		// specified version and return a fake version number
		// that is higher than anything we expect PMEM-CSI to reach.
		return version.NewVersion(100, 0)
	}
	ver, err := version.Parse(d.Version)
	if err != nil {
		panic(err)
	}
	return ver
}

func (d Deployment) DeploymentMode() string {
	if d.Testing {
		return "testing"
	}
	return "production"
}

// Name returns a string that encodes all attributes in the format expected by Parse.
func (d Deployment) Name() string {
	var parts []string
	switch {
	case d.HasOLM:
		parts = append(parts, "olm")
	case d.HasOperator:
		parts = append(parts, "operator")
	}
	if d.HasDriver {
		parts = append(parts, string(d.Mode))
		parts = append(parts, d.DeploymentMode())
	}
	if d.Version != "" {
		parts = append(parts, d.Version)
	}
	return strings.Join(parts, "-")
}

// Label returns the label used for objects belonging to the deployment.
// It's the same as the name minus the version. The reason for not including
// the version in the label value is that previous releases did not
// have that either. We have to stay consistent with that for up- and downgrade
// testing.
func (d Deployment) Label() string {
	d.Version = ""
	return d.Name()
}

// FindDeployment checks whether there is a PMEM-CSI driver and/or
// operator deployment in the cluster. A deployment is found via its
// deployment resp. statefulset object, which must have a
// pmem-csi.intel.com/deployment label.
func FindDeployment(c *Cluster) (*Deployment, error) {
	driver, err := findDriver(c)
	if err != nil {
		return nil, err
	}
	operator, err := findOperator(c)
	if err != nil {
		return nil, err
	}
	if operator != nil && driver != nil && operator.Name() != driver.Name() && !strings.HasPrefix(driver.Name(), operator.Name()) {
		return nil, fmt.Errorf("found two different deployments: operator %s and driver %s", operator.Name(), driver.Name())
	}
	// findDriver is able to discover some additional information, so return that result
	// if we have both.
	if driver != nil {
		return driver, nil
	}
	if operator != nil {
		return operator, nil
	}
	return nil, nil
}

var imageVersion = regexp.MustCompile(`pmem-csi-driver(?:-test)?:v(\d+\.\d+)`)

// versionFromContainerImage Retrieves version from driver container image tag
func versionFromContainerImage(image string) (string, bool) {
	m := imageVersion.FindStringSubmatch(image)
	if m == nil {
		// not pmem-csi-driver image
		return "", false
	}

	// If the version matches what we are currently testing, then we skip
	// the version (i.e. "current version" == "no explicit version").
	if m2 := imageVersion.FindStringSubmatch(os.Getenv("PMEM_CSI_IMAGE")); m2 == nil || m2[1] != m[1] {
		return m[1], true
	}

	return "", true
}

func findDriver(c *Cluster) (*Deployment, error) {
	list, err := c.cs.AppsV1().DaemonSets("").List(context.Background(), metav1.ListOptions{LabelSelector: deploymentLabel})
	if err != nil {
		return nil, err
	}

	if len(list.Items) == 0 {
		return nil, nil
	}
	name := list.Items[0].Labels[deploymentLabel]
	deployment, err := Parse(name)
	if err != nil {
		return nil, fmt.Errorf("parse label of deployment %s: %v", list.Items[0].Name, err)
	}
	deployment.Namespace = list.Items[0].Namespace

	drivers, err := c.cs.StorageV1().CSIDrivers().List(context.Background(), metav1.ListOptions{LabelSelector: deploymentLabel})
	if err != nil {
		return nil, err
	}
	if len(drivers.Items) != 1 {
		return nil, fmt.Errorf("expected one CSIDriver info, got: %v", drivers)
	}
	deployment.DriverName = drivers.Items[0].Name

	controllerSS, err := c.cs.AppsV1().StatefulSets("").List(context.Background(), metav1.ListOptions{LabelSelector: deploymentLabel})
	if err != nil {
		return nil, fmt.Errorf("checking for StatefulSet: %v", err)
	}
	controllerD, err := c.cs.AppsV1().Deployments("").List(context.Background(), metav1.ListOptions{LabelSelector: deploymentLabel})
	if err != nil {
		return nil, fmt.Errorf("checking for Deployment: %v", err)
	}
	deployment.HasController = len(controllerSS.Items) > 0 || len(controllerD.Items) > 0

	// Derive the version from the image tag. The annotation doesn't include it.
	for _, container := range list.Items[0].Spec.Template.Spec.Containers {
		if v, found := versionFromContainerImage(container.Image); found {
			deployment.Version = v
			break
		}
	}

	// Currently we don't support parallel installations, so all
	// objects must belong to each other.
	for _, item := range list.Items {
		if item.Labels[deploymentLabel] != name {
			return nil, fmt.Errorf("found at least two different deployments: %s and %s", item.Labels[deploymentLabel], name)
		}
	}

	return deployment, nil
}

func findOperator(c *Cluster) (*Deployment, error) {
	// We have to try multiple times because the Deployment
	// controller might still be working on the ReplicaSets that
	// findOperatorOnce is based on.
	for i := 0; ; i++ {
		deployment, err := findOperatorOnce(c)
		if err == nil {
			return deployment, nil
		}
		if i > 60 {
			return nil, err
		}
		framework.Logf("finding operator failed during attempt #%d, will retry: %v", err)
		time.Sleep(time.Second)
	}
}

func findOperatorOnce(c *Cluster) (*Deployment, error) {
	// In case of operator deployed by OLM the labels on the Deployment object
	// get overwritten by OLM reconcile loop.
	// But the ReplicaSet underneath holds the labels we set on Deployment.
	// So to cover all the cases we depend on ReplicaSet label.
	list, err := c.cs.AppsV1().ReplicaSets("").List(context.Background(), metav1.ListOptions{LabelSelector: deploymentLabel})
	if err != nil {
		return nil, err
	}

	isValid := func(replicaset appsv1.ReplicaSet) bool {
		// We can ignore the ReplicaSets of the driver.
		if replicaset.Labels["app"] != "pmem-csi-operator" {
			return false
		}
		// We can ignore replicasets which don't have any pods. Those
		// continue to exist with the new (?) apps.Deployment as owner.
		if *replicaset.Spec.Replicas == 0 && replicaset.Status.Replicas == 0 {
			return false
		}
		return true
	}

	// Find one ReplicaSet that actually belongs to an active PMEM-CSI operator.
	var activeReplicaSet *appsv1.ReplicaSet
	for _, item := range list.Items {
		if isValid(item) {
			activeReplicaSet = &item
			break
		}
	}
	if activeReplicaSet == nil {
		return nil, nil
	}
	deployment, err := Parse(activeReplicaSet.Labels[deploymentLabel])
	if err != nil {
		return nil, fmt.Errorf("parse label of deployment %s: %v", activeReplicaSet.Name, err)
	}
	deployment.Namespace = activeReplicaSet.Namespace

	// Derive the version from the image tag. The annotation doesn't include it.
	// If the version matches what we are currently testing, then we skip
	// the version (i.e. "current version" == "no explicit version").
	for _, container := range activeReplicaSet.Spec.Template.Spec.Containers {
		if v, found := versionFromContainerImage(container.Image); found {
			deployment.Version = v
			break
		}
	}

	// Currently we don't support parallel installations, so all
	// objects must belong to each other.
	for _, item := range list.Items {
		if isValid(item) && item.Labels[deploymentLabel] != activeReplicaSet.Labels[deploymentLabel] {
			return nil, fmt.Errorf("found at least two different deployments: %s and %s\n%+v\n%+v",
				item.Labels[deploymentLabel], activeReplicaSet.Labels[deploymentLabel],
				item, activeReplicaSet,
			)
		}
	}

	return deployment, nil
}

// The order matters here: the deployments listed first will also run first,
// and somehow that helps OLM. When it was run at the end, there were OLM
// errors ("operatorhubio-catalog-lbdrz 0/1 CrashLoopBackOff") that did not
// occur locally or when running the tests on a "fresh" CI cluster.
var allDeployments = []string{
	// Testing with OLM is disabled. Installing it was a frequent source of
	// random failures, it's simply not reliable enough.
	//
	// "olm", // operator installed by OLM
	// "olm-lvm-production",
	// "olm-direct-production",

	"lvm-production",
	"direct-production",
	"operator",
	// Uses second.pmem-csi.intel.com as driver name.
	"operator-lvm-production",
	// Uses kube-system, to ensure that deployment in a namespace also works,
	// and *no* controller.
	"operator-direct-production",
}
var deploymentRE = regexp.MustCompile(`^(operator|olm)?-?(\w*)?-?(testing|production)?-?([0-9\.]*)$`)

// Parse the deployment name and sets fields accordingly.
func Parse(deploymentName string) (*Deployment, error) {

	matches := deploymentRE.FindStringSubmatch(deploymentName)
	if matches == nil {
		return nil, fmt.Errorf("unsupported deployment %s", deploymentName)
	}

	deployment := &Deployment{
		Namespace:     "default",
		DriverName:    "pmem-csi.intel.com",
		HasController: true,
	}

	switch matches[1] {
	case "olm":
		deployment.HasOLM = true
		deployment.HasOperator = true
	case "operator":
		deployment.HasOperator = true
	}
	if matches[2] != "" {
		deployment.HasDriver = true
		deployment.Testing = matches[3] == "testing"
		if err := deployment.Mode.Set(matches[2]); err != nil {
			return nil, fmt.Errorf("deployment name %s: %v", deploymentName, err)
		}
	}
	deployment.Version = matches[4]

	// Introduce some variation in how we deploy.
	switch {
	case deploymentName == "operator":
		deployment.Namespace = "operator-test"
	case strings.HasPrefix(deploymentName, "operator-direct-production"):
		deployment.Namespace = "kube-system"
	case strings.HasPrefix(deploymentName, "operator-lvm-production"):
		deployment.DriverName = "second.pmem-csi.intel.com"
	}

	return deployment, nil
}

// MustParse calls Parse and panics when the name is not valid.
func MustParse(deploymentName string) *Deployment {
	deployment, err := Parse(deploymentName)
	if err != nil {
		framework.Failf("internal error while parsing %s: %v", deploymentName, err)
	}
	return deployment
}

// EnsureDeployment registers a BeforeEach function which will ensure that when
// a test runs, the desired deployment exists. Deployed drivers are intentionally
// kept running to speed up the execution of multiple tests that all want the
// same kind of deployment.
//
// The driver should never restart. A restart would indicate some
// (potentially intermittent) issue.
func EnsureDeployment(deploymentName string) *Deployment {
	deployment := MustParse(deploymentName)

	f := framework.NewDefaultFramework("cluster")
	f.SkipNamespaceCreation = true
	var prevVol Volumes

	ginkgo.BeforeEach(func() {
		ginkgo.By(fmt.Sprintf("preparing for test %q in namespace %s",
			ginkgo.CurrentGinkgoTestDescription().FullTestText,
			deployment.Namespace,
		))

		// Remember list of volumes before test, using out-of-band host commands (i.e. not CSI API).
		prevVol = GetHostVolumes(deployment)

		EnsureDeploymentNow(f, deployment)

		for _, h := range installHooks {
			h(deployment)
		}
	})

	ginkgo.AfterEach(func() {
		state := "success"
		if ginkgo.CurrentGinkgoTestDescription().Failed {
			state = "failure"
		}
		ginkgo.By(fmt.Sprintf("checking for test %q in namespace %s, test %s",
			ginkgo.CurrentGinkgoTestDescription().FullTestText,
			deployment.Namespace,
			state,
		))

		// Check list of volumes after test to detect left-overs
		prevVol.CheckForLeaks()

		// And check that PMEM is in a sane state.
		CheckPMEM()
	})

	return deployment
}

// EnsureDeploymentNow checks the currently running driver and replaces it if necessary.
func EnsureDeploymentNow(f *framework.Framework, deployment *Deployment) {
	ctx, _ := pmemlog.WithName(context.Background(), "EnsureDeployment")
	c, err := NewCluster(f.ClientSet, f.DynamicClient, f.ClientConfig())
	root := os.Getenv("REPO_ROOT")
	env := os.Environ()

	framework.ExpectNoError(err, "get cluster information")
	running, err := FindDeployment(c)
	framework.ExpectNoError(err, "check for PMEM-CSI components")
	framework.Logf("want %s PMEM-CSI deployment = %+v", deployment.Name(), *deployment)
	if running != nil {
		if reflect.DeepEqual(deployment, running) {
			framework.Logf("reusing existing %s PMEM-CSI components", deployment.Name())
			// Do some sanity checks on the running deployment before the test.
			if deployment.HasDriver {
				WaitForPMEMDriver(c, deployment, 1 /* controller replicas */)
				CheckPMEMDriver(c, deployment)
			}
			if deployment.HasOperator {
				WaitForOperator(c, deployment.Namespace)
			}
			return
		}
		framework.Logf("have %s PMEM-CSI deployment in namespace %s, want %s in namespace %s-> delete existing deployment",
			running.Name(), running.Namespace,
			deployment.Name(), deployment.Namespace)

		if running.HasOperator {
			// If both running and wanted operator deployments are managed by
			// OLM, then we do nothing for upgrades.
			// The ./test/start-operator.sh -olm script
			// is supposed to handle the case, in other words the OLM should treat
			// this as operator upgrade.
			// Downgrades are not supported. We have to delete the operator first.
			bothOLM := running.HasOLM && deployment.HasOLM
			if !bothOLM || deployment.ParseVersion().CompareVersion(running.ParseVersion()) < 0 {
				err := StopOperator(running)
				framework.ExpectNoError(err, "delete operator deployment: %s -> %s", running.Name(), deployment.Name())
			}
		}

		// Remove driver if not needed anymore or different.
		if !running.HasOLM || running.HasDriver && !deployment.HasDriver || running.Mode != deployment.Mode {
			if running.HasOLM {
				// We have to delete the operator
				// deployment first, otherwise OLM is
				// going to recreate the operator pod
				// each time RemoveObjects deletes it.
				err := StopOperator(running)
				framework.ExpectNoError(err, "remove PMEM-CSI operator: %s -> %s", running.Name(), deployment.Name())
			}
			err := RemoveObjects(c, running)
			framework.ExpectNoError(err, "remove PMEM-CSI deployment: %s -> %s", running.Name(), deployment.Name())
		}

		if running.HasOLM && !deployment.HasOLM {
			cmd := exec.Command("test/start-stop-olm.sh", "stop")
			cmd.Dir = root
			cmd.Env = env
			_, err := pmemexec.Run(ctx, cmd)
			framework.ExpectNoError(err, "stop OLM: %s -> %s", running.Name(), deployment.Name())
		}
	}

	if deployment.HasOLM {
		cmd := exec.Command("test/start-stop-olm.sh", "start")
		cmd.Dir = root
		cmd.Env = env
		_, err := pmemexec.Run(ctx, cmd)
		framework.ExpectNoError(err, "start OLM: %q", deployment.Name())
	}

	if deployment.Version != "" {
		// Find the latest dot release on the branch for which images are public.
		// Most recent tag is listed first. We better avoid pulling over and over again
		// to avoid throttling.
		tags, err := pmemexec.RunCommand(ctx, "git", "tag", "--sort=-version:refname")
		framework.ExpectNoError(err, "fetch git tags")
		scanner := bufio.NewScanner(strings.NewReader(tags))
		var tag string
		for scanner.Scan() {
			tag = scanner.Text()
			if strings.HasPrefix(tag, "v"+deployment.Version) {
				if _, err := pmemexec.RunCommand(ctx, "docker", "image", "inspect", "--format='exists'", "intel/pmem-csi-driver:"+tag); err == nil {
					break
				}
				if _, err := pmemexec.RunCommand(ctx, "docker", "image", "pull", "intel/pmem-csi-driver:"+tag); err == nil {
					break
				}
			}
		}
		framework.Logf("using %s images for release-%s", tag, deployment.Version)

		// Clean check out in _work/pmem-csi-release-<version>.
		// Pulling from remote must be done before running the test.
		workRoot := root + "/_work/pmem-csi-release-" + deployment.Version
		err = os.RemoveAll(workRoot)
		framework.ExpectNoError(err, "remove PMEM-CSI source code")
		_, err = pmemexec.RunCommand(ctx, "git", "clone", "--shared", root, workRoot)
		framework.ExpectNoError(err, "clone repo", deployment.Version)
		_, err = pmemexec.RunCommand(ctx, "git", "-C", workRoot, "checkout", tag)
		framework.ExpectNoError(err, "check out release-%s = %s of PMEM-CSI", deployment.Version, tag)

		// The setup script expects to have
		// the same _work as in the normal root.
		err = os.Symlink("../../_work", workRoot+"/_work")
		framework.ExpectNoError(err, "symlink the _work directory")

		// NOTE: Release branch does not have the OLM bundle
		// So we have to generate them. We use `make operator-generate-bundle`
		// from devel on released manifests(CRD, CSV etc.,) for generating
		// released OLM bundles. This step could be avoided if we keep the
		// generated oln-bundles under /deploy in the source tree.
		if deployment.HasOLM {
			make := exec.Command("make", "operator-generate-bundle", "VERSION="+tag, "REPO_ROOT="+workRoot)
			make.Dir = workRoot
			make.Env = env
			_, err := pmemexec.Run(ctx, make)
			framework.ExpectNoError(err, "%s: generate bundle for operator version %s", deployment.Name(), deployment.Version)
		}

		root = workRoot
		// The release branch does not pull from Docker Hub by default,
		// we have to select that explicitly.
		env = append(env, "REPO_ROOT="+root, "TEST_PMEM_REGISTRY=intel", "TEST_PMEM_IMAGE_TAG="+tag)
	}

	if deployment.HasOperator {
		// At the moment, the only supported deployment method is via test/start-operator.sh.
		cmdArgs := []string{}
		if deployment.HasOLM {
			cmdArgs = append(cmdArgs, "-olm")
		}
		cmd := exec.Command("test/start-operator.sh", cmdArgs...)
		cmd.Dir = os.Getenv("REPO_ROOT")
		cmd.Env = append(env,
			"TEST_OPERATOR_NAMESPACE="+deployment.Namespace,
			"TEST_OPERATOR_DEPLOYMENT_LABEL="+deployment.Label())
		_, err := pmemexec.Run(ctx, cmd)
		framework.ExpectNoError(err, "create operator deployment: %q", deployment.Name())
		WaitForOperator(c, deployment.Namespace)
	}
	if deployment.HasDriver {
		if deployment.HasOperator {
			// Deploy driver through operator.
			dep := deployment.GetDriverDeployment(c)
			EnsureDeploymentCR(f, dep)
		} else {
			// Deploy with script.
			cmd := exec.Command("test/setup-deployment.sh")
			cmd.Dir = root
			cmd.Env = append(env, "TEST_KUBERNETES_FLAVOR=",
				"TEST_DEPLOYMENT_QUIET=quiet",
				"TEST_DEPLOYMENTMODE="+deployment.DeploymentMode(),
				"TEST_DRIVER_NAMESPACE="+deployment.Namespace,
				"TEST_DEVICEMODE="+string(deployment.Mode))
			_, err = pmemexec.Run(ctx, cmd)
			framework.ExpectNoError(err, "create %s PMEM-CSI deployment", deployment.Name())
		}

		// We check for a running driver the same way at the moment, by directly
		// looking at the driver state. Long-term we want the operator to do that
		// checking itself.
		WaitForPMEMDriver(c, deployment, 1 /* controller replicas */)
		CheckPMEMDriver(c, deployment)
	}
}

func StopOperator(d *Deployment) error {
	ctx, _ := pmemlog.WithName(context.Background(), "StopOperator")
	cmd := exec.Command("test/stop-operator.sh")
	if d.HasOLM {
		cmd.Args = append(cmd.Args, "-olm")
	}
	// Keep both the CRD and namespace in case the required deployment is also the operator
	// required for version skew tests, otherwise it brings down the existing driver deployment(s)
	if d.HasOperator {
		cmd.Args = append(cmd.Args, "-keep-crd", "-keep-namespace")
	}
	cmd.Dir = os.Getenv("REPO_ROOT")
	cmd.Env = append(os.Environ(),
		"TEST_OPERATOR_NAMESPACE="+d.Namespace,
		"TEST_OPERATOR_DEPLOYMENT_LABEL="+d.Label())
	_, err := pmemexec.Run(ctx, cmd)

	return err
}

// GetDriverDeployment returns the spec for the driver deployment that is used
// for deployments like operator-lvm-production.
func (d *Deployment) GetDriverDeployment(c *Cluster) api.PmemCSIDeployment {
	dep := api.PmemCSIDeployment{
		// TypeMeta is needed because
		// DefaultUnstructuredConverter does not add it for us. Is there a better way?
		TypeMeta: metav1.TypeMeta{
			APIVersion: api.SchemeGroupVersion.String(),
			Kind:       "PmemCSIDeployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: d.DriverName,
			Labels: map[string]string{
				deploymentLabel: d.Label(),
			},
		},
		Spec: api.DeploymentSpec{
			Labels: map[string]string{
				deploymentLabel: d.Label(),
			},
			DeviceMode: d.Mode,
			// As in setup-deployment.sh, only 50% of the available
			// PMEM must be used for LVM, otherwise other tests cannot
			// run after the LVM driver was deployed once.
			PMEMPercentage: 50,
			NodeSelector: map[string]string{
				// Provided by NFD.
				"feature.node.kubernetes.io/memory-nv.dax": "true",
			},
		},
	}
	return dep
}

// DeleteAllPods deletes all currently running pods that belong to the deployment.
func (d Deployment) DeleteAllPods(c *Cluster) error {
	listOptions := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s in (%s)", deploymentLabel, d.Label()),
	}
	pods, err := c.cs.CoreV1().Pods(d.Namespace).List(context.Background(), listOptions)
	if err != nil {
		return fmt.Errorf("list all PMEM-CSI pods: %v", err)
	}
	// Kick of deletion of several pods at once.
	if err := c.cs.CoreV1().Pods(d.Namespace).DeleteCollection(context.Background(),
		metav1.DeleteOptions{},
		listOptions,
	); err != nil {
		return fmt.Errorf("delete all PMEM-CSI pods: %v", err)
	}
	// But still wait for every single one to be gone...
	for _, pod := range pods.Items {
		if err := waitForPodDeletion(c, pod); err != nil {
			return fmt.Errorf("wait for pod deletion: %v", err)
		}
	}
	return nil
}

// LookupCSIAddresses returns controller and node addresses for pod/dial.go (<namespace>.<pod>:<port>).
// Only works for testing deployments.
func LookupCSIAddresses(ctx context.Context, c *Cluster, namespace string) (nodeAddress, controllerAddress string, err error) {
	// Node #1 is expected to have a PMEM-CSI node driver
	// instance. If it doesn't, connecting to the PMEM-CSI
	// node service will fail. If we only have one node,
	// then we use that one.
	node := 1
	if node >= c.NumNodes() {
		node = 0
	}
	ip := c.NodeIP(node)
	pod, err := c.GetAppInstance(ctx, labels.Set{"app.kubernetes.io/component": "node-testing"}, ip, namespace)
	if err != nil {
		return "", "", fmt.Errorf("find socat pod on node #%d = %s: %v", node, ip, err)
	}

	for _, port := range pod.Spec.Containers[0].Ports {
		if port.Name == "csi-socket" {
			nodeAddress = fmt.Sprintf("%s.%s:%d", namespace, pod.Name, port.ContainerPort)
			// Also use that same node as controller.
			controllerAddress = nodeAddress
			return

		}
	}
	// Fallback for PMEM-CSI 0.9. Can be removed once we stop testing against it.
	nodeAddress = fmt.Sprintf("%s.%s:9735", namespace, pod.Name)
	controllerAddress = nodeAddress
	return
	// return "", "", fmt.Errorf("container port 'csi-socket' not found in pod %+v", pod)
}

// DescribeForAll registers tests like gomega.Describe does, except that
// each test will then be invoked for each supported PMEM-CSI deployment
// which has a functional PMEM-CSI driver.
func DescribeForAll(what string, f func(d *Deployment)) bool {
	DescribeForSome(what, HasDriver, f)
	return true
}

// HasDriver is a filter function for DescribeForSome.
func HasDriver(d *Deployment) bool {
	return d.HasDriver
}

// HasOperator is a filter function for DescribeForSome.
func HasOperator(d *Deployment) bool {
	return d.HasOperator
}

// RunAllTests is a filter function for DescribeForSome which decides
// against what we run the full Kubernetes storage test
// suite. Currently do this for deployments created via .yaml files
// whereas testing with the operator is excluded. This is meant to
// keep overall test suite runtime reasonable and avoid duplication.
func RunAllTests(d *Deployment) bool {
	return d.HasDriver && !d.HasOperator
}

// DescribeForSome registers tests like gomega.Describe does, except that
// each test will then be invoked for those PMEM-CSI deployments which
// pass the filter function.
func DescribeForSome(what string, enabled func(d *Deployment) bool, f func(d *Deployment)) bool {
	for _, deploymentName := range allDeployments {
		deployment := MustParse(deploymentName)
		if enabled(deployment) {
			Describe(deploymentName, deploymentName, what, f)
		}
	}

	return true
}

// deployment name -> top level describe string -> list of test functions for that combination
var tests = map[string]map[string][]func(d *Deployment){}

// Describe remembers a certain test. The actual registration in
// Ginkgo happens in DefineTests, ordered such that all tests with the
// same "deployment" string are defined one after the after with the
// given "describe" string.
//
// An empty deployment is valid. Those tests will run after the ones
// which use deployments. Their callback is passed a nil Deployment.
//
// When "describe" is already unique, "what" can be left empty.
func Describe(deployment, describe, what string, f func(d *Deployment)) bool {
	group := tests[deployment]
	if group == nil {
		group = map[string][]func(d *Deployment){}
	}
	group[describe] = append(group[describe], func(d *Deployment) {
		if what == "" {
			// Skip one nesting layer.
			f(d)
			return
		}
		ginkgo.Describe(what, func() {
			f(d)
		})
	})
	tests[deployment] = group

	return true
}

// DefineTests must be called to register all tests defined so far via Describe.
//
// They are placed inside a "Deploy" node because:
//   - The order of specs inside a node is not randomized by default,
//     which is what we want, in contrast to the top-level nodes,
//     which always get randomized (https://github.com/onsi/ginkgo/issues/966#issuecomment-1110046593).
//   - It is easier to focus on specific tests with, for example, "Deploy operator".
//     "^operator" does not work because the internal test name string has some
//     builtin, hidden string at the beginning (something like "top level").
func DefineTests() {
	ginkgo.Context("Deploy", defineTests)
}

func defineTests() {
	all := allDeployments[:]
	for deploymentName := range tests {
		if !haveDeployment(all, deploymentName) {
			all = append(all, deploymentName)
		}
	}

	for _, deploymentName := range all {
		group, ok := tests[deploymentName]
		if !ok {
			continue
		}
		deploymentName := deploymentName
		for describe, funcs := range group {
			funcs := funcs
			klog.InfoS("defining tests", "deployment", deploymentName, "test", describe)
			ginkgo.Describe(describe, func() {
				var deployment *Deployment
				if deploymentName != "" {
					deployment = EnsureDeployment(deploymentName)
				}
				for _, f := range funcs {
					f(deployment)
				}
			})
		}
	}
}

func haveDeployment(all []string, one string) bool {
	for _, a := range all {
		if a == one {
			return true
		}
	}
	return false
}

// waitForPodDeletion returns an error if it takes too long for the pod to fully terminate.
func waitForPodDeletion(c *Cluster, pod v1.Pod) error {
	return wait.PollImmediate(2*time.Second, time.Minute, func() (bool, error) {
		existingPod, err := c.cs.CoreV1().Pods(pod.Namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil // done
		}
		if err != nil {
			return true, err // stop wait with error
		}
		if pod.UID != existingPod.UID {
			return true, nil // also done (pod was restarted)
		}
		return false, nil
	})
}
