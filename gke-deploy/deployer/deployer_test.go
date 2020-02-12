package deployer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	applicationsv1beta1 "github.com/kubernetes-sigs/application/pkg/apis/app/v1beta1"

	"github.com/GoogleCloudPlatform/cloud-builders/gke-deploy/services"
	"github.com/GoogleCloudPlatform/cloud-builders/gke-deploy/testservices"
)

const (
	singleGcsFile      = "gs://bucket/multi-resource.yaml"
	gcsDirectory       = "gs://bucket/directory/*"
	gcsNestedDir       = "gs://bucket/nested-directory"
	dirWithoutWildcard = "gs://bucket/directory"
	gcsOutputBucket    = "gs://out"
	expandedFile       = "gs://out/expanded-resources.yaml"
	suggestedFile      = "gs://out/suggested-resources.yaml"
)

var testOutputDir string

func TestPrepare(t *testing.T) {
	ctx := context.Background()

	image := newImageWithTag(t, "my-image:1.0.0")
	appName := "my-app"
	appVersion := "b2e43cb"
	namespace := "default"
	labels := make(map[string]string)
	annotations := make(map[string]string)

	tests := []struct {
		name string

		image               name.Reference
		appName             string
		appVersion          string
		config              string
		expectedSuggested   string
		expectedExpanded    string
		namespace           string
		labels              map[string]string
		annotations         map[string]string
		exposePort          int
		recursive           bool
		createApplicationCR bool
		applicationLinks    []applicationsv1beta1.Link
	}{{
		name: "Config is directory",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/directory",
		expectedSuggested: "testing/expected-suggested/directory.yaml",
		expectedExpanded:  "testing/expected-expanded/directory.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
	}, {
		name: "Config is a recursive directory",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/nested-directory",
		expectedSuggested: "testing/expected-suggested/nested-directory.yaml",
		expectedExpanded:  "testing/expected-expanded/nested-directory.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
		recursive:         true,
	}, {
		name: "Config is file",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/multi-resource.yaml",
		expectedSuggested: "testing/expected-suggested/multi-resource.yaml",
		expectedExpanded:  "testing/expected-expanded/multi-resource.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
	}, {
		name: "Add custom labels",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/deployment.yaml",
		expectedSuggested: "testing/expected-suggested/custom-labels.yaml",
		expectedExpanded:  "testing/expected-expanded/custom-labels.yaml",
		labels: map[string]string{
			"foo":         "bar",
			"hi":          "bye",
			"a/b/c.d.f.g": "h/i/j.k.l.m",
		},
		annotations: annotations,
		namespace:   namespace,
		exposePort:  0,
	}, {
		name: "Add custom annotations",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/deployment.yaml",
		expectedSuggested: "testing/expected-suggested/custom-annotations.yaml",
		expectedExpanded:  "testing/expected-expanded/custom-annotations.yaml",
		labels:            labels,
		annotations: map[string]string{
			"foo":         "bar",
			"hi":          "bye",
			"a/b/c.d.f.g": "h/i/j.k.l.m",
		},
		namespace:  namespace,
		exposePort: 0,
	}, {
		name: "AppName and AppVersion not set",

		image:             image,
		appName:           "",
		appVersion:        "",
		config:            "testing/configs/deployment.yaml",
		expectedSuggested: "testing/expected-suggested/missing-app-name-and-version.yaml",
		expectedExpanded:  "testing/expected-expanded/missing-app-name-and-version.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
	}, {
		name: "Namespace is not default",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/deployment.yaml",
		expectedSuggested: "testing/expected-suggested/foobar-namespace.yaml",
		expectedExpanded:  "testing/expected-expanded/foobar-namespace.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         "foobar",
		exposePort:        0,
	}, {
		name: "Wait for service object to be ready",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/service.yaml",
		expectedSuggested: "testing/expected-suggested/service.yaml",
		expectedExpanded:  "testing/expected-expanded/service.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
	}, {
		name: "No config arg",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "",
		expectedSuggested: "testing/expected-suggested/no-config.yaml",
		expectedExpanded:  "testing/expected-expanded/no-config.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
	}, {
		name: "Expose application",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/deployment.yaml",
		expectedSuggested: "testing/expected-suggested/exposed-application.yaml",
		expectedExpanded:  "testing/expected-expanded/exposed-application.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        80,
	}, {
		name: "Namespace is empty",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/deployment.yaml",
		expectedSuggested: "testing/expected-suggested/empty-namespace.yaml",
		expectedExpanded:  "testing/expected-expanded/empty-namespace.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         "",
		exposePort:        0,
	}, {
		name: "Create Application CR",

		image:               image,
		appName:             appName,
		appVersion:          "",
		config:              "testing/configs/deployment-and-service",
		expectedSuggested:   "testing/expected-suggested/create-application-cr.yaml",
		expectedExpanded:    "testing/expected-expanded/create-application-cr.yaml",
		labels:              labels,
		annotations:         annotations,
		namespace:           namespace,
		exposePort:          0,
		createApplicationCR: true,
	}, {
		name: "Create Application CR with version",

		image:               image,
		appName:             appName,
		appVersion:          appVersion,
		config:              "testing/configs/deployment-and-service",
		expectedSuggested:   "testing/expected-suggested/create-application-cr-with-version.yaml",
		expectedExpanded:    "testing/expected-expanded/create-application-cr-with-version.yaml",
		labels:              labels,
		annotations:         annotations,
		namespace:           namespace,
		exposePort:          0,
		createApplicationCR: true,
	}, {
		name: "Create Application CR with links",

		image:               image,
		appName:             appName,
		appVersion:          "",
		config:              "testing/configs/deployment-and-service",
		expectedSuggested:   "testing/expected-suggested/create-application-cr-with-links.yaml",
		expectedExpanded:    "testing/expected-expanded/create-application-cr-with-links.yaml",
		labels:              labels,
		annotations:         annotations,
		namespace:           namespace,
		exposePort:          0,
		createApplicationCR: true,
		applicationLinks: []applicationsv1beta1.Link{
			{
				Description: "My description 1",
				URL:         "https://foo.com/bar",
			},
			{
				Description: "My description 2",
				URL:         "https://asdf.com",
			},
		},
	}, {
		name: "Application CR already exists",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/directory-with-application",
		expectedSuggested: "testing/expected-suggested/existing-application-cr.yaml",
		expectedExpanded:  "testing/expected-expanded/existing-application-cr.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
	}, {
		name: "Add links to existing Application CR",

		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            "testing/configs/directory-with-application",
		expectedSuggested: "testing/expected-suggested/existing-application-cr-with-links.yaml",
		expectedExpanded:  "testing/expected-expanded/existing-application-cr-with-links.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
		applicationLinks: []applicationsv1beta1.Link{
			{
				Description: "My description 1",
				URL:         "https://foo.com/bar",
			},
			{
				Description: "My description 2",
				URL:         "https://asdf.com",
			},
		},
	}, {
		name:              "Single config file in GCS",
		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            singleGcsFile,
		expectedSuggested: "testing/expected-suggested/multi-resource.yaml",
		expectedExpanded:  "testing/expected-expanded/multi-resource.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
	}, {
		name:              "Config files in a GCS directory",
		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            gcsDirectory,
		expectedSuggested: "testing/expected-suggested/directory.yaml",
		expectedExpanded:  "testing/expected-expanded/directory.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
	}, {
		name:              "Config files in a nested GCS directory",
		image:             image,
		appName:           appName,
		appVersion:        appVersion,
		config:            gcsNestedDir,
		expectedSuggested: "testing/expected-suggested/nested-directory.yaml",
		expectedExpanded:  "testing/expected-expanded/nested-directory.yaml",
		labels:            labels,
		annotations:       annotations,
		namespace:         namespace,
		exposePort:        0,
		recursive:         true,
	}}

	gcs := buildTestGcsService(t)
	remote := testservices.TestRemote{
		ImageResp: &testservices.TestImage{
			Hash: v1.Hash{
				Algorithm: "sha256",
				Hex:       "foobar",
			},
			Err: nil,
		},
		ImageErr: nil,
	}
	oss, err := services.NewOS(ctx)
	if err != nil {
		t.Fatalf("Failed to create os: %v", err)
	}
	d := Deployer{Clients: &services.Clients{OS: oss, Remote: &remote, GCS: gcs}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			dir, err := ioutil.TempDir("/tmp", "gke-deploy_deploy_test")
			if err != nil {
				t.Fatalf("Failed to create tmp directory: %v", err)
			}
			defer os.RemoveAll(dir)

			suggestedDir, err := ioutil.TempDir("/tmp", "gke-deploy_deploy_test_suggested")
			if err != nil {
				t.Fatalf("Failed to create tmp directory: %v", err)
			}
			defer os.RemoveAll(suggestedDir)

			expandedDir, err := ioutil.TempDir("/tmp", "gke-deploy_deploy_test_expected")
			if err != nil {
				t.Fatalf("Failed to create tmp directory: %v", err)
			}
			defer os.RemoveAll(expandedDir)

			if err := d.Prepare(ctx, tc.image, tc.appName, tc.appVersion, tc.config, suggestedDir, expandedDir, tc.namespace, tc.labels, tc.annotations, tc.exposePort, tc.recursive, tc.createApplicationCR, tc.applicationLinks); err != nil {
				t.Fatalf("Prepare(ctx, %v, %s, %s, %s, %s, %s, %s, %s, %v, %v, %t, %v) = %v; want <nil>", tc.image, tc.appName, tc.appVersion, tc.config, suggestedDir, expandedDir, tc.namespace, tc.labels, tc.annotations, tc.recursive, tc.createApplicationCR, tc.applicationLinks, err)
			}

			err = compareFiles(tc.expectedExpanded, expandedDir)
			if err != nil {
				t.Fatalf("Failure with expanded file generation: %v", err)
			}

			err = compareFiles(tc.expectedSuggested, suggestedDir)
			if err != nil {
				t.Fatalf("Failure with suggested file generation: %v", err)
			}
		})
	}

	t.Run("Output to GCS", func(t *testing.T) {

		testOutputDir, err = ioutil.TempDir("", "gke-deploy_deploy_test_output")
		config := "testing/configs/multi-resource.yaml"

		if err != nil {
			t.Fatalf("Failed to create tmp directory: %v", err)
		}
		defer os.RemoveAll(testOutputDir)

		if err := d.Prepare(ctx, image, appName, appVersion, config, gcsOutputBucket, gcsOutputBucket, namespace, labels, annotations, 0, false, false, nil); err != nil {
			t.Fatalf("Prepare(ctx, %v, %s, %s, %s, %s, %s, %s, %s, %v, %v, %t, %v) = %v; want <nil>", image, appName, appVersion, config, gcsOutputBucket, gcsOutputBucket, namespace, labels, annotations, false, false, nil, err)
		}

		suggestedDir, err := ioutil.TempDir("/tmp", "gke-deploy_deploy_test_suggested")
		if err != nil {
			t.Fatalf("Failed to create tmp directory: %v", err)
		}
		defer os.RemoveAll(suggestedDir)
		err = gcs.Copy(ctx, suggestedFile, suggestedDir, false)
		if err != nil {
			t.Fatalf("Failed to create tmp directory: %v", err)
		}

		expandedDir, err := ioutil.TempDir("/tmp", "gke-deploy_deploy_test_expected")
		if err != nil {
			t.Fatalf("Failed to create tmp directory: %v", err)
		}
		defer os.RemoveAll(expandedDir)
		err = gcs.Copy(ctx, expandedFile, expandedDir, false)
		if err != nil {
			t.Fatalf("Failed to create tmp directory: %v", err)
		}

		expectedSuggested := "testing/expected-suggested/multi-resource.yaml"
		err = compareFiles(expectedSuggested, suggestedDir)
		if err != nil {
			t.Fatalf("Failure with suggested file generation: %v", err)
		}

		expectedExpanded := "testing/expected-expanded/multi-resource.yaml"
		err = compareFiles(expectedExpanded, expandedDir)
		if err != nil {
			t.Fatalf("Failure with expanded file generation: %v", err)
		}

	})

}

