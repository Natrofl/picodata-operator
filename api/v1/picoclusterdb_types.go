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

package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PicoclusterDB is the Schema for the picoclusterdbs API.
type PicoclusterDB struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec PicoclusterDBSpec `json:"spec"`

	// +optional
	Status PicoclusterDBStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PicoclusterDBList contains a list of PicoclusterDB.
type PicoclusterDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PicoclusterDB `json:"items"`
}

// -----------------------------------------------------------------------
// Spec
// -----------------------------------------------------------------------

// PicoclusterDBSpec defines the desired state of PicoclusterDB.
type PicoclusterDBSpec struct {
	// Image settings for Picodata container.
	Image ImageSpec `json:"image"`

	// ImagePullSecrets is an optional list of references to secrets for pulling the image.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// ClusterName is the Picodata cluster name. Immutable after creation.
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`

	// AdminPassword references a Secret containing the admin user password.
	AdminPassword SecretKeyRef `json:"adminPassword"`

	// Cluster-level Picodata parameters.
	// +optional
	Cluster ClusterSpec `json:"cluster,omitempty"`

	// Tiers defines one or more Picodata tiers. Each tier becomes a separate StatefulSet.
	// +kubebuilder:validation:MinItems=1
	Tiers []TierSpec `json:"tiers"`

	// Service defines ports exposed by the ClusterIP Service for each tier.
	// +optional
	Service ServiceSpec `json:"service,omitempty"`

	// StartupProbe applied to all Picodata containers.
	// +optional
	StartupProbe *corev1.Probe `json:"startupProbe,omitempty"`

	// LivenessProbe applied to all Picodata containers.
	// +optional
	LivenessProbe *corev1.Probe `json:"livenessProbe,omitempty"`

	// ReadinessProbe applied to all Picodata containers.
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`
}

