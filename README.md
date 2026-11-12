# kustomize-overlay-diff

[![ci](https://github.com/moveeeax/kustomize-overlay-diff/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/kustomize-overlay-diff/actions/workflows/ci.yml)
[![go](https://img.shields.io/badge/go-1.26%2B-00ADD8)](https://go.dev)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Reads a kustomize tree and answers the question a reviewer actually has: what
does this overlay do to its base, and is any of it dead?

It parses `kustomization.yaml` files directly. No cluster, no `kustomize`
binary, no rendering thousands of lines to diff them — so it runs on a laptop
offline and in CI in under a second.

## What it finds

| rule | severity | what it means |
|---|---|---|
| `dead-patch` | error | a patch target matches no resource in the chain, so the patch does nothing |
| `missing-base` | error | a `resources:`/`bases:` entry points at nothing, or at a directory with no kustomization |
| `cycle` | error | the base chain loops |
| `shadowed-image` | warning | the same image is overridden at two levels; the deeper one is dead |
| `dropped-resource` | warning | the overlay lists files out of a kustomized base one by one and silently leaves one out |

Alongside the findings it prints the delta itself: the resolved chain, what the
overlay adds or drops, which resources each patch really hits, image overrides
with both values, and the name and namespace transforms.

## Usage

```
$ kustomize-overlay-diff --dir examples/shop
tree /home/m/src/shop/examples/shop

base  (3 resources)
  chain  (root base)
  adds   Deployment/shop-api
  adds   Service/shop-api
  adds   Deployment/shop-worker
  image  ghcr.io/acme/shop-api -> :3.2.0

overlays/prod  (2 resources)
  chain       (root base)
  transforms  namePrefix=prod- namespace=shop-prod
  adds        Deployment/shop-api
  adds        Service/shop-api
  drops       Deployment/shop-worker
  patch       hpa-tuning.yaml -> HorizontalPodAutoscaler/shop-api (matches nothing)

overlays/staging  (4 resources)
  chain       base -> components/observability -> overlays/staging
  transforms  namePrefix=stg- namespace=shop-staging
  patch       api-resources.yaml -> Deployment/shop-.* matches Deployment/shop-api, Deployment/shop-worker
  image       ghcr.io/acme/shop-api :3.2.0 -> :3.3.0-rc.1

findings
SEVERITY  RULE              LOCATION                                MESSAGE
warning   dropped-resource  overlays/prod/kustomization.yaml:7      base/kustomization.yaml declares worker-deployment.yaml (Deployment/shop-worker) but this kustomization lists files from base one by one and leaves it out, with no $patch: delete
error     missing-base      overlays/prod/kustomization.yaml:9      resources entry "../../components/legacy-metrics" cannot be resolved: no such file or directory
error     dead-patch        overlays/prod/kustomization.yaml:15     patch hpa-tuning.yaml targets HorizontalPodAutoscaler/shop-api, which matches no resource in the chain
warning   shadowed-image    overlays/staging/kustomization.yaml:18  image ghcr.io/acme/shop-api is overridden to :3.3.0-rc.1 here and to :3.2.0 in base/kustomization.yaml; the overlay wins and the base override is dead

4 overlays, 4 findings (2 errors, 2 warnings)
```

Other forms:

```sh
kustomize-overlay-diff --dir ./k8s --format markdown --fail-on error
kustomize-overlay-diff --dir ./k8s --ignore shadowed-image --min-severity error
kustomize-overlay-diff --dir ./k8s --format json | jq '.findings[] | select(.severity=="error")'
kustomize-overlay-diff --dir ./k8s --format github
```

`--format github` prints one GitHub Actions workflow command per finding and
nothing else; inside a workflow step each becomes an annotation on that file
and line:

```
::error file=k8s/overlays/prod/kustomization.yaml,line=15,title=dead-patch::patch hpa-tuning.yaml targets HorizontalPodAutoscaler/shop-api, which matches no resource in the chain
```

`error` findings become `::error`, `warning` findings `::warning`. Paths are
relative to the working directory, which is what GitHub expects when the step
runs at the checkout root; a file outside the working directory keeps its
absolute path.

### Flags

| flag | default | meaning |
|---|---|---|
| `--dir` | `.` | root of the tree to audit; every kustomization under it is analysed |
| `--format` | `table` | `table`, `json`, `markdown` or `github` |
| `--min-severity` | `warning` | hide anything below this |
| `--ignore` | — | rule id to suppress; repeatable, comma-separated accepted |
| `--fail-on` | `error` | exit 1 once a finding reaches this severity, or `never` |

Exit codes: `0` clean, `1` findings at or above `--fail-on`, `2` a usage error
or YAML that does not parse.

## Install

Requires Go 1.26 or newer.

```sh
go install github.com/moveeeax/kustomize-overlay-diff/cmd/kustomize-overlay-diff@latest
```

Or build from a clone:

```sh
go build -o kustomize-overlay-diff ./cmd/kustomize-overlay-diff
```

## GitHub Action

```yaml
- uses: moveeeax/kustomize-overlay-diff@v0
  with:
    dir: k8s
    fail-on: error
```

The markdown report lands in the job summary, each finding is annotated on its
file and line so it shows in the pull request diff, and `outputs.exit-code`
carries the result if you want to keep the step green and decide later. Set
`annotations: false` to keep only the summary.

| input | default |
|---|---|
| `dir` | `.` |
| `format` | `markdown` |
| `min-severity` | `warning` |
| `fail-on` | `error` |
| `ignore` | — |
| `annotations` | `true` |
| `go-version` | `1.27` |

## How it works

Each `kustomization.yaml` becomes a node. A `resources:` entry that is a
directory with its own kustomization is a base edge; an entry that is a file is
a resource; anything else is a `missing-base`. Git-url bases are recognised and
left alone.

The part that matters is the order. Kustomize applies patches to the resources
a kustomization has accumulated, and only then applies that kustomization's own
`namePrefix`, `nameSuffix` and `namespace`. So every node is resolved twice:

- **accumulated** — everything the bases rendered, plus this node's own files.
  Patch targets are matched against this set.
- **rendered** — the accumulated set after this node's name and namespace
  transforms, which is what the parent sees.

Match the other way around and every overlay with a `namePrefix` reports its
own patches as dead.

Patch target names follow kustomize too: a name under `patches: target:` is an
anchored regular expression, while a name read out of a patch document (an
untargeted `patches:` entry or a `patchesStrategicMerge:` file) is literal.

### What it does not claim

- A `labelSelector` target is reported with its matches left blank and never
  called dead — labels cannot be resolved without rendering.
- Remote (git-url) bases are not fetched, so resources behind them are not in
  the chain.
- `dropped-resource` fires on one specific, real mistake: reaching into a
  kustomized base and listing its files individually. Inheriting the directory
  and removing something with `$patch: delete` is deliberate and stays quiet.

## Development

```sh
go test ./...        # fixtures live in testdata/, one tree per rule
go vet ./...
```

MIT licensed.
