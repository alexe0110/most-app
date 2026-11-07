-- name: InsertDocument :one
-- A received file starts as pending with no category; the category and 'saved'
-- status are set once the user picks one (SaveDocumentCategory).
insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id)
values (@user_id, @status, @kind, @category, @title, @tg_file_id, @tg_file_unique_id)
returning *;

-- name: GetDocumentByUniqueID :one
select *
from documents
where user_id = @user_id
  and tg_file_unique_id = @tg_file_unique_id;

-- name: GetDocumentByID :one
select *
from documents
where id = @id
  and user_id = @user_id;

-- name: SaveDocumentCategory :exec
update documents
set category   = @category,
    status     = 'saved',
    updated_at = now()
where id = @id
  and user_id = @user_id;

-- name: UpdateDocumentBackupStatus :exec
update documents
set backup_status = @backup_status,
    updated_at    = now()
where id = @id;

-- name: UpdateDocumentTitle :exec
update documents
set title      = @title,
    updated_at = now()
where id = @id
  and user_id = @user_id;

-- name: UpdateDocumentCategory :exec
update documents
set category   = @category,
    updated_at = now()
where id = @id
  and user_id = @user_id;

-- name: DeleteDocumentByID :exec
delete
from documents
where id = @id
  and user_id = @user_id;

-- name: CountSavedDocuments :one
select count(*)
from documents
where user_id = @user_id
  and status = 'saved';

-- name: CountDocumentsNotBackedUp :one
select count(*)
from documents
where user_id = @user_id
  and status = 'saved'
  and backup_status <> 'backed_up';

-- name: DocumentCategoryCounts :many
select category, count(*) as n
from documents
where user_id = @user_id
  and status = 'saved'
group by category
order by category;

-- name: ListDocumentsByCategory :many
select *
from documents
where user_id = @user_id
  and category = @category
  and status = 'saved'
order by created_at desc
limit @lim offset @off;

-- name: SearchDocuments :many
select *
from documents
where user_id = @user_id
  and status = 'saved'
  and (title ilike '%' || @query || '%' or similarity(title, @query) > 0.3)
order by similarity(title, @query) desc, created_at desc
limit @lim;

-- name: MarkDocumentBackedUp :exec
update documents
set backup_status = 'backed_up',
    drive_file_id = @drive_file_id,
    updated_at    = now()
where id = @id;

-- name: ListNotBackedUpDocumentIDs :many
select id
from documents
where user_id = @user_id
  and status = 'saved'
  and backup_status <> 'backed_up'
order by created_at;
