variable "region" {
  description = "AWS region to deploy into"
  default     = "eu-north-1"
}

variable "cluster_name" {
  description = "EKS cluster name"
  default     = "di-agent-eks"
}

variable "kubernetes_version" {
  description = "Kubernetes version for the EKS control plane"
  default     = "1.36"
}

variable "node_count" {
  description = "Desired size of the managed node group (mirrors the local PoC's worker count)"
  default     = 3
}

variable "instance_type" {
  description = "Instance type for the managed node group"
  default     = "t3.medium"
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC created for this cluster"
  default     = "10.60.0.0/16"
}
