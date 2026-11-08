-- +goose Up
alter table documents add column file_size bigint;
alter table documents add column mime_type text;

-- +goose Down
alter table documents drop column mime_type;
alter table documents drop column file_size;
