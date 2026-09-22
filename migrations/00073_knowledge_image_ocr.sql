-- +goose Up
ALTER TABLE knowledge.pipeline_stage_runs DROP CONSTRAINT pipeline_stage_runs_stage_key_check;
ALTER TABLE knowledge.pipeline_stage_runs ADD CONSTRAINT pipeline_stage_runs_stage_key_check CHECK (stage_key IN (
 'metadata','native_text','pdf_page_analysis','pdf_ocr','image_ocr','image_description',
 'consolidate_text','chunk','lexical_index','embedding','finalize'
));

-- +goose Down
-- Retained image OCR history must not be deleted to make rollback succeed.
ALTER TABLE knowledge.pipeline_stage_runs DROP CONSTRAINT pipeline_stage_runs_stage_key_check;
ALTER TABLE knowledge.pipeline_stage_runs ADD CONSTRAINT pipeline_stage_runs_stage_key_check CHECK (stage_key IN (
 'metadata','native_text','pdf_page_analysis','pdf_ocr','image_description',
 'consolidate_text','chunk','lexical_index','embedding','finalize'
));
