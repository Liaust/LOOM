package knowledge

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// SourceContext describes source material, never qualified semantic authority.
type SourceContext struct {
	SourceCategory string `json:"source_category,omitempty"`
	SourcePosture  string `json:"source_posture,omitempty"`
	Declaration    string `json:"declaration,omitempty"`
	TopicKey       string `json:"topic_key,omitempty"`
	CollectionKey  string `json:"collection_key,omitempty"`
}

func sourceCategory(kind string) string {
	switch kind {
	case RootKindBoxNotes:
		return "notes"
	case RootKindBoxTopics:
		return "topics"
	case RootKindBoxLibrary:
		return "library"
	case RootKindProjectNotes, RootKindProjectMaterial:
		return "projects"
	default:
		return ""
	}
}

func validateSourceCategory(category string) error {
	switch category {
	case "", "notes", "topics", "library", "projects":
		return nil
	default:
		return fmt.Errorf("%w: unsupported source category %q", ErrInvalid, category)
	}
}

func ValidateSourceCategory(category string) error { return validateSourceCategory(category) }

func sourceCategorySQL(kind string) string {
	return "CASE " + kind + " WHEN 'box_notes' THEN 'notes' WHEN 'box_topics' THEN 'topics' WHEN 'box_library' THEN 'library' WHEN 'project_notes' THEN 'projects' WHEN 'project_material' THEN 'projects' ELSE '' END"
}

func sourceContextForRoot(root SourceRoot, relativePath string) SourceContext {
	context := SourceContext{SourceCategory: sourceCategory(root.RootKind)}
	if context.SourceCategory == "" {
		return context
	}
	context.SourcePosture = "source_material"
	registration, _ := jsonObject(root.Metadata)["registration_metadata"].(map[string]any)
	declaration, _ := registration["knowledge_source"].(map[string]any)
	if root.RootKind == RootKindProjectMaterial {
		context.Declaration = stringMapValue(declaration, "declaration")
	}
	var area string
	switch root.RootKind {
	case RootKindBoxTopics:
		area, context.SourcePosture = "Topics", "draft"
	case RootKindBoxLibrary:
		area, context.SourcePosture = "Library", "attributed_claim"
	}
	if area != "" {
		local := strings.TrimPrefix(strings.TrimPrefix(root.RootRelativePath, area), "/")
		if relativePath != "" {
			local = path.Join(local, relativePath)
		}
		// Only a declared area subdirectory supplies grouping; a root file does not.
		if head, _, found := strings.Cut(local, "/"); (found || relativePath == "") && head != "" && head != "." && head != ".." {
			if area == "Topics" {
				context.TopicKey = head
			} else {
				context.CollectionKey = head
			}
		}
	}
	return context
}

func sourceContextFromObject(metadata json.RawMessage, relativePath string) SourceContext {
	rootData, _ := jsonObject(metadata)["source_root"].(map[string]any)
	root := SourceRoot{RootKind: stringMapValue(rootData, "root_kind"), RootRelativePath: stringMapValue(rootData, "root_relative_path")}
	root.Metadata, _ = json.Marshal(map[string]any{"registration_metadata": map[string]any{"knowledge_source": rootData["knowledge_source"]}})
	return sourceContextForRoot(root, relativePath)
}

func sourceDeclaration(root SourceRoot) any {
	registration, _ := jsonObject(root.Metadata)["registration_metadata"].(map[string]any)
	return registration["knowledge_source"]
}

func sourceContextFromRootJSON(raw []byte, relativePath string) SourceContext {
	var root SourceRoot
	if json.Unmarshal(raw, &root) != nil {
		return SourceContext{}
	}
	return sourceContextForRoot(root, relativePath)
}

func sourceContextRootSQL(root string) string {
	return "jsonb_build_object('root_kind', " + root + ".root_kind, 'root_relative_path', " + root + ".root_relative_path, 'metadata', " + root + ".metadata)"
}

// New roots retain admission's exact declaration and catalog evidence. A changed
// policy or source observation hides prior publications until eligible admission.
// This runs before query limits, so excluded hits cannot crowd out valid ones.
func visibleNotesKnowledgeObjectSQL(object string) string {
	return notesKnowledgeVisibilitySQL(object, true)
}

