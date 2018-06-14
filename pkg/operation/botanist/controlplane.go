// Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package botanist

import (
	"fmt"
	"path/filepath"

	"github.com/gardener/gardener/pkg/operation/common"
	"github.com/gardener/gardener/pkg/operation/terraformer"
	"github.com/gardener/gardener/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeployNamespace creates a namespace in the Seed cluster which is used to deploy all the control plane
// components for the Shoot cluster. Moreover, the cloud provider configuration and all the secrets will be
// stored as ConfigMaps/Secrets.
func (b *Botanist) DeployNamespace() error {
	namespace, err := b.K8sSeedClient.CreateNamespace(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: b.Shoot.SeedNamespace,
			Labels: map[string]string{
				common.GardenRole: common.GardenRoleShoot,
			},
		},
	}, true)
	if err != nil {
		return err
	}
	b.SeedNamespaceObject = namespace
	return nil
}

// DeployNetworkPolicy deploys the network policy from the Helm chart to isolate control planes from each
// others and pods within the control plane.
func (b *Botanist) DeployNetworkPolicy() error {
	return b.ApplyChartSeed(filepath.Join(common.ChartPath, "seed-controlplane", "charts", "network-policy"), "network-policy", b.Shoot.SeedNamespace, nil, nil)
}

// DeployBackupNamespaceFromShoot creates a namespace in the Seed cluster from info in shoot object, which is used to deploy all the backup infrastructure
// realted resources for shoot cluster. Moreover, the terraform configuration and all the secrets will be
// stored as ConfigMaps/Secrets.
func (b *Botanist) DeployBackupNamespaceFromShoot() error {
	_, err := b.K8sSeedClient.CreateNamespace(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: common.GenerateBackupNamespaceName(common.GenerateBackupInfrastructureName(b.Shoot.SeedNamespace, b.Shoot.Info.Status.UID)),
			Labels: map[string]string{
				common.GardenRole: common.GardenRoleBackup,
			},
		},
	}, true)
	return err
}

// DeployBackupNamespaceFromBackupInfrastructure creates a namespace in the Seed cluster from info in shoot object, which is used to deploy all the backup infrastructure
// realted resources for shoot cluster. Moreover, the terraform configuration and all the secrets will be
// stored as ConfigMaps/Secrets.
func (b *Botanist) DeployBackupNamespaceFromBackupInfrastructure() error {
	_, err := b.K8sSeedClient.CreateNamespace(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: common.GenerateBackupNamespaceName(b.BackupInfrastructure.Name),
			Labels: map[string]string{
				common.GardenRole: common.GardenRoleBackup,
			},
		},
	}, true)
	return err
}

