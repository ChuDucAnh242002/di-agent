output "cluster_name" {
  value = module.eks.cluster_name
}

output "region" {
  value = var.region
}

output "nodegroup_name" {
  value = local.nodegroup_name
}
