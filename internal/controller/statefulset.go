/*
Copyright 2026.

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

package controller

import (
	"crypto/sha256"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	picodatav1 "github.com/picodata/picodata-operator/api/v1"
)

const (
	dataVolumeName     = "picodata"
	configVolumeName   = "pico-conf"
	configOutVolumeName = "pico-conf-out"
	configOutDir       = "/pico-conf-out"
	configMountPath    = instanceDir + "/config.yaml"
	configTemplatePath = "/etc/picodata-tpl/config.yaml"
)

// statefulSetName returns the name of the StatefulSet for a specific replicaset.
// rsIndex is 1-based: {tier}-{cluster}-1, {tier}-{cluster}-2, …
func statefulSetName(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec, rsIndex int32) string {
	return fmt.Sprintf("%s-%s-%d", tier.Name, cluster.Name, rsIndex)
}

// configHash produces a short hash of the config content for use as a pod annotation.
// A change in the hash triggers a rolling restart of the StatefulSet.
func configHash(configData string) string {
	sum := sha256.Sum256([]byte(configData))
	return fmt.Sprintf("%x", sum[:8])
}

// buildStatefulSet constructs the StatefulSet for a single replicaset within a tier.
// rsIndex is 1-based.
func buildStatefulSet(
	cluster *picodatav1.PicoclusterDB,
	tier *picodatav1.TierSpec,
	rsIndex int32,
	configData string,
) *appsv1.StatefulSet {
	labels := replicasetLabels(cluster, tier, rsIndex)
	svc := cluster.Spec.Service
	binaryPort := servicePort(svc.BinaryPort, 3301)
	httpPort := servicePort(svc.HttpPort, 8081)
	pgPort := servicePort(svc.PgPort, 5432)

	image := fmt.Sprintf("%s/%s", cluster.Spec.Image.Repository, cluster.Spec.Image.Tag)
	pullPolicy := cluster.Spec.Image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}

	// Each StatefulSet manages exactly one replicaset: RF pods.
	replicas := tier.ReplicationFactor

	// Pod annotations — checksum triggers rolling restart when config changes.
	podAnnotations := map[string]string{
		"prometheus.io/scrape": "true",
		"prometheus.io/port":   fmt.Sprintf("%d", httpPort),
		"checksum/config":      configHash(configData),
	}

	// Environment variables — mirror the Helm chart approach.
	env := []corev1.EnvVar{
		{
			Name: "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
			},
		},
		{
			Name: "INSTANCE_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
		{
			Name: "INSTANCE_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
		{
			Name:  "PICODATA_IPROTO_LISTEN",
			Value: fmt.Sprintf("$(INSTANCE_NAME):%d", binaryPort),
		},
		{
			Name: "PICODATA_IPROTO_ADVERTISE",
			Value: fmt.Sprintf("$(INSTANCE_NAME).%s-%s-interconnect.$(INSTANCE_NAMESPACE).svc.cluster.local:%d",
				tier.Name, cluster.Name, binaryPort),
		},
		{
			Name:  "PICODATA_HTTP_LISTEN",
			Value: fmt.Sprintf("0.0.0.0:%d", httpPort),
		},
		{
			Name: "PICODATA_HTTP_ADVERTISE",
			Value: fmt.Sprintf("$(INSTANCE_NAME).%s-%s-interconnect.$(INSTANCE_NAMESPACE).svc.cluster.local:%d",
				tier.Name, cluster.Name, httpPort),
		},
		{
			Name:  "PICODATA_PG_LISTEN",
			Value: fmt.Sprintf("0.0.0.0:%d", pgPort),
		},
		{
			Name: "PICODATA_PG_ADVERTISE",
			Value: fmt.Sprintf("$(INSTANCE_NAME).%s-%s-interconnect.$(INSTANCE_NAMESPACE).svc.cluster.local:%d",
				tier.Name, cluster.Name, pgPort),
		},
		{
			Name:  "PICODATA_REPLICASET_NAME",
			Value: fmt.Sprintf("%s_%d", tier.Name, rsIndex),
		},
		{
			Name:  "PICODATA_FAILURE_DOMAIN",
			Value: "HOST=$(INSTANCE_NAME)",
		},
		{
			Name:  "PICODATA_CONFIG_FILE",
			Value: configMountPath,
		},
		{
			Name:  "PICODATA_ADMIN_SOCK",
			Value: instanceDir + "/admin.sock",
		},
		{
			Name: "PICODATA_ADMIN_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cluster.Spec.AdminPassword.SecretName,
					},
					Key: cluster.Spec.AdminPassword.Key,
				},
			},
		},
	}

	// Append user-defined extra env vars.
	env = append(env, tier.Env...)


	// Container ports.
	ports := []corev1.ContainerPort{
		{Name: "binary", ContainerPort: binaryPort, Protocol: corev1.ProtocolTCP},
		{Name: "http", ContainerPort: httpPort, Protocol: corev1.ProtocolTCP},
		{Name: "pg", ContainerPort: pgPort, Protocol: corev1.ProtocolTCP},
	}
	for _, plugin := range tier.Plugins {
		for _, svc := range plugin.Services {
			ports = append(ports, corev1.ContainerPort{
				Name:          fmt.Sprintf("%s-%s", plugin.Name, svc.Name),
				ContainerPort: svc.ListenerPort,
				Protocol:      corev1.ProtocolTCP,
			})
		}
	}

	// Volume mounts — init container writes processed config to emptyDir; main container reads it.
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      configOutVolumeName,
			MountPath: configMountPath,
			SubPath:   "config.yaml",
		},
		{
			Name:      dataVolumeName,
			MountPath: instanceDir,
		},
	}

	// Container spec.
	container := corev1.Container{
		Name:            "picodata",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Ports:           ports,
		Env:             env,
		VolumeMounts:    volumeMounts,
		Resources:       tier.Resources,
	}

	// Probes — use cluster-level defaults if defined.
	if cluster.Spec.StartupProbe != nil {
		container.StartupProbe = cluster.Spec.StartupProbe
	} else {
		container.StartupProbe = defaultStartupProbe(httpPort)
	}
	if cluster.Spec.LivenessProbe != nil {
		container.LivenessProbe = cluster.Spec.LivenessProbe
	} else {
		container.LivenessProbe = defaultLivenessProbe(httpPort)
	}
	if cluster.Spec.ReadinessProbe != nil {
		container.ReadinessProbe = cluster.Spec.ReadinessProbe
	} else {
		container.ReadinessProbe = defaultReadinessProbe(httpPort)
	}

	// Ensure the PVC is writable by the picodata process (UID/GID 1000).
	podSecCtx := tier.SecurityContext
	if podSecCtx == nil {
		podSecCtx = &corev1.PodSecurityContext{}
	}
	if podSecCtx.FSGroup == nil {
		podSecCtx.FSGroup = ptr(int64(1000))
	}

	// Merge user affinity with auto-injected per-replicaset anti-affinity.
	// This ensures pods of the same replicaset are spread across different nodes
	// without over-constraining pods from different replicasets.
	// Disabled via disableAutoAntiAffinity for single-node test deployments.
	affinity := tier.Affinity
	if !tier.DisableAutoAntiAffinity {
		affinity = mergeReplicasetAntiAffinity(tier.Affinity, replicasetLabels(cluster, tier, rsIndex))
	}

	// Pod spec.
	podSpec := corev1.PodSpec{
		SecurityContext:               podSecCtx,
		InitContainers:                []corev1.Container{buildConfigInitContainer(cluster, tier, image, pullPolicy)},
		Containers:                    []corev1.Container{container},
		ImagePullSecrets:              cluster.Spec.ImagePullSecrets,
		Affinity:                      affinity,
		Tolerations:                   tier.Tolerations,
		NodeSelector:                  tier.NodeSelector,
		TopologySpreadConstraints:     tier.TopologySpreadConstraints,
		TerminationGracePeriodSeconds: ptr(int64(30)),
		Volumes: []corev1.Volume{
			{
				Name: configVolumeName,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: configMapName(cluster, tier),
						},
						Items: []corev1.KeyToPath{
							{Key: "config.yaml", Path: "config.yaml"},
						},
					},
				},
			},
			{
				Name: configOutVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
	}

	// Storage size for the PVC.
	storageSize := tier.Storage.Size
	if storageSize.IsZero() {
		storageSize = resource.MustParse("1Gi")
	}

	pvcSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: storageSize,
			},
		},
	}
	if tier.Storage.StorageClassName != nil {
		pvcSpec.StorageClassName = tier.Storage.StorageClassName
	}

	stsName := statefulSetName(cluster, tier, rsIndex)

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stsName,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: interconnectServiceName(cluster, tier),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: podAnnotations,
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      dataVolumeName,
						Namespace: cluster.Namespace,
					},
					Spec: pvcSpec,
				},
			},
			// Parallel allows all pods in a tier to start simultaneously.
			// This is required for tiers with replicationFactor > 1: with OrderedReady,
			// pod-0 can never pass the startup probe (replicaset not-ready) because
			// pod-1 won't start until pod-0 is Ready — a deadlock.
			// Picodata instances discover each other via the shared peer address, so
			// concurrent startup is safe.
			PodManagementPolicy: appsv1.ParallelPodManagement,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
		},
	}
}

// -----------------------------------------------------------------------
// Default probes
// -----------------------------------------------------------------------

func defaultStartupProbe(httpPort int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/api/v1/health/startup",
				Port: intstrFromInt32(httpPort),
			},
		},
		PeriodSeconds:    30,
		FailureThreshold: 20,
		TimeoutSeconds:   3,
	}
}

func defaultLivenessProbe(httpPort int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/api/v1/health/live",
				Port: intstrFromInt32(httpPort),
			},
		},
		PeriodSeconds:    20,
		FailureThreshold: 3,
		TimeoutSeconds:   3,
	}
}

func defaultReadinessProbe(httpPort int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/api/v1/health/ready",
				Port: intstrFromInt32(httpPort),
			},
		},
		PeriodSeconds:    20,
		FailureThreshold: 3,
		TimeoutSeconds:   3,
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func ptr[T any](v T) *T { return &v }

// buildConfigInitContainer returns an init container that generates the per-pod config.yaml
// by substituting plugin listener advertise placeholders with the pod's actual FQDN.
// The ConfigMap is mounted as a read-only template; the final config is written to the PVC.
func buildConfigInitContainer(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec, image string, pullPolicy corev1.PullPolicy) corev1.Container {
	var sedExprs []string
	for _, plugin := range tier.Plugins {
		for _, svc := range plugin.Services {
			if svc.ListenerPort <= 0 {
				continue
			}
			placeholder := fmt.Sprintf("__PLUGIN_ADVERTISE_%s_%s__",
				strings.ToUpper(plugin.Name), strings.ToUpper(svc.Name))
			sedExprs = append(sedExprs, fmt.Sprintf(
				`-e "s|%s|$INSTANCE_NAME.%s-%s-interconnect.$INSTANCE_NAMESPACE.svc.cluster.local:%d|g"`,
				placeholder, tier.Name, cluster.Name, svc.ListenerPort))
		}
	}

	var script string
	if len(sedExprs) > 0 {
		script = fmt.Sprintf("sed %s %s > %s/config.yaml", strings.Join(sedExprs, " "), configTemplatePath, configOutDir)
	} else {
		script = fmt.Sprintf("cp %s %s/config.yaml", configTemplatePath, configOutDir)
	}

	return corev1.Container{
		Name:            "config-init",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{script},
		Env: []corev1.EnvVar{
			{
				Name: "INSTANCE_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
				},
			},
			{
				Name: "INSTANCE_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      configVolumeName,
				MountPath: configTemplatePath,
				SubPath:   "config.yaml",
			},
			{
				Name:      configOutVolumeName,
				MountPath: configOutDir,
			},
		},
	}
}

// mergeReplicasetAntiAffinity returns an Affinity that includes a required
// pod anti-affinity term preventing two pods of the same replicaset from
// landing on the same node. Any user-supplied affinity rules are preserved.
func mergeReplicasetAntiAffinity(base *corev1.Affinity, rsLabels map[string]string) *corev1.Affinity {
	term := corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{MatchLabels: rsLabels},
		TopologyKey:   "kubernetes.io/hostname",
	}

	var affinity *corev1.Affinity
	if base != nil {
		copy := *base
		affinity = &copy
	} else {
		affinity = &corev1.Affinity{}
	}
	if affinity.PodAntiAffinity == nil {
		affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
	}
	affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(
		affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
		term,
	)
	return affinity
}

func intstrFromInt32(v int32) intstr.IntOrString {
	return intstr.FromInt32(v)
}
