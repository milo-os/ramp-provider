# ramp-provider

Datum Cloud Ramp accounting-vendor sync provider for
[milo-os/compliance](https://github.com/milo-os/compliance).

## What it does

The provider polls Ramp's accounting-vendors endpoint on a schedule and,
for each active vendor, creates or refreshes a Draft `Vendor` resource in
the Milo control plane so the compliance team has a starting point instead
of hand-typing every record. Existing `Vendor`s are matched by the
`compliance.miloapis.com/ramp-vendor-id` annotation so re-syncs are
idempotent; `Vendor`s whose compliance profile has been promoted to
`Active` are never overwritten — the provider records them and moves on.

Configuration is entirely deployment-time: there is no CRD, and nothing
about a particular Ramp account is stored in the control plane. Operators
that don't pay vendors through Ramp simply don't deploy this controller.

## Configuration

| Flag | Env | Description |
| --- | --- | --- |
| `--milo-kubeconfig` | | Path to a kubeconfig that points at the Milo aggregated API server where Vendors live. When empty, the in-cluster client is used. |
| `--leader-election-namespace` | | Namespace for the leader-election `Lease`. Use `milo-system` (or wherever the Milo control plane lives) when `--milo-kubeconfig` is set, since the pod's own namespace usually doesn't exist on Milo. |
| `--ramp-endpoint` | `RAMP_ENDPOINT` | Ramp API base URL. Defaults to `https://api.ramp.com`. |
| `--ramp-token-url` | `RAMP_TOKEN_URL` | OAuth2 token endpoint. Defaults to `https://api.ramp.com/developer/v1/token`. |
| `--ramp-client-id-file` | `RAMP_CLIENT_ID_FILE` | Path to a file (typically a mounted Secret key) containing the Ramp OAuth2 client_id. |
| `--ramp-client-secret-file` | `RAMP_CLIENT_SECRET_FILE` | Path to a file containing the Ramp OAuth2 client_secret. |
| `--ramp-client-id` | `RAMP_CLIENT_ID` | Inline client_id. Discouraged; prefer the file form so the secret doesn't leak via process args. |
| `--ramp-client-secret` | `RAMP_CLIENT_SECRET` | Inline client_secret. Same caveat. |
| `--resync-interval` | `RESYNC_INTERVAL` | How often to re-list vendors from Ramp. Default `24h`. The first sync fires immediately on startup. |
| `--leader-elect` | | Required when running more than one replica; the syncer participates in leader election so only one pod writes Vendor records at a time. |

The kustomize base in `config/base/manager` expects a `Secret` named
`ramp-credentials` (in the same namespace as the deployment) containing
`client_id` and `client_secret` keys, mounted at `/etc/ramp/`.

## Layout

- `cmd/main.go` — manager binary, flag parsing, controller-runtime wiring.
- `internal/ramp/` — Ramp HTTP client, OAuth2 token cache, and the field
  mapper that turns a Ramp `AccountingVendor` into the small struct the
  syncer needs.
- `internal/syncer/` — periodic sync loop. Lists Ramp vendors, finds
  existing Vendors via the `compliance.miloapis.com/imported-from=ramp`
  label, and upserts unstructured `Vendor` objects against Milo without
  importing the compliance API types directly.
- `config/` — kustomize manifests: base deployment plus `controller_rbac`,
  `leader_election`, and `namespace` components.

## Why no CRD?

The Ramp connection is a deployment-time concern. The operator either
runs this provider (with Ramp credentials wired in) or they don't —
nothing about that decision needs to be representable as runtime state in
the control plane. Imported `Vendor`s carry an `imported-from=ramp` label
and a `ramp-vendor-id` annotation so other tools can tell where they came
from without a separate resource.
