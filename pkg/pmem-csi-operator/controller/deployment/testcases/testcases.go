/*
Copyright 2020 Intel Corporation

SPDX-License-Identifier: Apache-2.0
*/

// Package testcases contains test cases for the operator which can be used both during
// unit and E2E testing.
package testcases

import (
	"fmt"

	api "github.com/intel/pmem-csi/pkg/apis/pmemcsi/v1beta1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UpdateTest defines a starting deployment and a function which will
// change one or more fields in it.
type UpdateTest struct {
	Name       string
	Deployment api.PmemCSIDeployment
	Mutate     func(d *api.PmemCSIDeployment)
}

func UpdateTests() []UpdateTest {
	singleMutators := map[string]func(d *api.PmemCSIDeployment){
		"image": func(d *api.PmemCSIDeployment) {
			d.Spec.Image = "updated-image"
		},
		"pullPolicy": func(d *api.PmemCSIDeployment) {
			d.Spec.PullPolicy = corev1.PullNever
		},
		"provisionerImage": func(d *api.PmemCSIDeployment) {
			d.Spec.ProvisionerImage = "still-no-such-provisioner-image"
		},
		"nodeRegistrarImage": func(d *api.PmemCSIDeployment) {
			d.Spec.NodeRegistrarImage = "still-no-such-registrar-image"
		},
		"controllerDriverResources": func(d *api.PmemCSIDeployment) {
			d.Spec.ControllerDriverResources = &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("201m"),
					corev1.ResourceMemory: resource.MustParse("101Mi"),
				},
			}
		},
		"controllerReplicas": func(d *api.PmemCSIDeployment) {
			d.Spec.ControllerReplicas = 5
		},
		"nodeDriverResources": func(d *api.PmemCSIDeployment) {
			d.Spec.NodeDriverResources = &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("501m"),
					corev1.ResourceMemory: resource.MustParse("501Mi"),
				},
			}
		},
		"provisionerResources": func(d *api.PmemCSIDeployment) {
			d.Spec.ProvisionerResources = &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("101m"),
					corev1.ResourceMemory: resource.MustParse("101Mi"),
				},
			}
		},
		"nodeRegistrarResources": func(d *api.PmemCSIDeployment) {
			d.Spec.NodeRegistrarResources = &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("301m"),
					corev1.ResourceMemory: resource.MustParse("301Mi"),
				},
			}
		},
		"logLevel": func(d *api.PmemCSIDeployment) {
			d.Spec.LogLevel++
		},
		"logFormat": func(d *api.PmemCSIDeployment) {
			if d.Spec.LogFormat == api.LogFormatText {
				d.Spec.LogFormat = api.LogFormatJSON
			} else {
				d.Spec.LogFormat = api.LogFormatText
			}
		},
		"nodeSelector": func(d *api.PmemCSIDeployment) {
			d.Spec.NodeSelector = map[string]string{
				"still-no-such-label": "still-no-such-value",
			}
		},
		"pmemPercentage": func(d *api.PmemCSIDeployment) {
			d.Spec.PMEMPercentage++
		},
		"labels": func(d *api.PmemCSIDeployment) {
			if d.Spec.Labels == nil {
				d.Spec.Labels = map[string]string{}
			}
			d.Spec.Labels["foo"] = "bar"
		},
		"kubeletDir": func(d *api.PmemCSIDeployment) {
			d.Spec.KubeletDir = "/foo/bar"
		},
		"openshift": func(d *api.PmemCSIDeployment) {
			d.Spec.ControllerTLSSecret = "-openshift-"
		},
	}

	full := api.PmemCSIDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pmem-csi-with-values",
		},
		Spec: api.DeploymentSpec{
			Image:              "base-image",
			PullPolicy:         corev1.PullIfNotPresent,
			ProvisionerImage:   "no-such-provisioner-image",
			NodeRegistrarImage: "no-such-registrar-image",
			DeviceMode:         api.DeviceModeDirect,
			LogLevel:           4,
			NodeSelector: map[string]string{
				"no-such-label": "no-such-value",
			},
			PMEMPercentage: 50,
			Labels: map[string]string{
				"a": "b",
			},
			ControllerDriverResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("20m"),
					corev1.ResourceMemory: resource.MustParse("10Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
			NodeDriverResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("50Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("500Mi"),
				},
			},

			ProvisionerResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("10Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("200Mi"),
				},
			},
			NodeRegistrarResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("30m"),
					corev1.ResourceMemory: resource.MustParse("30Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("300m"),
					corev1.ResourceMemory: resource.MustParse("300Mi"),
				},
			},
		},
	}

	baseDeployments := map[string]api.PmemCSIDeployment{
		"default deployment": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "pmem-csi-with-defaults",
			},
		},
		"deployment with specific values": full,
	}

	updateAll := func(d *api.PmemCSIDeployment) {
		for _, mutator := range singleMutators {
			mutator(d)
		}
	}

	var tests []UpdateTest

	for baseName, dep := range baseDeployments {
		for mutatorName, mutator := range singleMutators {
			tests = append(tests, UpdateTest{
				Name:       fmt.Sprintf("%s in %s", mutatorName, baseName),
				Deployment: dep,
				Mutate:     mutator,
			})
		}
		tests = append(tests, UpdateTest{
			Name:       fmt.Sprintf("all in %s", baseName),
			Deployment: dep,
			Mutate:     updateAll,
		})
	}

	// Special case: remove -openshift-
	openshiftDep := api.PmemCSIDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pmem-csi-for-openshift",
		},
		Spec: api.DeploymentSpec{
			ControllerTLSSecret: "-openshift-",
		},
	}
	tests = append(tests, UpdateTest{
		Name:       "remove-openshift",
		Deployment: openshiftDep,
		Mutate: func(d *api.PmemCSIDeployment) {
			d.Spec.ControllerTLSSecret = ""
		},
	})

	return tests
}
