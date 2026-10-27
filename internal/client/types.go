package client

import (
	"encoding/json"
	"fmt"
)

// The parts of the v1 answers the provider reads. Unknown fields are
// ignored, so a newer API never breaks an older provider.

// Project is a project: instances, databases, buckets and serverless
// services grouped by what they are for.
type Project struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Description        *string `json:"description"`
	Region             string  `json:"region"`
	DeletionProtection bool    `json:"deletion_protection"`
	DeploysFrozen      bool    `json:"deploys_frozen"`
	FrozenReason       *string `json:"frozen_reason"`
	Paused             bool    `json:"paused"`
	Deleting           bool    `json:"deleting"`
	TemplateID         *string `json:"template_id"`
	ServiceCount       *int64  `json:"service_count"`
	InstanceCount      *int64  `json:"instance_count"`
	DatabaseCount      *int64  `json:"database_count"`
	BucketCount        *int64  `json:"bucket_count"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

// GitSource is where a service built from Git takes its code.
type GitSource struct {
	// GITHUB, GITLAB, BITBUCKET (a connected account) or PUBLIC_URL.
	Provider     string  `json:"provider"`
	ConnectionID *string `json:"connection_id"`
	RepoFullName *string `json:"repo_full_name"`
	RepoURL      *string `json:"repo_url"`
	Branch       *string `json:"branch"`
	TagPattern   *string `json:"tag_pattern"`
}

// ImageSource is the image a service runs.
type ImageSource struct {
	Ref                  string  `json:"ref"`
	RegistryCredentialID *string `json:"registry_credential_id"`
}

// ServiceSource is GIT, IMAGE or UPLOAD (a folder sent by `mw deploy`).
type ServiceSource struct {
	Kind  string       `json:"kind"`
	Git   *GitSource   `json:"git"`
	Image *ImageSource `json:"image"`
}

// ServiceSettings are a service's runtime and build settings.
type ServiceSettings struct {
	Size                    *string  `json:"size"`
	VCPUMillis              int64    `json:"vcpu_millis"`
	MemoryMiB               int64    `json:"memory_mib"`
	BuildCommand            *string  `json:"build_command"`
	StartCommand            *string  `json:"start_command"`
	ReleaseCommand          *string  `json:"release_command"`
	RootDir                 string   `json:"root_dir"`
	DockerfilePath          *string  `json:"dockerfile_path"`
	WatchPaths              []string `json:"watch_paths"`
	Port                    int64    `json:"port"`
	HealthCheckPath         *string  `json:"health_check_path"`
	StartupTimeoutSeconds   int64    `json:"startup_timeout_seconds"`
	MinInstances            int64    `json:"min_instances"`
	MaxInstances            int64    `json:"max_instances"`
	Concurrency             int64    `json:"concurrency"`
	IdleTimeoutSeconds      int64    `json:"idle_timeout_seconds"`
	RequestTimeoutSeconds   int64    `json:"request_timeout_seconds"`
	GracefulShutdownSeconds int64    `json:"graceful_shutdown_seconds"`
	RolloutPercent          *int64   `json:"rollout_percent"`
	RolloutMinutes          *int64   `json:"rollout_minutes"`
	Instances               int64    `json:"instances"`
	CronSchedule            *string  `json:"cron_schedule"`
	CronTimezone            string   `json:"cron_timezone"`
	CronTimeoutSeconds      int64    `json:"cron_timeout_seconds"`
}

// Service is a serverless service.
type Service struct {
	ID                 string          `json:"id"`
	ProjectID          string          `json:"project_id"`
	ProjectName        string          `json:"project_name"`
	Name               string          `json:"name"`
	Kind               string          `json:"kind"`
	Status             string          `json:"status"`
	StatusReason       *string         `json:"status_reason"`
	Source             ServiceSource   `json:"source"`
	Settings           ServiceSettings `json:"settings"`
	FileManagedFields  []string        `json:"file_managed_fields"`
	AutoDeploy         bool            `json:"auto_deploy"`
	WaitForCI          bool            `json:"wait_for_ci"`
	DeletionProtection bool            `json:"deletion_protection"`
	URL                *string         `json:"url"`
	InternalURL        *string         `json:"internal_url"`
	LiveRevisionID     *string         `json:"live_revision_id"`
	ReadyInstances     int64           `json:"ready_instances"`
	CreatedAt          string          `json:"created_at"`
}

// Deployment is the part of a deployment the provider reports.
type Deployment struct {
	ID      string `json:"id"`
	Number  int64  `json:"number"`
	Status  string `json:"status"`
	Trigger string `json:"trigger"`
}

// ServiceWithDeployment is what creating a service or changing its settings
// answers: the service, and the deployment the change started (if any).
type ServiceWithDeployment struct {
	Service    Service     `json:"service"`
	Deployment *Deployment `json:"deployment"`
}

// GitConnection is a GitHub, GitLab or Bitbucket account connected in the
// console.
type GitConnection struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	AccountLogin string `json:"account_login"`
	Status       string `json:"status"`
}

// Variable is one of a service's variables. A secret's value is never
// returned (null), and plain values only to tokens that may also write them.
type Variable struct {
	Key              string  `json:"key"`
	Value            *string `json:"value"`
	IsSecret         bool    `json:"is_secret"`
	AvailableAtBuild bool    `json:"available_at_build"`
	Version          int64   `json:"version"`
	UpdatedAt        string  `json:"updated_at"`
}

// VariableInput creates or changes a variable.
type VariableInput struct {
	Key              string  `json:"key"`
	Value            *string `json:"value,omitempty"`
	IsSecret         *bool   `json:"is_secret,omitempty"`
	AvailableAtBuild *bool   `json:"available_at_build,omitempty"`
}

// VariableChange is what setting or removing variables did.
type VariableChange struct {
	Created    []string    `json:"created"`
	Updated    []string    `json:"updated"`
	Removed    []string    `json:"removed"`
	Unchanged  []string    `json:"unchanged"`
	Deployment *Deployment `json:"deployment"`
}

// DNSInstruction is a DNS record to create for a domain. Found says whether
// the last check saw it; Option groups a root domain's alternatives.
type DNSInstruction struct {
	Type   string  `json:"type"`
	Name   string  `json:"name"`
	Value  string  `json:"value"`
	Found  *bool   `json:"found"`
	Option *string `json:"option"`
}

// DomainError says why a domain is not active yet.
type DomainError struct {
	Code    string  `json:"code"`
	Message *string `json:"message"`
}

// Domain is a service's address: its default one, or a custom domain.
type Domain struct {
	ID           string           `json:"id"`
	Hostname     string           `json:"hostname"`
	IsDefault    bool             `json:"is_default"`
	Status       string           `json:"status"`
	Apex         bool             `json:"apex"`
	RedirectTo   *string          `json:"redirect_to"`
	ViaRedirect  bool             `json:"via_redirect"`
	Instructions []DNSInstruction `json:"instructions"`
	Error        *DomainError     `json:"error"`
	ActivatedAt  *string          `json:"activated_at"`
	CreatedAt    string           `json:"created_at"`
}

// Binding is a bucket or database a service reads as variables.
type Binding struct {
	ID                  string   `json:"id"`
	ResourceKind        string   `json:"resource_kind"`
	ResourceID          string   `json:"resource_id"`
	ResourceName        *string  `json:"resource_name"`
	EnvProfile          string   `json:"env_profile"`
	EnvPrefix           string   `json:"env_prefix"`
	Access              string   `json:"access"`
	Variables           []string `json:"variables"`
	CredentialRotatedAt *string  `json:"credential_rotated_at"`
	CreatedAt           string   `json:"created_at"`
}

// BindingChange is what binding or unbinding answers.
type BindingChange struct {
	Binding    *Binding    `json:"binding"`
	Deployment *Deployment `json:"deployment"`
}

// LogDrain is a destination a project's logs are also sent to. Its URL comes
// back shortened (host and last characters); its token never comes back.
type LogDrain struct {
	ID                  string  `json:"id"`
	ProjectID           string  `json:"project_id"`
	Kind                string  `json:"kind"`
	URL                 string  `json:"url"`
	Enabled             bool    `json:"enabled"`
	Status              string  `json:"status"`
	HasToken            bool    `json:"has_token"`
	LastSentAt          *string `json:"last_sent_at"`
	LastError           *string `json:"last_error"`
	ConsecutiveFailures int64   `json:"consecutive_failures"`
	LinesSent           string  `json:"lines_sent"`
	CreatedAt           string  `json:"created_at"`
}

// OutboundAddresses are the addresses services reach the internet from.
type OutboundAddresses struct {
	Ranges      []string `json:"ranges"`
	Addresses   []string `json:"addresses"`
	AddressesV6 []string `json:"addresses_v6"`
}

// Series is a database's release series ("18", "8.4", "11.8"). The API sent
// whole numbers before MySQL's series made them strings; both are read.
type Series string

// UnmarshalJSON takes "8.4" or 18.
func (s *Series) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = Series(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("a release series is a string or a number, not %s", b)
	}
	*s = Series(n.String())
	return nil
}

// DatabaseSize is a size on offer, with what it gives.
type DatabaseSize struct {
	Code     string `json:"code"`
	CPUs     int64  `json:"cpus"`
	MemoryMB int64  `json:"memory_mb"`
	DiskGB   int64  `json:"disk_gb"`
}

// DatabaseConnection is where a database answers (never with a password).
type DatabaseConnection struct {
	Host       string  `json:"host"`
	Port       int64   `json:"port"`
	PoolerPort *int64  `json:"pooler_port"`
	Database   *string `json:"database"`
	Username   string  `json:"username"`
	URI        string  `json:"uri"`
	DirectURI  *string `json:"direct_uri"`
}

// DatabaseMaintenance is a database's weekly window.
type DatabaseMaintenance struct {
	Day     *int64 `json:"day"`
	HourUTC int64  `json:"hour_utc"`
}

// DatabaseDisk is a database's disk, and how far it grows by itself.
type DatabaseDisk struct {
	SizeGB        int64  `json:"size_gb"`
	AutogrowMaxGB *int64 `json:"autogrow_max_gb"`
}

// Database is a managed PostgreSQL, MySQL, MariaDB or Redis (Valkey)
// database.
type Database struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Engine             string              `json:"engine"`
	Version            Series              `json:"version"`
	Size               DatabaseSize        `json:"size"`
	Status             string              `json:"status"`
	Failure            *string             `json:"failure"`
	ChangesPending     bool                `json:"changes_pending"`
	ProjectID          *string             `json:"project_id"`
	Connection         DatabaseConnection  `json:"connection"`
	AllowedCIDRs       []string            `json:"allowed_cidrs"`
	Extensions         []string            `json:"extensions"`
	EvictionPolicy     *string             `json:"eviction_policy"`
	BackupsEnabled     bool                `json:"backups_enabled"`
	DeletionProtection bool                `json:"deletion_protection"`
	Maintenance        DatabaseMaintenance `json:"maintenance"`
	Disk               DatabaseDisk        `json:"disk"`
	CreatedAt          string              `json:"created_at"`
	DeletedAt          *string             `json:"deleted_at"`
}

// CreatedDatabase is a new database and its owner's password (null for a
// developer's token: an administrator reveals it).
type CreatedDatabase struct {
	Database Database `json:"database"`
	Password *string  `json:"password"`
}

// DatabaseEngineInfo is what one engine offers.
type DatabaseEngineInfo struct {
	Engine   string         `json:"engine"`
	Versions []Series       `json:"versions"`
	Sizes    []DatabaseSize `json:"sizes"`
}

// DatabasesInfo is GET /v1/databases/info.
type DatabasesInfo struct {
	Available bool                 `json:"available"`
	Engines   []DatabaseEngineInfo `json:"engines"`
}

// CorsRule is one of a bucket's CORS rules.
type CorsRule struct {
	AllowedOrigins []string `json:"allowed_origins"`
	AllowedMethods []string `json:"allowed_methods"`
	AllowedHeaders []string `json:"allowed_headers"`
	ExposeHeaders  []string `json:"expose_headers"`
	MaxAgeSeconds  *int64   `json:"max_age_seconds"`
}

// LifecycleRule expires a bucket's objects under a prefix.
type LifecycleRule struct {
	ID         string `json:"id"`
	Prefix     string `json:"prefix"`
	ExpireDays int64  `json:"expire_days"`
	Enabled    bool   `json:"enabled"`
}

// Bucket is an Object Storage bucket.
type Bucket struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Location           string          `json:"location"`
	Public             bool            `json:"public"`
	PublicURL          *string         `json:"public_url"`
	DeletionProtection bool            `json:"deletion_protection"`
	QuotaBytes         *string         `json:"quota_bytes"`
	QuotaMode          string          `json:"quota_mode"`
	SizeBytes          string          `json:"size_bytes"`
	ObjectCount        string          `json:"object_count"`
	Cors               []CorsRule      `json:"cors"`
	Lifecycle          []LifecycleRule `json:"lifecycle"`
	AbortMultipartDays *int64          `json:"abort_multipart_days"`
	Status             string          `json:"status"`
	SettingsPending    bool            `json:"settings_pending"`
	DeletedAt          *string         `json:"deleted_at"`
	PurgeAfter         *string         `json:"purge_after"`
	CreatedAt          string          `json:"created_at"`
}
