-- name: InsertDocument :one
-- A received file starts as pending with no category; the category and 'saved'
-- status are set once the user picks one (SaveDocumentCategory).
insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id, file_size, mime_type, media_group_id)
values (@user_id, @status, @kind, @category, @title, @tg_file_id, @tg_file_unique_id, @file_size, @mime_type, @media_group_id)
returning *;

-- name: TitleExistsInCategory :one
select exists (
    select 1
    from documents
    where user_id = @user_id
      and category = @category
      and status = 'saved'
      and title = @title
);

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

-- name: SaveDocumentCategory :execrows
-- Only a pending document is saved: of two concurrent taps exactly one gets 1 row.
update documents
set category   = @category,
    status     = 'saved',
    updated_at = now()
where id = @id
  and user_id = @user_id
  and status = 'pending';

-- name: UpdateDocumentBackupStatus :exec
update documents
set backup_status = @backup_status,
    updated_at    = now()
where id = @id
  and user_id = @user_id;

-- name: MarkDocumentUploading :exec
update documents
set backup_status = 'uploading'
where id = @id
  and user_id = @user_id;

-- name: ClaimFailedForRetry :execrows
-- Atomically takes a failed upload for retry; 0 rows means it is not failed
-- (already retried, in flight, done) or not the user's.
update documents
set backup_status = 'pending',
    updated_at    = now()
where id = @id
  and user_id = @user_id
  and backup_status = 'failed';

-- name: UpdateDocumentTitle :execrows
update documents
set title      = @title,
    updated_at = now()
where id = @id
  and user_id = @user_id;

-- name: UpdateDocumentCategory :execrows
update documents
set category   = @category,
    updated_at = now()
where id = @id
  and user_id = @user_id;

-- name: DeleteDocumentByID :execrows
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
  and backup_status not in ('backed_up', 'too_large');

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
-- @pattern is the query with LIKE metacharacters escaped (backslash escape); @query is raw, for trigram similarity.
select *
from documents
where user_id = @user_id
  and status = 'saved'
  and (title ilike '%' || @pattern || '%' escape '\' or similarity(title, @query) > 0.3)
order by similarity(title, @query) desc, created_at desc
limit @lim;

-- name: MarkDocumentBackedUp :exec
update documents
set backup_status = 'backed_up',
    drive_file_id = @drive_file_id,
    updated_at    = now()
where id = @id
  and user_id = @user_id;

-- name: ClaimTransferableDocuments :many
-- Atomically takes every saved document not yet on Drive (none, failed) for
-- upload; a concurrent caller gets no rows for the same documents.
update documents
set backup_status = 'pending',
    updated_at    = now()
where user_id = @user_id
  and status = 'saved'
  and backup_status in ('none', 'failed')
returning id, created_at;

-- name: DocumentBackupStatusCounts :many
select backup_status, count(*) as n
from documents
where user_id = @user_id
  and status = 'saved'
group by backup_status;

-- name: ClaimFailedDocuments :many
-- Atomically takes every failed upload of the user for retry.
update documents
set backup_status = 'pending',
    updated_at    = now()
where user_id = @user_id
  and status = 'saved'
  and backup_status = 'failed'
returning id, created_at;

-- name: ListSavedDocumentsForExport :many
select *
from documents
where user_id = @user_id
  and status = 'saved'
order by created_at
limit @lim;

-- name: DeleteUserDocuments :exec
delete
from documents
where user_id = @user_id;

-- name: ResetPendingUploads :exec
update documents
set backup_status = 'none',
    updated_at    = now()
where user_id = @user_id
  and status = 'saved'
  and backup_status in ('pending', 'uploading');

-- name: StuckUploads :many
select d.id, d.user_id, d.updated_at
from documents d
         join integrations i
              on i.user_id = d.user_id
                  and i.bot = 'docs'
                  and i.provider = 'google'
                  and i.status = 'active'
where d.status = 'saved'
  and d.backup_status in ('pending', 'uploading')
  and d.updated_at < @older_than
order by d.updated_at
limit @lim;

-- name: ListPendingByMediaGroup :many
-- The user's still-pending documents of one album, in arrival order.
select *
from documents
where user_id = @user_id
  and media_group_id = @media_group_id
  and status = 'pending'
order by created_at, id;

-- name: UserIntakeStats :one
-- One round trip for the intake guards: pending backlog, files created since
-- @since (flood check) and the total (quota).
select count(*) filter (where status = 'pending')::bigint as pending,
       count(*) filter (where created_at > @since)::bigint as recent,
       count(*)::bigint                                    as total
from documents
where user_id = @user_id;

-- name: DeleteStalePending :execrows
-- Global TTL purge: pending documents nobody categorized, by age only.
delete
from documents
where status = 'pending'
  and created_at < @older_than;
