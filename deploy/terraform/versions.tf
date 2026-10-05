terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 3.0"
    }
  }

  # State is local by default so a fresh clone can `terraform apply` with no
  # setup. For anything shared, move it to S3:
  #
  # backend "s3" {
  #   bucket       = "<state-bucket>"
  #   key          = "podcast-agent/terraform.tfstate"
  #   region       = "us-east-1"
  #   use_lockfile = true
  # }
}