// DeleteNamespace deletes the namespace in the Seed cluster which holds the control plane components. The built-in
// garbage collection in Kubernetes will automatically delete all resources which belong to this namespace. This
// comprises volumes and load balancers as well.
func (b *Botanist) DeleteNamespace() error {
	err := b.K8sSeedClient.DeleteNamespace(b.Shoot.SeedNamespace)
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// DeleteBackupNamespace deletes the namespace in the Seed cluster which holds the backup infrastructure state. The built-in
// garbage collection in Kubernetes will automatically delete all resources which belong to this namespace.
func (b *Botanist) DeleteBackupNamespace() error {
	err := b.K8sSeedClient.DeleteNamespace(common.GenerateBackupNamespaceName(b.BackupInfrastructure.Name))
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// DeployKubeAPIServerService creates a Service of type 'LoadBalancer' in the Seed cluster which is used to expose the
// kube-apiserver deployment (of the Shoot cluster). It waits until the load balancer is available and stores the address
// on the Botanist's APIServerAddress attribute.
func (b *Botanist) DeployKubeAPIServerService() error {
	return b.ApplyChartSeed(filepath.Join(common.ChartPath, "seed-controlplane", "charts", "kube-apiserver-service"), "kube-apiserver-service", b.Shoot.SeedNamespace, nil, map[string]interface{}{
		"cloudProvider": b.Seed.CloudProvider,
	})
}

// RefreshKubeAPIServerChecksums updates the cloud provider checksum in the kube-apiserver pod spec template.
func (b *Botanist) RefreshKubeAPIServerChecksums() error {
	return b.patchDeploymentCloudProviderChecksum(common.KubeAPIServerDeploymentName)
}

// DeleteKubeAPIServer deletes the kube-apiserver deployment in the Seed cluster which holds the Shoot's control plane.
func (b *Botanist) DeleteKubeAPIServer() error {
	err := b.K8sSeedClient.DeleteDeployment(b.Shoot.SeedNamespace, common.KubeAPIServerDeploymentName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// RefreshKubeControllerManagerChecksums updates the cloud provider checksum in the kube-controller-manager pod spec template.
func (b *Botanist) RefreshKubeControllerManagerChecksums() error {
	return b.patchDeploymentCloudProviderChecksum(common.KubeControllerManagerDeploymentName)
}

// MoveBackupTerraformResources copies the terraform resources realted to backup infrastructure creation from  a shoot's main namespace
// in the Seed cluster to Shoot's backup namespace.
func (b *Botanist) MoveBackupTerraformResources() error {
	t := terraformer.NewFromOperation(b.Operation, common.TerraformerPurposeBackup)

	// Clean up possible existing job/pod artifacts from previous runs
	jobPodList, err := t.ListJobPods()
	if err != nil {
		return err
	}
	if err := t.CleanupJob(jobPodList); err != nil {
		return err
	}
	if err := t.WaitForCleanEnvironment(); err != nil {
		return err
	}

	var (
		backupInfrastructureName = common.GenerateBackupInfrastructureName(b.Shoot.SeedNamespace, b.Shoot.Info.Status.UID)
		backupNamespace          = common.GenerateBackupNamespaceName(backupInfrastructureName)
		oldPrefix                = fmt.Sprintf("%s.%s", b.Shoot.Info.Name, common.TerraformerPurposeBackup)
		newPrefix                = fmt.Sprintf("%s.%s", backupInfrastructureName, common.TerraformerPurposeBackup)
	)

	if tfConfig, err := b.K8sSeedClient.GetConfigMap(b.Shoot.SeedNamespace, oldPrefix+common.TerraformerConfigSuffix); err == nil {
		if _, err := b.K8sSeedClient.CreateConfigMap(backupNamespace, newPrefix+common.TerraformerConfigSuffix, tfConfig.Data, true); err != nil {
			return err
		}
		if err = b.K8sSeedClient.DeleteConfigMap(b.Shoot.SeedNamespace, oldPrefix+common.TerraformerConfigSuffix); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	if tfState, err := b.K8sSeedClient.GetConfigMap(b.Shoot.SeedNamespace, oldPrefix+common.TerraformerStateSuffix); err == nil {
		if _, err := b.K8sSeedClient.CreateConfigMap(backupNamespace, newPrefix+common.TerraformerStateSuffix, tfState.Data, true); err != nil {
			return err
		}
		if err = b.K8sSeedClient.DeleteConfigMap(b.Shoot.SeedNamespace, oldPrefix+common.TerraformerStateSuffix); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	if tfVariables, err := b.K8sSeedClient.GetSecret(b.Shoot.SeedNamespace, oldPrefix+common.TerraformerVariablesSuffix); err == nil {
		if _, err := b.K8sSeedClient.CreateSecret(backupNamespace, newPrefix+common.TerraformerVariablesSuffix, corev1.SecretTypeOpaque, tfVariables.Data, true); err != nil {
			return err
		}
		if err = b.K8sSeedClient.DeleteSecret(b.Shoot.SeedNamespace, oldPrefix+common.TerraformerVariablesSuffix); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

// DeployBackupInfrastructure creates a BackupInfrastructure resource into the project namespace of shoot on garden cluster.
// BackupInfrastructure controller acting on resource will actually create required cloud resources and updates the status.
func (b *Botanist) DeployBackupInfrastructure() error {
	return b.ApplyChartGarden(filepath.Join(common.ChartPath, "garden-project", "charts", "backup-infrastructure"), "backup-infrastructure", b.Operation.Shoot.Info.Namespace, nil, map[string]interface{}{
		"name":     common.GenerateBackupInfrastructureName(b.Shoot.SeedNamespace, b.Shoot.Info.Status.UID),
		"seed":     b.Seed.Info.Name,
		"shootUID": b.Shoot.Info.Status.UID,
	})
}

// DeleteBackupInfrastructure deletes the sets deletionTimestamp on the backupInfrastructure resource in the Garden namespace
// which is responsible for actual deletion of cloud resource for Shoot's backup infrastructure.
func (b *Botanist) DeleteBackupInfrastructure() error {
	err := b.K8sGardenClient.GardenClientset().GardenV1beta1().BackupInfrastructures(b.Shoot.Info.Namespace).Delete(common.GenerateBackupInfrastructureName(b.Shoot.SeedNamespace, b.Shoot.Info.Status.UID), nil)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// DeleteKubeAddonManager deletes the kube-addon-manager deployment in the Seed cluster which holds the Shoot's control plane. It
// needs to be deleted before trying to remove any resources in the Shoot cluster, otherwise it will automatically recreate
// them and block the infrastructure deletion.
func (b *Botanist) DeleteKubeAddonManager() error {
	err := b.K8sSeedClient.DeleteDeployment(b.Shoot.SeedNamespace, common.KubeAddonManagerDeploymentName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// DeployMachineControllerManager deploys the machine-controller-manager into the Shoot namespace in the Seed cluster. It is responsible
// for managing the worker nodes of the Shoot.
func (b *Botanist) DeployMachineControllerManager() error {
	var (
		name          = "machine-controller-manager"
		defaultValues = map[string]interface{}{
			"podAnnotations": map[string]interface{}{
				"checksum/secret-machine-controller-manager": b.CheckSums[name],
			},
			"namespace": map[string]interface{}{
				"uid": b.SeedNamespaceObject.UID,
			},
		}
	)

	values, err := b.InjectImages(defaultValues, b.K8sSeedClient.Version(), map[string]string{name: name})
	if err != nil {
		return err
	}

	return b.ApplyChartSeed(filepath.Join(common.ChartPath, "seed-controlplane", "charts", name), name, b.Shoot.SeedNamespace, nil, values)
}

// DeployClusterAutoscaler deploys the cluster-autoscaler into the Shoot namespace in the Seed cluster. It is responsible
// for automatically scaling the worker pools of the Shoot.
func (b *Botanist) DeployClusterAutoscaler() error {
	if !b.Shoot.ClusterAutoscalerEnabled() {
		return b.DeleteClusterAutoscaler()
	}

	var (
		name        = "cluster-autoscaler"
		workerPools = []map[string]interface{}{}
		replicas    = 1
	)

	for _, worker := range b.MachineDeployments {
		// Skip worker pools for which min=max=0.
		if worker.Minimum == 0 && worker.Maximum == 0 {
			continue
		}

		// Cluster Autoscaler requires min>=1. We ensure that in the API server validation part, however,
		// for backwards compatibility, we treat existing worker pools whose minimum equals 0 as min=1.
		min := worker.Minimum
		if worker.Minimum == 0 {
			min = 1
		}

		workerPools = append(workerPools, map[string]interface{}{
			"name": worker.Name,
			"min":  min,
			"max":  worker.Maximum,
		})
	}

	if b.Shoot.Hibernated {
		replicas = 0
	}

	defaultValues := map[string]interface{}{
		"podAnnotations": map[string]interface{}{
			"checksum/secret-cluster-autoscaler": b.CheckSums[name],
		},
		"namespace": map[string]interface{}{
			"uid": b.SeedNamespaceObject.UID,
		},
		"replicas":    replicas,
		"workerPools": workerPools,
	}

	values, err := b.InjectImages(defaultValues, b.K8sSeedClient.Version(), map[string]string{name: name})
	if err != nil {
		return err
	}

	return b.ApplyChartSeed(filepath.Join(common.ChartPath, "seed-controlplane", "charts", name), name, b.Shoot.SeedNamespace, nil, values)
}

// DeleteClusterAutoscaler deletes the cluster-autoscaler deployment in the Seed cluster which holds the Shoot's control plane.
func (b *Botanist) DeleteClusterAutoscaler() error {
	err := b.K8sSeedClient.DeleteDeployment(b.Shoot.SeedNamespace, common.ClusterAutoscalerDeploymentName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// DeploySeedMonitoring will install the Helm release "seed-monitoring" in the Seed clusters. It comprises components
// to monitor the Shoot cluster whose control plane runs in the Seed cluster.
func (b *Botanist) DeploySeedMonitoring() error {
	var (
		kubecfgSecret    = b.Secrets["kubecfg"]
		basicAuth        = utils.CreateSHA1Secret(kubecfgSecret.Data["username"], kubecfgSecret.Data["password"])
		alertManagerHost = b.Seed.GetIngressFQDN("a", b.Shoot.Info.Name, b.Garden.ProjectName)
		grafanaHost      = b.Seed.GetIngressFQDN("g", b.Shoot.Info.Name, b.Garden.ProjectName)
		prometheusHost   = b.Seed.GetIngressFQDN("p", b.Shoot.Info.Name, b.Garden.ProjectName)
		replicas         = 1
	)

	if b.Shoot.Hibernated {
		replicas = 0
	}

	var (
		alertManagerConfig = map[string]interface{}{
			"ingress": map[string]interface{}{
				"basicAuthSecret": basicAuth,
				"host":            alertManagerHost,
			},
			"replicas": replicas,
		}
		grafanaConfig = map[string]interface{}{
			"ingress": map[string]interface{}{
				"basicAuthSecret": basicAuth,
				"host":            grafanaHost,
			},
			"replicas": replicas,
		}
		prometheusConfig = map[string]interface{}{
			"networks": map[string]interface{}{
				"pods":     b.Shoot.GetPodNetwork(),
				"services": b.Shoot.GetServiceNetwork(),
				"nodes":    b.Shoot.GetNodeNetwork(),
			},
			"ingress": map[string]interface{}{
				"basicAuthSecret": basicAuth,
				"host":            prometheusHost,
			},
			"namespace": map[string]interface{}{
				"uid": b.SeedNamespaceObject.UID,
			},
			"podAnnotations": map[string]interface{}{
				"checksum/secret-prometheus":                b.CheckSums["prometheus"],
				"checksum/secret-kube-apiserver-basic-auth": b.CheckSums["kube-apiserver-basic-auth"],
				"checksum/secret-vpn-seed":                  b.CheckSums["vpn-seed"],
				"checksum/secret-vpn-seed-tlsauth":          b.CheckSums["vpn-seed-tlsauth"],
			},
			"replicas":           replicas,
			"apiserverServiceIP": common.ComputeClusterIP(b.Shoot.GetServiceNetwork(), 1),
			"seed": map[string]interface{}{
				"apiserver": b.K8sSeedClient.GetConfig().Host,
				"region":    b.Seed.Info.Spec.Cloud.Region,
				"profile":   b.Seed.Info.Spec.Cloud.Profile,
			},
		}
		kubeStateMetricsSeedConfig = map[string]interface{}{
			"replicas": replicas,
		}
		kubeStateMetricsShootConfig = map[string]interface{}{
			"replicas": replicas,
		}
	)

	alertManager, err := b.InjectImages(alertManagerConfig, b.K8sSeedClient.Version(), map[string]string{"alertmanager": "alertmanager", "configmap-reloader": "configmap-reloader"})
	if err != nil {
		return err
	}
	grafana, err := b.InjectImages(grafanaConfig, b.K8sSeedClient.Version(), map[string]string{"grafana": "grafana", "busybox": "busybox"})
	if err != nil {
		return err
	}
	prometheus, err := b.InjectImages(prometheusConfig, b.K8sSeedClient.Version(), map[string]string{
		"prometheus":         "prometheus",
		"configmap-reloader": "configmap-reloader",
		"vpn-seed":           "vpn-seed",
		"blackbox-exporter":  "blackbox-exporter",
	})
	if err != nil {
		return err
	}
	kubeStateMetricsSeed, err := b.InjectImages(kubeStateMetricsSeedConfig, b.K8sSeedClient.Version(), map[string]string{"kube-state-metrics": "kube-state-metrics"})
	if err != nil {
		return err
	}
	kubeStateMetricsShoot, err := b.InjectImages(kubeStateMetricsShootConfig, b.K8sSeedClient.Version(), map[string]string{"kube-state-metrics": "kube-state-metrics"})
	if err != nil {
		return err
	}

	values := map[string]interface{}{
		"global": map[string]interface{}{
			"shootKubeVersion": map[string]interface{}{
				"gitVersion": b.K8sShootClient.Version(),
			},
		},
		"alertmanager":             alertManager,
		"grafana":                  grafana,
		"prometheus":               prometheus,
		"kube-state-metrics-seed":  kubeStateMetricsSeed,
		"kube-state-metrics-shoot": kubeStateMetricsShoot,
	}

	alertingSMTPKeys := b.GetSecretKeysOfRole(common.GardenRoleAlertingSMTP)
	if len(alertingSMTPKeys) > 0 {
		emailConfigs := []map[string]interface{}{}
		for _, key := range alertingSMTPKeys {
			var (
				secret = b.Secrets[key]
				to     = string(secret.Data["to"])
			)
			if operatedBy, ok := b.Shoot.Info.Annotations[common.GardenOperatedBy]; ok && utils.TestEmail(operatedBy) {
				to = operatedBy
			}
			emailConfigs = append(emailConfigs, map[string]interface{}{
				"to":            to,
				"from":          string(secret.Data["from"]),
				"smarthost":     string(secret.Data["smarthost"]),
				"auth_username": string(secret.Data["auth_username"]),
				"auth_identity": string(secret.Data["auth_identity"]),
				"auth_password": string(secret.Data["auth_password"]),
			})
		}
		values["alertmanager"].(map[string]interface{})["email_configs"] = emailConfigs
	}

	return b.ApplyChartSeed(filepath.Join(common.ChartPath, "seed-monitoring"), fmt.Sprintf("%s-monitoring", b.Shoot.SeedNamespace), b.Shoot.SeedNamespace, nil, values)
}

// DeleteSeedMonitoring will delete the monitoring stack from the Seed cluster to avoid phantom alerts
// during the deletion process. More precisely, the Alertmanager and Prometheus StatefulSets will be
// deleted.
func (b *Botanist) DeleteSeedMonitoring() error {
	err := b.K8sSeedClient.DeleteStatefulSet(b.Shoot.SeedNamespace, common.AlertManagerDeploymentName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	err = b.K8sSeedClient.DeleteStatefulSet(b.Shoot.SeedNamespace, common.PrometheusDeploymentName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// patchDeployment patches the given deployment with the provided patch.
func (b *Botanist) patchDeploymentCloudProviderChecksum(deploymentName string) error {
	body := fmt.Sprintf(`[{"op": "replace", "path": "/spec/template/metadata/annotations/checksum~1secret-cloudprovider", "value": "%s"}]`, b.CheckSums[common.CloudProviderSecretName])

	_, err := b.K8sSeedClient.PatchDeployment(b.Shoot.SeedNamespace, deploymentName, []byte(body))
	return err
}
