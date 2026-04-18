variable "project_id" {
  type        = string
  description = "GCP project to deploy into."
}

variable "name" {
  type        = string
  description = "Name prefix for Cloud Run services, SAs, and GCS buckets."
  default     = "code-reviewer"
}

variable "regions" {
  type        = list(string)
  description = "Regions to deploy to. Use Claude-supporting regions (us-east5 is the default Vertex region for Claude)."
  default     = ["us-east5"]
}

variable "secret_version_adder" {
  type        = string
  description = "IAM member (user:you@example.com or group:...) allowed to add versions to the webhook secret."
}

variable "github_token_secret_id" {
  type        = string
  description = "Secret Manager secret name holding the bot's GitHub token (PAT or app-installation token). Create out-of-band with `gcloud secrets create`."
}

variable "vertex_location" {
  type        = string
  description = "Vertex AI region, passed to the reconciler as GOOGLE_CLOUD_LOCATION."
  default     = "us-east5"
}

variable "provider" {
  type        = string
  description = "Model provider: `claude` or `gemini`."
  default     = "claude"
  validation {
    condition     = contains(["claude", "gemini"], var.provider)
    error_message = "provider must be one of: claude, gemini."
  }
}

variable "apply" {
  type        = bool
  description = "Let the reviewer commit and push agent-made file edits back to the PR branch."
  default     = false
}

variable "use_judge" {
  type        = bool
  description = "Enable the second-pass AI judge to filter low-quality suggestions."
  default     = false
}

variable "concurrent_work" {
  type        = number
  description = "Maximum PRs the workqueue dispatcher will process concurrently."
  default     = 4
}

variable "max_retry" {
  type        = number
  description = "Retry budget per key before it lands in the dead-letter queue."
  default     = 5
}

variable "notification_channels" {
  type        = list(string)
  description = "Monitoring notification channel IDs for service + DLQ alerts."
  default     = []
}
