-- name: CreateDocument :one
insert into documents (user_id, status, category, title, tg_file_id, tg_file_unique_id, drive_file_id)
values ($1, $2, $3, $4, $5, $6, $7)
returning *;

-- name: GetDocumentByFileUniqueID :one
select *
from documents
where user_id = $1
  and tg_file_unique_id = $2;

-- name: SetDocumentStatus :exec
update documents
set status     = $2,
    updated_at = now()
where id = $1;

-- name: ListDocumentsByCategory :many
select *
from documents
where user_id = $1
  and category = $2
order by created_at desc;

-- name: SearchDocumentsByTitle :many
select *
from documents
where user_id = $1
  and title ilike '%' || $2 || '%'
order by created_at desc
limit $3;