func TestPrepareErrors(t *testing.T) {
	ctx := context.Background()

	image := newImageWithTag(t, "my-image:1.0.0")
	appName := "my-app"
	appVersion := "b2e43cb"
	namespace := "default"
	labels := make(map[string]string)
	annotations := make(map[string]string)

	tests := []struct {
		name string

		image               name.Reference
		appName             string
		appVersion          string
		config              string
		extraDirs           string
		suggestedDir        string
		namespace           string
		labels              map[string]string
		annotations         map[string]string
		remote              services.RemoteService
		recursive           bool
		createApplicationCR bool
		applicationLinks    []applicationsv1beta1.Link

		want string
	}{{
		name: "Failed to parse resources",

		image:       image,
		appName:     appName,
		appVersion:  appVersion,
		config:      "testing/configs/empty-directory",
		labels:      labels,
		annotations: annotations,
		namespace:   namespace,
		want:        "has no \".yaml\" or \".yml\" files to parse",
	}, {
		name: "Failed to get image digest",

		image:       image,
		appName:     appName,
		appVersion:  appVersion,
		config:      "testing/configs/deployment.yaml",
		labels:      labels,
		annotations: annotations,
		namespace:   namespace,

		remote: &testservices.TestRemote{
			ImageResp: nil,
			ImageErr:  fmt.Errorf("failed to get remote image"),
		},

		want: "failed to get remote image",
	}, {
		name: "Failed to save configs",

		image:        image,
		appName:      appName,
		appVersion:   appVersion,
		config:       "testing/configs/deployment.yaml",
		suggestedDir: "testing/configs/deployment.yaml",
		labels:       labels,
		annotations:  annotations,
		namespace:    namespace,
		want:         "output directory \"testing/configs/deployment.yaml\" exists as a file",
	}, {
		name: "Cannot set app.kubernetes.io/name label via custom labels",

		image:      image,
		appName:    appName,
		appVersion: appVersion,
		config:     "testing/configs/deployment.yaml",
		labels: map[string]string{
			"app.kubernetes.io/name": "foobar",
		},
		annotations: annotations,
		namespace:   namespace,
		want:        "app.kubernetes.io/name label must be set using the --app|-a flag",
	}, {
		name: "Cannot set app.kubernetes.io/version label via custom labels",

		image:      image,
		appName:    appName,
		appVersion: appVersion,
		config:     "testing/configs/deployment.yaml",
		labels: map[string]string{
			"app.kubernetes.io/version": "foobar",
		},
		annotations: annotations,
		namespace:   namespace,
		want:        "app.kubernetes.io/version label must be set using the --version|-v flag",
	}, {
		name: "Cannot set app.kubernetes.io/managed-by label via custom labels",

		image:      image,
		appName:    appName,
		appVersion: appVersion,
		config:     "testing/configs/deployment.yaml",
		labels: map[string]string{
			"app.kubernetes.io/managed-by": "foobar",
		},
		annotations: annotations,
		namespace:   namespace,
		want:        "app.kubernetes.io/managed-by label cannot be explicitly set",
	}, {
		name:        "GCS path is a directory but recursive flag is false",
		image:       image,
		appName:     appName,
		appVersion:  appVersion,
		config:      dirWithoutWildcard,
		labels:      labels,
		annotations: annotations,
		namespace:   namespace,
		want:        "failed to download configuration files",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oss, err := services.NewOS(ctx)
			if err != nil {
				t.Fatalf("Failed to create os: %v", err)
			}

			var remote services.RemoteService
			if tc.remote != nil {
				remote = tc.remote
			} else {
				remote = &testservices.TestRemote{
					ImageResp: &testservices.TestImage{
						Hash: v1.Hash{
							Algorithm: "sha256",
							Hex:       "foobar",
						},
						Err: nil,
					},
					ImageErr: nil,
				}
			}

			dir, err := ioutil.TempDir("/tmp", "gke-deploy_deploy_test")
			if err != nil {
				t.Fatalf("Failed to create tmp directory: %v", err)
			}
			defer os.RemoveAll(dir)

			suggestedDir := tc.suggestedDir
			if suggestedDir == "" {
				suggestedDir, err = ioutil.TempDir("/tmp", "gke-deploy_deploy_test_suggested")
				if err != nil {
					t.Fatalf("Failed to create tmp directory: %v", err)
				}
				defer os.RemoveAll(suggestedDir)
			}

			expandedDir, err := ioutil.TempDir("/tmp", "gke-deploy_deploy_test_expected")
			if err != nil {
				t.Fatalf("Failed to create tmp directory: %v", err)
			}
			defer os.RemoveAll(expandedDir)

			gcs := buildTestGcsService(t)
			d := Deployer{Clients: &services.Clients{OS: oss, Remote: remote, GCS: gcs}}

			var prepareErr error
			if prepareErr = d.Prepare(ctx, tc.image, tc.appName, tc.appVersion, tc.config, suggestedDir, expandedDir, tc.namespace, tc.labels, tc.annotations, 0, tc.recursive, tc.createApplicationCR, tc.applicationLinks); prepareErr == nil {
				t.Fatalf("Prepare(ctx, %v, %s, %s, %s, %s, %s, %s, %s, %v, %v, %t, %v) = <nil>; want error", tc.image, tc.appName, tc.appVersion, tc.config, suggestedDir, expandedDir, tc.namespace, tc.labels, tc.annotations, tc.recursive, tc.createApplicationCR, tc.applicationLinks)
			}

			if tc.want == "" {
				t.Fatalf("No error substring provided")
			}
			if !strings.Contains(prepareErr.Error(), tc.want) {
				t.Fatalf("Unexpected error: got \"%v\", want substring %s", prepareErr, tc.want)
			}
		})
	}
}

