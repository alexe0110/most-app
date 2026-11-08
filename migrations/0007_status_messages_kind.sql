-- +goose Up
alter table status_messages add column kind text not null default 'batch';

-- +goose Down
alter table status_messages drop column kind;