// Admission and current reads must use the same scope authority. Box areas are
// owned scopes, not projects; a matching watched-root name alone is insufficient.
func notesSyncedScopeSQL(root, scope string) string {
	return fmt.Sprintf(`((%[1]s.root_kind IN ('project_notes','project_material') AND EXISTS (
	 SELECT 1 FROM projects.projects scoped_project WHERE scoped_project.project_id = %[1]s.project_id
	 AND scoped_project.status = 'active' AND scoped_project.project_scope_id = %[2]s.scope_id
	)) OR (%[1]s.root_kind IN ('box_notes','box_topics','box_library')
	 AND %[1]s.project_id IS NULL AND %[1]s.node_id IS NOT NULL AND %[1]s.node_key <> ''
	 AND COALESCE(%[1]s.metadata->>'box_id','') <> ''
	 AND %[2]s.scope_type = 'box_area' AND %[2]s.status = 'active'
	 AND %[2]s.home_node_id = %[1]s.node_id
	 AND %[2]s.scope_key = 'loom_box:' || (%[1]s.metadata->>'box_id') || ':' || substring(%[1]s.root_kind from 5)
	 AND %[2]s.metadata->>'box_id' = %[1]s.metadata->>'box_id'
	 AND %[2]s.metadata->>'box_area' = substring(%[1]s.root_kind from 5)
	 AND %[2]s.metadata->>'owner_node_id' = %[1]s.node_id
	 AND %[2]s.metadata->>'owner_node_key' = %[1]s.node_key))`, root, scope)
}

func notesSyncedBoxOriginSQL(root, version, file, replica string) string {
	return fmt.Sprintf(`(%[1]s.root_kind NOT IN ('box_notes','box_topics','box_library') OR (
	 %[1]s.backend_root_key <> ''
	 AND %[2]s.source_node_id = %[1]s.node_id AND %[3]s.source_node_id = %[1]s.node_id
	 AND %[4]s.source_node_id = %[1]s.node_id
	 AND EXISTS (SELECT 1 FROM nodes.nodes source_owner WHERE source_owner.node_id = %[1]s.node_id
	 AND source_owner.node_key = %[1]s.node_key AND source_owner.status = 'active')
	 AND starts_with(%[2]s.source_path, 'watched-root://' || %[1]s.backend_root_key || '/')))`, root, version, file, replica)
}

// A logical source can have multiple retained Objects identities. Select by
// immutable acceptance order, before eligibility or pagination; a newer private
// or unavailable version must not resurrect an older source. Reads share this
// fence so a prior publication stops being current before the next admission.
func notesSyncedCurrentSourceSQL(scope, object, version, file string) string {
	return fmt.Sprintf(`NOT EXISTS (
	 SELECT 1 FROM objects.objects newer_object
	 JOIN files.file_metadata newer_file ON newer_file.object_id = newer_object.object_id
	 JOIN objects.object_versions newer_version ON newer_version.object_version_id = newer_file.latest_version_id
	 AND newer_version.object_id = newer_object.object_id
	 WHERE EXISTS (SELECT 1 FROM objects.object_scope_links newer_scope
	 WHERE newer_scope.scope_id = %[1]s.scope_id AND newer_scope.object_id = newer_object.object_id
	 AND newer_scope.relevance_status = 'active')
	 AND COALESCE(newer_version.source_node_id, newer_file.source_node_id)
	 IS NOT DISTINCT FROM COALESCE(%[3]s.source_node_id, %[4]s.source_node_id)
	 AND COALESCE(newer_version.source_path, newer_file.source_path, newer_object.metadata->>'source_path', '')
	 = COALESCE(%[3]s.source_path, %[4]s.source_path, %[2]s.metadata->>'source_path', '')
	 AND (newer_version.created_at, newer_version.object_version_id) > (%[3]s.created_at, %[3]s.object_version_id)
	)`, scope, object, version, file)
}

// Passage reads apply the same current-evidence fence to legacy roots too.
func notesKnowledgeVisibilitySQL(object string, legacySearch bool) string {
	return notesKnowledgeVisibilityPolicySQL(object, legacySearch, false)
}

func notesKnowledgeArchiveVisibilitySQL(object string) string {
	return notesKnowledgeVisibilityPolicySQL(object, false, true)
}

