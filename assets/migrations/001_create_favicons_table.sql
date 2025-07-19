-- +goose Up
-- +goose StatementBegin
CREATE TABLE favicons (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    domain TEXT NOT NULL UNIQUE,
    data BLOB NOT NULL,
    content_type TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
);

-- Create composite index for the main query pattern
CREATE INDEX idx_favicons_domain_expires ON favicons(domain, expires_at);

-- Create index for cleanup operations
CREATE INDEX idx_favicons_expires_at ON favicons(expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE favicons;
-- +goose StatementEnd
