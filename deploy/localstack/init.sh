#!/bin/bash
# LocalStack runs this once its services are up (it is mounted into
# /etc/localstack/init/ready.d). It mirrors deploy/terraform: one bucket, a
# jobs queue with a dead-letter queue, and an S3 -> SQS notification for new
# transcripts under incoming/.
set -euo pipefail

BUCKET=${BUCKET:-podcasts}
QUEUE=${QUEUE:-podcast-agent-jobs}
DLQ=${DLQ:-podcast-agent-dlq}
# Shorter than Terraform's 900s so a killed worker's job is retried within
# minutes. The worker's heartbeat keeps long jobs hidden anyway.
VISIBILITY_TIMEOUT=${VISIBILITY_TIMEOUT:-120}
MAX_RECEIVE_COUNT=${MAX_RECEIVE_COUNT:-3}

awslocal s3 mb "s3://$BUCKET"

dlq_url=$(awslocal sqs create-queue --queue-name "$DLQ" \
  --attributes MessageRetentionPeriod=1209600 \
  --query QueueUrl --output text)
dlq_arn=$(awslocal sqs get-queue-attributes --queue-url "$dlq_url" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)

# Attribute values are strings; RedrivePolicy is JSON inside one.
queue_url=$(awslocal sqs create-queue --queue-name "$QUEUE" --attributes "{
    \"VisibilityTimeout\": \"$VISIBILITY_TIMEOUT\",
    \"ReceiveMessageWaitTimeSeconds\": \"20\",
    \"RedrivePolicy\": \"{\\\"deadLetterTargetArn\\\":\\\"$dlq_arn\\\",\\\"maxReceiveCount\\\":$MAX_RECEIVE_COUNT}\"
  }" --query QueueUrl --output text)
queue_arn=$(awslocal sqs get-queue-attributes --queue-url "$queue_url" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)

# One rule per suffix, as in Terraform: S3 filters take a single suffix each.
rule() {
  echo "{\"Id\": \"incoming$1\", \"QueueArn\": \"$queue_arn\", \"Events\": [\"s3:ObjectCreated:*\"],
    \"Filter\": {\"Key\": {\"FilterRules\": [
      {\"Name\": \"prefix\", \"Value\": \"incoming/\"}, {\"Name\": \"suffix\", \"Value\": \"$1\"}]}}}"
}
awslocal s3api put-bucket-notification-configuration --bucket "$BUCKET" \
  --notification-configuration "{\"QueueConfigurations\": [$(rule .json), $(rule .txt)]}"

echo "podcast-agent: bucket s3://$BUCKET, queue $queue_url, DLQ $dlq_url"
