package test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/gcp"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/http-helper"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/gruntwork-io/terratest/modules/test-structure"
)

func TestGKEBasicTiller(t *testing.T) {
	// We are temporarily stopping the tests from running in parallel due to conflicting
	// kubectl configs. This is a limitation in the current Terratest functions and will
	// be fixed in a later release.
	//t.Parallel()

	// Uncomment any of the following to skip that section during the test
	// os.Setenv("SKIP_create_test_copy_of_examples", "true")
	// os.Setenv("SKIP_create_terratest_options", "true")
	// os.Setenv("SKIP_terraform_apply", "true")
	// os.Setenv("SKIP_wait_for_workers", "true")
	// os.Setenv("SKIP_helm_install", "true")
	// os.Setenv("SKIP_cleanup", "true")

	// Create a directory path that won't conflict
	workingDir := filepath.Join(".", "stages", t.Name())

	test_structure.RunTestStage(t, "create_test_copy_of_examples", func() {
		testFolder := test_structure.CopyTerraformFolderToTemp(t, "..", "examples")
		logger.Logf(t, "path to test folder %s\n", testFolder)
		terraformModulePath := filepath.Join(testFolder, "gke-basic-tiller")
		test_structure.SaveString(t, workingDir, "gkeBasicTillerTerraformModulePath", terraformModulePath)
	})

	test_structure.RunTestStage(t, "create_terratest_options", func() {
		gkeBasicTillerTerraformModulePath := test_structure.LoadString(t, workingDir, "gkeBasicTillerTerraformModulePath")
		uniqueID := random.UniqueId()
		project := gcp.GetGoogleProjectIDFromEnvVar(t)
		region := gcp.GetRandomRegion(t, project, nil, nil)
		gkeClusterTerratestOptions := createGKEClusterTerraformOptions(t, uniqueID, project, region, gkeBasicTillerTerraformModulePath)
		test_structure.SaveString(t, workingDir, "uniqueID", uniqueID)
		test_structure.SaveString(t, workingDir, "project", project)
		test_structure.SaveString(t, workingDir, "region", region)
		test_structure.SaveTerraformOptions(t, workingDir, gkeClusterTerratestOptions)
	})

	defer test_structure.RunTestStage(t, "cleanup", func() {
		gkeClusterTerratestOptions := test_structure.LoadTerraformOptions(t, workingDir)
		terraform.Destroy(t, gkeClusterTerratestOptions)
	})

	test_structure.RunTestStage(t, "terraform_apply", func() {
		gkeClusterTerratestOptions := test_structure.LoadTerraformOptions(t, workingDir)
		terraform.InitAndApply(t, gkeClusterTerratestOptions)
	})

	test_structure.RunTestStage(t, "wait_for_workers", func() {
		verifyGkeNodesAreReady(t)
	})

	test_structure.RunTestStage(t, "helm_install", func() {
		// Path to the helm chart we will test
		helmChartPath := "charts/minimal-pod"

		// Setup the kubectl config and context. Here we choose to use the defaults, which is:
		// - HOME/.kube/config for the kubectl config file
		// - Current context of the kubectl config file
		// We also specify that we are working in the default namespace (required to get the Pod)
		kubectlOptions := k8s.NewKubectlOptions("", "")
		kubectlOptions.Namespace = "default"

		// We generate a unique release name so that we can refer to after deployment.
		// By doing so, we can schedule the delete call here so that at the end of the test, we run
		// `helm delete RELEASE_NAME` to clean up any resources that were created.
		releaseName := fmt.Sprintf("nginx-%s", strings.ToLower(random.UniqueId()))

		// Setup the args. For this test, we will set the following input values:
		// - image=nginx:1.15.8
		// - fullnameOverride=minimal-pod-RANDOM_STRING
		// We use a fullnameOverride so we can find the Pod later during verification
		podName := fmt.Sprintf("%s-minimal-pod", releaseName)
		options := &helm.Options{
			SetValues: map[string]string{
				"image":            "nginx:1.15.8",
				"fullnameOverride": podName,
			},
			EnvVars: map[string]string{
				"HELM_TLS_VERIFY": "true",
				"HELM_TLS_ENABLE": "true",
			},
		}

		// Deploy the chart using `helm install`. Note that we use the version without `E`, since we want to assert the
		// install succeeds without any errors.
		helm.Install(t, options, helmChartPath, releaseName)

		// Now that the chart is deployed, verify the deployment. This function will open a tunnel to the Pod and hit the
		// nginx container endpoint.
		verifyNginxPod(t, kubectlOptions, podName)
	})
}

// verifyNginxPod will open a tunnel to the Pod and hit the endpoint to verify the nginx welcome page is shown.
func verifyNginxPod(t *testing.T, kubectlOptions *k8s.KubectlOptions, podName string) {
	// Wait for the pod to come up. It takes some time for the Pod to start, so retry a few times.
	retries := 15
	sleep := 5 * time.Second
	k8s.WaitUntilPodAvailable(t, kubectlOptions, podName, retries, sleep)

	// We will first open a tunnel to the pod, making sure to close it at the end of the test.
	tunnel := k8s.NewTunnel(kubectlOptions, k8s.ResourceTypePod, podName, 0, 80)
	defer tunnel.Close()
	tunnel.ForwardPort(t)

	// ... and now that we have the tunnel, we will verify that we get back a 200 OK with the nginx welcome page.
	// It takes some time for the Pod to start, so retry a few times.
	endpoint := fmt.Sprintf("http://%s", tunnel.Endpoint())
	http_helper.HttpGetWithRetryWithCustomValidation(
		t,
		endpoint,
		retries,
		sleep,
		func(statusCode int, body string) bool {
			return statusCode == 200 && strings.Contains(body, "Welcome to nginx")
		},
	)
}
