-- name: InsertFeedbackMessages :exec
-- All messages of one request (the header and the copies) get one feedback_id.
with request as (select uuid_generate_v7() as id)
insert
into feedback_messages (feedback_id, user_id, chat_id, chat_message_id)
select request.id, @user_id, @chat_id, m.chat_message_id
from request,
     unnest(@chat_message_ids::bigint[]) as m (chat_message_id);

-- name: CountFeedbackSince :one
-- An album is one request, hence distinct feedback_id.
select count(distinct feedback_id)
from feedback_messages
where user_id = $1
  and created_at >= $2;

-- name: FeedbackAuthor :one
select u.tg_user_id
from feedback_messages f
         join users u on u.id = f.user_id
where f.chat_id = $1
  and f.chat_message_id = $2
  and u.deleted_at is null;

-- name: DeleteUserFeedback :exec
delete
from feedback_messages
where user_id = $1;
