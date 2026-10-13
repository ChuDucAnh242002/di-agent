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
  default     = 4
}

variable "instance_type" {
  description = "Instance type for the managed node group"
  default     = "t3.small"
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC created for this cluster"
  default     = "10.60.0.0/16"
}

variable "schedule_enabled" {
  description = "Scale the node group to zero outside office hours to save cost"
  type        = bool
  default     = true
}

variable "schedule_timezone" {
  description = "IANA timezone for the schedule expressions"
  default     = "Europe/Helsinki"
}

variable "schedule_start" {
  description = "EventBridge Scheduler cron expression that scales the node group up"
  default     = "cron(0 9 ? * MON-FRI *)"
}

variable "schedule_stop" {
  description = "EventBridge Scheduler cron expression that scales the node group to zero"
  default     = "cron(0 17 ? * MON-FRI *)"
}
