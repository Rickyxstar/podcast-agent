# Only for llm_auth = "api_key". Terraform creates the empty secret; set its
# value out of band so the key never lands in state:
#
#   aws secretsmanager put-secret-value \
#     --secret-id "$(terraform output -raw anthropic_secret_arn)" \
#     --secret-string "$ANTHROPIC_API_KEY"
resource "aws_secretsmanager_secret" "anthropic" {
  count = var.llm_auth == "api_key" ? 1 : 0

  name        = "${var.name}/anthropic-api-key"
  description = "Anthropic API key for the podcast-agent worker"

  # Delete immediately so destroy + re-apply can reuse the name.
  recovery_window_in_days = 0
}
