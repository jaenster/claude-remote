# claude-remote

Self-hosted MCP server on Kubernetes that gives Claude a real Linux sandbox with internet access, and aggregates child MCP servers behind a single endpoint.

```
Claude --streamable HTTP--> claude-remote (pod on k8s)
                              |-- core: exec, read_file, write_file, list_files
                              |-- core: exec (background), exec_status, exec_kill, exec_list
                              |-- child: myserver_* (namespaced tools from any MCP server)
                              '-- child: another_*
```

## Why

Claude's built-in sandbox has no internet, can't `git clone`, and can't reach private repos. Running on your own Kubernetes cluster solves all three:

- **Real network access** -- git clone, curl, npm install, pip install all work
- **Private repo access** -- mount a GitHub token, clone and work on private projects
- **Background processes** -- long-running builds, servers, watchers that don't block tool calls
- **MCP aggregation** -- proxy any number of MCP servers through a single endpoint
- **Security by design** -- namespace isolation, NetworkPolicy blocking cluster-internal traffic, resource quotas, and RBAC scoping keep a prompt-injected session from reaching anything it shouldn't

## Quick start

### Local

```sh
go build -o claude-remote .
./claude-remote --addr=:8080
```

Add to Claude Code:

```sh
claude mcp add claude-remote --transport http --url http://localhost:8080/mcp
```

### Kubernetes

1. Edit `k8s/ingress.yaml` and `k8s/deployment.yaml` with your domain and image
2. Apply:

```sh
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/rbac.yaml
kubectl apply -f k8s/resourcequota.yaml
kubectl apply -f k8s/networkpolicy.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/ingress.yaml
```

Or use kustomize / ArgoCD pointing at the `k8s/` directory.

The included `Dockerfile` builds an image with Node.js 22, Go, Python 3, `gh` CLI, and 1Password CLI.

## Config

```yaml
server:
  name: claude-remote
  version: "0.1.0"

sandbox:
  workdir: /workspace

auth:
  # OIDC issuer (Authentik, Keycloak, Auth0, etc.)
  issuer: https://id.example.com/application/o/claude-remote/
  # Public URL of this server (for OAuth protected resource metadata)
  resource_url: https://mcp.example.com
  # Or use a simple bearer token (also settable via AUTH_TOKEN env var)
  # token: "some-secret"

children:
  - name: myserver
    command: my-mcp-server
    args: ["--flag"]
    env:
      DATABASE_URL: "postgres://..."
```

Pass the config file with `--config`:

```sh
claude-remote --config=/etc/claude-remote/config.yaml --addr=:8080
```

## Tools

| Tool | Description |
|-|-|
| `exec` | Run a shell command. Set `background: true` to return immediately with a process ID. |
| `exec_status` | Check stdout/stderr and exit status of a background process. |
| `exec_kill` | Kill a background process. |
| `exec_list` | List all tracked background processes. |
| `read_file` | Read a file from the sandbox filesystem. |
| `write_file` | Write a file to the sandbox filesystem. |
| `list_files` | List directory contents with sizes and timestamps. |

## Adding child MCP servers

Any MCP server that speaks stdio can be proxied. Add it to the `children` list in `config.yaml`:

```yaml
children:
  - name: redivivus
    command: redivivus-mcp
    args: []
    env: {}
```

Its tools appear namespaced: a child named `redivivus` with a tool `decompile` becomes `redivivus_decompile`. The input schema is forwarded as-is.

## Auth

### Simple token

Set `auth.token` in config or the `AUTH_TOKEN` environment variable. Requests must include `Authorization: Bearer <token>`.

### OIDC (Claude.ai connector)

Set `auth.issuer` and `auth.resource_url`. The server advertises `/.well-known/oauth-protected-resource` ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)) so Claude.ai auto-discovers your identity provider.

To set up a provider (e.g. Authentik):

1. Create an OAuth2/OpenID Connect provider
2. Set redirect URIs:
   - `https://claude.ai/api/mcp/auth_callback`
   - `http://localhost/callback` (for Claude Code)
3. Note the issuer URL and put it in `auth.issuer`

Then add `https://mcp.example.com/mcp` as a connector in Claude.ai settings.

## Network policy

The included `k8s/networkpolicy.yaml` allows:

- **Ingress**: only from your ingress controller namespace
- **Egress**: DNS + internet (HTTP/HTTPS/SSH/git)
- **Blocked**: all cluster-internal traffic (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)

## CI

The GitHub Actions workflow in `.github/workflows/ci.yaml` runs `go vet` and `go build` on every push, and builds + pushes a Docker image to GHCR on pushes to `main`.

## License

MIT
