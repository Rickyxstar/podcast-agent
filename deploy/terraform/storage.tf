# One bucket: transcripts land in incoming/, reports go to results/<episode>/.
resource "aws_s3_bucket" "data" {
  bucket_prefix = "${var.name}-"
  force_destroy = var.force_destroy
}

resource "aws_s3_bucket_public_access_block" "data" {
  bucket = aws_s3_bucket.data.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "data" {
  bucket = aws_s3_bucket.data.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "data" {
  bucket = aws_s3_bucket.data.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "data" {
  bucket = aws_s3_bucket.data.id

  rule {
    id     = "expire-noncurrent"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = 30
    }
  }

  dynamic "rule" {
    for_each = var.results_expiration_days > 0 ? [1] : []
    content {
      id     = "expire-results"
      status = "Enabled"

      filter {
        prefix = local.results_prefix
      }

      expiration {
        days = var.results_expiration_days
      }
    }
  }
}

# New transcripts under incoming/ go to SQS. One rule per suffix: S3 filters
# take a single suffix each.
resource "aws_s3_bucket_notification" "incoming" {
  bucket = aws_s3_bucket.data.id

  dynamic "queue" {
    for_each = toset([".json", ".txt"])
    content {
      id            = "incoming${queue.value}"
      queue_arn     = aws_sqs_queue.jobs.arn
      events        = ["s3:ObjectCreated:*"]
      filter_prefix = local.incoming_prefix
      filter_suffix = queue.value
    }
  }

  # S3 sends a test event on create and fails if it can't deliver it.
  depends_on = [aws_sqs_queue_policy.jobs]
}
