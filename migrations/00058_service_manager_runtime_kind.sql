-- +goose Up
ALTER TABLE capabilities.endpoint_runtime_bindings
    DROP CONSTRAINT IF EXISTS endpoint_runtime_bindings_runtime_kind_check;

ALTER TABLE capabilities.endpoint_runtime_bindings
    ADD CONSTRAINT endpoint_runtime_bindings_runtime_kind_check CHECK (
        runtime_kind IN (
            'script',
            'command',
            'http',
            'node_agent',
            'native',
            'module',
            'workflow',
            'external_process',
            'service_manager'
        )
    );

-- +goose Down
ALTER TABLE capabilities.endpoint_runtime_bindings
    DROP CONSTRAINT IF EXISTS endpoint_runtime_bindings_runtime_kind_check;

ALTER TABLE capabilities.endpoint_runtime_bindings
    ADD CONSTRAINT endpoint_runtime_bindings_runtime_kind_check CHECK (
        runtime_kind IN (
            'script',
            'command',
            'http',
            'node_agent',
            'native',
            'module',
            'workflow',
            'external_process'
        )
    );