func notesKnowledgeVisibilityPolicySQL(object string, legacySearch, archiveRead bool) string {
	legacy := ""
	declaration := "COALESCE(" + object + ".metadata->'source_root'->'knowledge_source','null'::jsonb) = COALESCE(visibility_root.metadata->'registration_metadata'->'knowledge_source','null'::jsonb)"
	rootReadable := "visibility_root.status = 'active'"
	scopeReadable := notesSyncedScopeSQL("visibility_root", "visibility_scope_owner")
	if archiveRead {
		stop := notesProjectArchiveStopSQL("visibility_root", object, notesReadArchiveOperationSQL(object))
		rootReadable = `((visibility_root.status='active' AND (visibility_root.project_id IS NULL OR EXISTS (
		 SELECT 1 FROM projects.project_watched_root_registrations read_writer
		 WHERE read_writer.project_watched_root_registration_id=visibility_root.project_watched_root_registration_id
		 AND read_writer.project_id=visibility_root.project_id AND read_writer.node_id=visibility_root.node_id
		 AND read_writer.owner_node_key=visibility_root.node_key AND read_writer.backend_root_key=visibility_root.backend_root_key
		 AND read_writer.root_relative_path=visibility_root.root_relative_path
		 AND read_writer.activation_status IN ('applied','reported')
		 AND read_writer.metadata=visibility_root.metadata->'registration_metadata')))
		 OR ` + stop + ` OR ` + declarationLegacyNotesEvidenceSQL("visibility_root", object, true) + `)`
		scopeReadable = `(` + scopeReadable + ` OR (` + stop + ` AND EXISTS (
		 SELECT 1 FROM projects.projects read_project WHERE read_project.project_id=visibility_root.project_id
		 AND read_project.project_scope_id=visibility_scope_owner.scope_id)))`
		declaration = `COALESCE(` + object + `.metadata->'source_root'->'knowledge_source','null'::jsonb) = COALESCE(` + notesEffectiveDeclarationSQL("visibility_root", object, notesReadArchiveOperationSQL(object)) + `,'null'::jsonb)`
	}
	if legacySearch {
		legacy = "(visibility_root.root_kind NOT IN ('box_topics','box_library','project_material') AND (visibility_root.root_kind <> 'box_notes' OR " + object + ".storage_entry_id IS NOT NULL)) OR "
		declaration = "CASE WHEN visibility_root.root_kind = 'box_notes' THEN " + declaration + " ELSE " + object + ".metadata->'source_root'->'knowledge_source' = visibility_root.metadata->'registration_metadata'->'knowledge_source' END"
	}
	return `EXISTS (SELECT 1 FROM knowledge.notes_source_roots visibility_root
	 WHERE visibility_root.notes_source_root_id = ` + object + `.notes_source_root_id
	 AND ` + declarationEnrollmentEvidenceSQL("visibility_root", object, archiveRead) + `
	 AND (` + legacy + `(
	 ` + rootReadable + `
	 AND ` + object + `.source_node_key = visibility_root.node_key
	 AND ` + object + `.project_id IS NOT DISTINCT FROM visibility_root.project_id
	 AND ` + object + `.metadata->'source_root'->>'source_path' = visibility_root.source_path
	 AND ` + object + `.metadata->'source_root'->>'root_relative_path' = visibility_root.root_relative_path
	 AND ` + declaration + `
	 AND ((` + object + `.storage_entry_id IS NULL AND EXISTS (
	 SELECT 1 FROM files.file_metadata visibility_file
	 JOIN objects.objects visibility_object ON visibility_object.object_id = visibility_file.object_id
	 JOIN objects.object_versions visibility_version ON visibility_version.object_version_id = visibility_file.latest_version_id
	 JOIN objects.object_scope_links visibility_scope ON visibility_scope.object_id = visibility_object.object_id
	 JOIN scopes.scopes visibility_scope_owner ON visibility_scope_owner.scope_id = visibility_scope.scope_id
	 JOIN sync.replicas visibility_replica ON visibility_replica.replica_id = ` + object + `.metadata->'synced_object'->>'replica_id'
	 WHERE visibility_file.object_id = ` + object + `.metadata->'synced_object'->>'object_id'
	 AND visibility_file.latest_version_id = ` + object + `.metadata->'synced_object'->>'object_version_id'
	 AND visibility_version.object_id = visibility_object.object_id
	 AND visibility_file.index_policy IS DISTINCT FROM 'private_no_index'
	 AND visibility_scope.relevance_status = 'active'
	 AND ` + scopeReadable + `
	 AND ` + notesSyncedBoxOriginSQL("visibility_root", "visibility_version", "visibility_file", "visibility_replica") + `
	 AND ` + notesSyncedCurrentSourceSQL("visibility_scope_owner", "visibility_object", "visibility_version", "visibility_file") + `
	 AND (visibility_root.root_kind NOT IN ('box_notes','box_topics','box_library') OR visibility_scope_owner.scope_key = ` + object + `.metadata->'synced_object'->>'scope_key')
	 AND visibility_object.status = 'active' AND visibility_object.object_type = 'file' AND visibility_version.status = 'active'
	 AND visibility_replica.replicated_kind = 'object_version' AND visibility_replica.replicated_id = visibility_version.object_version_id AND visibility_replica.freshness_state = 'fresh'
	 AND visibility_file.metadata = COALESCE(` + object + `.metadata->'file_metadata','{}'::jsonb)
	 AND visibility_object.metadata = COALESCE(` + object + `.metadata->'object_metadata','{}'::jsonb)
	 AND visibility_version.metadata = COALESCE(` + object + `.metadata->'version_metadata','{}'::jsonb)
	 )) OR EXISTS (
	 SELECT 1 FROM storage.storage_entries visibility_entry
	 WHERE visibility_entry.storage_entry_id = ` + object + `.storage_entry_id
	 AND visibility_entry.deleted_at IS NULL
	 AND visibility_entry.availability_state = 'available'
	 AND visibility_entry.origin_node_key = ` + object + `.source_node_key
	 AND visibility_entry.project_id IS NOT DISTINCT FROM ` + object + `.project_id
	 AND visibility_entry.watched_root_key = visibility_root.backend_root_key
	 AND visibility_entry.metadata = COALESCE(` + object + `.metadata->'storage_metadata','{}'::jsonb)
	 AND visibility_entry.checksum_hex = COALESCE(` + object + `.metadata->'storage_entry'->>'checksum_hex','')
	 )))))`
}

