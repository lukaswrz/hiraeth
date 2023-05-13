# hiraeth

## Overview

hiraeth is a simple web-based file sharing program. It allows authenticated users
to upload and share files with others, via a web interface. It also supports features
like file expiration dates and password protection for uploaded files.

## Configuration

Here is an example of how a configuration file could be written:

```toml
address = "localhost:8080"
name = "hiraeth"
data = "data"
database_file = "hiraeth.db"
trusted_proxies = [
  "127.0.0.1"
]
inline_types = [
  "image/png",
  "image/jpeg",
  "application/pdf"
]
session_secret = "secret"
chunk_size = 1048576
```
