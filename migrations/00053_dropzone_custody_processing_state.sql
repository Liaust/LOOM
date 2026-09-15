-- +goose Up

UPDATE storage.storage_entries
SET
    processing_state = 'backup_only',
    updated_at = now()
WHERE storage_class = 'dropzone_custody'
  AND source_area = 'dropzone'
  AND availability_state = 'available'
  AND processing_state = 'metadata_only';

-- +goose Down

UPDATE storage.storage_entries
SET
    processing_state = 'metadata_only',
    updated_at = now()
WHERE storage_class = 'dropzone_custody'
  AND source_area = 'dropzone'
  AND availability_state = 'available'
  AND processing_state = 'backup_only';
