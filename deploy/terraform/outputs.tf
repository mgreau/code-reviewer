output "github_events_service_name" {
  description = "Cloud Run service name of the webhook ingestor. Use `gcloud run services describe` to get its public URL and configure the GitHub webhook."
  value       = "${var.name}-gh"
}

output "reconciler_service_name" {
  description = "Cloud Run service name of the reconciler."
  value       = "${var.name}-rec-rec"
}

output "workqueue_receiver_name" {
  description = "Cloud Run service name of the workqueue receiver (internal-only; the CloudEvents bridge enqueues here)."
  value       = module.reconciler.receiver.name
}

output "webhook_secret_name" {
  description = "Secret Manager secret holding the GitHub webhook HMAC secret. Populate a version with the value you'll paste into the GitHub webhook config."
  value       = "${var.name}-gh-webhook-secret"
}
