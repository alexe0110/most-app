-- +goose Up
-- Cloud sync state: the bot is the source of truth, a worker makes the Drive file
-- match the row. meta_version counts title/category/content changes (updated_at
-- is bumped by backup status changes too, so it cannot detect them).
alter table documents
    add column sync_status   text    not null default 'ok'
        check (sync_status in ('ok', 'pending', 'failed', 'missing')),
    add column sync_reason   text,
    add column meta_version  integer not null default 0,
    add column content_dirty boolean not null default false;

-- +goose Down
alter table documents
    drop column content_dirty,
    drop column meta_version,
    drop column sync_reason,
    drop column sync_status;
