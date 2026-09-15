package filesystemmeta

import "time"

const (
	ObjectKindRegularFile = "regular_file"
	ObjectKindDirectory   = "directory"
	ObjectKindSymlink     = "symlink"
	ObjectKindHardLink    = "hard_link"
	ObjectKindSpecial     = "special"
	ObjectKindPackage     = "package"
	ObjectKindUnknown     = "unknown"
)

const (
	FidelityRiskNone              = "none"
	FidelityRiskMetadataOnly      = "metadata_only"
	FidelityRiskSkipped           = "skipped"
	FidelityRiskUnsafePermission  = "unsafe_permission"
	FidelityRiskUnsupportedObject = "unsupported_object"
	FidelityRiskExternalReference = "external_reference"
	FidelityRiskLiveMutation      = "live_mutation"
	FidelityRiskPathCollision     = "path_collision"
)

const (
	RestorePolicySafe     = "safe"
	RestorePolicyFaithful = "faithful"
	RestorePolicyRaw      = "raw"
)

const (
	FindingSeverityInfo     = "info"
	FindingSeverityWarning  = "warning"
	FindingSeverityError    = "error"
	FindingSeverityCritical = "critical"
)

const (
	FindingStatusOpen       = "open"
	FindingStatusResolved   = "resolved"
	FindingStatusSuppressed = "suppressed"
)

const (
	SourceTimeBasisFilesystemMtime     = "source_filesystem_mtime"
	SourceTimeBasisFilesystemBirthtime = "source_filesystem_birthtime"
)

type Observation struct {
	SourceModifiedAt    *time.Time `json:"source_modified_at,omitempty"`
	SourceModifiedBasis string     `json:"source_modified_basis,omitempty"`
	SourceCreatedAt     *time.Time `json:"source_created_at,omitempty"`
	SourceCreatedBasis  string     `json:"source_created_basis,omitempty"`
	Kind                string     `json:"kind"`
	SourceMode          uint32     `json:"source_mode,omitempty"`
	Executable          bool       `json:"executable"`
	UID                 *int       `json:"uid,omitempty"`
	GID                 *int       `json:"gid,omitempty"`
	UserName            string     `json:"user_name,omitempty"`
	GroupName           string     `json:"group_name,omitempty"`
	SymlinkTarget       string     `json:"symlink_target,omitempty"`
	DeviceID            *int64     `json:"device_id,omitempty"`
	Inode               *int64     `json:"inode,omitempty"`
	LinkCount           *int64     `json:"link_count,omitempty"`
	IsHardLink          bool       `json:"is_hard_link"`
	IsSparse            bool       `json:"is_sparse"`
	LogicalSizeBytes    int64      `json:"logical_size_bytes,omitempty"`
	AllocatedBytes      *int64     `json:"allocated_bytes,omitempty"`
	HasXattrs           bool       `json:"has_xattrs"`
	XattrNames          []string   `json:"xattr_names,omitempty"`
	HasACL              bool       `json:"has_acl"`
	HasResourceFork     bool       `json:"has_resource_fork"`
	HasFinderTags       bool       `json:"has_finder_tags"`
	HasQuarantine       bool       `json:"has_quarantine"`
	IsPackage           bool       `json:"is_package"`
	PackageKind         string     `json:"package_kind,omitempty"`
	UnicodeForm         string     `json:"unicode_form,omitempty"`
	CasefoldKey         string     `json:"casefold_key,omitempty"`
	Hidden              bool       `json:"hidden"`
	GeneratedMetadata   bool       `json:"generated_metadata"`
	PermissionDenied    bool       `json:"permission_denied"`
	Risks               []string   `json:"risks,omitempty"`
}

func ValidObjectKind(value string) bool {
	return validObjectKinds[value]
}

func ValidFidelityRisk(value string) bool {
	return validFidelityRisks[value]
}

func ValidRestorePolicy(value string) bool {
	return validRestorePolicies[value]
}

func ValidFindingSeverity(value string) bool {
	return validFindingSeverities[value]
}

func ValidFindingStatus(value string) bool {
	return validFindingStatuses[value]
}

var validObjectKinds = map[string]bool{
	ObjectKindRegularFile: true,
	ObjectKindDirectory:   true,
	ObjectKindSymlink:     true,
	ObjectKindHardLink:    true,
	ObjectKindSpecial:     true,
	ObjectKindPackage:     true,
	ObjectKindUnknown:     true,
}

var validFidelityRisks = map[string]bool{
	FidelityRiskNone:              true,
	FidelityRiskMetadataOnly:      true,
	FidelityRiskSkipped:           true,
	FidelityRiskUnsafePermission:  true,
	FidelityRiskUnsupportedObject: true,
	FidelityRiskExternalReference: true,
	FidelityRiskLiveMutation:      true,
	FidelityRiskPathCollision:     true,
}

var validRestorePolicies = map[string]bool{
	RestorePolicySafe:     true,
	RestorePolicyFaithful: true,
	RestorePolicyRaw:      true,
}

var validFindingSeverities = map[string]bool{
	FindingSeverityInfo:     true,
	FindingSeverityWarning:  true,
	FindingSeverityError:    true,
	FindingSeverityCritical: true,
}

var validFindingStatuses = map[string]bool{
	FindingStatusOpen:       true,
	FindingStatusResolved:   true,
	FindingStatusSuppressed: true,
}
