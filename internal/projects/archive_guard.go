package projects

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const archivedRuntimeMessage = "project is archived; restore or reactivate the project before executing project runtime"

var (
	ErrProjectRuntimeArchived           = errors.New("project runtime is archived")
	ErrProjectArchiveInProgress         = errors.New("project physical archive is in progress")
	ErrProjectArchiveUnsupportedCustody = errors.New("unsupported_custody")
	ErrProjectArchiveInvalidBinding     = errors.New("project archive identity binding is invalid")
	projectArchiveContractDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

const ProjectArchiveCanonicalCustody = "canonical_main"

// ProjectArchiveCustodyBinding is the reviewed identity and registration
// boundary for the project workspace adapter. It contains no authority to
// mutate either project state or filesystem custody.
type ProjectArchiveCustodyBinding struct {
	CustodyKind                  string `json:"custody_kind"`
	ProjectID                    string `json:"project_id"`
	ProjectScopeID               string `json:"project_scope_id"`
	ProjectScopeKey              string `json:"project_scope_key"`
	ProjectSlug                  string `json:"project_slug"`
	ProjectStatus                string `json:"project_status"`
	RegistrationID               string `json:"registration_id"`
	RegistrationRevision         int    `json:"registration_revision"`
	RegistrationStatus           string `json:"registration_status"`
	RegistrationActivationStatus string `json:"registration_activation_status"`
	RegisteredRoot               string `json:"registered_root"`
	ResolvedRoot                 string `json:"resolved_root"`
	CanonicalRoot                string `json:"canonical_root"`
	ContractPath                 string `json:"contract_path"`
	ContractHash                 string `json:"contract_hash"`
	ContractRelativePath         string `json:"contract_relative_path"`
	ContractContentDigest        string `json:"contract_content_digest"`
	ContractDeviceID             uint64 `json:"contract_device_id"`
	ContractInode                uint64 `json:"contract_inode"`
	ContractMode                 uint32 `json:"contract_mode"`
	ContractSizeBytes            int64  `json:"contract_size_bytes"`
	ContractModifiedUnixNS       int64  `json:"contract_modified_unix_ns"`
	ContractSchemaVersion        string `json:"contract_schema_version"`
	ContractKind                 string `json:"contract_kind"`
	ContractProjectID            string `json:"contract_project_id"`
	ContractProjectSlug          string `json:"contract_project_slug"`
	ContractOwnerNode            string `json:"contract_owner_node"`
	RepositoryProjectRoot        string `json:"repository_project_root,omitempty"`
	RepositorySourceRevision     int64  `json:"repository_source_revision,omitempty"`
	RepositoryMemberCount        int    `json:"repository_member_count"`
}

type ProjectArchiveUnsupportedCustodyError struct {
	ProjectID      string
	ProjectSlug    string
	OwnerNode      string
	RegisteredRoot string
	CanonicalRoot  string
	Reason         string
}

func (e ProjectArchiveUnsupportedCustodyError) Error() string {
	parts := []string{ErrProjectArchiveUnsupportedCustody.Error()}
	if e.ProjectSlug != "" {
		parts = append(parts, "project="+e.ProjectSlug)
	} else if e.ProjectID != "" {
		parts = append(parts, "project_id="+e.ProjectID)
	}
	if e.OwnerNode != "" {
		parts = append(parts, "owner_node="+e.OwnerNode)
	}
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	return strings.Join(parts, "; ")
}

func (e ProjectArchiveUnsupportedCustodyError) Unwrap() error {
	return ErrProjectArchiveUnsupportedCustody
}

func IsProjectArchiveUnsupportedCustody(err error) bool {
	return errors.Is(err, ErrProjectArchiveUnsupportedCustody)
}

type projectArchiveContractDocument struct {
	Kind          string `json:"kind"`
	SchemaVersion string `json:"schema_version"`
	Project       struct {
		ID        string `json:"id"`
		Slug      string `json:"slug"`
		OwnerNode string `json:"owner_node"`
		Status    string `json:"status"`
	} `json:"project"`
}

type projectArchiveContractFileBinding struct {
	RelativePath   string
	ContentDigest  string
	DeviceID       uint64
	Inode          uint64
	Mode           uint32
	SizeBytes      int64
	ModifiedUnixNS int64
}

// BuildProjectArchiveCustodyBinding fails closed unless the current
// registration is an active Main-owned project rooted at the exact canonical
// project workspace that the generic archive kernel will plan.
func BuildProjectArchiveCustodyBinding(detail ProjectRegistrationDetail, repository ProjectRepositoryReadModel, canonicalRoot string) (ProjectArchiveCustodyBinding, error) {
	project := detail.Project.Project
	if detail.Registration == nil {
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: registered project contract is required", ErrProjectArchiveInvalidBinding)
	}
	registration := *detail.Registration
	if strings.TrimSpace(project.ProjectID) == "" || strings.TrimSpace(project.Slug) == "" || project.Status != "active" {
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: project must have stable identity and active lifecycle", ErrProjectArchiveInvalidBinding)
	}
	if strings.TrimSpace(project.ProjectScopeID) == "" || strings.TrimSpace(registration.ProjectContractRegistrationID) == "" ||
		registration.ProjectID != project.ProjectID || registration.RegistrationRevision <= 0 || registration.RegistrationStatus != ProjectRegistrationStatusRegistered {
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: registration does not match current project identity", ErrProjectArchiveInvalidBinding)
	}
	if project.ProjectScopeKey != "project:"+project.Slug {
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: project scope key does not match slug", ErrProjectArchiveInvalidBinding)
	}

	var contract projectArchiveContractDocument
	if len(registration.Contract) == 0 || json.Unmarshal(registration.Contract, &contract) != nil {
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: registered project contract is not valid JSON", ErrProjectArchiveInvalidBinding)
	}
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Project.ID = strings.TrimSpace(contract.Project.ID)
	contract.Project.Slug = strings.TrimSpace(contract.Project.Slug)
	contract.Project.OwnerNode = strings.TrimSpace(contract.Project.OwnerNode)
	contract.Project.Status = strings.TrimSpace(contract.Project.Status)
	// Draft (including omitted status) registers as active without rewriting source.
	if contract.Kind != "loom.project" || contract.SchemaVersion != registration.ContractSchemaVersion ||
		contract.Project.ID != project.ProjectID || contract.Project.Slug != project.Slug || projectStatusFromContract(contract.Project.Status) != project.Status {
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: contract identity does not match current project and registration", ErrProjectArchiveInvalidBinding)
	}
	if !projectArchiveContractDigestPattern.MatchString(registration.ContractHash) {
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: registration contract hash is invalid", ErrProjectArchiveInvalidBinding)
	}
	if contract.Project.OwnerNode != "main" {
		return ProjectArchiveCustodyBinding{}, unsupportedProjectArchiveCustody(project, contract.Project.OwnerNode, registration.ProjectRoot, canonicalRoot, "project is not Main-owned")
	}

	canonicalResolved, err := resolveProjectArchiveDirectory(canonicalRoot)
	if err != nil {
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: canonical project root: %v", ErrProjectArchiveInvalidBinding, err)
	}
	registeredResolved, err := resolveProjectArchiveDirectory(registration.ProjectRoot)
	if err != nil {
		if filepath.Clean(strings.TrimSpace(registration.ProjectRoot)) != filepath.Clean(canonicalRoot) {
			return ProjectArchiveCustodyBinding{}, unsupportedProjectArchiveCustody(project, contract.Project.OwnerNode, registration.ProjectRoot, canonicalRoot, "registered root is not canonical Main custody")
		}
		return ProjectArchiveCustodyBinding{}, fmt.Errorf("%w: registered project root: %v", ErrProjectArchiveInvalidBinding, err)
	}
	if registeredResolved != canonicalResolved {
		return ProjectArchiveCustodyBinding{}, unsupportedProjectArchiveCustody(project, contract.Project.OwnerNode, registeredResolved, canonicalResolved, "registered root is outside canonical Main custody")
	}
	contractFile, err := authenticateProjectArchiveContract(registration.ContractPath, registration.ProjectRoot, registeredResolved, registration.ContractHash)
	if err != nil {
		return ProjectArchiveCustodyBinding{}, err
	}
	if err := validateProjectArchiveRepositoryBinding(project, registration, repository, canonicalResolved); err != nil {
		return ProjectArchiveCustodyBinding{}, err
	}

	binding := ProjectArchiveCustodyBinding{
		CustodyKind:                  ProjectArchiveCanonicalCustody,
		ProjectID:                    project.ProjectID,
		ProjectScopeID:               project.ProjectScopeID,
		ProjectScopeKey:              project.ProjectScopeKey,
		ProjectSlug:                  project.Slug,
		ProjectStatus:                project.Status,
		RegistrationID:               registration.ProjectContractRegistrationID,
		RegistrationRevision:         registration.RegistrationRevision,
		RegistrationStatus:           registration.RegistrationStatus,
		RegistrationActivationStatus: registration.ActivationStatus,
		RegisteredRoot:               filepath.Clean(registration.ProjectRoot),
		ResolvedRoot:                 registeredResolved,
		CanonicalRoot:                filepath.Clean(canonicalRoot),
		ContractPath:                 filepath.Clean(registration.ContractPath),
		ContractHash:                 registration.ContractHash,
		ContractRelativePath:         contractFile.RelativePath,
		ContractContentDigest:        contractFile.ContentDigest,
		ContractDeviceID:             contractFile.DeviceID,
		ContractInode:                contractFile.Inode,
		ContractMode:                 contractFile.Mode,
		ContractSizeBytes:            contractFile.SizeBytes,
		ContractModifiedUnixNS:       contractFile.ModifiedUnixNS,
		ContractSchemaVersion:        registration.ContractSchemaVersion,
		ContractKind:                 contract.Kind,
		ContractProjectID:            contract.Project.ID,
		ContractProjectSlug:          contract.Project.Slug,
		ContractOwnerNode:            contract.Project.OwnerNode,
		RepositoryMemberCount:        len(repository.Members),
	}
	if repository.Source != nil {
		binding.RepositoryProjectRoot = filepath.Clean(repository.Source.ProjectRoot)
		binding.RepositorySourceRevision = repository.Source.SourceRevision
	}
	return binding, nil
}

func unsupportedProjectArchiveCustody(project Project, ownerNode, registeredRoot, canonicalRoot, reason string) error {
	return ProjectArchiveUnsupportedCustodyError{
		ProjectID: project.ProjectID, ProjectSlug: project.Slug, OwnerNode: ownerNode,
		RegisteredRoot: registeredRoot, CanonicalRoot: canonicalRoot, Reason: reason,
	}
}

func resolveProjectArchiveDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("path must be absolute and clean")
	}
	info, err := os.Lstat(value)
	if err != nil {
		return "", err
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("path is not a directory")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func authenticateProjectArchiveContract(contractPath, registeredRoot, resolvedRoot, expectedDigest string) (projectArchiveContractFileBinding, error) {
	contractPath = strings.TrimSpace(contractPath)
	if contractPath == "" || !filepath.IsAbs(contractPath) || filepath.Clean(contractPath) != contractPath {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract path must be absolute and clean", ErrProjectArchiveInvalidBinding)
	}
	registeredRoot = filepath.Clean(strings.TrimSpace(registeredRoot))
	relative, err := filepath.Rel(registeredRoot, contractPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract path is outside registered project root", ErrProjectArchiveInvalidBinding)
	}
	relative = filepath.Clean(relative)
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 0 {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract path is invalid", ErrProjectArchiveInvalidBinding)
	}
	rootInfo, err := os.Lstat(resolvedRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: bound project root is not a real directory", ErrProjectArchiveInvalidBinding)
	}
	rootFD, err := unix.Open(resolvedRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: open bound project root: %v", ErrProjectArchiveInvalidBinding, err)
	}
	fds := []int{}
	defer func() {
		for index := len(fds) - 1; index >= 0; index-- {
			_ = unix.Close(fds[index])
		}
	}()
	rootFile := os.NewFile(uintptr(rootFD), resolvedRoot)
	if rootFile == nil {
		_ = unix.Close(rootFD)
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: hold bound project root", ErrProjectArchiveInvalidBinding)
	}
	defer rootFile.Close()
	openedRootInfo, err := rootFile.Stat()
	if err != nil || !os.SameFile(rootInfo, openedRootInfo) {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: bound project root changed while opening", ErrProjectArchiveInvalidBinding)
	}

	parentFD := rootFD
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\\`) {
			return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract path has unsafe component", ErrProjectArchiveInvalidBinding)
		}
		nextFD, err := unix.Openat(parentFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return projectArchiveContractFileBinding{}, fmt.Errorf("%w: open contract parent without following links: %v", ErrProjectArchiveInvalidBinding, err)
		}
		fds = append(fds, nextFD)
		parentFD = nextFD
	}
	name := parts[len(parts)-1]
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract filename is unsafe", ErrProjectArchiveInvalidBinding)
	}
	var namedBefore unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &namedBefore, unix.AT_SYMLINK_NOFOLLOW); err != nil || namedBefore.Mode&unix.S_IFMT != unix.S_IFREG {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract entry is not a no-follow regular file", ErrProjectArchiveInvalidBinding)
	}
	contractFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: open contract without following links: %v", ErrProjectArchiveInvalidBinding, err)
	}
	file := os.NewFile(uintptr(contractFD), contractPath)
	if file == nil {
		_ = unix.Close(contractFD)
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: hold contract file", ErrProjectArchiveInvalidBinding)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: opened contract is not a regular file", ErrProjectArchiveInvalidBinding)
	}
	openedStat, ok := openedInfo.Sys().(*syscall.Stat_t)
	if !ok || uint64(openedStat.Dev) != uint64(namedBefore.Dev) || uint64(openedStat.Ino) != uint64(namedBefore.Ino) {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract entry changed while opening", ErrProjectArchiveInvalidBinding)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: hash live contract: %v", ErrProjectArchiveInvalidBinding, err)
	}
	afterInfo, err := file.Stat()
	if err != nil || !stableProjectArchiveContractInfo(openedInfo, afterInfo) {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract changed while hashing", ErrProjectArchiveInvalidBinding)
	}
	var namedAfter unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &namedAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		uint64(namedAfter.Dev) != uint64(openedStat.Dev) || uint64(namedAfter.Ino) != uint64(openedStat.Ino) || namedAfter.Mode&unix.S_IFMT != unix.S_IFREG {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: contract entry was substituted while hashing", ErrProjectArchiveInvalidBinding)
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if digest != expectedDigest {
		return projectArchiveContractFileBinding{}, fmt.Errorf("%w: live contract content does not match registered contract_hash", ErrProjectArchiveInvalidBinding)
	}
	return projectArchiveContractFileBinding{
		RelativePath: filepath.ToSlash(relative), ContentDigest: digest,
		DeviceID: uint64(openedStat.Dev), Inode: uint64(openedStat.Ino), Mode: uint32(openedInfo.Mode()),
		SizeBytes: openedInfo.Size(), ModifiedUnixNS: openedInfo.ModTime().UnixNano(),
	}, nil
}

func stableProjectArchiveContractInfo(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func validateProjectArchiveRepositoryBinding(project Project, registration ProjectContractRegistration, repository ProjectRepositoryReadModel, canonicalRoot string) error {
	if repository.Project.ProjectID != project.ProjectID || repository.Project.ProjectScopeID != project.ProjectScopeID ||
		repository.Project.ProjectScopeKey != project.ProjectScopeKey || repository.Project.Slug != project.Slug || repository.Project.Status != project.Status {
		return fmt.Errorf("%w: repository snapshot does not match current project identity", ErrProjectArchiveInvalidBinding)
	}
	if repository.Source == nil {
		if len(repository.Members) != 0 || registration.ContractSchemaVersion == ProjectRepositoryProjectSchemaV05 {
			return fmt.Errorf("%w: repository members exist without source", ErrProjectArchiveInvalidBinding)
		}
		return nil
	}
	source := repository.Source
	if source.ProjectContractRegistrationID != registration.ProjectContractRegistrationID || source.SourceRevision <= 0 ||
		source.ProjectContractSchemaVersion != registration.ContractSchemaVersion || source.OwnerNode != "main" ||
		!projectArchiveContractDigestPattern.MatchString(source.SemanticDigest) || !projectArchiveContractDigestPattern.MatchString(source.LocationDigest) {
		return fmt.Errorf("%w: repository source does not match current registration", ErrProjectArchiveInvalidBinding)
	}
	if !isSupportedProjectRepositorySourceVersions(ProjectRepositorySourceVersions{ProjectContract: source.ProjectContractSchemaVersion, ReposContract: source.ReposContractSchemaVersion}) {
		return fmt.Errorf("%w: repository source contract version is unsupported", ErrProjectArchiveInvalidBinding)
	}
	resolved, err := resolveProjectArchiveDirectory(source.ProjectRoot)
	if err != nil || resolved != canonicalRoot {
		return fmt.Errorf("%w: repository source root does not match canonical custody", ErrProjectArchiveInvalidBinding)
	}
	previous := ""
	for index, member := range repository.Members {
		key := member.RepositoryID + "\x00" + member.Key
		if strings.TrimSpace(member.RepositoryID) == "" || strings.TrimSpace(member.Key) == "" || strings.TrimSpace(member.SourceBindingDigest) == "" ||
			!projectArchiveContractDigestPattern.MatchString(member.SourceBindingDigest) || member.ObservationRevision <= 0 || (index > 0 && key <= previous) {
			return fmt.Errorf("%w: repository members must be complete, ordered, and unique", ErrProjectArchiveInvalidBinding)
		}
		if member.MembershipLifecycle != RepositoryLifecycleActive {
			return fmt.Errorf("%w: repository membership is not active", ErrProjectArchiveInvalidBinding)
		}
		switch member.Role {
		case ProjectRepositoryRolePrimary, ProjectRepositoryRoleComponent:
			if member.RepositoryOwnerProjectID != project.ProjectID || member.RepositoryLifecycle != RepositoryLifecycleActive {
				return fmt.Errorf("%w: owning repository member does not match project lifecycle", ErrProjectArchiveInvalidBinding)
			}
		case ProjectRepositoryRoleReference:
			if member.RepositoryOwnerProjectID == project.ProjectID {
				return fmt.Errorf("%w: reference repository member claims project ownership", ErrProjectArchiveInvalidBinding)
			}
		default:
			return fmt.Errorf("%w: owning repository member does not match project", ErrProjectArchiveInvalidBinding)
		}
		previous = key
	}
	return nil
}

type RuntimeStatusQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type RuntimeRef struct {
	ProjectID    string
	ProjectRef   string
	ScopeID      string
	ScopeRef     string
	ResourceKind string
	ResourceRef  string
}

type RuntimeArchivedError struct {
	ProjectID     string
	ProjectSlug   string
	ProjectStatus string
	ScopeID       string
	ScopeStatus   string
	ResourceKind  string
	ResourceRef   string
}

type ProjectArchiveInProgressError struct {
	ProjectID    string
	ProjectSlug  string
	OperationID  string
	Phase        ProjectPhysicalArchivePhase
	ResourceKind string
	ResourceRef  string
}

func (e ProjectArchiveInProgressError) Error() string {
	parts := []string{"project physical archive is in progress; recover the exact operation before executing project runtime"}
	if e.ProjectSlug != "" {
		parts = append(parts, "project="+e.ProjectSlug)
	} else if e.ProjectID != "" {
		parts = append(parts, "project_id="+e.ProjectID)
	}
	if e.OperationID != "" {
		parts = append(parts, "operation_id="+e.OperationID)
	}
	if e.Phase != "" {
		parts = append(parts, "phase="+string(e.Phase))
	}
	if e.ResourceKind != "" || e.ResourceRef != "" {
		parts = append(parts, "resource="+strings.Trim(strings.TrimSpace(e.ResourceKind)+":"+strings.TrimSpace(e.ResourceRef), ":"))
	}
	return strings.Join(parts, "; ")
}

func (e ProjectArchiveInProgressError) Unwrap() error { return ErrProjectArchiveInProgress }

func IsProjectArchiveInProgress(err error) bool { return errors.Is(err, ErrProjectArchiveInProgress) }

func (e RuntimeArchivedError) Error() string {
	parts := []string{archivedRuntimeMessage}
	if e.ProjectSlug != "" {
		parts = append(parts, "project="+e.ProjectSlug)
	} else if e.ProjectID != "" {
		parts = append(parts, "project_id="+e.ProjectID)
	}
	if e.ResourceKind != "" || e.ResourceRef != "" {
		parts = append(parts, "resource="+strings.Trim(strings.TrimSpace(e.ResourceKind)+":"+strings.TrimSpace(e.ResourceRef), ":"))
	}
	return strings.Join(parts, "; ")
}

func (e RuntimeArchivedError) Unwrap() error {
	return ErrProjectRuntimeArchived
}

func IsProjectRuntimeArchived(err error) bool {
	return errors.Is(err, ErrProjectRuntimeArchived)
}

func EnsureProjectMutable(project Project, resourceKind, resourceRef string) error {
	if state, ok := ParseProjectPhysicalArchiveState(project.ArchiveState); ok && state.Restore != nil {
		return ProjectRestoreBlockedError{ProjectID: project.ProjectID, OperationID: state.Restore.OperationID, Phase: state.Restore.Phase}
	}
	if state, ok := ParseProjectPhysicalArchiveState(project.ArchiveState); ok && state.Status == ProjectPhysicalArchiveStatusInProgress {
		return ProjectArchiveInProgressError{
			ProjectID: project.ProjectID, ProjectSlug: project.Slug, OperationID: state.OperationID, Phase: state.Phase,
			ResourceKind: strings.TrimSpace(resourceKind), ResourceRef: strings.TrimSpace(resourceRef),
		}
	} else if !ok && projectPhysicalArchiveStateRawPresent(project.ArchiveState) {
		return ProjectArchiveInProgressError{
			ProjectID: project.ProjectID, ProjectSlug: project.Slug, Phase: ProjectPhysicalArchivePhase("invalid_state"),
			ResourceKind: strings.TrimSpace(resourceKind), ResourceRef: strings.TrimSpace(resourceRef),
		}
	}
	if strings.EqualFold(strings.TrimSpace(project.Status), "archived") {
		return RuntimeArchivedError{
			ProjectID:     project.ProjectID,
			ProjectSlug:   project.Slug,
			ProjectStatus: project.Status,
			ScopeID:       project.ProjectScopeID,
			ScopeStatus:   "archived",
			ResourceKind:  strings.TrimSpace(resourceKind),
			ResourceRef:   strings.TrimSpace(resourceRef),
		}
	}
	return nil
}

func EnsureProjectRegistrationMutable(detail ProjectRegistrationDetail, resourceKind, resourceRef string) error {
	return EnsureProjectMutable(detail.Project.Project, resourceKind, resourceRef)
}

func EnsureRuntimeActive(ctx context.Context, q RuntimeStatusQuerier, ref RuntimeRef) error {
	if q == nil {
		return fmt.Errorf("database is required")
	}
	ref = normalizeRuntimeRef(ref)
	if ref.ProjectID != "" {
		if err := ensureRuntimeProjectActive(ctx, q, ref.ProjectID, ref); err != nil {
			return err
		}
	}
	if ref.ProjectRef != "" && ref.ProjectRef != ref.ProjectID {
		if err := ensureRuntimeProjectActive(ctx, q, ref.ProjectRef, ref); err != nil {
			return err
		}
	}
	if ref.ScopeID != "" {
		if err := ensureRuntimeScopeActive(ctx, q, ref.ScopeID, ref); err != nil {
			return err
		}
	}
	if ref.ScopeRef != "" && ref.ScopeRef != ref.ScopeID {
		if err := ensureRuntimeScopeActive(ctx, q, ref.ScopeRef, ref); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRuntimeRef(ref RuntimeRef) RuntimeRef {
	ref.ProjectID = strings.TrimSpace(ref.ProjectID)
	ref.ProjectRef = strings.TrimSpace(ref.ProjectRef)
	ref.ScopeID = strings.TrimSpace(ref.ScopeID)
	ref.ScopeRef = strings.TrimSpace(ref.ScopeRef)
	ref.ResourceKind = strings.TrimSpace(ref.ResourceKind)
	ref.ResourceRef = strings.TrimSpace(ref.ResourceRef)
	return ref
}

func ensureRuntimeProjectActive(ctx context.Context, q RuntimeStatusQuerier, projectRef string, runtimeRef RuntimeRef) error {
	status, err := scanRuntimeStatus(q.QueryRowContext(ctx, `
		SELECT p.project_id, p.slug, p.status, s.scope_id, s.status, p.archive_state
		FROM projects.projects p
		JOIN scopes.scopes s ON s.scope_id = p.project_scope_id
		WHERE p.project_id = $1 OR p.slug = $1
		ORDER BY CASE WHEN p.project_id = $1 THEN 0 ELSE 1 END
		LIMIT 1
	`, projectRef))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resolve project runtime project %q: %w", projectRef, err)
	}
	if err != nil {
		return err
	}
	return status.archivedError(runtimeRef)
}

func ensureRuntimeScopeActive(ctx context.Context, q RuntimeStatusQuerier, scopeRef string, runtimeRef RuntimeRef) error {
	status, err := scanRuntimeStatus(q.QueryRowContext(ctx, `
		SELECT p.project_id, p.slug, COALESCE(p.status, ''), s.scope_id, s.status, COALESCE(p.archive_state, '{}'::jsonb)
		FROM scopes.scopes s
		LEFT JOIN projects.projects p ON p.project_scope_id = s.scope_id
		WHERE s.scope_id = $1 OR s.scope_key = $1 OR s.slug = $1
		ORDER BY CASE WHEN s.scope_id = $1 THEN 0 WHEN s.scope_key = $1 THEN 1 ELSE 2 END
		LIMIT 1
	`, scopeRef))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resolve project runtime scope %q: %w", scopeRef, err)
	}
	if err != nil {
		return err
	}
	if status.ProjectID == "" {
		return nil
	}
	return status.archivedError(runtimeRef)
}

type runtimeStatus struct {
	ProjectID     string
	ProjectSlug   string
	ProjectStatus string
	ScopeID       string
	ScopeStatus   string
	ArchiveState  json.RawMessage
}

func scanRuntimeStatus(row *sql.Row) (runtimeStatus, error) {
	var status runtimeStatus
	var projectID, slug, projectStatus sql.NullString
	var archiveState []byte
	if err := row.Scan(&projectID, &slug, &projectStatus, &status.ScopeID, &status.ScopeStatus, &archiveState); err != nil {
		return runtimeStatus{}, err
	}
	status.ProjectID = projectID.String
	status.ProjectSlug = slug.String
	status.ProjectStatus = projectStatus.String
	status.ArchiveState = append(json.RawMessage(nil), archiveState...)
	return status, nil
}

func (s runtimeStatus) archivedError(ref RuntimeRef) error {
	if state, ok := ParseProjectPhysicalArchiveState(s.ArchiveState); ok && state.Restore != nil {
		return ProjectRestoreBlockedError{ProjectID: s.ProjectID, OperationID: state.Restore.OperationID, Phase: state.Restore.Phase}
	}
	if state, ok := ParseProjectPhysicalArchiveState(s.ArchiveState); ok && state.Status == ProjectPhysicalArchiveStatusInProgress {
		return ProjectArchiveInProgressError{
			ProjectID: s.ProjectID, ProjectSlug: s.ProjectSlug, OperationID: state.OperationID, Phase: state.Phase,
			ResourceKind: ref.ResourceKind, ResourceRef: ref.ResourceRef,
		}
	} else if !ok && projectPhysicalArchiveStateRawPresent(s.ArchiveState) {
		return ProjectArchiveInProgressError{
			ProjectID: s.ProjectID, ProjectSlug: s.ProjectSlug, Phase: ProjectPhysicalArchivePhase("invalid_state"),
			ResourceKind: ref.ResourceKind, ResourceRef: ref.ResourceRef,
		}
	}
	if s.ProjectStatus != "archived" && s.ScopeStatus != "archived" {
		return nil
	}
	return RuntimeArchivedError{
		ProjectID:     s.ProjectID,
		ProjectSlug:   s.ProjectSlug,
		ProjectStatus: s.ProjectStatus,
		ScopeID:       s.ScopeID,
		ScopeStatus:   s.ScopeStatus,
		ResourceKind:  ref.ResourceKind,
		ResourceRef:   ref.ResourceRef,
	}
}
