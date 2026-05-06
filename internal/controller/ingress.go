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
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	picodatav1 "github.com/picodata/picodata-operator/api/v1"
)

// buildIngress constructs an Ingress that routes HTTP traffic to the tier's client Service.
func buildIngress(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec) *networkingv1.Ingress {
	spec := tier.Ingress
	svcName := clientServiceName(cluster, tier)
	httpPort := servicePort(cluster.Spec.Service.HttpPort, 8081)

	pathType := networkingv1.PathTypePrefix
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   cluster.Namespace,
			Labels:      tierLabels(cluster, tier),
			Annotations: spec.Annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: spec.IngressClassName,
			Rules: []networkingv1.IngressRule{
				{
					Host: spec.Host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: svcName,
											Port: networkingv1.ServiceBackendPort{
												Number: httpPort,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, t := range spec.TLS {
		ingress.Spec.TLS = append(ingress.Spec.TLS, networkingv1.IngressTLS{
			Hosts:      t.Hosts,
			SecretName: t.SecretName,
		})
	}

	return ingress
}
