# Buildkite setup

The native pipeline lives at [`.buildkite/pipeline.yml`](../.buildkite/pipeline.yml). Create a Buildkite pipeline for `coffeeinit/porter` using native Buildkite YAML steps, not GitHub Actions Compatibility. In the pipeline's YAML steps editor, set the initial step to:

```yaml
steps:
  - label: "Upload repository pipeline"
    command: buildkite-agent pipeline upload
```

Do not enable the GitHub Actions Compatibility plugin for this pipeline. If a build
starts with output such as `Running plugin github-actions-buildkite-plugin command
hook`, the pipeline is using the wrong execution mode and the repository's native
`.buildkite/pipeline.yml` has not started yet. Remove that plugin from the pipeline,
save the configuration, and retry from a fresh build. The compatibility hook installs
the `buildkite-gha` helper through `mise`; a GitHub release `504 Gateway Timeout`
while downloading that helper is an infrastructure/download failure, not a Porter
test failure and not something the repository pipeline can repair.

For an existing pipeline, the effective setup should be:

1. The pipeline source points at `coffeeinit/porter`.
2. The pipeline steps editor contains only the native upload step shown above.
3. No `github-actions-buildkite-plugin` appears in the pipeline plugins or command
   hooks.
4. The uploaded steps come from `.buildkite/pipeline.yml` in the checkout.

If GitHub Actions Compatibility is required for a separate pipeline, keep it
separate from Porter's native Buildkite pipeline. A transient `504` from
`github.com/buildkite/buildkite-gha/releases` can then be retried on that separate
pipeline after the release service recovers, without changing Porter source or CI
steps.

The upload command reads `.buildkite/pipeline.yml` from the checkout. Enable builds for the intended branches. If pull-request builds are enabled, keep **Allow builds from third-party forked repositories** disabled; forked code can modify repository scripts and should not run on self-hosted infrastructure. Buildkite's [pipeline YAML setup](https://buildkite.com/docs/pipelines/configure/defining-steps) and [GitHub pull-request security guidance](https://buildkite.com/docs/pipelines/source-control/github) describe these settings.

The portable steps use the pipeline's default queue. Its agents need Go 1.23, Node.js, Python 3, `make`, and Git. The KVM step is separate and skipped unless the build has `PORTER_RUN_KVM_ACCEPTANCE=true`. It also excludes fork pull requests in its condition. Set that variable only for a trusted build; do not enable it globally or for fork builds.

Create a dedicated self-hosted queue with the key `porter-kvm`, and run a KVM-capable agent tagged `queue=porter-kvm`. Configure that agent with readable local paths in `PORTER_KVM_KERNEL` and `PORTER_KVM_ROOTFS`; `PORTER_FIRECRACKER_BIN` is optional and defaults to `firecracker`. The step checks `/dev/kvm`, those image paths, and Firecracker before running `make kvm-acceptance`. A build remains queued if no agent is assigned to `porter-kvm`; once an agent accepts it, missing prerequisites fail with a direct error. See Buildkite's [queue setup](https://buildkite.com/docs/agent/queues).

The current KVM target runs `TestKVMFirecrackerApplyStopAcceptance` only. It is a narrow real-VM apply/stop check, not full beta acceptance for lifecycle, scaling, DNS/TLS, load balancing, or backup/restore. The existing GitHub Actions workflow is unchanged.
