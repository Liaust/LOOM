-- +goose Up
ALTER TABLE storage.storage_entries
    DROP CONSTRAINT IF EXISTS storage_entries_file_class_check;

ALTER TABLE storage.storage_entries
    ADD CONSTRAINT storage_entries_file_class_check CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'office_document',
        'directory',
        'package',
        'generated_metadata',
        'binary',
        'unknown'
    ));

ALTER TABLE knowledge.knowledge_objects
    DROP CONSTRAINT IF EXISTS knowledge_objects_file_class_check;

ALTER TABLE knowledge.knowledge_objects
    ADD CONSTRAINT knowledge_objects_file_class_check CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'office_document',
        'directory',
        'package',
        'generated_metadata',
        'binary',
        'unknown'
    ));

ALTER TABLE knowledge.knowledge_object_versions
    DROP CONSTRAINT IF EXISTS knowledge_object_versions_file_class_check;

ALTER TABLE knowledge.knowledge_object_versions
    ADD CONSTRAINT knowledge_object_versions_file_class_check CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'office_document',
        'directory',
        'package',
        'generated_metadata',
        'binary',
        'unknown'
    ));

-- +goose Down
ALTER TABLE knowledge.knowledge_object_versions
    DROP CONSTRAINT IF EXISTS knowledge_object_versions_file_class_check;

ALTER TABLE knowledge.knowledge_object_versions
    ADD CONSTRAINT knowledge_object_versions_file_class_check CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'directory',
        'package',
        'generated_metadata',
        'binary',
        'unknown'
    ));

ALTER TABLE knowledge.knowledge_objects
    DROP CONSTRAINT IF EXISTS knowledge_objects_file_class_check;

ALTER TABLE knowledge.knowledge_objects
    ADD CONSTRAINT knowledge_objects_file_class_check CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'directory',
        'package',
        'generated_metadata',
        'binary',
        'unknown'
    ));

ALTER TABLE storage.storage_entries
    DROP CONSTRAINT IF EXISTS storage_entries_file_class_check;

ALTER TABLE storage.storage_entries
    ADD CONSTRAINT storage_entries_file_class_check CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'directory',
        'package',
        'generated_metadata',
        'binary',
        'unknown'
    ));
