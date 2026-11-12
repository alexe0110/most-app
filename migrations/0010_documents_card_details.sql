-- +goose Up
-- Card details. Null means unknown: the page count of a PDF is learned when the
-- file passes through the bot (upload to Drive, converter result); pixel size comes
-- from Telegram (photos) or from the converter.
alter table documents
    add column page_count   integer check (page_count > 0),
    add column image_width  integer check (image_width > 0),
    add column image_height integer check (image_height > 0);

-- +goose Down
alter table documents
    drop column image_height,
    drop column image_width,
    drop column page_count;