func TestApply(t *testing.T) {
	ctx := context.Background()

	testDeploymentFile := "testing/deployment.yaml"
	testServiceFile := "testing/service.yaml"
	testDeploymentReadyFile := "testing/deployment-ready.yaml"
	testDeployment2ReadyFile := "testing/deployment-2-ready.yaml"
	testServiceUnreadyFile := "testing/service-unready.yaml"
	testServiceReadyFile := "testing/service-ready.yaml"
	testNamespaceFile := "testing/namespace.yaml"
	testNamespaceReadyFile := "testing/namespace-ready.yaml"
	testNamespaceReady2File := "testing/namespace-ready-2.yaml"

	clusterName := "test-cluster"
	clusterLocation := "us-east1-b"
	clusterProject := "my-project"
	namespace := "default"
	waitTimeout := 10 * time.Second

	gcs := buildTestGcsService(t)

	tests := []struct {
		name string

		clusterName     string
		clusterLocation string
		config          string
		namespace       string
		labels          map[string]string
		waitTimeout     time.Duration
		gcloud          services.GcloudService
		kubectl         testservices.TestKubectl
		gcs             services.GcsService
		recursive       bool
	}{{
		name: "Config is directory",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/directory",
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, "testing/deployment-2.yaml")): {nil},
				string(fileContents(t, testDeploymentFile)):          {nil},
				string(fileContents(t, testServiceFile)):             {nil, nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
					"test-app-2": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeployment2ReadyFile)),
							Err: nil,
						},
					},
				},
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						}, {
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
	}, {
		name: "Config is nested directory",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/nested-directory",
		namespace:       namespace,
		waitTimeout:     waitTimeout,
		recursive:       true,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil, nil},
				string(fileContents(t, testServiceFile)):    {nil, nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						}, {
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						}, {
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
	}, {
		name: "Config is file",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/multi-resource.yaml",
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil},
				string(fileContents(t, testServiceFile)):    {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
	}, {
		name: "Namespace is not default",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/deployment-and-namespace",
		namespace:       "foobar",
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Namespace": {
					"foobar": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testNamespaceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
	}, {
		name: "Wait for service object to be ready",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/service.yaml",
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testServiceFile)): {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceUnreadyFile)),
							Err: nil,
						}, {
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
	}, {
		name: "Multiple namespace configs",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/multiple-namespaces",
		namespace:       "foobar",
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Namespace": {
					"foobar": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testNamespaceReadyFile)),
							Err: nil,
						},
					},
					"foobar-2": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testNamespaceReady2File)),
							Err: nil,
						},
					},
				},
			},
		},
	}, {
		name: "No cluster name and location",

		clusterName:     "",
		clusterLocation: "",
		config:          "testing/configs/multi-resource.yaml",
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil},
				string(fileContents(t, testServiceFile)):    {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
	}, {
		name: "Namespace is empty",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/deployment-and-namespace",
		namespace:       "",
		waitTimeout:     waitTimeout,

		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Namespace": {
					"foobar": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testNamespaceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
	}, {
		name: "Namespace needs to be created",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/deployment-and-namespace",
		namespace:       "",
		waitTimeout:     waitTimeout,

		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil},
				string(fileContents(t, testNamespaceFile)):  {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Namespace": {
					"foobar": []testservices.GetResponse{
						{
							Res: "",
							Err: nil,
						},
					},
				},
			},
		},
		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
	}, {
		name: "Config files in a GCS directory ",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          gcsDirectory,
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, "testing/deployment-2.yaml")): {nil},
				string(fileContents(t, testDeploymentFile)):          {nil},
				string(fileContents(t, testServiceFile)):             {nil, nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
					"test-app-2": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeployment2ReadyFile)),
							Err: nil,
						},
					},
				},
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						}, {
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
		gcs: gcs,
	}, {
		name: "Config files in a nested GCS directory",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          gcsNestedDir,
		namespace:       namespace,
		waitTimeout:     waitTimeout,
		recursive:       true,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil, nil},
				string(fileContents(t, testServiceFile)):    {nil, nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						}, {
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						}, {
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
		gcs: gcs,
	}, {
		name: "Single config file in GCS",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          singleGcsFile,
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {nil},
				string(fileContents(t, testServiceFile)):    {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Deployment": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testDeploymentReadyFile)),
							Err: nil,
						},
					},
				},
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceReadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
		gcs: gcs,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Deployer{
				Clients: &services.Clients{
					Kubectl: &tc.kubectl,
					Gcloud:  tc.gcloud,
					OS:      &services.OS{},
					GCS:     tc.gcs,
				},
			}

			if err := d.Apply(ctx, tc.clusterName, tc.clusterLocation, clusterProject, tc.config, tc.namespace, tc.waitTimeout, tc.recursive); err != nil {
				t.Fatalf("Apply(ctx, %s, %s, %s, %s, %v, %v) = %v; want <nil>", tc.clusterName, tc.clusterLocation, tc.config, tc.namespace, tc.waitTimeout, tc.recursive, err)
			}

			// Verify that all expected applies were actually applied
			if len(tc.kubectl.ApplyFromStringResponse) != 0 {
				t.Fatalf("Apply(ctx, %s, %s, %s, %s, %v, %v) did not apply all of the expected configs. got %v; want []", tc.clusterName, tc.clusterLocation, tc.config, tc.namespace, tc.waitTimeout, tc.recursive, tc.kubectl.ApplyFromStringResponse)
			}

			// Verify that all expected gets were executed
			if len(tc.kubectl.GetResponse) != 0 {
				t.Fatalf("Apply(ctx, %s, %s, %s, %s, %v, %v) did not get all of the expected configs. got %v; want []", tc.clusterName, tc.clusterLocation, tc.config, tc.namespace, tc.waitTimeout, tc.recursive, tc.kubectl.GetResponse)
			}
		})
	}
}

