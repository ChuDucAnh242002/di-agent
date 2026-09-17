# EKS cluster backend for poc2 — an alternative to the local libvirt/kubeadm
# lab (see ../../main.tf) for running the di-agent-system Helm chart and the
# di-agent DaemonSet on a managed AWS Kubernetes cluster. Uses the widely
# used terraform-aws-modules building blocks instead of hand-rolled VPC/EKS
# resources to keep this module small and reviewable.

data "aws_availability_zones" "available" {
  state = "available"
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.8"

  name = "${var.cluster_name}-vpc"
  cidr = var.vpc_cidr

  azs             = slice(data.aws_availability_zones.available.names, 0, 3)
  private_subnets = [for i in range(3) : cidrsubnet(var.vpc_cidr, 4, i)]
  public_subnets  = [for i in range(3) : cidrsubnet(var.vpc_cidr, 4, i + 3)]

  enable_nat_gateway   = true
  single_nat_gateway   = true
  enable_dns_hostnames = true

  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1"
  }
  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
  }
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"

  cluster_name    = var.cluster_name
  cluster_version = var.kubernetes_version

  vpc_id     = module.vpc.vpc_id
  subnet_ids = module.vpc.private_subnets

  # PoC convenience: keep the API endpoint public. Restrict via
  # cluster_endpoint_public_access_cidrs in production.
  cluster_endpoint_public_access = true

  eks_managed_node_groups = {
    default = {
      min_size       = var.node_count
      max_size       = var.node_count + 2
      desired_size   = var.node_count
      instance_types = [var.instance_type]
    }
  }
}
