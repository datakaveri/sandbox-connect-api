# Sandbox Connect user documentation

This directory contains the Docusaurus documentation site with notebook concepts, API guides,
and booking/direct-mode tutorials.

## Requirements

Use Node.js 18 or newer and npm, as declared in `package.json`. Run these commands from
`user-docs/`:

```bash
npm ci
npm run start
```

The default local URL is `http://localhost:3000/user-docs/docs/intro`. Run the docs site and
API on different ports if you start both on the same machine; for example:

```bash
npm run start -- --port 3001
```

Set `DOCS_SITE_URL` to your public origin for production. The example production origin is
`https://docs.example.com`. `DOCS_BASE_URL` defaults to `/user-docs/`; use a leading and trailing
slash when overriding it.

## Build and preview

```bash
DOCS_SITE_URL=https://docs.example.com npm run build
npm run serve
```

The generated `build/` directory is a static site. Serve it under the configured base URL using
your web server or CDN. Preview follows that same base URL.

The nginx templates under `../infra/userdocs/` support hosting this site. Set the ingress
hostname and ensure prefix handling agrees with `DOCS_BASE_URL`. The supplied Dockerfile
expects this folder's `build/` and the nginx config in its build context:

```bash
mkdir -p ../.cache/userdocs-image
cp -R build ../.cache/userdocs-image/build
cp ../infra/userdocs/nginx.conf ../.cache/userdocs-image/nginx.conf
docker build -f ../infra/userdocs/Dockerfile \
  -t sandbox-user-docs:local ../.cache/userdocs-image
```

## OpenAPI source

`npm run start` and `npm run build` first copy `docs/swagger.yaml` and `docs/swagger.json`
from the repository root into `static/openapi/`. To synchronize only those copies:

```bash
npm run sync:openapi
```

When API annotations change, regenerate their source artifacts from the repository root:

```bash
go install github.com/swaggo/swag/cmd/swag@v1.16.4
./scripts/generate-openapi.sh
```

See the [maintained documentation index](../docs/README.md) for architecture and configuration.
