-- name: InsertStatusMessage :one
insert into status_messages (user_id, chat_id, message_id, kind)
values (@user_id, @chat_id, @message_id, @kind)
returning *;

-- name: GetOpenStatusMessageForUser :one
select *
from status_messages
where user_id = @user_id
  and closed_at is null
order by created_at desc
limit 1;

-- name: GetStatusMessageByID :one
select *
from status_messages
where id = @id;

-- name: CloseStatusMessage :exec
update status_messages
set closed_at = now()
where id = @id;

-- name: UpdateStatusMessageMessageID :exec
update status_messages
set message_id = @message_id
where id = @id;

-- name: AttachDocumentToStatus :exec
update documents
set status_message_id = @status_message_id,
    updated_at        = now()
where id = @id;

-- name: ListDocumentsByStatusMessage :many
select *
from documents
where status_message_id = @status_message_id
order by created_at;

-- name: DeleteUserStatusMessages :exec
delete
from status_messages
where user_id = @user_id;
