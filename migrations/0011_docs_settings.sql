-- +goose Up
-- Per-user settings of Most Docs. A separate table, not columns on users: users
-- is shared by all bots of the series. A missing row means the defaults.
create table docs_settings (
    user_id        uuid primary key references users (id) on delete cascade,
    inline_enabled boolean     not null default true,
    updated_at     timestamptz not null default now()
);

-- +goose Down
drop table docs_settings;
