-- name: UpsertIntegration :one
insert into integrations (user_id, bot, provider, refresh_token_enc, status)
values ($1, $2, $3, $4, $5)
on conflict (user_id, bot, provider) do update
    set refresh_token_enc = excluded.refresh_token_enc,
        status            = excluded.status,
        updated_at        = now()
returning *;

-- name: GetIntegration :one
select *
from integrations
where user_id = $1
  and bot = $2
  and provider = $3;
