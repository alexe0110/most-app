-- +goose Up
-- Product events for activation and retention metrics. A platform table shared by
-- all bots (bot column); props hold only enumerated values, never user text.
-- user_id is nullable only for data_deleted: that event is kept anonymously so
-- deletions can still be counted after the user's own rows are gone.
create table events (
    id         uuid        primary key default uuid_generate_v7(),
    user_id    uuid        references users (id) on delete cascade,
    bot        text        not null,
    name       text        not null,
    props      jsonb       not null default '{}',
    created_at timestamptz not null default now()
);

create index events_name_created_at on events (name, created_at);
create index events_user_created_at on events (user_id, created_at);

-- Where the user came from: the deep-link payload of their first /start.
alter table users
    add column source text;

-- +goose Down
alter table users
    drop column source;

drop table events;
