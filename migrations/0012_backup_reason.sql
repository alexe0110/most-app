-- +goose Up
-- Why an upload failed (quota, revoked, unavailable). Null means unknown or not
-- failed; it is cleared whenever the backup status moves on.
alter table documents
    add column backup_reason text;

-- +goose Down
alter table documents
    drop column backup_reason;
