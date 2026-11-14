-- Продуктовые метрики Most Files. Запросы только читают, без параметров, время
-- считается от now() по UTC. Каждый блок `-- name:` можно запускать отдельно:
--   psql "$DATABASE_URL" -f docs/metrics.sql   (выполнит все подряд)
--
-- Откуда данные: таблица events (docs/model.md) и users.source. «Новый
-- пользователь» — это start с props.new = true (первый /start, отмечен в
-- users.first_start_at; кто был в базе до появления событий, новым не считается); источник — payload этого
-- /start (https://t.me/<бот>?start=<источник>, только a-z, 0-9 и _), без payload
-- источник пуст и показывается как (none). Пользователь, удаливший данные, из
-- метрик исчезает: его события и источник удалены. События хранятся 180 дней,
-- поэтому окна ниже не длиннее.

-- name: sources_daily
-- Что показывает: сколько новых пользователей пришло за последние 30 дней по
-- дням и источникам.
with first_start as (
    select user_id, min(created_at) as at
    from events
    where name = 'start'
      and bot = 'docs'
      and user_id is not null
      and (props->>'new')::boolean
    group by user_id
)
select (f.at at time zone 'UTC')::date as day,
       coalesce(u.source, '(none)')    as source,
       count(*)                        as new_users
from first_start f
         join users u on u.id = f.user_id
where f.at >= now() - interval '30 days'
group by 1, 2
order by 1 desc, 3 desc, 2;

-- name: sources_weekly
-- Что показывает: сколько новых пользователей пришло за последние 12 недель по
-- неделям и источникам (какой канал растёт).
with first_start as (
    select user_id, min(created_at) as at
    from events
    where name = 'start'
      and bot = 'docs'
      and user_id is not null
      and (props->>'new')::boolean
    group by user_id
)
select date_trunc('week', f.at at time zone 'UTC')::date as week,
       coalesce(u.source, '(none)')                      as source,
       count(*)                                          as new_users
from first_start f
         join users u on u.id = f.user_id
where f.at >= now() - interval '12 weeks'
group by 1, 2
order by 1 desc, 3 desc, 2;

-- name: activation
-- Что показывает: какая доля новых пользователей сохранила хотя бы один документ
-- в первые 7 дней, по неделям первого /start. Последняя неделя неполная: окно
-- в 7 дней у части когорты ещё не закрылось.
with first_start as (
    select user_id, min(created_at) as at
    from events
    where name = 'start'
      and bot = 'docs'
      and user_id is not null
      and (props->>'new')::boolean
    group by user_id
),
     cohort as (
         select f.user_id,
                date_trunc('week', f.at at time zone 'UTC')::date as week,
                exists (select 1
                        from events d
                        where d.user_id = f.user_id
                          and d.name = 'doc_saved'
                          and d.bot = 'docs'
                          and d.created_at >= f.at
                          and d.created_at < f.at + interval '7 days') as activated
         from first_start f
         where f.at >= now() - interval '12 weeks'
     )
select week                                                   as cohort_week,
       count(*)                                               as new_users,
       count(*) filter (where activated)                      as activated_users,
       round(100.0 * count(*) filter (where activated) / count(*), 1) as activated_pct
from cohort
group by week
order by week desc;

-- name: drive_funnel
-- Что показывает: какая доля пользователей, сохранивших документ, подключила
-- Google Диск (когда угодно), по неделям первого сохранения.
with first_saved as (
    select user_id, min(created_at) as at
    from events
    where name = 'doc_saved'
      and bot = 'docs'
      and user_id is not null
    group by user_id
),
     cohort as (
         select s.user_id,
                date_trunc('week', s.at at time zone 'UTC')::date as week,
                exists (select 1
                        from events c
                        where c.user_id = s.user_id
                          and c.name = 'drive_connected'
                          and c.bot = 'docs') as connected
         from first_saved s
         where s.at >= now() - interval '12 weeks'
     )
select week                                                     as cohort_week,
       count(*)                                                 as users_with_doc,
       count(*) filter (where connected)                        as drive_connected_users,
       round(100.0 * count(*) filter (where connected) / count(*), 1) as drive_connected_pct
from cohort
group by week
order by week desc;

-- name: feature_usage
-- Что показывает: по неделям — сколько пользователей активны (любое событие) и
-- сколько из них пользовались отправкой из любого чата (inline_sent), конвертером
-- (convert_done), поиском (search) и экспортом (export), плюс сколько раз в
-- среднем на активного пользователя.
with weekly as (
    select date_trunc('week', created_at at time zone 'UTC')::date as week, user_id, name
    from events
    where user_id is not null
      and bot = 'docs'
      and created_at >= now() - interval '12 weeks'
)
select week,
       count(distinct user_id)                                                    as active_users,
       count(distinct user_id) filter (where name = 'inline_sent')               as inline_sent_users,
       round(count(*) filter (where name = 'inline_sent')::numeric / count(distinct user_id), 2)  as inline_sent_per_active,
       count(distinct user_id) filter (where name = 'convert_done')              as convert_done_users,
       round(count(*) filter (where name = 'convert_done')::numeric / count(distinct user_id), 2) as convert_done_per_active,
       count(distinct user_id) filter (where name = 'search')                    as search_users,
       round(count(*) filter (where name = 'search')::numeric / count(distinct user_id), 2)       as search_per_active,
       count(distinct user_id) filter (where name = 'export')                    as export_users,
       round(count(*) filter (where name = 'export')::numeric / count(distinct user_id), 2)       as export_per_active
from weekly
group by week
order by week desc;

-- name: retention
-- Что показывает: доля пользователей недельной когорты первого /start, у которых
-- есть любое событие ровно на 1-й, 7-й и 30-й день после дня первого /start.
-- В долю входят только те, у кого этот день уже наступил (иначе свежая когорта
-- выглядела бы нулевой); пока таких нет, значение пустое.
with first_start as (
    select user_id, min(created_at) as at
    from events
    where name = 'start'
      and bot = 'docs'
      and user_id is not null
      and (props->>'new')::boolean
    group by user_id
),
     activity as (
         select distinct user_id, (created_at at time zone 'UTC')::date as day
         from events
         where user_id is not null
           and bot = 'docs'
     ),
     cohort as (
         select f.user_id,
                (f.at at time zone 'UTC')::date                   as day0,
                date_trunc('week', f.at at time zone 'UTC')::date as week
         from first_start f
         where f.at >= now() - interval '12 weeks'
     ),
     returns as (
         select c.week,
                c.day0,
                coalesce(bool_or(a.day = c.day0 + 1), false)  as d1,
                coalesce(bool_or(a.day = c.day0 + 7), false)  as d7,
                coalesce(bool_or(a.day = c.day0 + 30), false) as d30
         from cohort c
                  left join activity a on a.user_id = c.user_id and a.day > c.day0
         group by c.user_id, c.week, c.day0
     )
select week                                                                 as cohort_week,
       count(*)                                                             as new_users,
       round(100.0 * count(*) filter (where d1)
                 / nullif(count(*) filter (where day0 <= (now() at time zone 'UTC')::date - 1), 0), 1)  as d1_pct,
       round(100.0 * count(*) filter (where d7)
                 / nullif(count(*) filter (where day0 <= (now() at time zone 'UTC')::date - 7), 0), 1)  as d7_pct,
       round(100.0 * count(*) filter (where d30)
                 / nullif(count(*) filter (where day0 <= (now() at time zone 'UTC')::date - 30), 0), 1) as d30_pct
from returns
group by week
order by week desc;
