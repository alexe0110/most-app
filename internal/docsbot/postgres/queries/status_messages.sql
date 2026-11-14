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
where id = @id
  and user_id = @user_id;

-- name: CloseStatusMessage :exec
update status_messages
set closed_at = now()
where id = @id
  and user_id = @user_id;

-- name: UpdateStatusMessageMessageID :exec
update status_messages
set message_id = @message_id
where id = @id
  and user_id = @user_id;

-- name: AttachDocumentToStatus :exec
update documents
set status_message_id = @status_message_id,
    updated_at        = now()
where documents.id = @id
  and documents.user_id = @user_id
  and exists (select 1
              from status_messages sm
              where sm.id = @status_message_id
                and sm.user_id = @user_id);

-- name: ListDocumentsByStatusMessage :many
select *
from documents
where status_message_id = @status_message_id
  and user_id = @user_id
order by created_at;

-- name: DeleteClosedStatusMessages :execrows
-- Global TTL purge: status messages closed before @older_than. Documents keep
-- living: documents.status_message_id is on delete set null. A message with a
-- failed upload stays: its «Повторить» button looks the row up, and without it
-- the user would get a generic error.
delete
from status_messages
where closed_at < @older_than
  and not exists (select 1
                  from documents d
                  where d.status_message_id = status_messages.id
                    and d.backup_status = 'failed');

-- name: DeleteUserStatusMessages :exec
delete
from status_messages
where user_id = @user_id;
