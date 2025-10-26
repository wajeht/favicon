# 🌐 Favicon
[![Node.js CI](https://github.com/wajeht/favicon/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/wajeht/favicon/actions/workflows/ci.yml) [![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://github.com/wajeht/favicon/blob/main/LICENSE) [![Open Source Love svg1](https://badges.frapsoft.com/os/v1/open-source.svg?v=103)](https://github.com/wajeht/favicon)

automagically grab the favicon of a url


# 📖 Usage

add this to your html:

```html
<img loading="lozy" src="https://favicon.jaw.dev?url=<url>" />
```

> [!NOTE]
> the first request will be slow, but the subsequent requests will be cached.

## How it works

1. **First Request (Cache Miss)**:
   - Extracts the domain from the provided URL
   - Attempts to fetch favicon from multiple common locations in parallel:
     - `/favicon.ico`, `/favicon.png`, `/favicon.svg`
     - Apple touch icons
     - Web app manifest icons
   - Returns the first successful match (within 1.5 second timeout)
   - Optimizes images by resizing to 16x16 if needed
   - Stores the favicon in SQLite database with 24-hour expiration
   - Returns the favicon with `X-Favicon-Source: fetched` header

2. **Subsequent Requests (Cache Hit)**:
   - Checks database for cached favicon
   - If found and not expired, returns immediately
   - Response includes ETag for browser caching
   - Returns with `X-Favicon-Source: cached` header
   - Much faster than initial fetch (~3µs vs 500ms+)

3. **Fallback**:
   - If no favicon found after timeout, returns a default favicon
   - Response includes `X-Favicon-Source: default` header

## 📑 Docs

- See [DEVELOPMENT](./docs/development.md) for `development` guide.
- See [CONTRIBUTION](./docs/contribution.md) for `contribution` guide.

## 📜 License

Distributed under the MIT License © [wajeht](https://github.com/wajeht). See [LICENSE](./LICENSE) for more information.
