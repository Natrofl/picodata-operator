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
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	picodatav1 "github.com/picodata/picodata-operator/api/v1"
)

// reconcileTierPlugins installs, migrates, and enables plugins declared in tier.Plugins.
// Called only when the tier is fully ready (all pods Running).
// Returns the observed plugin states for status reporting.
func (r *PicoclusterDBReconciler) reconcileTierPlugins(
	ctx context.Context,
	cluster *picodatav1.PicoclusterDB,
	tier *picodatav1.TierSpec,
) ([]picodatav1.PluginStatus, error) {
	if len(tier.Plugins) == 0 {
		return nil, nil
	}

	log := logf.FromContext(ctx)

	password, err := r.getAdminPassword(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("get admin password: %w", err)
	}

	pgPort := servicePort(cluster.Spec.Service.PgPort, 5432)
	svcHost := fmt.Sprintf("%s-%s.%s.svc.cluster.local", tier.Name, cluster.Name, cluster.Namespace)
	connStr := fmt.Sprintf(
		"host=%s port=%d user=admin password=%s dbname=picodata sslmode=disable",
		svcHost, pgPort, password,
	)

	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", svcHost, err)
	}
	defer conn.Close(ctx) //nolint:errcheck

	var statuses []picodatav1.PluginStatus
	for _, plugin := range tier.Plugins {
		status, err := r.reconcilePlugin(ctx, conn, plugin, tier.Name)
		if err != nil {
			log.Error(err, "Plugin reconcile failed", "plugin", plugin.Name, "version", plugin.Version)
			return nil, fmt.Errorf("plugin %s: %w", plugin.Name, err)
		}
		statuses = append(statuses, status)
	}

	// Drop versions that are no longer in spec.
	if err := r.dropObsoletePlugins(ctx, conn, tier); err != nil {
		log.Error(err, "Failed to drop obsolete plugin versions (non-fatal)")
	}

	return statuses, nil
}

// reconcilePlugin brings a single plugin to the desired state.
func (r *PicoclusterDBReconciler) reconcilePlugin(
	ctx context.Context,
	conn *pgx.Conn,
	plugin picodatav1.PluginSpec,
	tierName string,
) (picodatav1.PluginStatus, error) {
	log := logf.FromContext(ctx)

	exists, enabled, err := queryPluginState(ctx, conn, plugin.Name, plugin.Version)
	if err != nil {
		return picodatav1.PluginStatus{}, err
	}
	if !exists {
		log.Info("Creating plugin", "name", plugin.Name, "version", plugin.Version)
		if _, err := conn.Exec(ctx, fmt.Sprintf(
			"CREATE PLUGIN %s %s", plugin.Name, plugin.Version,
		)); err != nil {
			return picodatav1.PluginStatus{}, fmt.Errorf("CREATE PLUGIN: %w", err)
		}
	}

	// ADD SERVICE before SET migration_context: Radix validates that the service is already
	// bound to the tier referenced in tier_for_db_N before accepting the context parameter.
	// spec.Services is used here because _pico_service is populated only after MIGRATE TO.
	if !enabled {
		for _, svc := range plugin.Services {
			log.Info("Adding plugin service to tier", "plugin", plugin.Name, "service", svc.Name, "tier", tierName)
			if _, err := conn.Exec(ctx, fmt.Sprintf(
				"ALTER PLUGIN %s %s ADD SERVICE %s TO TIER %s",
				plugin.Name, plugin.Version, svc.Name, tierName,
			)); err != nil {
				return picodatav1.PluginStatus{}, fmt.Errorf("ADD SERVICE %s TO TIER: %w", svc.Name, err)
			}
		}

		for k, v := range plugin.MigrationContext {
			log.Info("Setting migration context", "plugin", plugin.Name, "key", k)
			if _, err := conn.Exec(ctx, fmt.Sprintf(
				"ALTER PLUGIN %s %s SET migration_context.%s='%s' OPTION(TIMEOUT=1200)",
				plugin.Name, plugin.Version, k, v,
			)); err != nil {
				return picodatav1.PluginStatus{}, fmt.Errorf("SET migration_context.%s: %w", k, err)
			}
		}

		log.Info("Migrating plugin", "name", plugin.Name, "version", plugin.Version)
		if _, err := conn.Exec(ctx, fmt.Sprintf(
			"ALTER PLUGIN %s MIGRATE TO %s", plugin.Name, plugin.Version,
		)); err != nil {
			return picodatav1.PluginStatus{}, fmt.Errorf("MIGRATE PLUGIN: %w", err)
		}
	}

	// Idempotent post-migration pass: bind services discovered from _pico_service
	// that are not yet bound to this tier (covers services absent from spec.Services).
	services, err := queryPluginServices(ctx, conn, plugin.Name, plugin.Version)
	if err != nil {
		return picodatav1.PluginStatus{}, err
	}
	for _, svc := range services {
		if !containsString(svc.tiers, tierName) {
			log.Info("Adding plugin service to tier", "plugin", plugin.Name, "service", svc.name, "tier", tierName)
			if _, err := conn.Exec(ctx, fmt.Sprintf(
				"ALTER PLUGIN %s %s ADD SERVICE %s TO TIER %s",
				plugin.Name, plugin.Version, svc.name, tierName,
			)); err != nil {
				return picodatav1.PluginStatus{}, fmt.Errorf("ADD SERVICE %s TO TIER: %w", svc.name, err)
			}
		}
	}

	if !enabled {
		log.Info("Enabling plugin", "name", plugin.Name, "version", plugin.Version)
		if _, err := conn.Exec(ctx, fmt.Sprintf(
			"ALTER PLUGIN %s %s ENABLE", plugin.Name, plugin.Version,
		)); err != nil {
			return picodatav1.PluginStatus{}, fmt.Errorf("ENABLE PLUGIN: %w", err)
		}
		enabled = true
	}

	return picodatav1.PluginStatus{
		Name:    plugin.Name,
		Version: plugin.Version,
		Enabled: enabled,
	}, nil
}

