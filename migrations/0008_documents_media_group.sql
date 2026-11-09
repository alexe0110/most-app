-- +goose Up
-- Telegram album (media group) the document arrived in; null for a single file.
alter table documents add column media_group_id text;
create index documents_user_media_group_idx on documents (user_id, media_group_id)
    where media_group_id is not null;

-- +goose Down
drop index documents_user_media_group_idx;
alter table documents drop column media_group_id;