// ImageSpec describes the Picodata container image.
type ImageSpec struct {
	// Repository is the Docker registry + image name, e.g. "docker.binary.picodata.io".
	// +kubebuilder:validation:MinLength=1
	Repository string `json:"repository"`

	// Tag is the image tag, e.g. "picodata:master".
	// +kubebuilder:validation:MinLength=1
	Tag string `json:"tag"`

	// PullPolicy for the image.
	// +optional
	// +kubebuilder:default=IfNotPresent
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// SecretKeyRef points to a key inside a Secret.
type SecretKeyRef struct {
	// SecretName is the name of the Secret.
	SecretName string `json:"secretName"`
	// Key is the key within the Secret.
	Key string `json:"key"`
}

// ClusterSpec contains cluster-wide Picodata parameters.
type ClusterSpec struct {
	// DefaultReplicationFactor is the default number of replicas per replicaset.
	// +optional
	// +kubebuilder:default=1
	DefaultReplicationFactor int32 `json:"defaultReplicationFactor,omitempty"`

	// DefaultBucketCount is the default number of vshard buckets in the cluster.
	// +optional
	// +kubebuilder:default=3000
	DefaultBucketCount int32 `json:"defaultBucketCount,omitempty"`

	// Shredding enables secure deletion of data files by overwriting.
	// +optional
	Shredding bool `json:"shredding,omitempty"`
}

// TierSpec defines a single Picodata tier.
type TierSpec struct {
	// Name is the tier name. Immutable after cluster bootstrap.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Replicas is the desired number of replicasets in this tier.
	// Total pods = Replicas × ReplicationFactor.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas"`

	// ReplicationFactor is the number of replicas per replicaset in this tier. Immutable.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	ReplicationFactor int32 `json:"replicationFactor,omitempty"`

	// CanVote indicates whether instances in this tier can participate in raft leader election. Immutable.
	// +optional
	// +kubebuilder:default=true
	CanVote bool `json:"canVote,omitempty"`

	// Storage configuration for the PersistentVolumeClaim attached to each instance.
	Storage StorageSpec `json:"storage"`

	// Memtx storage engine settings.
	// +optional
	Memtx MemtxSpec `json:"memtx,omitempty"`

	// Vinyl storage engine settings.
	// +optional
	Vinyl VinylSpec `json:"vinyl,omitempty"`

	// Pg (PostgreSQL protocol) settings for this tier.
	// +optional
	Pg PgSpec `json:"pg,omitempty"`

	// Log settings for instances in this tier.
	// +optional
	Log LogSpec `json:"log,omitempty"`

	// Resources sets CPU/memory requests and limits for each Picodata pod.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// ShareDir is the path inside the container where Picodata looks for plugin binaries.
	// Required when Plugins is non-empty. Maps to instance.share_dir in config.yaml.
	// +optional
	ShareDir string `json:"shareDir,omitempty"`

	// Plugins lists Picodata plugins to install and enable on this tier.
	// +optional
	Plugins []PluginSpec `json:"plugins,omitempty"`

	// Env defines extra environment variables injected into each pod.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Affinity rules for scheduling pods of this tier.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations for scheduling pods of this tier.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// NodeSelector for scheduling pods of this tier.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// TopologySpreadConstraints for distributing pods across topology domains.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// SecurityContext is the pod-level security context applied to all pods in this tier.
	// Use fsGroup to ensure the PVC is writable by the Picodata process (GID 1000 for official images).
	// +optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`

	// Ingress configures an optional Ingress resource for the HTTP port of this tier.
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`

	// DisableAutoAntiAffinity disables the automatic per-replicaset pod anti-affinity
	// that spreads pods across nodes. Set to true when deploying on a single node (e.g. for tests).
	// +optional
	DisableAutoAntiAffinity bool `json:"disableAutoAntiAffinity,omitempty"`
}

// StorageSpec defines persistent storage for a tier.
type StorageSpec struct {
	// Size is the storage size requested for each instance, e.g. "1Gi".
	// +optional
	// +kubebuilder:default="1Gi"
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClassName is the name of the StorageClass. Nil uses the default.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// MemtxSpec contains memtx engine configuration.
type MemtxSpec struct {
	// Memory is the amount of RAM allocated for memtx tuples, e.g. "128M".
	// +optional
	// +kubebuilder:default="128M"
	Memory string `json:"memory,omitempty"`
}

// VinylSpec contains vinyl engine configuration.
type VinylSpec struct {
	// Memory is the maximum RAM for the vinyl engine, e.g. "64M".
	// +optional
	// +kubebuilder:default="64M"
	Memory string `json:"memory,omitempty"`

	// Cache is the cache size for the vinyl engine, e.g. "32M".
	// +optional
	// +kubebuilder:default="32M"
	Cache string `json:"cache,omitempty"`
}

// PgSpec configures the PostgreSQL protocol listener.
type PgSpec struct {
	// Enabled controls whether the pg port listens on all interfaces (true) or only localhost.
	// +optional
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// SSL enables TLS for pgproto connections.
	// +optional
	SSL bool `json:"ssl,omitempty"`
}

// LogSpec configures Picodata instance logging.
type LogSpec struct {
	// Level sets the log verbosity.
	// +optional
	// +kubebuilder:default=info
	// +kubebuilder:validation:Enum=trace;debug;info;warn;error
	Level string `json:"level,omitempty"`

	// Format sets the log output format.
	// +optional
	// +kubebuilder:default=plain
	// +kubebuilder:validation:Enum=plain;json
	Format string `json:"format,omitempty"`

	// Destination is the log output target. Nil means stdout.
	// +optional
	Destination *string `json:"destination,omitempty"`
}

// ServiceSpec defines ports for the client-facing Service.
type ServiceSpec struct {
	// Type is the Kubernetes Service type.
	// +optional
	// +kubebuilder:default=ClusterIP
	Type corev1.ServiceType `json:"type,omitempty"`

	// BinaryPort is the iproto (binary) port.
	// +optional
	// +kubebuilder:default=3301
	BinaryPort int32 `json:"binaryPort,omitempty"`

	// HttpPort is the HTTP port (Web UI + /metrics).
	// +optional
	// +kubebuilder:default=8081
	HttpPort int32 `json:"httpPort,omitempty"`

	// PgPort is the PostgreSQL protocol port.
	// +optional
	// +kubebuilder:default=5432
	PgPort int32 `json:"pgPort,omitempty"`
}

// -----------------------------------------------------------------------
// Status
// -----------------------------------------------------------------------

// PicoclusterDBStatus defines the observed state of PicoclusterDB.
type PicoclusterDBStatus struct {
	// Phase is the high-level cluster state.
	// +optional
	Phase ClusterPhase `json:"phase,omitempty"`

	// Tiers contains per-tier observed state.
	// +optional
	Tiers []TierStatus `json:"tiers,omitempty"`

	// Conditions holds detailed conditions for the cluster.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterPhase represents the overall cluster lifecycle state.
// +kubebuilder:validation:Enum=Pending;Initializing;Ready;Degraded;Unknown
type ClusterPhase string

const (
	ClusterPhasePending      ClusterPhase = "Pending"
	ClusterPhaseInitializing ClusterPhase = "Initializing"
	ClusterPhaseReady        ClusterPhase = "Ready"
	ClusterPhaseDegraded     ClusterPhase = "Degraded"
	ClusterPhaseUnknown      ClusterPhase = "Unknown"
)

// PluginSpec describes a Picodata plugin to install on a tier.
type PluginSpec struct {
	// Name is the plugin name as registered in Picodata.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Version is the plugin version, e.g. "1.0.0".
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// MigrationContext holds key-value pairs passed to the plugin before migration runs.
	// Each entry becomes: ALTER PLUGIN <name> <version> SET migration_context.<key>='<value>'
	// +optional
	MigrationContext map[string]string `json:"migrationContext,omitempty"`

	// Services lists plugin services that expose a network listener.
	// Each service generates a listener config in config.yaml and a port in the tier Service.
	// +optional
	Services []PluginServiceSpec `json:"services,omitempty"`
}

// PluginServiceSpec configures a single plugin service listener.
type PluginServiceSpec struct {
	// Name is the service name as defined in the plugin manifest.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// ListenerPort is the port this service listens on inside the container.
	// Added to the tier's ClusterIP Service.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ListenerPort int32 `json:"listenerPort"`
}

// IngressSpec configures an optional Kubernetes Ingress for the HTTP port of a tier.
type IngressSpec struct {
	// Enabled controls whether the Ingress resource is created.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Host is the hostname the Ingress routes traffic from.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// IngressClassName sets the spec.ingressClassName field (e.g. "nginx", "traefik").
	// +optional
	IngressClassName *string `json:"ingressClassName,omitempty"`

	// Annotations are added to the Ingress metadata.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// TLS configures TLS on the Ingress.
	// +optional
	TLS []IngressTLSSpec `json:"tls,omitempty"`
}

// IngressTLSSpec mirrors networking/v1 IngressTLS.
type IngressTLSSpec struct {
	// Hosts included in the TLS certificate.
	// +optional
	Hosts []string `json:"hosts,omitempty"`

	// SecretName is the name of the secret containing the TLS certificate.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// PluginStatus reflects the observed state of a plugin on a tier.
type PluginStatus struct {
	// Name of the plugin.
	Name string `json:"name"`

	// Version currently installed and active.
	Version string `json:"version"`

	// Enabled is true when the plugin is enabled in Picodata.
	Enabled bool `json:"enabled"`
}

// TierStatus holds the observed state of a single tier.
type TierStatus struct {
	// Name of the tier.
	Name string `json:"name"`

	// ReadyReplicas is the number of pods in Ready state.
	ReadyReplicas int32 `json:"readyReplicas"`

	// DesiredReplicas is the number of pods requested in the spec.
	DesiredReplicas int32 `json:"desiredReplicas"`

	// Plugins reflects the observed state of plugins on this tier.
	// +optional
	Plugins []PluginStatus `json:"plugins,omitempty"`
}

// Condition type constants used in status.
const (
	ConditionReady = "Ready"
)

func init() {
	SchemeBuilder.Register(&PicoclusterDB{}, &PicoclusterDBList{})
}
