-- name: CreateUser :one
insert into users (tg_user_id, lang)
values ($1, $2)
returning *;

-- name: GetUserByTgID :one
select *
from users
where tg_user_id = $1
  and deleted_at is null;

-- name: SoftDeleteUser :exec
update users
set deleted_at = now()
where id = $1;
