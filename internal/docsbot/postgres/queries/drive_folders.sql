-- name: UpsertDriveFolder :one
insert into drive_folders (user_id, category, drive_folder_id)
values ($1, $2, $3)
on conflict (user_id, category) do update
    set drive_folder_id = excluded.drive_folder_id
returning *;

-- name: GetDriveFolder :one
select *
from drive_folders
where user_id = $1
  and category = $2;

-- name: DeleteDriveFolder :exec
delete
from drive_folders
where user_id = $1
  and category = $2;

-- name: DeleteUserDriveFolders :exec
delete
from drive_folders
where user_id = @user_id;