// dropObsoletePlugins disables and drops versions of plugins no longer in tier.Plugins.
func (r *PicoclusterDBReconciler) dropObsoletePlugins(
	ctx context.Context,
	conn *pgx.Conn,
	tier *picodatav1.TierSpec,
) error {
	log := logf.FromContext(ctx)

	// Build a set of desired name+version.
	desired := make(map[string]string, len(tier.Plugins)) // name → version
	for _, p := range tier.Plugins {
		desired[p.Name] = p.Version
	}

	rows, err := conn.Query(ctx, "SELECT name, version, enabled FROM _pico_plugin")
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		name, version string
		enabled       bool
	}
	var installed []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.version, &r.enabled); err != nil {
			continue
		}
		installed = append(installed, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, inst := range installed {
		wantVersion, inSpec := desired[inst.name]
		if inSpec && inst.version == wantVersion {
			continue // current desired version — keep
		}
		// Old version or plugin removed from spec.
		if inst.enabled {
			log.Info("Disabling obsolete plugin version", "name", inst.name, "version", inst.version)
			if _, err := conn.Exec(ctx, fmt.Sprintf(
				"ALTER PLUGIN %s %s DISABLE", inst.name, inst.version,
			)); err != nil {
				return fmt.Errorf("DISABLE PLUGIN %s %s: %w", inst.name, inst.version, err)
			}
		}
		log.Info("Dropping obsolete plugin version", "name", inst.name, "version", inst.version)
		if _, err := conn.Exec(ctx, fmt.Sprintf(
			"DROP PLUGIN %s %s", inst.name, inst.version,
		)); err != nil {
			return fmt.Errorf("DROP PLUGIN %s %s: %w", inst.name, inst.version, err)
		}
	}
	return nil
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

type pluginService struct {
	name  string
	tiers []string
}

// queryPluginState returns whether a specific plugin version exists and is enabled.
func queryPluginState(ctx context.Context, conn *pgx.Conn, name, version string) (exists, enabled bool, err error) {
	rows, err := conn.Query(ctx,
		"SELECT enabled FROM _pico_plugin WHERE name = '"+name+"' AND version = '"+version+"'",
	)
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&enabled); err != nil {
			return false, false, err
		}
		return true, enabled, rows.Err()
	}
	return false, false, rows.Err()
}

// queryPluginServices returns services registered for a plugin version.
func queryPluginServices(ctx context.Context, conn *pgx.Conn, name, version string) ([]pluginService, error) {
	rows, err := conn.Query(ctx,
		"SELECT name, tiers FROM _pico_service WHERE plugin_name = '"+name+"' AND version = '"+version+"'",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []pluginService
	for rows.Next() {
		var svcName, tiersJSON string
		if err := rows.Scan(&svcName, &tiersJSON); err != nil {
			continue
		}
		var tiers []string
		_ = json.Unmarshal([]byte(tiersJSON), &tiers)
		result = append(result, pluginService{name: svcName, tiers: tiers})
	}
	return result, rows.Err()
}

// getAdminPassword reads the admin password from the Secret referenced in the cluster spec.
func (r *PicoclusterDBReconciler) getAdminPassword(
	ctx context.Context,
	cluster *picodatav1.PicoclusterDB,
) (string, error) {
	ref := cluster.Spec.AdminPassword
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.SecretName, Namespace: cluster.Namespace}, secret); err != nil {
		return "", err
	}
	val, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %q", ref.Key, ref.SecretName)
	}
	return string(val), nil
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
