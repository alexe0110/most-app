-- +goose Up
alter table documents drop constraint documents_backup_status_check;
alter table documents
    add constraint documents_backup_status_check
    check (backup_status in ('none', 'pending', 'uploading', 'backed_up', 'failed', 'too_large'));

-- +goose Down
alter table documents drop constraint documents_backup_status_check;
alter table documents
    add constraint documents_backup_status_check
    check (backup_status in ('none', 'pending', 'uploading', 'backed_up', 'failed'));
