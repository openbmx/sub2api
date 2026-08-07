# Sub2API Docker Image

Sub2API is an AI API Gateway Platform for distributing and managing AI product subscription API quotas.

## Deploying this fork

Every compose file here defaults to `ghcr.io/openbmx/sub2api:latest`, this
fork's own CI build, so a plain `docker compose up -d` runs the code in this
tree. The upstream image `weishaw/sub2api:latest` contains upstream code only —
this fork's changes are simply not in it. Set `SUB2API_IMAGE` in `.env` to pin a
version, run a locally built image, or deliberately fall back to upstream.

### One command

```bash
cd deploy && ./one-click.sh
```

Windows (Docker Desktop):

```powershell
cd deploy; .\one-click.ps1
```

It creates `.env` from `.env.example`, generates any missing secrets, builds the
image from this working tree, and starts the stack. Re-running is safe: existing
secrets are never regenerated — rotating `TOTP_ENCRYPTION_KEY` would invalidate
every stored TOTP secret and every saved Prompt Audit endpoint token.

Options: `--local` (host directories instead of named volumes, easier to back up
and migrate) and `--no-build` (reuse the current image).

### Publishing your own image instead of building on the server

`.github/workflows/release.yml` already pushes to GHCR using the built-in
`GITHUB_TOKEN`, so a fork needs **no secrets**. Two one-time steps are required
first, because GitHub's defaults for forks block both of them:

1. **Enable Actions.** GitHub disables workflows on forked repositories by
   default — without this, pushing a tag silently does nothing. Open the
   repository's **Actions** tab and confirm the "enable workflows" prompt.
2. **Make the package public** (only if you want to `docker pull` without
   logging in). Images published to GHCR start out private. After the first
   successful run, go to the package page → *Package settings* → *Change
   visibility* → Public. Otherwise run `docker login ghcr.io -u <user>` with a
   PAT that has `read:packages` on every machine that pulls.

Then push a tag:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

CI publishes `ghcr.io/openbmx/sub2api:latest` (and `:0.1.0`). That is already the
compose default, so pulling it needs no `.env` change at all:

```bash
docker compose -f docker-compose.yml pull && docker compose -f docker-compose.yml up -d
```

Pin an exact version when you want reproducible rollouts:

```bash
# .env
SUB2API_IMAGE=ghcr.io/openbmx/sub2api:0.1.172-openbmx.1
```

No DockerHub account is needed: the workflow passes
`DOCKERHUB_USERNAME: ${{ secrets.DOCKERHUB_USERNAME || 'skip' }}`, and the
release config skips those pushes when it reads `skip`.

**Make it fast.** The default config also cross-builds arm64 under QEMU, which is
slow. Unless you deploy on ARM, set a repository variable
**`SIMPLE_RELEASE` = `true`** (Settings → Secrets and variables → Actions →
Variables). Every tag push then builds only the x86_64 GHCR image and skips the
binary artifacts. You can also enable it per-run from the Actions tab via
*Run workflow* → `simple_release`.

#### Keeping the fork current

Upstream tags will conflict with your own version numbers if you merge them, so
use a distinct scheme for your releases (for example `v0.1.0-openbmx`) or track
upstream on a branch and tag only your own merges.

### Manual build

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

`docker-compose.build.yml` is an overlay that only adds a `build:` section; it is
always combined with a base compose file. Set `SUB2API_IMAGE=sub2api:local` in
`.env` so the image that gets built is also the one that runs.

Database migrations apply automatically on startup (`AUTO_SETUP=true`), so an
upgrade is just: rebuild, `up -d`, done.

## Quick Start

```bash
docker run -d \
  --name sub2api \
  -p 8080:8080 \
  -e DATABASE_URL="postgres://user:pass@host:5432/sub2api" \
  -e REDIS_URL="redis://host:6379" \
  ghcr.io/openbmx/sub2api:latest
```

## Docker Compose

```yaml
version: '3.8'

services:
  sub2api:
    image: ghcr.io/openbmx/sub2api:latest
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgres://postgres:postgres@db:5432/sub2api?sslmode=disable
      - REDIS_URL=redis://redis:6379
    depends_on:
      - db
      - redis

  db:
    image: postgres:15-alpine
    environment:
      - POSTGRES_USER=postgres
      - POSTGRES_PASSWORD=postgres
      - POSTGRES_DB=sub2api
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data

volumes:
  postgres_data:
  redis_data:
```

## Environment Variables

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `DATABASE_URL` | PostgreSQL connection string | Yes | - |
| `REDIS_URL` | Redis connection string | Yes | - |
| `PORT` | Server port | No | `8080` |
| `GIN_MODE` | Gin framework mode (`debug`/`release`) | No | `release` |

## Supported Architectures

- `linux/amd64`
- `linux/arm64`

## Tags

- `latest` - Latest stable release
- `x.y.z` - Specific version
- `x.y` - Latest patch of minor version
- `x` - Latest minor of major version

## Links

- [GitHub Repository](https://github.com/openbmx/sub2api)
- [Documentation](https://github.com/openbmx/sub2api#readme)
- [Upstream project](https://github.com/Wei-Shaw/sub2api) — this fork tracks it

## What still points upstream, on purpose

`pricing.remote_url` / `pricing.hash_url` (see `config.example.yaml`, defaults in
`backend/internal/config/config.go`) fetch from `Wei-Shaw/model-price-repo`. That
is a **pricing data feed**, not code: repointing it would mean standing up and
continuously syncing your own price repository for no functional gain. Everything
that resolves *code* — the in-app updater, the rollback list, the install and
deploy scripts, the images in every compose file — resolves against this fork.
