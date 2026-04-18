/*
Path B deployment: workqueue-backed reconciler on Cloud Run.

Shape:

  GitHub ─webhook─▶ github-events (Cloud Run, public .run.app URL)
                         │
                         ▼
                   cloudevent-broker (Pub/Sub)
                         │
                         ▼
                   cloudevents-workqueue subscriber
                         │   key = pullrequesturl
                         ▼
                   regional-go-reconciler (workqueue + reconciler svc)
                         │
                         ▼
                   reconciler (OUR cmd/reconciler, built with ko)

github-events handles webhook HMAC. Our reconciler only needs GITHUB_TOKEN
(for GitHub API + git push). Both secrets must be created out-of-band in
Secret Manager — see deploy/terraform/README.md.
*/

terraform {
  required_providers {
    google = { source = "hashicorp/google" }
    ko     = { source = "ko-build/ko" }
    cosign = { source = "chainguard-dev/cosign" }
  }
}

module "networking" {
  source = "chainguard-dev/common/infra//modules/networking"

  name       = var.name
  project_id = var.project_id
  regions    = var.regions
}

module "cloudevent-broker" {
  source = "chainguard-dev/common/infra//modules/cloudevent-broker"

  name       = var.name
  project_id = var.project_id
  regions    = module.networking.regional-networks
}

# Ingest GitHub webhooks → CloudEvents on the broker.
# `service-ingress = INGRESS_TRAFFIC_ALL` is what gives us a public .run.app URL
# that GitHub can post to — no DNS or load balancer needed.
module "github-events" {
  source = "chainguard-dev/common/infra//modules/github-events"

  project_id = var.project_id
  name       = "${var.name}-gh"
  regions    = module.networking.regional-networks
  ingress    = module.cloudevent-broker.ingress

  service-ingress      = "INGRESS_TRAFFIC_ALL"
  secret_version_adder = var.secret_version_adder

  notification_channels = var.notification_channels
}

# Service account the reconciler runs as.
resource "google_service_account" "reconciler" {
  project      = var.project_id
  account_id   = "${var.name}-rec"
  display_name = "code-reviewer reconciler"
}

resource "google_project_iam_member" "vertex_user" {
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.reconciler.email}"
}

# Bot's personal GitHub token (separate from the webhook secret that
# github-events manages). Create out-of-band; TF only grants access.
data "google_secret_manager_secret" "github_token" {
  project   = var.project_id
  secret_id = var.github_token_secret_id
}

resource "google_secret_manager_secret_iam_member" "github_token_access" {
  project   = var.project_id
  secret_id = data.google_secret_manager_secret.github_token.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.reconciler.email}"
}

# The reconciler + its workqueue. This one module stands up:
#   - workqueue receiver + dispatcher services
#   - GCS bucket + Pub/Sub topic for queue state
#   - the reconciler service itself (our cmd/reconciler built with ko)
module "reconciler" {
  source = "driftlessaf/reconcilers/infra//modules/regional-go-reconciler"

  project_id = var.project_id
  name       = var.name
  regions    = module.networking.regional-networks

  service_account = google_service_account.reconciler.email

  concurrent-work             = var.concurrent_work
  max-retry                   = var.max_retry
  enable_dead_letter_alerting = true

  containers = {
    "reconciler" = {
      source = {
        working_dir = "${path.module}/../.."
        importpath  = "./cmd/reconciler"
      }
      ports = [{ container_port = 8080 }]
      env = [
        { name = "GOOGLE_CLOUD_PROJECT",  value = var.project_id },
        { name = "GOOGLE_CLOUD_LOCATION", value = var.vertex_location },
        { name = "PROVIDER",              value = var.provider },
        { name = "CLONE",                 value = "true" },
        { name = "APPLY",                 value = tostring(var.apply) },
        { name = "JUDGE",                 value = tostring(var.use_judge) },
      ]
      env_from_secrets = [
        {
          name    = "GITHUB_TOKEN"
          secret  = var.github_token_secret_id
          version = "latest"
        },
      ]
    }
  }

  notification_channels = var.notification_channels
}

# Bridge: subscribe to the broker, filter to issue_comment events on PRs,
# extract the PR URL as the workqueue key, and enqueue.
module "cloudevents-workqueue" {
  source = "driftlessaf/reconcilers/infra//modules/cloudevents-workqueue"

  project_id = var.project_id
  name       = "${var.name}-bridge"
  regions    = module.networking.regional-networks

  broker = module.cloudevent-broker.broker

  # issue_comment covers `@bot` mentions on PRs (the only event we care
  # about for the reconciler path). Add more types here if we later want
  # to trigger on e.g. PR opens or synchronize events.
  filters = [
    { "type" = "dev.chainguard.github.issue_comment" },
  ]

  # The github-events module sets the `pullrequesturl` extension on
  # CloudEvents originating from PR comments.
  extension_key = "pullrequesturl"

  workqueue = {
    name = module.reconciler.receiver.name
  }

  notification_channels = var.notification_channels
}