const ignoredNotesKnowledgeContractReason = "ignored_notes_control_contract"

func ignoredNotesKnowledgeRelativePath(relativePath string) bool {
	value := strings.TrimSpace(strings.ReplaceAll(relativePath, "\\", "/"))
	if value == "" {
		return false
	}
	cleaned := path.Clean(value)
	base := strings.ToLower(path.Base(cleaned))
	switch base {
	case "loom.notes.yaml", "loom.notes.yml":
		return true
	default:
		return false
	}
}

func visibleNotesKnowledgeRelativePathSQL(column string) string {
	return "(lower(" + column + ") NOT IN ('loom.notes.yaml', 'loom.notes.yml') AND lower(" + column + ") NOT LIKE '%/loom.notes.yaml' AND lower(" + column + ") NOT LIKE '%/loom.notes.yml')"
}

// Only v0.5 uses this additional live fence. A stripped version marker cannot
// downgrade a registration whose authoritative project source is still v0.5.
func declarationEnrollmentMembershipSQL(root string) string {
	return declarationEnrollmentEvidenceSQL(root, "", false)
}

func declarationEnrollmentEvidenceSQL(root, object string, archiveRead bool) string {
	retainedVersion := "true"
	archive := "false"
	if object != "" {
		retainedVersion = "NOT COALESCE((" + object + ".metadata#>'{source_root,knowledge_source}') ? 'schema_version',false)"
	}
	if archiveRead {
		// This replaces only the archive-induced writer stop, never the source
		// fence. The immutable custody receipt authenticates the stop; current
		// accepted declaration/configuration must still equal retained evidence.
		archive = notesProjectArchiveStopSQL(root, object, notesReadArchiveOperationSQL(object)) +
			" AND accepted.registration_status IN ('registered','archived')" +
			" AND " + object + ".metadata#>'{source_root,knowledge_source}'=expected.value#>'{metadata,knowledge_source}'"
	}
	live := "accepted.registration_status='registered' AND declared_project.status='active' AND declared.activation_status='reported'" +
		" AND declared.metadata=" + root + ".metadata->'registration_metadata' AND (declared.metadata - 'declaration_adapter')=expected.value->'metadata'" +
		" AND owner_report.status NOT IN ('disabled','blocked','stale')"
	return `(` + declarationLegacyNotesEvidenceSQL(root, object, archiveRead) + ` OR (` + retainedVersion + ` AND NOT COALESCE((` + root + `.metadata#>'{registration_metadata,knowledge_source}') ? 'schema_version',false)
	 AND NOT EXISTS (SELECT 1 FROM projects.project_watched_root_registrations declaration_probe
	 JOIN projects.project_contract_registrations declaration_source USING(project_contract_registration_id)
	 WHERE declaration_probe.project_watched_root_registration_id=` + root + `.project_watched_root_registration_id
	 AND declaration_source.contract_schema_version='project.contract.v0.5'))
	 OR EXISTS (
	 SELECT 1 FROM projects.project_watched_root_registrations declared
	 JOIN projects.project_contract_registrations accepted USING(project_contract_registration_id)
	 JOIN projects.projects declared_project ON declared_project.project_id=declared.project_id
	 JOIN nodes.nodes declared_node ON declared_node.node_id=declared.node_id
	 JOIN watched_roots.roots owner_report ON owner_report.watched_root_id=declared.watched_root_id
	 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(accepted.registration_plan_json->'watched_roots')='array' THEN accepted.registration_plan_json->'watched_roots' ELSE '[]'::jsonb END) expected
	 WHERE declared.project_watched_root_registration_id=` + root + `.project_watched_root_registration_id
	 AND accepted.contract_schema_version='project.contract.v0.5' AND accepted.project_id=declared.project_id
	 AND ((` + live + `) OR (` + archive + `))
	 AND accepted.validation_report_json->>'ok'='true' AND accepted.validation_report_json->>'registerable'='true'
	 AND accepted.registration_plan_json->>'registerable'='true'
	 AND accepted.validation_report_json->'watched_roots' @> jsonb_build_array(expected.value)
	 AND declared.project_id=` + root + `.project_id AND declared.node_id=` + root + `.node_id
	 AND declared.owner_node_key=` + root + `.node_key AND declared_node.node_key=declared.owner_node_key
	 AND declared.backend_root_key=` + root + `.backend_root_key AND declared.root_relative_path=` + root + `.root_relative_path
	 AND ` + root + `.source_path=accepted.project_root || '/' || declared.root_relative_path
	 AND expected.value->>'activation_status'='pending_agent_apply'
	 AND declared.source_kinds_json=expected.value->'source_kinds' AND declared.source_kinds_json ? 'project_material'
	 AND NOT declared.source_kinds_json ?| ARRAY['notes_contract','repos_contract']
	 AND declared.local_root_key=expected.value->>'key' AND declared.backend_root_key=expected.value->>'backend_root_key'
	 AND declared.owner_node_key=expected.value->>'owner_node' AND declared.root_relative_path=expected.value->>'root_relative_path'
	 AND declared.display_name=expected.value->>'display_name'
	 AND declared.sync_mode=expected.value->>'sync_mode' AND declared.index_mode=expected.value->>'index_mode'
	 AND declared.backup_mode=expected.value->>'backup_mode' AND declared.delete_mode=expected.value->>'delete_mode'
	 AND ((NOT declared.metadata ? 'declaration_adapter' AND declared.safe_root_key=expected.value->>'safe_root_key'
  AND declared.config_hash=expected.value->>'config_hash' AND declared.config_json=expected.value->'config_json')
  OR projects.declaration_watch_binding_matches(expected.value,declared.metadata,declared.config_json,declared.config_hash,declared.safe_root_key,declared.project_id,declared.node_id,(` + archive + `)))
	 AND owner_report.node_id=declared.node_id AND owner_report.root_key=declared.backend_root_key
	 AND owner_report.config_hash=declared.config_hash AND owner_report.config_json=declared.config_json
	 AND expected.value#>>'{metadata,knowledge_source,schema_version}'=accepted.contract_schema_version
	 AND expected.value#>>'{metadata,knowledge_source,source_hash}'=accepted.contract_hash
	 AND expected.value#>>'{metadata,knowledge_source,project_id}'=accepted.project_id
	 AND expected.value#>>'{metadata,knowledge_source,project_root}'=accepted.project_root
	 AND expected.value#>>'{metadata,knowledge_source,owner_node}'=accepted.contract_json#>>'{project,owner_node}'
	 AND accepted.contract_json#>>'{project,id}'=declared.project_id
	 AND (accepted.contract_json->'resources'->(expected.value#>>'{metadata,knowledge_source,resource_key}'))->>'kind'='knowledge'
	 AND (accepted.contract_json->'resources'->(expected.value#>>'{metadata,knowledge_source,resource_key}'))#>>'{knowledge,path}'=declared.root_relative_path
	 AND (accepted.contract_json->'resources'->(expected.value#>>'{metadata,knowledge_source,resource_key}'))#>>'{knowledge,category}'=expected.value#>>'{metadata,knowledge_source,declaration}'
	 ))`
}

