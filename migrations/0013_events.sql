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
-- The daily purge deletes by age alone.
create index events_created_at on events (created_at);

-- Where the user came from: the deep-link payload of their first /start.
-- first_start_at marks that first /start. It is a column, not "no start event yet",
-- because events expire after 180 days and a returning user must not become new again.
alter table users
    add column source text,
    add column first_start_at timestamptz;

-- Users who existed before events were tracked are not new: their next /start must
-- neither count as a first start nor give them a source.
update users
set first_start_at = created_at;

-- +goose Down
alter table users
    drop column first_start_at,
    drop column source;

drop table events;
