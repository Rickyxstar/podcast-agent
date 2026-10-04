# Both in-cluster identities use EKS Pod Identity: no static keys, no OIDC
# provider wiring. A role is bound to a namespace + service account.
data "aws_iam_policy_document" "pod_identity_trust" {
  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]

    principals {
      type        = "Service"
      identifiers = ["pods.eks.amazonaws.com"]
    }
  }
}

# --- Worker ---

resource "aws_iam_role" "worker" {
  name               = "${var.name}-worker"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_trust.json
}

data "aws_iam_policy_document" "worker" {
  statement {
    sid       = "ReadTranscripts"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.data.arn}/${local.incoming_prefix}*"]
  }

  # GetObject on results/ lets the worker skip work already done (ETag check).
  statement {
    sid       = "ReadWriteResults"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.data.arn}/${local.results_prefix}*"]
  }

  # Without ListBucket, S3 answers a missing key with 403 instead of 404, and
  # the worker would read "no results yet" as an error.
  statement {
    sid       = "ListForNotFound"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.data.arn]

    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["${local.incoming_prefix}*", "${local.results_prefix}*"]
    }
  }

  statement {
    sid = "ConsumeJobs"
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueAttributes",
    ]
    resources = [aws_sqs_queue.jobs.arn]
  }

  dynamic "statement" {
    for_each = var.llm_auth == "bedrock" ? [1] : []
    content {
      sid = "InvokeClaude"
      actions = [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream",
      ]
      # Cross-region inference profiles route to foundation models in other
      # regions, so the model ARN's region is a wildcard.
      resources = [
        "arn:aws:bedrock:*::foundation-model/anthropic.*",
        "arn:aws:bedrock:*:${data.aws_caller_identity.current.account_id}:inference-profile/*",
      ]
    }
  }

  dynamic "statement" {
    for_each = var.llm_auth == "api_key" ? [1] : []
    content {
      sid       = "ReadAnthropicKey"
      actions   = ["secretsmanager:GetSecretValue"]
      resources = [aws_secretsmanager_secret.anthropic[0].arn]
    }
  }
}

resource "aws_iam_role_policy" "worker" {
  name   = "worker"
  role   = aws_iam_role.worker.id
  policy = data.aws_iam_policy_document.worker.json
}

resource "aws_eks_pod_identity_association" "worker" {
  cluster_name    = module.eks.cluster_name
  namespace       = var.k8s_namespace
  service_account = var.k8s_service_account
  role_arn        = aws_iam_role.worker.arn
}

# --- KEDA operator ---
# The ScaledObject's TriggerAuthentication uses podIdentity provider "aws",
# which acts as the KEDA operator. It only needs to read queue depth.

resource "aws_iam_role" "keda" {
  name               = "${var.name}-keda-operator"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_trust.json
}

data "aws_iam_policy_document" "keda" {
  statement {
    actions   = ["sqs:GetQueueAttributes"]
    resources = [aws_sqs_queue.jobs.arn]
  }
}

resource "aws_iam_role_policy" "keda" {
  name   = "read-queue-depth"
  role   = aws_iam_role.keda.id
  policy = data.aws_iam_policy_document.keda.json
}

resource "aws_eks_pod_identity_association" "keda" {
  cluster_name    = module.eks.cluster_name
  namespace       = "keda"
  service_account = "keda-operator"
  role_arn        = aws_iam_role.keda.arn
}
