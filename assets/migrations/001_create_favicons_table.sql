-- +goose Up
-- +goose StatementBegin
CREATE TABLE favicons (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    domain TEXT NOT NULL UNIQUE,
    data BLOB NOT NULL,
    content_type TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Create index for the main query pattern
CREATE INDEX idx_favicons_domain ON favicons(domain);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE favicons;
-- +goose StatementEnd
