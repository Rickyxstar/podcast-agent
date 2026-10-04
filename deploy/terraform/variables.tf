variable "name" {
  description = "Name prefix for every resource."
  type        = string
  default     = "podcast-agent"
}

variable "region" {
  description = "AWS region. Pick one where Bedrock serves Claude if llm_auth is bedrock."
  type        = string
  default     = "us-west-2"
}

# --- Network + cluster ---

variable "vpc_cidr" {
  description = "VPC CIDR block."
  type        = string
  default     = "10.0.0.0/16"
}

variable "kubernetes_version" {
  description = "EKS Kubernetes version."
  type        = string
  default     = "1.35"
}

variable "node_arch" {
  description = "Node CPU architecture: arm64 (Graviton, cheaper, matches an Apple Silicon build) or amd64."
  type        = string
  default     = "arm64"

  validation {
    condition     = contains(["arm64", "amd64"], var.node_arch)
    error_message = "node_arch must be arm64 or amd64."
  }
}

variable "node_instance_types" {
  description = "Node instance types. Empty picks t4g.medium for arm64 or t3.medium for amd64."
  type        = list(string)
  default     = []
}

variable "node_min_size" {
  description = "Minimum nodes in the managed node group."
  type        = number
  default     = 2
}

variable "node_max_size" {
  description = "Maximum nodes in the managed node group."
  type        = number
  default     = 3
}

# --- App wiring ---

variable "k8s_namespace" {
  description = "Namespace the Helm chart installs the worker into."
  type        = string
  default     = "podcast-agent"
}

variable "k8s_service_account" {
  description = "Worker service account name; the Pod Identity association binds the worker role to it."
  type        = string
  default     = "podcast-agent"
}

variable "llm_auth" {
  description = "How workers reach the LLM: bedrock (IAM via Pod Identity, no secret) or api_key (Anthropic key in Secrets Manager)."
  type        = string
  default     = "bedrock"

  validation {
    condition     = contains(["bedrock", "api_key"], var.llm_auth)
    error_message = "llm_auth must be bedrock or api_key."
  }
}

variable "sqs_visibility_timeout_seconds" {
  description = "SQS visibility timeout. Keep it above the worker's TIMEOUT (10m by default); the worker also extends it while a job runs."
  type        = number
  default     = 900
}

variable "sqs_max_receive_count" {
  description = "Deliveries before a message moves to the DLQ."
  type        = number
  default     = 3
}

variable "results_expiration_days" {
  description = "Delete objects under results/ after this many days. 0 keeps them forever."
  type        = number
  default     = 0
}

# --- Teardown + alerts ---

variable "force_destroy" {
  description = "Let `terraform destroy` delete a non-empty bucket and ECR repo. Handy for a demo, wrong for production."
  type        = bool
  default     = true
}

variable "keda_chart_version" {
  description = "KEDA Helm chart version."
  type        = string
  default     = "2.21.0"
}

variable "alert_email" {
  description = "Email for DLQ and budget alerts. Empty skips the SNS subscription and the budget."
  type        = string
  default     = ""
}

variable "monthly_budget_usd" {
  description = "AWS Budgets monthly limit; alerts at 80% actual and 100% forecast. Needs alert_email."
  type        = number
  default     = 25
}
