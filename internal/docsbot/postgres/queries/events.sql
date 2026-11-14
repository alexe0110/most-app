-- name: InsertEvent :exec
-- An empty user_id stores the event anonymously (data_deleted).
insert into events (user_id, bot, name, props)
values ($1, $2, $3, $4);

-- name: LockUserFirstStart :one
-- Serializes the first-start check of one user: two parallel /start commands must
-- not both see "never started". The result is null for a user who has not started.
select first_start_at
from users
where id = $1
for update;

-- name: MarkFirstStart :exec
update users
set first_start_at = now()
where id = $1;

-- name: DeleteUserEvents :exec
delete
from events
where user_id = $1;

-- name: SetUserSource :exec
-- Attribution is the first source only: a stored one is never overwritten.
update users
set source = $2
where id = $1
  and source is null;
