-- +goose Up

-- One-time OAuth states with their PKCE verifier, bound to a user and bot.
-- A row is created when the auth URL is issued and deleted when the callback
-- consumes it; expires_at enforces a short TTL.
create table oauth_states (
    state      text        primary key,
    user_id    uuid        not null references users (id) on delete cascade,
    bot        text        not null,
    verifier   text        not null,
    expires_at timestamptz not null,
    created_at timestamptz not null default now()
);

-- Supports cleanup of expired states.
create index oauth_states_expires_at_idx on oauth_states (expires_at);

-- +goose Down
drop table if exists oauth_states;