// Retained Notes keep their original identity. This branch is available only
// through an immutable journal action and the registration owner's committed
// receipt; neither a v0.5 parent nor a declaration_adapter flag grants it.
// The old report is allowed during delivery/ACK lag, distinct from the effective
// successor configuration. All ordinary object/privacy/custody fences surround it.
func declarationLegacyNotesEvidenceSQL(root, object string, archiveRead bool) string {
	objectIdentity := "true"
	if object != "" {
		objectIdentity = object + `.source_node_key=` + root + `.node_key AND ` + object + `.project_id=` + root + `.project_id
   AND ` + object + `.metadata#>>'{source_root,source_path}'=` + root + `.source_path
   AND ` + object + `.metadata#>>'{source_root,root_relative_path}'=` + root + `.root_relative_path
   AND NOT COALESCE((` + object + `.metadata#>'{source_root,knowledge_source}') ? 'schema_version',false)`
	}
	archive := "false"
	if archiveRead {
		archive = notesProjectArchiveStopSQL(root, object, notesReadArchiveOperationSQL(object))
	}
	return `EXISTS (
 SELECT 1 FROM projects.project_watched_root_registrations legacy_row
 JOIN projects.project_contract_registrations legacy_source USING(project_contract_registration_id)
 JOIN projects.projects legacy_project ON legacy_project.project_id=legacy_row.project_id
 JOIN nodes.nodes legacy_node ON legacy_node.node_id=legacy_row.node_id
 JOIN watched_roots.roots legacy_report ON legacy_report.watched_root_id=legacy_row.watched_root_id
 JOIN projects.declaration_owner_receipts adoption ON adoption.project_id=legacy_row.project_id AND adoption.owner='projects'
 JOIN projects.declaration_operations operation ON operation.operation_id=adoption.operation_id AND operation.project_id=adoption.project_id
 JOIN projects.declaration_action_receipts action ON action.operation_id=adoption.operation_id AND action.action_id=adoption.action_id
 CROSS JOIN LATERAL (SELECT operation.resolution->'payloads'->adoption.action_id AS value) intent
 CROSS JOIN LATERAL (SELECT intent.value->'predecessor' AS value) predecessor
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(predecessor.value->'members')='array' THEN predecessor.value->'members' ELSE '[]'::jsonb END) member
 CROSS JOIN LATERAL (SELECT member.value->'registration' AS value) old_row
 CROSS JOIN LATERAL (SELECT member.value->'report' AS value) old_report
 CROSS JOIN LATERAL (
  SELECT entry.value FROM jsonb_each(operation.resolution->'payloads') entry
  WHERE entry.value#>'{group,predecessor}'=predecessor.value
  AND entry.value#>>'{group,project_id}'=legacy_row.project_id AND entry.value#>>'{group,node_id}'=legacy_row.node_id
  ORDER BY entry.key LIMIT 1
 ) desired
 CROSS JOIN LATERAL jsonb_array_elements(desired.value#>'{group,roots}') successor
 CROSS JOIN LATERAL (SELECT successor.value->'metadata' || jsonb_build_object('declaration_adapter',jsonb_build_object(
  'schema_version','project.watch.binding.v1','project_id',legacy_row.project_id,'node_id',legacy_row.node_id,
  'group_hash',desired.value->>'group_hash','safe_root_key','declaration_'||lower(legacy_row.project_id),
  'compiler_config_json',successor.value->>'compiler_config_json','compiler_config_hash',successor.value->>'compiler_config_hash',
  'effective_config_hash',successor.value->>'config_hash')) AS value) successor_metadata
 WHERE ` + root + `.status='active' AND ` + root + `.root_kind='project_notes' AND ` + objectIdentity + `
 AND NOT COALESCE((` + root + `.metadata#>'{registration_metadata,knowledge_source}') ? 'schema_version',false)
 AND legacy_row.project_watched_root_registration_id=` + root + `.project_watched_root_registration_id
 AND legacy_row.project_watched_root_registration_id=old_row.value->>'project_watched_root_registration_id'
 AND legacy_row.project_contract_registration_id=predecessor.value->>'registration_id'
 AND adoption.result->>'effect_ref'=legacy_source.project_contract_registration_id
 AND adoption.actor_id=operation.actor_id AND adoption.origin_node_id=operation.origin_node_id AND adoption.target_node_id=operation.target_node_id
 AND action.owner=adoption.owner AND action.token=adoption.token AND action.input_hash=adoption.input_hash
 AND action.action_id='register_project:project' AND operation.state<>'superseded'
 AND predecessor.value->>'schema_version'='project.declaration_legacy_predecessor.v1'
 AND predecessor.value->>'project_id'=legacy_row.project_id AND predecessor.value->>'node_id'=legacy_row.node_id
 AND legacy_source.contract_schema_version='project.contract.v0.5'
 AND legacy_source.contract_json=intent.value->'declaration'
 AND legacy_source.contract_hash=intent.value->>'contract_hash' AND legacy_source.contract_path=intent.value->>'contract_path'
 AND legacy_source.project_root=intent.value->>'project_root' AND legacy_source.project_root=predecessor.value->>'project_root'
 AND legacy_source.contract_json#>>'{legacy_contracts,project,digest}'=predecessor.value->>'contract_hash'
 AND legacy_source.contract_json#>>'{legacy_contracts,project,schema_version}'=predecessor.value->>'contract_schema_version'
 AND legacy_source.contract_json#>>'{project,id}'=legacy_row.project_id
 AND legacy_source.contract_json#>>'{project,owner_node}'=legacy_node.node_key
 AND legacy_source.validation_report_json->>'ok'='true' AND legacy_source.validation_report_json->>'registerable'='false'
 AND legacy_source.registration_plan_json->>'registerable'='false'
 AND legacy_source.registration_plan_json#>'{declaration,document}'=legacy_source.contract_json
 AND legacy_source.validation_report_json->'declaration'=legacy_source.registration_plan_json->'declaration'
 AND legacy_source.validation_report_json->'watched_roots'=legacy_source.registration_plan_json->'watched_roots'
 AND jsonb_array_length(legacy_source.registration_plan_json->'watched_roots')=jsonb_array_length(desired.value#>'{group,roots}')
 AND NOT EXISTS (
  SELECT 1 FROM jsonb_array_elements(desired.value#>'{group,roots}') g
  WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(legacy_source.registration_plan_json->'watched_roots') e
   WHERE e->>'backend_root_key'=g->>'backend_root_key' AND e->>'worker_key'=g->>'worker_key'
   AND e->>'key'=g->>'local_root_key' AND e->>'config_hash'=g->>'compiler_config_hash'
   AND e->'config_json'=convert_from(decode(g->>'compiler_config_json','base64'),'UTF8')::jsonb
   AND e->'metadata'=g->'metadata' AND e->'source_kinds'=g->'source_kinds'))
 AND (SELECT count(*) FROM jsonb_array_elements(legacy_source.registration_plan_json#>'{declaration,sources}'))=jsonb_array_length(desired.value#>'{group,sources}')
 AND (SELECT count(DISTINCT s->>'ref') FROM jsonb_array_elements(legacy_source.registration_plan_json#>'{declaration,sources}') s)=jsonb_array_length(desired.value#>'{group,sources}')
 AND NOT EXISTS (
  SELECT 1 FROM jsonb_array_elements(legacy_source.registration_plan_json#>'{declaration,sources}') s
  WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(desired.value#>'{group,sources}') g
   WHERE s->>'ref'=g->>'ref' AND s->>'schema_version'=g->>'schema_version'
   AND s->>'hash'=g->>'hash' AND s->>'revision'=g->>'revision'
   AND 'sha256:'||encode(sha256(decode(s->>'raw','base64')),'hex')=g->>'hash'))
 AND legacy_row.project_id=` + root + `.project_id AND legacy_row.node_id=` + root + `.node_id
 AND legacy_row.owner_node_key=` + root + `.node_key AND legacy_row.owner_node_key=legacy_node.node_key AND legacy_node.status='active'
 AND legacy_project.home_node_id=legacy_row.node_id
 AND legacy_row.backend_root_key=` + root + `.backend_root_key AND legacy_row.root_relative_path=` + root + `.root_relative_path
 AND ` + root + `.source_path=legacy_source.project_root || '/' || legacy_row.root_relative_path
 AND legacy_row.backend_root_key=old_row.value->>'backend_root_key' AND legacy_row.backend_root_key=successor.value->>'backend_root_key'
 AND legacy_row.watched_root_id=old_row.value->>'watched_root_id'
 AND legacy_row.root_relative_path=old_row.value->>'root_relative_path'
 AND legacy_row.local_root_key=old_row.value->>'local_root_key' AND legacy_row.worker_key=old_row.value->>'worker_key'
 AND legacy_row.display_name=old_row.value->>'display_name'
 AND legacy_row.source_kinds_json=old_row.value->'source_kinds' AND legacy_row.source_kinds_json ? 'notes_contract'
 AND NOT legacy_row.source_kinds_json ?| ARRAY['repos_contract','project_material']
 AND legacy_row.sync_mode=old_row.value->>'sync_mode' AND legacy_row.index_mode=old_row.value->>'index_mode'
 AND legacy_row.backup_mode=old_row.value->>'backup_mode' AND legacy_row.delete_mode=old_row.value->>'delete_mode'
 AND legacy_row.created_at=(old_row.value->>'created_at')::timestamptz
 AND (` + root + `.metadata->'registration_metadata'=old_row.value->'metadata' OR ` + root + `.metadata->'registration_metadata'=successor_metadata.value)
 AND ((legacy_source.registration_status='registered' AND legacy_project.status='active' AND legacy_project.archive_state='{}'::jsonb
  AND ((legacy_row.metadata=old_row.value->'metadata' AND legacy_row.safe_root_key='project'
   AND legacy_row.config_hash=old_row.value->>'config_hash' AND legacy_row.config_json=old_row.value->'config_json'
   AND legacy_row.activation_status=old_row.value->>'activation_status'
   AND legacy_row.last_applied_at=(old_row.value->>'last_applied_at')::timestamptz)
  OR (legacy_row.metadata=successor_metadata.value AND legacy_row.safe_root_key='declaration_'||lower(legacy_row.project_id)
   AND legacy_row.config_hash=successor.value->>'config_hash' AND legacy_row.config_json=successor.value->'config_json'
   AND legacy_row.activation_status IN ('pending_agent_apply','applied','reported')
   AND EXISTS (SELECT 1 FROM projects.declaration_watch_deliveries delivery JOIN communication.messages message ON message.communication_message_id=delivery.message_id
    WHERE delivery.operation_id=operation.operation_id AND delivery.project_id=legacy_row.project_id AND delivery.node_id=legacy_row.node_id
    AND delivery.group_hash=desired.value->>'group_hash' AND message.payload_json->'group'=desired.value->'group'
    AND message.kind='project.watch.reconcile.v1' AND message.node_id=legacy_row.node_id))))
  OR (` + archive + ` AND legacy_source.registration_status IN ('registered','archived')))
 AND legacy_report.watched_root_id=old_report.value->>'watched_root_id' AND legacy_report.node_id=legacy_row.node_id
 AND legacy_report.root_key=legacy_row.backend_root_key AND legacy_report.worker_key=legacy_row.worker_key
 AND legacy_report.display_name=old_report.value->>'display_name'
 AND legacy_report.created_at=(old_report.value->>'created_at')::timestamptz
 AND legacy_report.last_reported_at >= (old_row.value->>'last_applied_at')::timestamptz
 AND ((legacy_report.status=old_report.value->>'status' AND legacy_report.safe_root_key='project' AND legacy_report.config_hash=old_report.value->>'config_hash'
   AND legacy_report.config_json=old_report.value->'config_json'
   AND COALESCE((SELECT jsonb_object_agg(key,value) FROM jsonb_each(legacy_report.metadata)
    WHERE key IN ('source','runtime_worker','root_reachable','safe_root_key','root_relative_path')),'{}'::jsonb)=old_report.value->'metadata')
  OR (legacy_report.status='healthy' AND legacy_row.metadata=successor_metadata.value AND legacy_report.safe_root_key=legacy_row.safe_root_key
   AND legacy_report.config_hash=successor.value->>'config_hash' AND legacy_report.config_json=successor.value->'config_json'))
 AND COALESCE((SELECT jsonb_object_agg(key,value) FROM jsonb_each(legacy_report.metadata)
  WHERE key IN ('source','runtime_worker','root_reachable','safe_root_key','root_relative_path')),'{}'::jsonb)
  = CASE WHEN legacy_report.safe_root_key='project' THEN old_report.value->'metadata'
    ELSE jsonb_build_object('source','loom-node-agent','runtime_worker',legacy_row.worker_key,'root_reachable',true,
      'safe_root_key',legacy_row.safe_root_key,'root_relative_path',legacy_row.root_relative_path) END
 )`
}