func TestApplyErrors(t *testing.T) {
	ctx := context.Background()

	testDeploymentFile := "testing/deployment.yaml"
	testServiceFile := "testing/service.yaml"
	testServiceUnreadyFile := "testing/service-unready.yaml"
	testNamespaceFile := "testing/namespace.yaml"
	namespace := "default"
	waitTimeout := 10 * time.Second
	clusterName := "test-cluster"
	clusterLocation := "us-east1-b"
	clusterProject := "my-project"
	configDir := "path/to/config"

	gcs := buildTestGcsService(t)

	tests := []struct {
		name string

		clusterName     string
		clusterLocation string
		config          string
		namespace       string
		waitTimeout     time.Duration
		gcloud          services.GcloudService
		kubectl         testservices.TestKubectl
		gcs             services.GcsService
		recursive       bool

		want string
	}{{
		name: "Failed to parse resources",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/empty-directory",
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		want: "directory \"testing/configs/empty-directory\" has no \".yaml\" or \".yml\" files to parse",
	}, {
		name: "Failed to get deploy namespace to cluster",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/deployment-and-namespace",
		namespace:       "foobar",
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testNamespaceFile)): {fmt.Errorf("failed to apply kubernetes manifests to cluster")},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Namespace": {
					"foobar": []testservices.GetResponse{
						{
							Res: "",
							Err: nil,
						},
					},
				},
			},
		},
		want: "failed to apply Namespace configuration file with name \"foobar\" to cluster: failed to apply config from string: failed to apply kubernetes manifests to cluster",
	}, {
		name: "Failed to deploy resources to cluster",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/deployment.yaml",
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testDeploymentFile)): {fmt.Errorf("failed to apply kubernetes manifests to cluster")},
			},
		},
		want: "failed to apply Deployment configuration file with name \"test-app\" to cluster: failed to apply config from string",
	}, {
		name: "Wait timeout",

		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          "testing/configs/service.yaml",
		namespace:       namespace,
		waitTimeout:     0 * time.Minute,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		kubectl: testservices.TestKubectl{
			ApplyFromStringResponse: map[string][]error{
				string(fileContents(t, testServiceFile)): {nil},
			},
			GetResponse: map[string]map[string][]testservices.GetResponse{
				"Service": {
					"test-app": []testservices.GetResponse{
						{
							Res: string(fileContents(t, testServiceUnreadyFile)),
							Err: nil,
						},
					},
				},
			},
		},
		want: "timed out after 0s while waiting for deployed objects to be ready",
	}, {
		name: "clusterName is provided but clusterLocation is not",

		clusterName:     clusterName,
		clusterLocation: "",
		config:          configDir,
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},

		want: "clusterName and clusterLocation either must both be provided, or neither should be provided",
	}, {
		name: "clusterLocation is provided but clusterName is not",

		clusterName:     "",
		clusterLocation: clusterLocation,
		config:          configDir,
		namespace:       namespace,
		waitTimeout:     waitTimeout,

		gcloud: &testservices.TestGcloud{
			ContainerClustersGetCredentialsErr: nil,
		},
		want: "clusterName and clusterLocation either must both be provided, or neither should be provided",
	}, {
		name:            "GCS path is a directory but recursive flag is false",
		clusterName:     clusterName,
		clusterLocation: clusterLocation,
		config:          dirWithoutWildcard,
		namespace:       namespace,
		waitTimeout:     waitTimeout,
		want:            "failed to download configuration files",
		gcs:             gcs,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Deployer{
				Clients: &services.Clients{
					Kubectl: &tc.kubectl,
					Gcloud:  tc.gcloud,
					OS:      &services.OS{},
					GCS:     tc.gcs,
				},
			}

			var applyErr error
			if applyErr = d.Apply(ctx, tc.clusterName, tc.clusterLocation, clusterProject, tc.config, tc.namespace, tc.waitTimeout, tc.recursive); applyErr == nil {
				t.Errorf("Apply(ctx, %s, %s, %s, %s, %v, %v) = <nil>; want error", tc.clusterName, tc.clusterLocation, tc.config, tc.namespace, tc.waitTimeout, tc.recursive)
			}

			// Verify that all expected applies were actually applied
			if len(tc.kubectl.ApplyFromStringResponse) != 0 {
				t.Fatalf("Apply(ctx, %s, %s, %s, %s, %v, %v) did not apply all of the expected configs. got %v; want []", tc.clusterName, tc.clusterLocation, tc.config, tc.namespace, tc.waitTimeout, tc.recursive, tc.kubectl.ApplyFromStringResponse)
			}

			// Verify that all expected gets were executed
			if len(tc.kubectl.GetResponse) != 0 {
				t.Fatalf("Apply(ctx, %s, %s, %s, %s, %v, %v) did not get all of the expected configs. got %v; want []", tc.clusterName, tc.clusterLocation, tc.config, tc.namespace, tc.waitTimeout, tc.recursive, tc.kubectl.GetResponse)
			}

			if tc.want == "" {
				t.Fatalf("No error substring provided")
			}
			if !strings.Contains(applyErr.Error(), tc.want) {
				t.Fatalf("Unexpected error: got \"%v\", want substring %s", applyErr, tc.want)
			}
		})
	}
}

