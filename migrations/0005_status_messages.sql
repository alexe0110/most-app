-- +goose Up
create table status_messages (
    id         uuid        primary key default uuid_generate_v7(),
    user_id    uuid        not null references users (id) on delete cascade,
    chat_id    bigint      not null,
    message_id bigint      not null,
    created_at timestamptz not null default now(),
    closed_at  timestamptz
);
-- one open status message per user is looked up on every category pick
create index status_messages_user_open_idx on status_messages (user_id) where closed_at is null;

alter table documents add column status_message_id uuid references status_messages (id) on delete set null;

-- +goose Down
alter table documents drop column status_message_id;
drop table status_messages;
