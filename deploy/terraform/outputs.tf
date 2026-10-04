output "region" {
  value = var.region
}

output "cluster_name" {
  value = module.eks.cluster_name
}

output "kubeconfig_command" {
  description = "Points kubectl at the cluster."
  value       = "aws eks update-kubeconfig --region ${var.region} --name ${module.eks.cluster_name}"
}

output "ecr_repository_url" {
  value = aws_ecr_repository.app.repository_url
}

output "bucket" {
  value = aws_s3_bucket.data.bucket
}

output "queue_url" {
  value = aws_sqs_queue.jobs.url
}

output "dlq_url" {
  value = aws_sqs_queue.dlq.url
}

output "k8s_namespace" {
  value = var.k8s_namespace
}

output "k8s_service_account" {
  value = var.k8s_service_account
}


# Environment for the worker container, ready to paste into Helm values.
output "worker_env" {
  value = {
    STORAGE         = "s3"
    S3_BUCKET       = aws_s3_bucket.data.bucket
    SQS_QUEUE_URL   = aws_sqs_queue.jobs.url
    AWS_REGION      = var.region
    LLM_PROVIDER    = "anthropic"
    SEARCH_PROVIDER = "brave"
  }
}
