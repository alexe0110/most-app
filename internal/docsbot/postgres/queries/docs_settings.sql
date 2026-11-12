-- name: GetInlineEnabled :one
-- A missing row means the default: enabled.
select coalesce(
    (select inline_enabled from docs_settings where user_id = @user_id),
    true
)::boolean as inline_enabled;

-- name: SetInlineEnabled :exec
insert into docs_settings (user_id, inline_enabled)
values (@user_id, @inline_enabled)
on conflict (user_id) do update
    set inline_enabled = excluded.inline_enabled,
        updated_at     = now();

-- name: DeleteUserDocsSettings :exec
delete from docs_settings
where user_id = @user_id;
