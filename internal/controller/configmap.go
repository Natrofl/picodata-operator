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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	picodatav1 "github.com/picodata/picodata-operator/api/v1"
)

const instanceDir = "/pico"

// configMapName returns the name of the ConfigMap for a given tier.
func configMapName(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec) string {
	return fmt.Sprintf("%s-%s", tier.Name, cluster.Name)
}

// buildConfigMap constructs the ConfigMap that holds picodata config.yaml for a tier.
// Uses the new-style config format introduced in Picodata 25.x:
// iproto/http/pgproto sections instead of the legacy listen/http_listen/pg fields.
func buildConfigMap(cluster *picodatav1.PicoclusterDB, tier *picodatav1.TierSpec) *corev1.ConfigMap {
	svc := cluster.Spec.Service
	binaryPort := servicePort(svc.BinaryPort, 3301)
	httpPort := servicePort(svc.HttpPort, 8081)
	pgPort := servicePort(svc.PgPort, 5432)

	cl := cluster.Spec.Cluster
	defaultRepl := cl.DefaultReplicationFactor
	if defaultRepl == 0 {
		defaultRepl = 1
	}
	defaultBuckets := cl.DefaultBucketCount
	if defaultBuckets == 0 {
		defaultBuckets = 3000
	}

	// Build tier section — every instance needs the full cluster topology.
	tiersYAML := ""
	for _, t := range cluster.Spec.Tiers {
		rf := t.ReplicationFactor
		if rf == 0 {
			rf = 1
		}
		tiersYAML += fmt.Sprintf("          %s:\n            replication_factor: %d\n            can_vote: %v\n",
			t.Name, rf, t.CanVote)
	}

	// peer — DNS address of instance 0 of replicaset 1 of the first tier (bootstrap entry point).
	// Pod naming: {tier}-{cluster}-{rsIndex}-{ordinal}, rsIndex is 1-based.
	firstTier := cluster.Spec.Tiers[0]
	peerFQDN := fmt.Sprintf("%s-%s-1-0.%s-%s-interconnect.%s.svc.cluster.local:%d",
		firstTier.Name, cluster.Name,
		firstTier.Name, cluster.Name,
		cluster.Namespace,
		binaryPort,
	)

	logDestination := "null"
	if tier.Log.Destination != nil {
		logDestination = *tier.Log.Destination
	}
	logLevel := tier.Log.Level
	if logLevel == "" {
		logLevel = "info"
	}
	logFormat := tier.Log.Format
	if logFormat == "" {
		logFormat = "plain"
	}

	memtxMemory := tier.Memtx.Memory
	if memtxMemory == "" {
		memtxMemory = "128M"
	}
	vinylMemory := tier.Vinyl.Memory
	if vinylMemory == "" {
		vinylMemory = "64M"
	}
	vinylCache := tier.Vinyl.Cache
	if vinylCache == "" {
		vinylCache = "32M"
	}

	// pgproto.listen: expose on all interfaces when enabled, otherwise only localhost.
	pgListen := fmt.Sprintf("127.0.0.1:%d", pgPort)
	if tier.Pg.Enabled {
		pgListen = fmt.Sprintf("0.0.0.0:%d", pgPort)
	}

	pgTLS := tier.Pg.SSL

	// share_dir must be set on every tier: CREATE PLUGIN is cluster-wide DDL and each
	// instance reads this path. Default is /usr/share/picodata (present in all official images).
	shareDir := tier.ShareDir
	if shareDir == "" {
		shareDir = "/usr/share/picodata"
	}
	shareDirLine := fmt.Sprintf("  share_dir: %s\n", shareDir)

	pluginSection := buildPluginListenerConfig(tier)

	// New-style config: iproto / http / pgproto sections.
	// iproto.listen and iproto.advertise are overridden by env vars
	// PICODATA_IPROTO_LISTEN / PICODATA_IPROTO_ADVERTISE set in the StatefulSet,
	// so we provide sane defaults here that will be superseded at runtime.
	configYAML := fmt.Sprintf(`cluster:
  name: %s
  default_replication_factor: %d
  default_bucket_count: %d
  shredding: %v
  tier:
%sinstance:
  instance_dir: %s
  tier: %s
%s  peer:
  - %s
  admin_socket: %s/admin.sock
  log:
    level: %s
    destination: %s
    format: %s
  memtx:
    memory: %s
  vinyl:
    memory: %s
    cache: %s
  http:
    listen: 0.0.0.0:%d
  iproto:
    listen: 0.0.0.0:%d
  pgproto:
    enabled: %v
    listen: %s
    tls:
      enabled: %v
%s`,
		cluster.Spec.ClusterName,
		defaultRepl,
		defaultBuckets,
		cl.Shredding,
		tiersYAML,
		instanceDir,
		tier.Name,
		shareDirLine,
		peerFQDN,
		instanceDir,
		logLevel,
		logDestination,
		logFormat,
		memtxMemory,
		vinylMemory,
		vinylCache,
		httpPort,
		binaryPort,
		tier.Pg.Enabled,
		pgListen,
		pgTLS,
		pluginSection,
	)

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName(cluster, tier),
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			"config.yaml": configYAML,
		},
	}
}

func servicePort(v, defaultV int32) int32 {
	if v == 0 {
		return defaultV
	}
	return v
}

// buildPluginListenerConfig generates the instance.plugin section for all plugins
// in a tier that declare services with a ListenerPort.
//
// Example output:
//
//	plugin:
//	  radix:
//	    service:
//	      radix:
//	        listener:
//	          enabled: true
//	          listen: "0.0.0.0:8082"
func buildPluginListenerConfig(tier *picodatav1.TierSpec) string {
	// Collect plugins that have at least one service with a port.
	type svcEntry struct {
		name string
		port int32
	}
	type pluginEntry struct {
		name     string
		services []svcEntry
	}

	var plugins []pluginEntry
	for _, p := range tier.Plugins {
		var svcs []svcEntry
		for _, s := range p.Services {
			if s.ListenerPort > 0 {
				svcs = append(svcs, svcEntry{name: s.Name, port: s.ListenerPort})
			}
		}
		if len(svcs) > 0 {
			plugins = append(plugins, pluginEntry{name: p.Name, services: svcs})
		}
	}
	if len(plugins) == 0 {
		return ""
	}

	out := "  plugin:\n"
	for _, p := range plugins {
		out += fmt.Sprintf("    %s:\n      service:\n", p.name)
		for _, s := range p.services {
			out += fmt.Sprintf("        %s:\n          listener:\n            enabled: true\n            listen: \"0.0.0.0:%d\"\n            advertise: \"__PLUGIN_ADVERTISE_%s_%s__\"\n",
				s.name, s.port, strings.ToUpper(p.name), strings.ToUpper(s.name))
		}
	}
	return out
}
