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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	picodatav1 "github.com/picodata/picodata-operator/api/v1"
)

// interconnectServiceName returns the name of the headless Service used for
// inter-instance communication (iproto advertise, DNS).
func interconnectServiceName(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec) string {
	return fmt.Sprintf("%s-%s-interconnect", tier.Name, cluster.Name)
}

// clientServiceName returns the name of the ClusterIP Service exposed to clients.
func clientServiceName(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec) string {
	return fmt.Sprintf("%s-%s", tier.Name, cluster.Name)
}

// tierLabels returns labels common to all pods in a tier.
// Used by Services that span the entire tier.
func tierLabels(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     fmt.Sprintf("%s-%s", tier.Name, cluster.Name),
		"app.kubernetes.io/cluster":  cluster.Spec.ClusterName,
		"app.kubernetes.io/instance": cluster.Name,
	}
}

// replicasetLabels returns labels for a specific replicaset within a tier.
// Used as the StatefulSet pod selector — unique per StatefulSet.
// rsIndex is 1-based.
func replicasetLabels(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec, rsIndex int32) map[string]string {
	labels := tierLabels(cluster, tier)
	labels["picodata.io/replicaset"] = fmt.Sprintf("%d", rsIndex)
	return labels
}

// buildInterconnectService builds the headless Service used for inter-instance DNS.
// Each pod gets a stable FQDN:
//
//	<pod>.<tier>-<cluster>-interconnect.<ns>.svc.cluster.local
func buildInterconnectService(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec) *corev1.Service {
	svc := cluster.Spec.Service
	binaryPort := servicePort(svc.BinaryPort, 3301)
	pgPort := servicePort(svc.PgPort, 5432)

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      interconnectServiceName(cluster, tier),
			Namespace: cluster.Namespace,
			Labels:    tierLabels(cluster, tier),
		},
		Spec: corev1.ServiceSpec{
			// ClusterIP: None makes this headless — DNS returns individual pod IPs.
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
			Selector:                 tierLabels(cluster, tier),
			Ports: []corev1.ServicePort{
				{
					Name:       "binary",
					Protocol:   corev1.ProtocolTCP,
					Port:       binaryPort,
					TargetPort: intstr.FromInt32(binaryPort),
				},
				{
					Name:       "pg",
					Protocol:   corev1.ProtocolTCP,
					Port:       pgPort,
					TargetPort: intstr.FromInt32(pgPort),
				},
			},
		},
	}
}

// buildClientService builds the ClusterIP Service for client access.
func buildClientService(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec) *corev1.Service {
	svc := cluster.Spec.Service
	binaryPort := servicePort(svc.BinaryPort, 3301)
	httpPort := servicePort(svc.HttpPort, 8081)
	pgPort := servicePort(svc.PgPort, 5432)

	svcType := svc.Type
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}

	ports := []corev1.ServicePort{
		{
			Name:       "binary",
			Protocol:   corev1.ProtocolTCP,
			Port:       binaryPort,
			TargetPort: intstr.FromInt32(binaryPort),
		},
		{
			Name:       "http",
			Protocol:   corev1.ProtocolTCP,
			Port:       httpPort,
			TargetPort: intstr.FromInt32(httpPort),
		},
		{
			Name:       "pg",
			Protocol:   corev1.ProtocolTCP,
			Port:       pgPort,
			TargetPort: intstr.FromInt32(pgPort),
		},
	}
	for _, plugin := range tier.Plugins {
		for _, svc := range plugin.Services {
			ports = append(ports, corev1.ServicePort{
				Name:       fmt.Sprintf("%s-%s", plugin.Name, svc.Name),
				Protocol:   corev1.ProtocolTCP,
				Port:       svc.ListenerPort,
				TargetPort: intstr.FromInt32(svc.ListenerPort),
			})
		}
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clientServiceName(cluster, tier),
			Namespace: cluster.Namespace,
			Labels:    tierLabels(cluster, tier),
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: tierLabels(cluster, tier),
			Ports:    ports,
		},
	}
}