func fileContents(t *testing.T, filename string) []byte {
	contents, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Fatalf("failed to read file %s", filename)
	}
	return contents
}

func newImageWithTag(t *testing.T, image string) name.Reference {
	ref, err := name.NewTag(image)
	if err != nil {
		t.Fatalf("failed to create image with tag: %v", err)
	}
	return ref
}

func compareFiles(expectedFile, actualDirectory string) error {
	actualFiles, err := ioutil.ReadDir(actualDirectory)
	if err != nil {
		return fmt.Errorf("failed to read directory: %v", actualDirectory)
	}

	if len(actualFiles) != 1 {
		return fmt.Errorf("incorrect number of k8s files created in %s: %v", actualDirectory, len(actualFiles))
	}

	path := filepath.Join(actualDirectory, actualFiles[0].Name())
	actualContents, err := ioutil.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read actual output file: %v", path)
	}

	expectedContents, err := ioutil.ReadFile(expectedFile)
	if err != nil {
		return fmt.Errorf("failed to read expected file: %v", expectedFile)
	}

	if diff := cmp.Diff(expectedContents, actualContents); diff != "" {
		return fmt.Errorf("produced diff on files (-want +got):\n%s", diff)
	}
	return nil
}

func buildTestGcsService(t *testing.T) *testservices.TestGcsService {
	t.Helper()

	gsutil, err := services.NewGsutil(context.Background(), false)
	if err != nil {
		log.Fatal(err)
	}

	return &testservices.TestGcsService{CopyResponse: map[string]func(src, dst string, recursive bool) error{
		singleGcsFile: func(src, dst string, recursive bool) error {
			return gsutil.Copy(context.Background(), "testing/configs/multi-resource.yaml", dst, recursive)
		},
		gcsDirectory: func(src, dst string, recursive bool) error {
			return gsutil.Copy(context.Background(), "testing/configs/directory/*", dst, recursive)
		},
		gcsNestedDir: func(src, dst string, recursive bool) error {
			return gsutil.Copy(context.Background(), "testing/configs/nested-directory", dst, recursive)
		},
		dirWithoutWildcard: func(src, dst string, recursive bool) error {

			return errors.New("failed to download configuration files")
		},
		expandedFile: func(src, dst string, recursive bool) error {
			if strings.HasPrefix(src, gcsOutputBucket) {
				return gsutil.Copy(context.Background(), strings.Join([]string{testOutputDir, "expanded-resources.yaml"}, "/"), dst, recursive)
			}
			return gsutil.Copy(context.Background(), src, strings.Join([]string{testOutputDir, "expanded-resources.yaml"}, "/"), recursive)
		},
		suggestedFile: func(src, dst string, recursive bool) error {
			if strings.HasPrefix(src, gcsOutputBucket) {
				return gsutil.Copy(context.Background(), strings.Join([]string{testOutputDir, "suggested-resources.yaml"}, "/"), dst, recursive)
			}
			return gsutil.Copy(context.Background(), src, strings.Join([]string{testOutputDir, "suggested-resources.yaml"}, "/"), recursive)

		},
	},
	}

}

func copyFile(src, dst, name string) {
	from, err := os.Open(src)
	if err != nil {
		log.Fatal(err)
	}
	defer from.Close()

	to, err := os.OpenFile(strings.Join([]string{dst, name}, "/"), os.O_RDWR|os.O_CREATE, 0777)
	if err != nil {
		log.Fatal(err)
	}
	defer to.Close()

	_, err = io.Copy(to, from)
	if err != nil {
		log.Fatal(err)
	}
}
