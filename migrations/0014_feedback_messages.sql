-- +goose Up
-- Links a message in the feedback chat to the user who wrote it, so a reply to that
-- message reaches the right user. Only the link is stored: the text and files stay
-- in Telegram. A platform table shared by all bots, like events.
-- chat_id is part of the key because message ids are unique only within a chat:
-- changing FEEDBACK_CHAT_ID must not make an old id point at the wrong user.
-- feedback_id groups the header and the copies of one request, so an album is
-- counted as one request by the hourly limit.
create table feedback_messages (
    id              uuid        primary key default uuid_generate_v7(),
    feedback_id     uuid        not null,
    user_id         uuid        not null references users (id) on delete cascade,
    chat_id         bigint      not null,
    chat_message_id bigint      not null,
    created_at      timestamptz not null default now(),
    unique (chat_id, chat_message_id)
);

create index feedback_messages_user_created_at on feedback_messages (user_id, created_at);
-- The daily purge deletes by age alone.
create index feedback_messages_created_at on feedback_messages (created_at);

-- +goose Down
drop table feedback_messages;
