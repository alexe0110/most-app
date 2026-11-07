-- +goose Up

-- kind records how the file arrived so "Прислать файл" can resend it with the
-- right method (sendPhoto vs sendDocument). Existing rows are plain documents.
alter table documents
    add column kind text not null default 'document'
        constraint documents_kind_check check (kind in ('photo', 'document'));
alter table documents alter column kind drop default;

-- backup_status tracks the Drive upload lifecycle, separate from status. Until
-- Drive is connected it stays 'none' and the document lives only in Telegram.
alter table documents
    add column backup_status text not null default 'none'
        constraint documents_backup_status_check
        check (backup_status in ('none', 'pending', 'uploading', 'backed_up', 'failed'));

-- A document is saved as soon as its category is chosen; the Drive upload moved
-- to backup_status, so status only needs pending/saved now.
alter table documents drop constraint documents_status_check;
alter table documents
    add constraint documents_status_check check (status in ('pending', 'saved'));

-- +goose Down
alter table documents drop constraint documents_status_check;
alter table documents
    add constraint documents_status_check check (status in ('pending', 'uploading', 'saved', 'failed'));
alter table documents drop column backup_status;
alter table documents drop column kind;
