# Deployment: Path B (workqueue reconciler)

Stands up the full reconciler flow on a vanilla GCP project:

```
GitHub ─webhook─▶ github-events  ──▶ broker ──▶ workqueue ──▶ reconciler ──▶ GitHub
                 (Cloud Run)       (Pub/Sub)    (GCS+PubSub)  (our code)
```

No Dockerfile, no DNS, no load balancer — `ko` builds from source, Cloud Run gives us the `.run.app` URL.

## One-time project setup

Enable APIs:

```bash
gcloud services enable \
  run.googleapis.com \
  pubsub.googleapis.com \
  storage.googleapis.com \
  secretmanager.googleapis.com \
  artifactregistry.googleapis.com \
  compute.googleapis.com \
  aiplatform.googleapis.com \
  --project "$PROJECT_ID"
```

Create the bot's GitHub token secret (the webhook HMAC secret is created automatically by the `github-events` module; you'll only populate a version for it):

```bash
gcloud secrets create code-reviewer-github-token \
  --project "$PROJECT_ID" --replication-policy=automatic
printf '%s' "$GITHUB_TOKEN" | gcloud secrets versions add code-reviewer-github-token \
  --project "$PROJECT_ID" --data-file=-
```

## Apply

```bash
cd deploy/terraform
terraform init
terraform apply \
  -var project_id="$PROJECT_ID" \
  -var secret_version_adder="user:you@example.com" \
  -var github_token_secret_id=code-reviewer-github-token
```

Apply takes a few minutes — most of it is `ko build` uploading the reconciler image to Artifact Registry.

## Populate the webhook secret

After apply, create a random value and populate it into the secret that `github-events` made for you:

```bash
WEBHOOK_SECRET=$(python3 -c 'import secrets;print(secrets.token_hex(32))')
printf '%s' "$WEBHOOK_SECRET" | gcloud secrets versions add \
  "$(terraform output -raw webhook_secret_name)" \
  --project "$PROJECT_ID" --data-file=-
```

Keep `$WEBHOOK_SECRET` handy — you'll paste it into GitHub next.

## Configure the GitHub webhook

Grab the public URL of the webhook ingestor:

```bash
SERVICE="$(terraform output -raw github_events_service_name)"
REGION="$(terraform output -json | jq -r '.github_events_service_name.value' | xargs -I{} gcloud run services list --project "$PROJECT_ID" --filter="metadata.name={}" --format='value(metadata.labels.cloud.googleapis.com/location)' | head -1)"
WEBHOOK_URL="$(gcloud run services describe "$SERVICE" --project "$PROJECT_ID" --region "$REGION" --format='value(status.url)')/"
echo "Webhook URL: $WEBHOOK_URL"
```

In the GitHub repo (or org) → Settings → Webhooks → Add webhook:

- **Payload URL**: the `$WEBHOOK_URL` above
- **Content type**: `application/json`
- **Secret**: the `$WEBHOOK_SECRET` from the previous step
- **Events**: check "Issue comments" (plus "Pings" for testing)
- **Active**: on

## Smoke test

On a PR in that repo, comment `@<bot-login> review`. Watch the flow:

```bash
# 1. github-events received the webhook:
gcloud run services logs read code-reviewer-gh --project "$PROJECT_ID" --region "$REGION" --limit 20

# 2. reconciler got woken up:
gcloud run services logs read code-reviewer-rec-rec --project "$PROJECT_ID" --region "$REGION" --limit 20
```

## Troubleshooting

- **Nothing happens after a PR comment**: check the webhook delivery page on GitHub (Settings → Webhooks → click the webhook → Recent Deliveries). A 401 means HMAC mismatch; re-populate the webhook secret. A 404 means the URL is wrong.
- **Webhook delivers but reconciler never fires**: look at the CloudEvents broker dashboard and the `cloudevents-workqueue` bridge logs. Most likely the filter didn't match — we currently only subscribe to `issue_comment`.
- **Reconciler is retrying forever**: `max_retry` defaults to 5; after that, keys go to the DLQ. Check the workqueue dashboard for dead-letter count. Look at reconciler logs to see why `Process` returned an error.
- **Hit Vertex quota limits**: switch regions (`vertex_location`) or lower `concurrent_work`.

## What's intentionally out

- **DNS + managed certs**: this uses the raw `.run.app` URL. Upgrade to `serverless-gclb` + a domain when you want a stable webhook URL behind your own hostname.
- **GitHub App auth**: using a PAT via Secret Manager. A GitHub App would rotate keys and scope per-install; add that when the reviewer is multi-tenant.
- **Apply/skip verbs from the reconciler path**: right now the reconciler only runs reviews. Apply/skip still work via `reviewer serve` locally. See the upstream PR for the follow-up.
