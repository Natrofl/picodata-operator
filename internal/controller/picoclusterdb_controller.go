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
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	picodatav1 "github.com/picodata/picodata-operator/api/v1"
)

// PicoclusterDBReconciler reconciles a PicoclusterDB object.
type PicoclusterDBReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// RBAC markers — operator needs access to manage these resources.
// +kubebuilder:rbac:groups=picodata.picodata.io,resources=picoclusterdbs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=picodata.picodata.io,resources=picoclusterdbs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=picodata.picodata.io,resources=picoclusterdbs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete

func (r *PicoclusterDBReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the PicoclusterDB resource.
	cluster := &picodatav1.PicoclusterDB{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling PicoclusterDB", "name", cluster.Name, "namespace", cluster.Namespace)

	// 2. Reconcile each tier.
	tierStatuses := make([]picodatav1.TierStatus, 0, len(cluster.Spec.Tiers))
	allReady := true

	for i := range cluster.Spec.Tiers {
		tier := &cluster.Spec.Tiers[i]

		if err := r.reconcileTier(ctx, cluster, tier); err != nil {
			return ctrl.Result{}, fmt.Errorf("tier %s: %w", tier.Name, err)
		}

		// Collect tier status from the StatefulSet.
		ts, ready, err := r.tierStatus(ctx, cluster, tier)
		if err != nil {
			return ctrl.Result{}, err
		}
		tierStatuses = append(tierStatuses, ts)
		if !ready {
			allReady = false
		}
	}

	// 3. Update overall cluster status.
	return ctrl.Result{}, r.updateStatus(ctx, cluster, tierStatuses, allReady)
}

// reconcileTier ensures ConfigMap, Services and StatefulSet are in sync for one tier.
func (r *PicoclusterDBReconciler) reconcileTier(
	ctx context.Context,
	cluster *picodatav1.PicoclusterDB,
	tier *picodatav1.TierSpec,
) error {
	// Build the desired ConfigMap first — StatefulSet references it via annotation hash.
	desiredCM := buildConfigMap(cluster, tier)
	if err := controllerutil.SetControllerReference(cluster, desiredCM, r.Scheme); err != nil {
		return err
	}
	if err := r.reconcileConfigMap(ctx, desiredCM); err != nil {
		return fmt.Errorf("configmap: %w", err)
	}

	// Headless Service must exist before StatefulSet so pod DNS resolves correctly.
	desiredHeadless := buildInterconnectService(cluster, tier)
	if err := controllerutil.SetControllerReference(cluster, desiredHeadless, r.Scheme); err != nil {
		return err
	}
	if err := r.reconcileService(ctx, desiredHeadless); err != nil {
		return fmt.Errorf("headless service: %w", err)
	}

	// Client ClusterIP Service.
	desiredClient := buildClientService(cluster, tier)
	if err := controllerutil.SetControllerReference(cluster, desiredClient, r.Scheme); err != nil {
		return err
	}
	if err := r.reconcileService(ctx, desiredClient); err != nil {
		return fmt.Errorf("client service: %w", err)
	}

	// One StatefulSet per replicaset.
	configData := desiredCM.Data["config.yaml"]
	for rsIndex := int32(1); rsIndex <= tier.Replicas; rsIndex++ {
		desiredSTS := buildStatefulSet(cluster, tier, rsIndex, configData)
		if err := controllerutil.SetControllerReference(cluster, desiredSTS, r.Scheme); err != nil {
			return err
		}
		if err := r.reconcileStatefulSet(ctx, desiredSTS); err != nil {
			return fmt.Errorf("statefulset rs%d: %w", rsIndex, err)
		}
	}

	// Remove StatefulSets for replicasets that no longer exist (scale down).
	if err := r.deleteExtraStatefulSets(ctx, cluster, tier); err != nil {
		return fmt.Errorf("cleanup statefulsets: %w", err)
	}

	return nil
}

// deleteExtraStatefulSets deletes StatefulSets whose replicaset index exceeds tier.Replicas.
func (r *PicoclusterDBReconciler) deleteExtraStatefulSets(
	ctx context.Context,
	cluster *picodatav1.PicoclusterDB,
	tier *picodatav1.TierSpec,
) error {
	stsList := &appsv1.StatefulSetList{}
	if err := r.List(ctx, stsList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(tierLabels(cluster, tier)),
	); err != nil {
		return err
	}
	for i := range stsList.Items {
		sts := &stsList.Items[i]
		rsStr, ok := sts.Labels["picodata.io/replicaset"]
		if !ok {
			continue
		}
		rsIndex, err := strconv.ParseInt(rsStr, 10, 32)
		if err != nil {
			continue
		}
		if int32(rsIndex) > tier.Replicas {
			if err := r.Delete(ctx, sts); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

// -----------------------------------------------------------------------
// Individual resource reconcilers
// -----------------------------------------------------------------------

func (r *PicoclusterDBReconciler) reconcileConfigMap(ctx context.Context, desired *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Data = desired.Data
	existing.Labels = desired.Labels
	return r.Update(ctx, existing)
}

func (r *PicoclusterDBReconciler) reconcileService(ctx context.Context, desired *corev1.Service) error {
	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	// Update only ports and labels; preserve ClusterIP (immutable field).
	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Selector = desired.Spec.Selector
	existing.Labels = desired.Labels
	return r.Update(ctx, existing)
}

func (r *PicoclusterDBReconciler) reconcileStatefulSet(ctx context.Context, desired *appsv1.StatefulSet) error {
	existing := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	// Update mutable fields: replicas, pod template (image, env, resources, probes, annotations).
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template = desired.Spec.Template
	existing.Labels = desired.Labels
	return r.Update(ctx, existing)
}

// -----------------------------------------------------------------------
// Status helpers
// -----------------------------------------------------------------------

// tierStatus aggregates ReadyReplicas across all StatefulSets of the tier.
func (r *PicoclusterDBReconciler) tierStatus(
	ctx context.Context,
	cluster *picodatav1.PicoclusterDB,
	tier *picodatav1.TierSpec,
) (picodatav1.TierStatus, bool, error) {
	stsList := &appsv1.StatefulSetList{}
	if err := r.List(ctx, stsList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(tierLabels(cluster, tier)),
	); err != nil {
		return picodatav1.TierStatus{}, false, err
	}

	desiredPods := tier.Replicas * tier.ReplicationFactor
	var readyPods int32
	for _, sts := range stsList.Items {
		readyPods += sts.Status.ReadyReplicas
	}

	return picodatav1.TierStatus{
		Name:            tier.Name,
		ReadyReplicas:   readyPods,
		DesiredReplicas: desiredPods,
	}, readyPods >= desiredPods, nil
}

// updateStatus computes and persists the overall cluster status.
func (r *PicoclusterDBReconciler) updateStatus(
	ctx context.Context,
	cluster *picodatav1.PicoclusterDB,
	tiers []picodatav1.TierStatus,
	allReady bool,
) error {
	phase := picodatav1.ClusterPhaseInitializing
	condStatus := metav1.ConditionFalse
	condReason := "TiersNotReady"
	condMessage := "One or more tiers are not yet ready"

	if allReady {
		phase = picodatav1.ClusterPhaseReady
		condStatus = metav1.ConditionTrue
		condReason = "AllTiersReady"
		condMessage = fmt.Sprintf("All %d tier(s) are ready", len(tiers))
	}

	now := metav1.Now()
	readyCond := metav1.Condition{
		Type:               picodatav1.ConditionReady,
		Status:             condStatus,
		Reason:             condReason,
		Message:            condMessage,
		LastTransitionTime: now,
	}

	// Preserve existing LastTransitionTime if the condition hasn't changed.
	for _, c := range cluster.Status.Conditions {
		if c.Type == picodatav1.ConditionReady && c.Status == condStatus {
			readyCond.LastTransitionTime = c.LastTransitionTime
			break
		}
	}

	patch := client.MergeFrom(cluster.DeepCopy())
	cluster.Status.Phase = phase
	cluster.Status.Tiers = tiers
	cluster.Status.Conditions = []metav1.Condition{readyCond}

	return r.Status().Patch(ctx, cluster, patch)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PicoclusterDBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&picodatav1.PicoclusterDB{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Named("picoclusterdb").
		Complete(r)
}
