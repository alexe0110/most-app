-- name: EnsureUser :one
-- Upsert on the shared tg_user_id. A returning user (even a previously
-- soft-deleted one) is revived; the stored lang is kept.
insert into users (tg_user_id, lang)
values ($1, $2)
on conflict (tg_user_id) do update
    set deleted_at = null
returning *;

-- name: GetUserByTgID :one
select *
from users
where tg_user_id = $1
  and deleted_at is null;

-- name: GetUserTgID :one
select tg_user_id
from users
where id = $1
  and deleted_at is null;

-- name: SoftDeleteUser :exec
-- The acquisition source and the first-start mark go with the rest of the data:
-- a user who comes back after deleting everything is a stranger again.
update users
set deleted_at     = now(),
    source         = null,
    first_start_at = null
where id = $1;
