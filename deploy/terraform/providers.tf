provider "aws" {
  region = var.region

  default_tags {
    tags = local.tags
  }
}

# Helm talks to the cluster this config creates. Credentials come from the
# AWS CLI at plan/apply time, so `aws` must be on PATH.
provider "helm" {
  kubernetes = {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)
    exec = {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
    }
  }
}
