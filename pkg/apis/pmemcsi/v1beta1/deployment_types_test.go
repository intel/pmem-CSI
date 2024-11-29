/*
Copyright 2019  Intel Corporation.

SPDX-License-Identifier: Apache-2.0
*/
package v1beta1_test

import (
	"os"
	"testing"

	"github.com/intel/pmem-csi/pkg/apis"
	api "github.com/intel/pmem-csi/pkg/apis/pmemcsi/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestDeploymentType(t *testing.T) {
	RegisterFailHandler(Fail)

	err := apis.AddToScheme(scheme.Scheme)
	Expect(err).Should(BeNil(), "Failed to add api schema")

	RunSpecs(t, "PMEM Operator API test suite")
}

var _ = Describe("Operator", func() {

	BeforeEach(func() {
	})

	Context("API", func() {
		It("shall set defaults for empty deployment", func() {
			d := api.PmemCSIDeployment{}
			err := d.EnsureDefaults("")
			Expect(err).ShouldNot(HaveOccurred(), "ensure defaults")

			Expect(d.Spec.LogLevel).Should(BeEquivalentTo(api.DefaultLogLevel), "default logging level mismatch")
			Expect(d.Spec.DeviceMode).Should(BeEquivalentTo(api.DefaultDeviceMode), "default driver mode mismatch")
			Expect(d.Spec.Image).Should(BeEquivalentTo(api.DefaultDriverImage), "default driver image mismatch")
			Expect(d.Spec.PullPolicy).Should(BeEquivalentTo(api.DefaultImagePullPolicy), "default image pull policy mismatch")
			Expect(d.Spec.ProvisionerImage).Should(BeEquivalentTo(api.DefaultProvisionerImage), "default provisioner image mismatch")
			Expect(d.Spec.NodeRegistrarImage).Should(BeEquivalentTo(api.DefaultRegistrarImage), "default node driver registrar image mismatch")

			Expect(d.Spec.ControllerDriverResources).ShouldNot(BeNil(), "default controller resources not set")

			Expect(d.Spec.NodeDriverResources).ShouldNot(BeNil(), "default node driver resources not set")
			rs := d.Spec.NodeDriverResources.Requests
			Expect(rs.Cpu().String()).Should(BeEquivalentTo(api.DefaultNodeResourceRequestCPU), "node driver 'cpu' resource request mismatch")
			Expect(rs.Memory().String()).Should(BeEquivalentTo(api.DefaultNodeResourceRequestMemory), "node driver 'cpu' resource request mismatch")

			Expect(d.Spec.NodeRegistrarResources).ShouldNot(BeNil(), "default node registrar resources not set")

			rs = d.Spec.NodeRegistrarResources.Requests
			Expect(rs.Cpu().String()).Should(BeEquivalentTo(api.DefaultNodeRegistrarRequestCPU), "node registrar 'cpu' resource request mismatch")
			Expect(rs.Memory().String()).Should(BeEquivalentTo(api.DefaultNodeRegistrarRequestMemory), "node registrar 'cpu' resource request mismatch")

			Expect(d.Spec.ProvisionerResources).ShouldNot(BeNil(), "default provisioner resources not set")
			rs = d.Spec.ProvisionerResources.Requests
			Expect(rs.Cpu().String()).Should(BeEquivalentTo(api.DefaultProvisionerRequestCPU), "provisioner 'cpu' resource request mismatch")
			Expect(rs.Memory().String()).Should(BeEquivalentTo(api.DefaultProvisionerRequestMemory), "provisioner 'cpu' resource request mismatch")
		})

		It("shall be able to set values", func() {
			yaml := `kind: PmemCSIDeployment
apiVersion: pmem-csi.intel.com/v1beta1
metadata:
  name: test-deployment
spec:
  logLevel: 10
  deviceMode: direct
  image: test-driver:v0.0.0
  imagePullPolicy: Never
  provisionerImage: test-provisioner:v0.0.0
  nodeRegistrarImage: test-driver-registrar:v0.0.0
  controllerDriverResources:
    requests:
      cpu: 1000m
      memory: 10Mi
  nodeDriverResources:
    requests:
      cpu: 2000m
      memory: 100Mi
  nodeRegistrarResources:
    requests:
      cpu: 100m
      memory: 250Mi
  provisionerResources:
    requests:
      cpu: 50m
      memory: 150Mi
`
			decode := scheme.Codecs.UniversalDeserializer().Decode

			obj, _, err := decode([]byte(yaml), nil, nil)
			Expect(err).Should(BeNil(), "Failed to parse deployment")
			Expect(obj).ShouldNot(BeNil(), "Nil deployment object")

			d := obj.(*api.PmemCSIDeployment)
			err = d.EnsureDefaults("")
			Expect(err).ShouldNot(HaveOccurred(), "ensure defaults")

			Expect(d.Spec.LogLevel).Should(BeEquivalentTo(10), "logging level mismatch")
			Expect(d.Spec.DeviceMode).Should(BeEquivalentTo("direct"), "driver mode mismatch")
			Expect(d.Spec.Image).Should(BeEquivalentTo("test-driver:v0.0.0"), "driver image mismatch")
			Expect(d.Spec.PullPolicy).Should(BeEquivalentTo("Never"), "image pull policy mismatch")
			Expect(d.Spec.ProvisionerImage).Should(BeEquivalentTo("test-provisioner:v0.0.0"), "provisioner image mismatch")
			Expect(d.Spec.NodeRegistrarImage).Should(BeEquivalentTo("test-driver-registrar:v0.0.0"), "node driver registrar image mismatch")

			Expect(d.Spec.ControllerDriverResources).ShouldNot(BeNil(), "controller driver resources not set")
			rs := d.Spec.ControllerDriverResources.Requests
			Expect(rs.Cpu().Cmp(resource.MustParse("1000m"))).Should(BeZero(), "controller driver 'cpu' resource requests mismatch")
			Expect(rs.Memory().Cmp(resource.MustParse("10Mi"))).Should(BeZero(), "controller driver 'memory' resource requests mismatch")

			Expect(d.Spec.NodeDriverResources).ShouldNot(BeNil(), "node driver resources not set")
			rs = d.Spec.NodeDriverResources.Requests
			Expect(rs.Cpu().Cmp(resource.MustParse("2000m"))).Should(BeZero(), "node driver 'cpu' resource requests mismatch")
			Expect(rs.Memory().Cmp(resource.MustParse("100Mi"))).Should(BeZero(), "node driver 'memory' resource requests mismatch")

			Expect(d.Spec.NodeRegistrarResources).ShouldNot(BeNil(), "node registrar resources not set")
			rs = d.Spec.NodeRegistrarResources.Requests
			Expect(rs.Cpu().Cmp(resource.MustParse("100m"))).Should(BeZero(), "node registrar 'cpu' resource requests mismatch")
			Expect(rs.Memory().Cmp(resource.MustParse("250Mi"))).Should(BeZero(), "node registrar 'memory' resource request mismatch")

			Expect(d.Spec.ProvisionerResources).ShouldNot(BeNil(), "provisioner resources not set")
			rs = d.Spec.ProvisionerResources.Requests
			Expect(rs.Cpu().Cmp(resource.MustParse("50m"))).Should(BeZero(), "provisioner 'cpu' resource requests mismatch")
			Expect(rs.Memory().Cmp(resource.MustParse("150Mi"))).Should(BeZero(), "provisioner 'memory' resource requests mismatch")
		})

		It("should have valid json schema", func() {

			crdFile := os.Getenv("REPO_ROOT") + "/deploy/crd/pmem-csi.intel.com_pmemcsideployments.yaml"
			data, err := os.ReadFile(crdFile)
			Expect(err).ShouldNot(HaveOccurred(), "load crd data")
			crd := &apiextensions.CustomResourceDefinition{}

			deserializer := scheme.Codecs.UniversalDeserializer()
			_, _, err = deserializer.Decode(data, nil, crd)
			Expect(err).ShouldNot(HaveOccurred(), "decode crd file")

			var crdProp *apiextensions.JSONSchemaProps
			for _, v := range crd.Spec.Versions {
				if v.Name == api.SchemeBuilder.GroupVersion.Version {
					crdProp = v.Schema.OpenAPIV3Schema
				}
			}
			Expect(crdProp).ShouldNot(BeNil(), "Nil CRD schmea")
			Expect(crdProp.Type).Should(BeEquivalentTo("object"), "Deployment JSON schema type mismatch")
			spec, ok := crdProp.Properties["spec"]
			Expect(ok).Should(BeTrue(), "Deployment JSON schema does not have 'spec'")
			status, ok := crdProp.Properties["status"]
			Expect(ok).Should(BeTrue(), "Deployment JSON schema does not have 'status'")

			specProperties := map[string]string{
				"logLevel":                  "integer",
				"image":                     "string",
				"imagePullPolicy":           "string",
				"provisionerImage":          "string",
				"nodeRegistrarImage":        "string",
				"controllerDriverResources": "object",
				"nodeDriverResources":       "object",
				"provisionerResources":      "object",
				"nodeRegistrarResources":    "object",
				"kubeletDir":                "string",
			}

			for key := range spec.Properties {
				By(key)
			}

			for prop, tipe := range specProperties {
				jsonProp, ok := spec.Properties[prop]
				Expect(ok).Should(BeTrue(), "Missing %q property in deployment spec", prop)
				Expect(jsonProp.Type).Should(BeEquivalentTo(tipe), "%q property type mismatch", prop)
			}

			statusProperties := map[string]string{
				"phase": "string",
			}
			for prop, tipe := range statusProperties {
				jsonProp, ok := status.Properties[prop]
				Expect(ok).Should(BeTrue(), "Missing %q property in deployment status", prop)
				Expect(jsonProp.Type).Should(BeEquivalentTo(tipe), "%q property type mismatch", prop)
			}
		})
	})
})
