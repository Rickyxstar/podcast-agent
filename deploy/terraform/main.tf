data "aws_availability_zones" "available" {
  state = "available"

  filter {
    name   = "opt-in-status"
    values = ["opt-in-not-required"]
  }
}

data "aws_caller_identity" "current" {}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)

  node_instance_types = length(var.node_instance_types) > 0 ? var.node_instance_types : (
    var.node_arch == "arm64" ? ["t4g.medium"] : ["t3.medium"]
  )
  node_ami_type = var.node_arch == "arm64" ? "AL2023_ARM_64_STANDARD" : "AL2023_x86_64_STANDARD"

  # S3 key layout. The bucket notification watches incoming/ only, so writing
  # results/ can never retrigger the agent.
  incoming_prefix = "incoming/"
  results_prefix  = "results/"

  tags = {
    Project   = var.name
    ManagedBy = "terraform"
  }
}

# Two AZs with one shared NAT gateway: enough for a multi-AZ node group
# without paying for a NAT per AZ.
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 6.0"

  name = var.name
  cidr = var.vpc_cidr
  azs  = local.azs

  private_subnets = [for i, _ in local.azs : cidrsubnet(var.vpc_cidr, 4, i)]
  public_subnets  = [for i, _ in local.azs : cidrsubnet(var.vpc_cidr, 8, 48 + i)]

  enable_nat_gateway = true
  single_nat_gateway = true

  public_subnet_tags  = { "kubernetes.io/role/elb" = 1 }
  private_subnet_tags = { "kubernetes.io/role/internal-elb" = 1 }
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 21.0"

  name               = var.name
  kubernetes_version = var.kubernetes_version

  # Public endpoint so kubectl and the Helm provider work from a laptop.
  endpoint_public_access                   = true
  enable_cluster_creator_admin_permissions = true

  vpc_id     = module.vpc.vpc_id
  subnet_ids = module.vpc.private_subnets

  addons = {
    vpc-cni = {
      before_compute = true
    }
    eks-pod-identity-agent = {
      before_compute = true
    }
    kube-proxy = {}
    coredns    = {}
    # Container Insights metrics + Fluent Bit logs to CloudWatch. Its agents
    # authenticate with the node role, which gets CloudWatchAgentServerPolicy.
    amazon-cloudwatch-observability = {}
  }

  eks_managed_node_groups = {
    default = {
      ami_type       = local.node_ami_type
      instance_types = local.node_instance_types

      min_size     = var.node_min_size
      max_size     = var.node_max_size
      desired_size = var.node_min_size

      iam_role_additional_policies = {
        cloudwatch = "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy"
      }
    }
  }
}
