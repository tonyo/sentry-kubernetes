package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/getsentry/sentry-go"
)

const instanceMetadataUrl = "http://metadata.google.internal/computeMetadata/v1/instance/attributes/?recursive=true"
const projectMetadataUrl = "http://metadata.google.internal/computeMetadata/v1/project/?recursive=true"

type IntegrationGKE struct{}

type InstanceMetadata struct {
	// Allow both types of casing for compatibility
	ClusterName1     string `json:"cluster-name"`
	ClusterName2     string `json:"clusterName"`
	ClusterLocation1 string `json:"cluster-location"`
	ClusterLocation2 string `json:"clusterLocation"`
}

type ProjectMetadata struct {
	ProjectId1        string `json:"project-id"`
	ProjectId2        string `json:"projectId"`
	NumericProjectId1 int    `json:"numeric-project-id"`
	NumericProjectId2 int    `json:"numericProjectId"`
}

func (im *InstanceMetadata) ClusterName() string {
	if im.ClusterName1 != "" {
		return im.ClusterName1
	}
	return im.ClusterName2
}

func (im *InstanceMetadata) ClusterLocation() string {
	if im.ClusterLocation1 != "" {
		return im.ClusterLocation1
	}
	return im.ClusterLocation2
}

func (pm *ProjectMetadata) ProjectId() string {
	if pm.ProjectId1 != "" {
		return pm.ProjectId1
	}
	return pm.ProjectId2
}

func (pm *ProjectMetadata) NumericProjectId() int {
	if pm.NumericProjectId1 != 0 {
		return pm.NumericProjectId1
	}
	return pm.NumericProjectId2
}

func getClusterUrl(clusterLocation string, clusterName string, projectId string) string {
	if clusterLocation == "" || clusterName == "" || projectId == "" {
		return ""
	}
	return fmt.Sprintf(
		"https://console.cloud.google.com/kubernetes/clusters/details/%s/%s/details?project=%s",
		clusterLocation,
		clusterName,
		projectId,
	)
}

func readGoogleMetadata(url string, output interface{}) error {
	client := http.Client{}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header = http.Header{
		"Metadata-Flavor": {"Google"},
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot fetch metadata: %v", err)
	}
	defer resp.Body.Close()

	if err = json.NewDecoder(resp.Body).Decode(output); err != nil {
		return fmt.Errorf("cannot decode metadata: %v", err)
	}
	return nil
}

func (igke *IntegrationGKE) IsEnabled() bool {
	return isTruthy(os.Getenv("SENTRY_K8S_INTEGRATION_GKE_ENABLED"))
}

func (igke *IntegrationGKE) Apply() {
	_, logger := getLoggerWithTag(context.Background(), "integration", "gke")
	logger.Info().Msg("Running GKE integration")

	// Instance metadata
	var instanceMeta InstanceMetadata
	err := readGoogleMetadata(instanceMetadataUrl, &instanceMeta)
	if err != nil {
		logger.Error().Msgf("Error running GKE integration: %v", err)
		return
	}

	scope := sentry.CurrentHub().Scope()

	clusterName := instanceMeta.ClusterName()
	setTagIfNotEmpty(scope, "gke_cluster_name", clusterName)
	clusterLocation := instanceMeta.ClusterLocation()
	setTagIfNotEmpty(scope, "gke_cluster_location", clusterLocation)

	// Project metadata
	var projectMeta ProjectMetadata
	err = readGoogleMetadata(projectMetadataUrl, &projectMeta)
	if err != nil {
		logger.Error().Msgf("Error running GKE integration: %v", err)
		return
	}

	projectName := projectMeta.ProjectId()
	setTagIfNotEmpty(scope, "gke_project_name", projectName)

	gkeContext := map[string]interface{}{
		"Cluster name":     clusterName,
		"Cluster location": clusterLocation,
		"GCP project":      projectName,
	}

	clusterUrl := getClusterUrl(clusterLocation, clusterName, projectName)
	if clusterUrl != "" {
		gkeContext["Cluster URL"] = clusterUrl
	}

	logger.Info().Msgf("GKE Context discovered: %v", gkeContext)

	scope.SetContext(
		"Google Kubernetes Engine",
		gkeContext,
	)
}

func (igke *IntegrationGKE) GetLinkToPodLogs(podName string, namespace string) string {
	projectName := ""
	clusterName := ""
	clusterLocation := ""
	link := ("https://console.cloud.google.com/logs/query;query=" +
		"resource.type%3D%22k8s_container%22%0A" +
		fmt.Sprintf("resource.labels.project_id%3D%22%s%22%0A", projectName) +
		fmt.Sprintf("resource.labels.location%3D%22%s%22%0A", clusterLocation) +
		fmt.Sprintf("resource.labels.cluster_name%3D%22%s%22%0A", clusterName) +
		fmt.Sprintf("resource.labels.namespace_name%3D%22%s%22%0A", namespace) +
		fmt.Sprintf("resource.labels.pod_name%3D%22%s%22%0A", podName) +
		";duration=PT1H?project=internal-sentry")
	return link
}
