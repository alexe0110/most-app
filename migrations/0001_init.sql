-- +goose Up

-- pg_trgm powers the fuzzy (typo-tolerant) search on documents.title.
create extension if not exists pg_trgm;

-- UUID v7 is time-sortable and does not leak row counts. Postgres 17 has no
-- native generator, so we define one and use it as the default for every id.
-- +goose StatementBegin
create function uuid_generate_v7() returns uuid
language plpgsql
volatile
as $$
begin
    return encode(
        set_bit(
            set_bit(
                overlay(
                    uuid_send(gen_random_uuid())
                    placing substring(int8send(floor(extract(epoch from clock_timestamp()) * 1000)::bigint) from 3)
                    from 1 for 6
                ),
                52, 1
            ),
            53, 1
        ),
        'hex'
    )::uuid;
end
$$;
-- +goose StatementEnd

-- Пользователь Most. Один Telegram-аккаунт = один пользователь, общий на серию.
create table users (
    id          uuid        primary key default uuid_generate_v7(),
    tg_user_id  bigint      not null,
    lang        text        not null default 'ru',
    created_at  timestamptz not null default now(),
    deleted_at  timestamptz,
    unique (tg_user_id)
);

-- Подключение внешнего аккаунта для конкретного бота серии.
create table integrations (
    id                uuid        primary key default uuid_generate_v7(),
    user_id           uuid        not null references users (id) on delete cascade,
    bot               text        not null,
    provider          text        not null,
    refresh_token_enc bytea,
    status            text        not null default 'active',
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),
    unique (user_id, bot, provider)
);

-- Документ, присланный боту. Создаётся при получении файла, проходит статусы.
create table documents (
    id                uuid        primary key default uuid_generate_v7(),
    user_id           uuid        not null references users (id) on delete cascade,
    status            text        not null,
    category          text        not null,
    title             text        not null,
    tg_file_id        text        not null,
    tg_file_unique_id text        not null,
    drive_file_id     text,
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),
    unique (user_id, tg_file_unique_id),
    constraint documents_status_check
        check (status in ('pending', 'uploading', 'saved', 'failed'))
);

-- Каталог: документы пользователя по категориям.
create index documents_user_category_idx on documents (user_id, category);

-- Поиск по названию с опечатками.
create index documents_title_trgm_idx on documents using gin (title gin_trgm_ops);

-- Кеш папок Most Docs/<категория> на Drive пользователя.
create table drive_folders (
    user_id         uuid        not null references users (id) on delete cascade,
    category        text        not null,
    drive_folder_id text        not null,
    created_at      timestamptz not null default now(),
    primary key (user_id, category)
);

-- +goose Down
drop table if exists drive_folders;
drop table if exists documents;
drop table if exists integrations;
drop table if exists users;
drop function if exists uuid_generate_v7();
drop extension if exists pg_trgm;
