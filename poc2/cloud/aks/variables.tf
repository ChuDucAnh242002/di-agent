variable "location" {
  description = "Azure region to deploy into"
  default     = "Sweden Central"
}

variable "resource_group_name" {
  description = "Resource group that will hold the AKS cluster (created by this module)"
  default     = "di-agent-poc2"
}

variable "cluster_name" {
  description = "AKS cluster name"
  default     = "di-agent-aks"
}

variable "kubernetes_version" {
  description = "Kubernetes version; null lets Azure pick its current default"
  default     = null
}

variable "node_count" {
  description = "Number of nodes in the default node pool (mirrors the local PoC's worker count)"
  default     = 3
}

variable "vm_size" {
  description = "VM size for the default node pool"
  default     = "standard_dc2ads_v6"
}

variable "acr_name" {
  description = "Optional Azure Container Registry name to create and grant the cluster AcrPull on. Leave empty to skip and pull images from an external registry (e.g. ghcr.io) instead."
  default     = ""
}
