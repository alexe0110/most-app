-- name: CreateOAuthState :exec
insert into oauth_states (state, user_id, bot, verifier, expires_at)
values ($1, $2, $3, $4, $5);

-- name: TakeOAuthState :one
-- Fetch and delete a state in one shot. The delete makes the state single-use
-- even under a race; the TTL is enforced by the caller against expires_at.
delete from oauth_states
where state = $1
returning user_id, bot, verifier, expires_at;
