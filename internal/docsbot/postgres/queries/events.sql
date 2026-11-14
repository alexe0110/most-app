-- name: InsertEvent :exec
-- An empty user_id stores the event anonymously (data_deleted).
insert into events (user_id, bot, name, props)
values ($1, $2, $3, $4);

-- name: LockUser :exec
-- Serializes the first-start check of one user: two parallel /start commands must
-- not both see "no start yet".
select id
from users
where id = $1
for update;

-- name: UserHasEvent :one
select exists (select 1
               from events
               where user_id = $1
                 and bot = $2
                 and name = $3);

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
